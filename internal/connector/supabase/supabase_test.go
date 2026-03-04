package supabase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
)

func TestBuildSQL(t *testing.T) {
	sql, err := buildSQL("edge_logs", 1700000000000000, 1700003600000000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "SELECT id, timestamp, event_message FROM edge_logs WHERE timestamp >= 1700000000000000 AND timestamp < 1700003600000000 ORDER BY timestamp ASC LIMIT 1000"
	if sql != expected {
		t.Fatalf("unexpected SQL:\ngot:  %s\nwant: %s", sql, expected)
	}
}

func TestBuildSQL_InvalidTable(t *testing.T) {
	_, err := buildSQL("users; DROP TABLE--", 0, 1000)
	if err == nil {
		t.Fatal("expected error for invalid table name")
	}
	if !strings.Contains(err.Error(), "not in allow-list") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestToRawLog(t *testing.T) {
	row := map[string]any{
		"id":            "uuid-123",
		"timestamp":     float64(1700000000123456), // microseconds
		"event_message": "POST /rest/v1/users 200",
		"extra_field":   "extra_value",
	}

	raw := toRawLog(row, "edge_logs")

	if raw.Source != "supabase" {
		t.Fatalf("expected source 'supabase', got %q", raw.Source)
	}
	if raw.Raw != "POST /rest/v1/users 200" {
		t.Fatalf("unexpected Raw: %q", raw.Raw)
	}

	// 1700000000123456 micros = 1700000000 sec + 123456 usec
	expectedTS := time.Unix(1700000000, 123456*1000)
	if !raw.Timestamp.Equal(expectedTS) {
		t.Fatalf("expected timestamp %v, got %v", expectedTS, raw.Timestamp)
	}
	if raw.Metadata["table"] != "edge_logs" {
		t.Fatalf("expected table 'edge_logs', got %v", raw.Metadata["table"])
	}
	if raw.Metadata["id"] != "uuid-123" {
		t.Fatalf("expected id 'uuid-123', got %v", raw.Metadata["id"])
	}
	if raw.Metadata["extra_field"] != "extra_value" {
		t.Fatalf("expected extra_field 'extra_value', got %v", raw.Metadata["extra_field"])
	}
	// event_message should not be duplicated in metadata
	if _, ok := raw.Metadata["event_message"]; ok {
		t.Fatal("event_message should not appear in metadata")
	}
}

func TestQuery_SingleTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/analytics/endpoints/logs.all") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		sql := r.URL.Query().Get("sql")
		if !strings.Contains(sql, "edge_logs") {
			t.Fatalf("expected SQL for edge_logs, got: %s", sql)
		}
		resp := logsResponse{
			Result: []map[string]any{
				{"id": "1", "timestamp": float64(1700000000000000), "event_message": "hello from edge"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Connector{}
	start := time.Unix(1700000000, 0)
	end := start.Add(1 * time.Hour)
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_ref": "proj_abc", "tables": "edge_logs"},
	}
	logs, err := c.Query(context.Background(), cfg, connector.QueryParams{Start: start, End: end})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Raw != "hello from edge" {
		t.Fatalf("unexpected message: %q", logs[0].Raw)
	}
}

func TestQuery_MultipleTables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sql := r.URL.Query().Get("sql")
		var resp logsResponse
		if strings.Contains(sql, "edge_logs") {
			resp = logsResponse{Result: []map[string]any{
				{"id": "e1", "timestamp": float64(1700000002000000), "event_message": "edge later"},
			}}
		} else if strings.Contains(sql, "postgres_logs") {
			resp = logsResponse{Result: []map[string]any{
				{"id": "p1", "timestamp": float64(1700000001000000), "event_message": "postgres earlier"},
			}}
		} else {
			resp = logsResponse{}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Connector{}
	start := time.Unix(1700000000, 0)
	end := start.Add(1 * time.Hour)
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_ref": "proj_abc", "tables": "edge_logs,postgres_logs"},
	}
	logs, err := c.Query(context.Background(), cfg, connector.QueryParams{Start: start, End: end})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	// Should be sorted by timestamp — postgres (earlier) before edge (later).
	if logs[0].Raw != "postgres earlier" {
		t.Fatalf("expected 'postgres earlier' first, got %q", logs[0].Raw)
	}
	if logs[1].Raw != "edge later" {
		t.Fatalf("expected 'edge later' second, got %q", logs[1].Raw)
	}
}

func TestQuery_SlidingWindow(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(logsResponse{Result: []map[string]any{
			{"id": "1", "timestamp": float64(1700000000000000), "event_message": "log"},
		}})
	}))
	defer srv.Close()

	c := &Connector{}
	start := time.Unix(1700000000, 0)
	end := start.Add(48 * time.Hour) // 48 hours = 2 chunks
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_ref": "proj_abc", "tables": "edge_logs"},
	}
	_, err := c.Query(context.Background(), cfg, connector.QueryParams{Start: start, End: end})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1 table × 2 chunks = 2 API calls
	if calls.Load() != 2 {
		t.Fatalf("expected 2 API calls (2 chunks × 1 table), got %d", calls.Load())
	}
}

func TestQuery_MissingProjectRef(t *testing.T) {
	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey: "tok",
		Extra:  map[string]string{},
	}
	_, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err == nil {
		t.Fatal("expected error for missing project_ref")
	}
}

func TestQuery_DefaultTables(t *testing.T) {
	var tablesSeen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sql := r.URL.Query().Get("sql")
		for _, table := range []string{"edge_logs", "postgres_logs", "auth_logs", "function_logs"} {
			if strings.Contains(sql, table) {
				tablesSeen = append(tablesSeen, table)
			}
		}
		json.NewEncoder(w).Encode(logsResponse{})
	}))
	defer srv.Close()

	c := &Connector{}
	start := time.Unix(1700000000, 0)
	end := start.Add(1 * time.Hour)
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_ref": "proj_abc"},
		// No "tables" key — should use defaults.
	}
	_, err := c.Query(context.Background(), cfg, connector.QueryParams{Start: start, End: end})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tablesSeen) != 4 {
		t.Fatalf("expected 4 default tables queried, got %d: %v", len(tablesSeen), tablesSeen)
	}
}

func TestQuery_CustomTables(t *testing.T) {
	var tablesSeen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sql := r.URL.Query().Get("sql")
		for _, table := range []string{"storage_logs", "realtime_logs"} {
			if strings.Contains(sql, table) {
				tablesSeen = append(tablesSeen, table)
			}
		}
		json.NewEncoder(w).Encode(logsResponse{})
	}))
	defer srv.Close()

	c := &Connector{}
	start := time.Unix(1700000000, 0)
	end := start.Add(1 * time.Hour)
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_ref": "proj_abc", "tables": "storage_logs, realtime_logs"},
	}
	_, err := c.Query(context.Background(), cfg, connector.QueryParams{Start: start, End: end})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tablesSeen) != 2 {
		t.Fatalf("expected 2 custom tables queried, got %d: %v", len(tablesSeen), tablesSeen)
	}
}

func TestStream_ReceivesLogs(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		// Each poll queries all tables; return data only on first call for edge_logs.
		sql := r.URL.Query().Get("sql")
		if call <= 1 && strings.Contains(sql, "edge_logs") {
			json.NewEncoder(w).Encode(logsResponse{Result: []map[string]any{
				{"id": "1", "timestamp": float64(time.Now().UnixMicro()), "event_message": "stream log"},
			}})
		} else {
			json.NewEncoder(w).Encode(logsResponse{})
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_ref": "proj_abc", "tables": "edge_logs", "poll_interval": "50ms"},
	}
	ch, err := c.Stream(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	timeout := time.After(2 * time.Second)
	select {
	case l, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if l.Raw != "stream log" {
			t.Fatalf("expected 'stream log', got %q", l.Raw)
		}
	case <-timeout:
		t.Fatal("timed out waiting for log")
	}
}

func TestStream_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(logsResponse{})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_ref": "proj_abc", "tables": "edge_logs", "poll_interval": "50ms"},
	}
	ch, err := c.Stream(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cancel()

	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for channel to close")
		}
	}
}
