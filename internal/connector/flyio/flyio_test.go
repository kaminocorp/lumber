package flyio

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
	w := logWrapper{
		ID:   "log_1",
		Type: "log",
		Attributes: logAttributes{
			Timestamp: "2026-02-23T10:30:00.123Z",
			Message:   "Listening on 0.0.0.0:8080",
			Level:     "info",
			Instance:  "148ed726c12358",
			Region:    "ord",
			Meta:      map[string]any{"custom": "value"},
		},
	}

	raw := toRawLog(w)

	if raw.Source != "flyio" {
		t.Fatalf("expected source 'flyio', got %q", raw.Source)
	}
	if raw.Raw != "Listening on 0.0.0.0:8080" {
		t.Fatalf("unexpected Raw: %q", raw.Raw)
	}
	expected, _ := time.Parse(time.RFC3339Nano, "2026-02-23T10:30:00.123Z")
	if !raw.Timestamp.Equal(expected) {
		t.Fatalf("expected timestamp %v, got %v", expected, raw.Timestamp)
	}
	if raw.Metadata["level"] != "info" {
		t.Fatalf("expected level 'info', got %v", raw.Metadata["level"])
	}
	if raw.Metadata["instance"] != "148ed726c12358" {
		t.Fatalf("expected instance '148ed726c12358', got %v", raw.Metadata["instance"])
	}
	if raw.Metadata["region"] != "ord" {
		t.Fatalf("expected region 'ord', got %v", raw.Metadata["region"])
	}
	if raw.Metadata["id"] != "log_1" {
		t.Fatalf("expected id 'log_1', got %v", raw.Metadata["id"])
	}
	if raw.Metadata["custom"] != "value" {
		t.Fatalf("expected custom 'value', got %v", raw.Metadata["custom"])
	}
}

func TestQuery_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/apps/my-app/logs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fly-tok" {
			t.Fatalf("unexpected auth: %s", r.Header.Get("Authorization"))
		}
		resp := logsResponse{
			Data: []logWrapper{
				{ID: "1", Attributes: logAttributes{Timestamp: "2026-02-23T10:00:00Z", Message: "hello", Level: "info", Instance: "a", Region: "ord"}},
				{ID: "2", Attributes: logAttributes{Timestamp: "2026-02-23T10:01:00Z", Message: "world", Level: "warn", Instance: "b", Region: "lax"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "fly-tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"app_name": "my-app"},
	}
	logs, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	if logs[0].Raw != "hello" || logs[1].Raw != "world" {
		t.Fatalf("unexpected messages: %q, %q", logs[0].Raw, logs[1].Raw)
	}
}

func TestQuery_Pagination(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var resp logsResponse
		if call == 1 {
			resp = logsResponse{
				Data: []logWrapper{{ID: "1", Attributes: logAttributes{Timestamp: "2026-02-23T10:00:00Z", Message: "page1", Level: "info"}}},
				Meta: meta{NextToken: "tok_abc"},
			}
		} else {
			if r.URL.Query().Get("next_token") != "tok_abc" {
				t.Fatalf("expected next_token 'tok_abc', got %q", r.URL.Query().Get("next_token"))
			}
			resp = logsResponse{
				Data: []logWrapper{{ID: "2", Attributes: logAttributes{Timestamp: "2026-02-23T10:01:00Z", Message: "page2", Level: "info"}}},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"app_name": "app"},
	}
	logs, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 API calls, got %d", calls.Load())
	}
}

func TestQuery_ClientSideTimeFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := logsResponse{
			Data: []logWrapper{
				{ID: "1", Attributes: logAttributes{Timestamp: "2026-02-23T09:00:00Z", Message: "too early", Level: "info"}},
				{ID: "2", Attributes: logAttributes{Timestamp: "2026-02-23T10:30:00Z", Message: "in range", Level: "info"}},
				{ID: "3", Attributes: logAttributes{Timestamp: "2026-02-23T12:00:00Z", Message: "too late", Level: "info"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Connector{}
	start, _ := time.Parse(time.RFC3339, "2026-02-23T10:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-02-23T11:00:00Z")
	cfg := connector.ConnectorConfig{
		APIKey:   "tok",
		Endpoint: srv.URL,
		Extra:    map[string]string{"app_name": "app"},
	}
	logs, err := c.Query(context.Background(), cfg, connector.QueryParams{Start: start, End: end})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Raw != "in range" {
		t.Fatalf("expected 'in range', got %q", logs[0].Raw)
	}
}

func TestQuery_MissingAppName(t *testing.T) {
	c := &Connector{}
	cfg := connector.ConnectorConfig{
		APIKey: "tok",
		Extra:  map[string]string{},
	}
	_, err := c.Query(context.Background(), cfg, connector.QueryParams{})
	if err == nil {
		t.Fatal("expected error for missing app_name")
	}
}

func TestStream_ReceivesLogs(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var resp logsResponse
		if call == 1 {
			resp = logsResponse{
				Data: []logWrapper{{ID: "1", Attributes: logAttributes{Timestamp: "2026-02-23T10:00:00Z", Message: "first", Level: "info"}}},
			}
		} else {
			resp = logsResponse{
				Data: []logWrapper{{ID: "2", Attributes: logAttributes{Timestamp: "2026-02-23T10:01:00Z", Message: "second", Level: "info"}}},
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
		Extra:    map[string]string{"app_name": "app", "poll_interval": "50ms"},
	}
	ch, err := c.Stream(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var received []string
	timeout := time.After(2 * time.Second)
	for len(received) < 2 {
		select {
		case l, ok := <-ch:
			if !ok {
				t.Fatal("channel closed unexpectedly")
			}
			received = append(received, l.Raw)
		case <-timeout:
			t.Fatalf("timed out, got %d logs", len(received))
		}
	}

	if received[0] != "first" {
		t.Fatalf("expected 'first', got %q", received[0])
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
		Extra:    map[string]string{"app_name": "app", "poll_interval": "50ms"},
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
