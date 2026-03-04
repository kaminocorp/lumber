package supabase

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
	"github.com/kaminocorp/lumber/internal/connector/httpclient"
	"github.com/kaminocorp/lumber/internal/model"
)

const defaultEndpoint = "https://api.supabase.com"
const defaultPollInterval = 10 * time.Second
const maxWindowDuration = 24 * time.Hour

var defaultTables = []string{"edge_logs", "postgres_logs", "auth_logs", "function_logs"}

var allowedTables = map[string]bool{
	"edge_logs":         true,
	"postgres_logs":     true,
	"auth_logs":         true,
	"function_logs":     true,
	"storage_logs":      true,
	"function_edge_logs": true,
	"realtime_logs":     true,
}

func init() {
	connector.Register("supabase", func() connector.Connector {
		return &Connector{}
	})
}

// Connector implements the connector.Connector interface for Supabase's Management API analytics endpoint.
type Connector struct{}

// Response type — schema varies per table, so we use map[string]any.
type logsResponse struct {
	Result []map[string]any `json:"result"`
}

// buildSQL generates a SELECT query for the given table and microsecond time range.
// Returns an error if the table name is not in the allow-list.
func buildSQL(table string, fromMicros, toMicros int64) (string, error) {
	if !allowedTables[table] {
		return "", fmt.Errorf("supabase connector: table %q not in allow-list", table)
	}
	return fmt.Sprintf(
		"SELECT id, timestamp, event_message FROM %s WHERE timestamp >= %d AND timestamp < %d ORDER BY timestamp ASC LIMIT 1000",
		table, fromMicros, toMicros,
	), nil
}

func toRawLog(row map[string]any, table string) model.RawLog {
	var ts time.Time
	if v, ok := row["timestamp"]; ok {
		if f, ok := v.(float64); ok {
			micros := int64(f)
			sec := micros / 1_000_000
			rem := micros % 1_000_000
			ts = time.Unix(sec, rem*1000)
		}
	}

	var raw string
	if v, ok := row["event_message"]; ok {
		if s, ok := v.(string); ok {
			raw = s
		}
	}

	md := map[string]any{"table": table}
	for k, v := range row {
		if k == "event_message" {
			continue
		}
		md[k] = v
	}

	return model.RawLog{
		Timestamp: ts,
		Source:    "supabase",
		Raw:       raw,
		Metadata:  md,
	}
}

func parseTables(cfg connector.ConnectorConfig) []string {
	if raw := cfg.Extra["tables"]; raw != "" {
		parts := strings.Split(raw, ",")
		tables := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				tables = append(tables, t)
			}
		}
		if len(tables) > 0 {
			return tables
		}
	}
	return defaultTables
}

func (c *Connector) Query(ctx context.Context, cfg connector.ConnectorConfig, params connector.QueryParams) ([]model.RawLog, error) {
	projectRef := cfg.Extra["project_ref"]
	if projectRef == "" {
		return nil, fmt.Errorf("supabase connector: missing required config key \"project_ref\" in Extra")
	}

	baseURL := cfg.Endpoint
	if baseURL == "" {
		baseURL = defaultEndpoint
	}
	client := httpclient.New(baseURL, cfg.APIKey)
	path := "/v1/projects/" + projectRef + "/analytics/endpoints/logs.all"
	tables := parseTables(cfg)

	// Default time range: last 1 hour.
	now := time.Now()
	start := params.Start
	end := params.End
	if start.IsZero() && end.IsZero() {
		end = now
		start = now.Add(-1 * time.Hour)
	} else if start.IsZero() {
		start = end.Add(-1 * time.Hour)
	} else if end.IsZero() {
		end = now
	}

	// Split into 24-hour chunks.
	var results []model.RawLog
	chunkStart := start
	for chunkStart.Before(end) {
		chunkEnd := chunkStart.Add(maxWindowDuration)
		if chunkEnd.After(end) {
			chunkEnd = end
		}

		fromMicros := chunkStart.UnixMicro()
		toMicros := chunkEnd.UnixMicro()

		for _, table := range tables {
			sql, err := buildSQL(table, fromMicros, toMicros)
			if err != nil {
				return nil, err
			}

			q := url.Values{}
			q.Set("sql", sql)
			q.Set("iso_timestamp_start", chunkStart.UTC().Format(time.RFC3339))
			q.Set("iso_timestamp_end", chunkEnd.UTC().Format(time.RFC3339))

			var resp logsResponse
			if err := client.GetJSON(ctx, path, q, &resp); err != nil {
				return nil, fmt.Errorf("supabase connector: %w", err)
			}

			for _, row := range resp.Result {
				results = append(results, toRawLog(row, table))
			}
		}

		chunkStart = chunkEnd
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.Before(results[j].Timestamp)
	})

	if params.Limit > 0 && len(results) > params.Limit {
		results = results[:params.Limit]
	}

	return results, nil
}

func (c *Connector) Stream(ctx context.Context, cfg connector.ConnectorConfig) (<-chan model.RawLog, error) {
	projectRef := cfg.Extra["project_ref"]
	if projectRef == "" {
		return nil, fmt.Errorf("supabase connector: missing required config key \"project_ref\" in Extra")
	}

	baseURL := cfg.Endpoint
	if baseURL == "" {
		baseURL = defaultEndpoint
	}
	client := httpclient.New(baseURL, cfg.APIKey)
	path := "/v1/projects/" + projectRef + "/analytics/endpoints/logs.all"
	tables := parseTables(cfg)

	pollInterval := defaultPollInterval
	if raw := cfg.Extra["poll_interval"]; raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			pollInterval = d
		}
	}

	ch := make(chan model.RawLog, 64)
	go func() {
		defer close(ch)
		lastMicros := time.Now().Add(-1 * time.Minute).UnixMicro()

		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		lastMicros = pollStream(ctx, client, path, tables, lastMicros, ch)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lastMicros = pollStream(ctx, client, path, tables, lastMicros, ch)
			}
		}
	}()

	return ch, nil
}

func pollStream(ctx context.Context, client *httpclient.Client, path string, tables []string, lastMicros int64, ch chan<- model.RawLog) int64 {
	nowMicros := time.Now().UnixMicro()
	fromMicros := lastMicros + 1
	maxSeen := lastMicros

	for _, table := range tables {
		sql, err := buildSQL(table, fromMicros, nowMicros)
		if err != nil {
			slog.Warn("sql build error", "connector", "supabase", "table", table, "error", err)
			continue
		}

		from := time.UnixMicro(fromMicros)
		to := time.UnixMicro(nowMicros)

		q := url.Values{}
		q.Set("sql", sql)
		q.Set("iso_timestamp_start", from.UTC().Format(time.RFC3339))
		q.Set("iso_timestamp_end", to.UTC().Format(time.RFC3339))

		var resp logsResponse
		if err := client.GetJSON(ctx, path, q, &resp); err != nil {
			slog.Warn("poll error", "connector", "supabase", "table", table, "error", err)
			continue
		}

		for _, row := range resp.Result {
			raw := toRawLog(row, table)
			rowMicros := raw.Timestamp.UnixMicro()
			if rowMicros > maxSeen {
				maxSeen = rowMicros
			}
			select {
			case ch <- raw:
			case <-ctx.Done():
				return maxSeen
			}
		}
	}

	return maxSeen
}
