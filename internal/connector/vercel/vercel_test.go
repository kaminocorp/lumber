package vercel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
)

func TestToRawLog(t *testing.T) {
	entry := logEntry{
		ID:        "log_abc",
		Message:   "GET /api/hello 200 in 12ms",
		Timestamp: 1700000000000,
		Source:    "lambda",
		Level:     "info",
		Proxy: &proxyInfo{
			StatusCode: 200,
			Path:       "/api/hello",
			Method:     "GET",
			Host:       "my-app.vercel.app",
		},
	}

	raw := toRawLog(entry)

	if raw.Source != "vercel" {
		t.Fatalf("expected source 'vercel', got %q", raw.Source)
	}
	if raw.Raw != entry.Message {
		t.Fatalf("expected Raw %q, got %q", entry.Message, raw.Raw)
	}
	expected := time.UnixMilli(1700000000000)
	if !raw.Timestamp.Equal(expected) {
		t.Fatalf("expected timestamp %v, got %v", expected, raw.Timestamp)
	}
	if raw.Metadata["level"] != "info" {
		t.Fatalf("expected level 'info', got %v", raw.Metadata["level"])
	}
	if raw.Metadata["source"] != "lambda" {
		t.Fatalf("expected source 'lambda', got %v", raw.Metadata["source"])
	}
	if raw.Metadata["id"] != "log_abc" {
		t.Fatalf("expected id 'log_abc', got %v", raw.Metadata["id"])
	}
	if raw.Metadata["status_code"] != 200 {
		t.Fatalf("expected status_code 200, got %v", raw.Metadata["status_code"])
	}
	if raw.Metadata["path"] != "/api/hello" {
		t.Fatalf("expected path '/api/hello', got %v", raw.Metadata["path"])
	}
	if raw.Metadata["method"] != "GET" {
		t.Fatalf("expected method 'GET', got %v", raw.Metadata["method"])
	}
	if raw.Metadata["host"] != "my-app.vercel.app" {
		t.Fatalf("expected host 'my-app.vercel.app', got %v", raw.Metadata["host"])
	}
}

func TestToRawLog_NoProxy(t *testing.T) {
	entry := logEntry{
		ID:        "log_xyz",
		Message:   "build started",
		Timestamp: 1700000001000,
		Source:    "build",
		Level:     "info",
	}

	raw := toRawLog(entry)

	if _, ok := raw.Metadata["status_code"]; ok {
		t.Fatal("expected no status_code in metadata when proxy is nil")
	}
	if _, ok := raw.Metadata["path"]; ok {
		t.Fatal("expected no path in metadata when proxy is nil")
	}
}

func TestQuery_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/proj_123/logs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		resp := logsResponse{
			Data: []logEntry{
				{ID: "1", Message: "hello", Timestamp: 1700000000000, Source: "lambda", Level: "info"},
				{ID: "2", Message: "world", Timestamp: 1700000001000, Source: "edge", Level: "warning"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "test-token",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_123"},
	}
	logs, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	if logs[0].Raw != "hello" || logs[1].Raw != "world" {
		t.Fatalf("unexpected log messages: %q, %q", logs[0].Raw, logs[1].Raw)
	}
}

func TestQuery_Pagination(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var resp logsResponse
		if call == 1 {
			resp = logsResponse{
				Data:       []logEntry{{ID: "1", Message: "page1", Timestamp: 1700000000000, Level: "info"}},
				Pagination: pagination{Next: "cursor_abc"},
			}
		} else {
			if r.URL.Query().Get("next") != "cursor_abc" {
				t.Fatalf("expected cursor 'cursor_abc', got %q", r.URL.Query().Get("next"))
			}
			resp = logsResponse{
				Data: []logEntry{{ID: "2", Message: "page2", Timestamp: 1700000001000, Level: "info"}},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_1"},
	}
	logs, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	if logs[0].Raw != "page1" || logs[1].Raw != "page2" {
		t.Fatalf("unexpected messages: %q, %q", logs[0].Raw, logs[1].Raw)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 API calls, got %d", calls.Load())
	}
}

func TestQuery_MissingProjectID(t *testing.T) {
	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey: "tok",
		Extra:  map[string]string{},
	}
	_, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
}

func TestQuery_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "bad-token",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_1"},
	}
	_, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestStream_ReceivesLogs(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var resp logsResponse
		if call == 1 {
			resp = logsResponse{
				Data: []logEntry{
					{ID: "1", Message: "first", Timestamp: 1700000000000, Level: "info"},
				},
			}
		} else {
			resp = logsResponse{
				Data: []logEntry{
					{ID: "2", Message: "second", Timestamp: 1700000001000, Level: "info"},
				},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_1", "poll_interval": "50ms"},
	}
	ch, err := c.Stream(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Collect at least 2 logs.
	var received []string
	timeout := time.After(2 * time.Second)
	for len(received) < 2 {
		select {
		case log, ok := <-ch:
			if !ok {
				t.Fatal("channel closed unexpectedly")
			}
			received = append(received, log.Raw)
		case <-timeout:
			t.Fatalf("timed out waiting for logs, got %d", len(received))
		}
	}

	if received[0] != "first" {
		t.Fatalf("expected first log 'first', got %q", received[0])
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
		Extra:    map[string]string{"project_id": "proj_1", "poll_interval": "50ms"},
	}
	ch, err := c.Stream(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cancel()

	// Channel should close promptly.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // success — channel closed
			}
		case <-timeout:
			t.Fatal("timed out waiting for channel to close")
		}
	}
}
