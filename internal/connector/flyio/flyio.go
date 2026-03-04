package flyio

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
	"github.com/kaminocorp/lumber/internal/connector/httpclient"
	"github.com/kaminocorp/lumber/internal/model"
)

const defaultEndpoint = "https://api.fly.io"
const defaultPollInterval = 5 * time.Second

func init() {
	connector.Register("flyio", func() connector.Connector {
		return &Connector{}
	})
}

// Connector implements the connector.Connector interface for Fly.io's HTTP logs API.
type Connector struct{}

// Response types (unexported).

type logsResponse struct {
	Data []logWrapper `json:"data"`
	Meta meta         `json:"meta"`
}

type logWrapper struct {
	ID         string        `json:"id"`
	Type       string        `json:"type"`
	Attributes logAttributes `json:"attributes"`
}

type logAttributes struct {
	Timestamp string         `json:"timestamp"` // RFC 3339
	Message   string         `json:"message"`
	Level     string         `json:"level"`
	Instance  string         `json:"instance"`
	Region    string         `json:"region"`
	Meta      map[string]any `json:"meta"`
}

type meta struct {
	NextToken string `json:"next_token"`
}

func toRawLog(w logWrapper) model.RawLog {
	ts, _ := time.Parse(time.RFC3339Nano, w.Attributes.Timestamp)

	md := map[string]any{
		"level":    w.Attributes.Level,
		"instance": w.Attributes.Instance,
		"region":   w.Attributes.Region,
		"id":       w.ID,
	}
	for k, v := range w.Attributes.Meta {
		md[k] = v
	}

	return model.RawLog{
		Timestamp: ts,
		Source:    "flyio",
		Raw:       w.Attributes.Message,
		Metadata:  md,
	}
}

func (c *Connector) Query(ctx context.Context, cfg connector.ConnectorConfig, params connector.QueryParams) ([]model.RawLog, error) {
	appName := cfg.Extra["app_name"]
	if appName == "" {
		return nil, fmt.Errorf("flyio connector: missing required config key \"app_name\" in Extra")
	}

	baseURL := cfg.Endpoint
	if baseURL == "" {
		baseURL = defaultEndpoint
	}
	client := httpclient.New(baseURL, cfg.APIKey)
	path := "/api/v1/apps/" + appName + "/logs"

	var results []model.RawLog
	cursor := ""

	for {
		q := url.Values{}
		if cursor != "" {
			q.Set("next_token", cursor)
		}

		var resp logsResponse
		if err := client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, fmt.Errorf("flyio connector: %w", err)
		}

		for _, entry := range resp.Data {
			raw := toRawLog(entry)

			// Client-side time filter (Fly.io has no server-side time range).
			if !params.Start.IsZero() && raw.Timestamp.Before(params.Start) {
				continue
			}
			if !params.End.IsZero() && !raw.Timestamp.Before(params.End) {
				continue
			}

			results = append(results, raw)
			if params.Limit > 0 && len(results) >= params.Limit {
				return results[:params.Limit], nil
			}
		}

		cursor = resp.Meta.NextToken
		if cursor == "" {
			break
		}
	}

	return results, nil
}

func (c *Connector) Stream(ctx context.Context, cfg connector.ConnectorConfig) (<-chan model.RawLog, error) {
	appName := cfg.Extra["app_name"]
	if appName == "" {
		return nil, fmt.Errorf("flyio connector: missing required config key \"app_name\" in Extra")
	}

	baseURL := cfg.Endpoint
	if baseURL == "" {
		baseURL = defaultEndpoint
	}
	client := httpclient.New(baseURL, cfg.APIKey)
	path := "/api/v1/apps/" + appName + "/logs"

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

		cursor = poll(ctx, client, path, cursor, ch)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cursor = poll(ctx, client, path, cursor, ch)
			}
		}
	}()

	return ch, nil
}

func poll(ctx context.Context, client *httpclient.Client, path, cursor string, ch chan<- model.RawLog) string {
	q := url.Values{}
	if cursor != "" {
		q.Set("next_token", cursor)
	}

	var resp logsResponse
	if err := client.GetJSON(ctx, path, q, &resp); err != nil {
		slog.Warn("poll error", "connector", "flyio", "error", err)
		return cursor
	}

	for _, entry := range resp.Data {
		select {
		case ch <- toRawLog(entry):
		case <-ctx.Done():
			return cursor
		}
	}

	if resp.Meta.NextToken != "" {
		return resp.Meta.NextToken
	}
	return cursor
}
