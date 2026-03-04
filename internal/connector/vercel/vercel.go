package vercel

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
	"github.com/kaminocorp/lumber/internal/connector/httpclient"
	"github.com/kaminocorp/lumber/internal/model"
)

const defaultEndpoint = "https://api.vercel.com"
const defaultPollInterval = 5 * time.Second

func init() {
	connector.Register("vercel", func() connector.Connector {
		return &Connector{}
	})
}

// Connector implements the connector.Connector interface for Vercel's REST logs API.
type Connector struct{}

// Response types (unexported).

type logsResponse struct {
	Data       []logEntry `json:"data"`
	Pagination pagination `json:"pagination"`
}

type logEntry struct {
	ID        string     `json:"id"`
	Message   string     `json:"message"`
	Timestamp int64      `json:"timestamp"` // unix milliseconds
	Source    string     `json:"source"`    // "build", "edge", "lambda", "static"
	Level     string     `json:"level"`     // "info", "warning", "error"
	Proxy     *proxyInfo `json:"proxy,omitempty"`
}

type proxyInfo struct {
	StatusCode int    `json:"statusCode"`
	Path       string `json:"path"`
	Method     string `json:"method"`
	Host       string `json:"host"`
}

type pagination struct {
	Next string `json:"next"`
}

func toRawLog(entry logEntry) model.RawLog {
	md := map[string]any{
		"level":  entry.Level,
		"source": entry.Source,
		"id":     entry.ID,
	}
	if entry.Proxy != nil {
		md["status_code"] = entry.Proxy.StatusCode
		md["path"] = entry.Proxy.Path
		md["method"] = entry.Proxy.Method
		md["host"] = entry.Proxy.Host
	}
	return model.RawLog{
		Timestamp: time.UnixMilli(entry.Timestamp),
		Source:    "vercel",
		Raw:       entry.Message,
		Metadata:  md,
	}
}

func (c *Connector) Query(ctx context.Context, cfg connector.ConnectorConfig, params connector.QueryParams) ([]model.RawLog, error) {
	projectID := cfg.Extra["project_id"]
	if projectID == "" {
		return nil, fmt.Errorf("vercel connector: missing required config key \"project_id\" in Extra")
	}

	baseURL := cfg.Endpoint
	if baseURL == "" {
		baseURL = defaultEndpoint
	}
	client := httpclient.New(baseURL, cfg.APIKey)
	path := "/v1/projects/" + projectID + "/logs"

	var results []model.RawLog
	cursor := ""

	for {
		q := url.Values{}
		if !params.Start.IsZero() {
			q.Set("from", strconv.FormatInt(params.Start.UnixMilli(), 10))
		}
		if !params.End.IsZero() {
			q.Set("to", strconv.FormatInt(params.End.UnixMilli(), 10))
		}
		if teamID := cfg.Extra["team_id"]; teamID != "" {
			q.Set("teamId", teamID)
		}
		if cursor != "" {
			q.Set("next", cursor)
		}

		var resp logsResponse
		if err := client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, fmt.Errorf("vercel connector: %w", err)
		}

		for _, entry := range resp.Data {
			results = append(results, toRawLog(entry))
			if params.Limit > 0 && len(results) >= params.Limit {
				return results[:params.Limit], nil
			}
		}

		cursor = resp.Pagination.Next
		if cursor == "" {
			break
		}
	}

	return results, nil
}

func (c *Connector) Stream(ctx context.Context, cfg connector.ConnectorConfig) (<-chan model.RawLog, error) {
	projectID := cfg.Extra["project_id"]
	if projectID == "" {
		return nil, fmt.Errorf("vercel connector: missing required config key \"project_id\" in Extra")
	}

	baseURL := cfg.Endpoint
	if baseURL == "" {
		baseURL = defaultEndpoint
	}
	client := httpclient.New(baseURL, cfg.APIKey)
	path := "/v1/projects/" + projectID + "/logs"

	pollInterval := defaultPollInterval
	if raw := cfg.Extra["poll_interval"]; raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			pollInterval = d
		}
	}

	ch := make(chan model.RawLog, 64)
	go func() {
		defer close(ch)
		cursor := ""
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		// Do an initial poll immediately.
		cursor = poll(ctx, client, path, cfg.Extra["team_id"], cursor, ch)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cursor = poll(ctx, client, path, cfg.Extra["team_id"], cursor, ch)
			}
		}
	}()

	return ch, nil
}

// poll fetches one page of logs and sends them to ch. Returns the updated cursor.
func poll(ctx context.Context, client *httpclient.Client, path, teamID, cursor string, ch chan<- model.RawLog) string {
	q := url.Values{}
	if teamID != "" {
		q.Set("teamId", teamID)
	}
	if cursor != "" {
		q.Set("next", cursor)
	}

	var resp logsResponse
	if err := client.GetJSON(ctx, path, q, &resp); err != nil {
		slog.Warn("poll error", "connector", "vercel", "error", err)
		return cursor
	}

	for _, entry := range resp.Data {
		select {
		case ch <- toRawLog(entry):
		case <-ctx.Done():
			return cursor
		}
	}

	if resp.Pagination.Next != "" {
		return resp.Pagination.Next
	}
	return cursor
}
