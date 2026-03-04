package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
	"github.com/kaminocorp/lumber/internal/engine"
	"github.com/kaminocorp/lumber/internal/engine/classifier"
	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/engine/dedup"
	"github.com/kaminocorp/lumber/internal/engine/embedder"
	"github.com/kaminocorp/lumber/internal/engine/taxonomy"

	_ "github.com/kaminocorp/lumber/internal/connector/vercel"
)

// Model paths relative to internal/pipeline/.
const (
	integrationModelPath      = "../../models/model_quantized.onnx"
	integrationVocabPath      = "../../models/vocab.txt"
	integrationProjectionPath = "../../models/2_Dense/model.safetensors"
)

// skipWithoutModel skips the test when ONNX model files are not present.
func skipWithoutModel(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(integrationModelPath); os.IsNotExist(err) {
		t.Skip("ONNX model not available, skipping integration test")
	}
}

// newIntegrationEngine creates a real ONNX-backed engine for integration tests.
func newIntegrationEngine(t *testing.T) *engine.Engine {
	t.Helper()
	skipWithoutModel(t)

	emb, err := embedder.New(integrationModelPath, integrationVocabPath, integrationProjectionPath)
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	t.Cleanup(func() { emb.Close() })

	tax, err := taxonomy.New(taxonomy.DefaultRoots(), emb)
	if err != nil {
		t.Fatalf("failed to create taxonomy: %v", err)
	}

	cls := classifier.New(0.5)
	cmp := compactor.New(compactor.Standard)

	return engine.New(emb, tax, cls, cmp)
}

// Vercel API response types for httptest fixtures.
type vercelResponse struct {
	Data       []vercelLogEntry `json:"data"`
	Pagination vercelPagination `json:"pagination"`
}

type vercelLogEntry struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"` // unix milliseconds
	Source    string `json:"source"`
	Level     string `json:"level"`
}

type vercelPagination struct {
	Next string `json:"next"`
}

func newVercelConnector(t *testing.T) connector.Connector {
	t.Helper()
	ctor, err := connector.Get("vercel")
	if err != nil {
		t.Fatalf("failed to get vercel connector: %v", err)
	}
	return ctor()
}

// TestIntegration_VercelStreamThroughPipeline streams 3 realistic logs through
// httptest → Vercel connector → real ONNX engine → mock output.
func TestIntegration_VercelStreamThroughPipeline(t *testing.T) {
	eng := newIntegrationEngine(t)

	var reqCount atomic.Int32
	nowMs := time.Now().UnixMilli()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		resp := vercelResponse{}
		if n == 1 {
			resp.Data = []vercelLogEntry{
				{ID: "1", Message: "ERROR: connection refused to db-primary:5432 dial tcp timeout", Timestamp: nowMs, Source: "lambda", Level: "error"},
				{ID: "2", Message: "GET /api/users 200 OK response served in 12ms", Timestamp: nowMs + 1, Source: "edge", Level: "info"},
				{ID: "3", Message: "WARN: slow query SELECT * FROM orders took 15230ms exceeding threshold", Timestamp: nowMs + 2, Source: "lambda", Level: "warning"},
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	out := &mockOutput{}
	conn := newVercelConnector(t)
	p := New(conn, eng, out)
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := connector.ConnectorConfig{
		Provider: "vercel",
		APIKey:   "test-token",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_test"},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.Stream(ctx, cfg)
	}()

	// Wait for all 3 events to arrive in the mock output.
	deadline := time.After(10 * time.Second)
	for len(out.Events()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for events, got %d", len(out.Events()))
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("unexpected stream error: %v", err)
	}

	events := out.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	for i, e := range events {
		if e.Type == "" {
			t.Errorf("event %d: empty Type", i)
		}
		if e.Category == "" {
			t.Errorf("event %d: empty Category", i)
		}
		if e.Severity == "" {
			t.Errorf("event %d: empty Severity", i)
		}
		if e.Summary == "" {
			t.Errorf("event %d: empty Summary", i)
		}
		if e.Confidence <= 0 {
			t.Errorf("event %d: expected positive confidence, got %f", i, e.Confidence)
		}
	}
}

// TestIntegration_VercelQueryThroughPipeline exercises query mode with
// 3 semantically distinct logs through the real classification engine.
func TestIntegration_VercelQueryThroughPipeline(t *testing.T) {
	eng := newIntegrationEngine(t)

	now := time.Now()
	nowMs := now.UnixMilli()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := vercelResponse{
			Data: []vercelLogEntry{
				{ID: "1", Message: "ERROR: authentication failed invalid bearer token for user admin", Timestamp: nowMs, Source: "lambda", Level: "error"},
				{ID: "2", Message: "deploy succeeded build completed and promoted to production in 45s", Timestamp: nowMs + 1000, Source: "build", Level: "info"},
				{ID: "3", Message: "WARN: memory usage at 92% on instance i-abc123 approaching resource limit", Timestamp: nowMs + 2000, Source: "lambda", Level: "warning"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	out := &mockOutput{}
	conn := newVercelConnector(t)
	p := New(conn, eng, out)
	defer p.Close()

	cfg := connector.ConnectorConfig{
		Provider: "vercel",
		APIKey:   "test-token",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_test"},
	}
	params := connector.QueryParams{
		Start: now.Add(-time.Hour),
		End:   now.Add(time.Hour),
	}

	if err := p.Query(context.Background(), cfg, params); err != nil {
		t.Fatalf("query error: %v", err)
	}

	events := out.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	for i, e := range events {
		if e.Type == "" || e.Category == "" || e.Severity == "" || e.Summary == "" {
			t.Errorf("event %d: incomplete fields: type=%q category=%q severity=%q summary=%q",
				i, e.Type, e.Category, e.Severity, e.Summary)
		}
		if e.Confidence <= 0 {
			t.Errorf("event %d: expected positive confidence, got %f", i, e.Confidence)
		}
	}
}

// TestIntegration_BadLogDoesNotCrash mixes valid logs with edge cases (empty string,
// binary content) and verifies the pipeline continues without crashing.
func TestIntegration_BadLogDoesNotCrash(t *testing.T) {
	eng := newIntegrationEngine(t)

	nowMs := time.Now().UnixMilli()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := vercelResponse{
			Data: []vercelLogEntry{
				{ID: "1", Message: "ERROR: connection timeout to redis-primary:6379", Timestamp: nowMs, Source: "lambda", Level: "error"},
				{ID: "2", Message: "", Timestamp: nowMs + 1, Source: "lambda", Level: "info"},
				{ID: "3", Message: "\x00\x01\x02\xff\xfe binary garbage", Timestamp: nowMs + 2, Source: "edge", Level: "info"},
				{ID: "4", Message: "GET /api/health 200 OK 2ms", Timestamp: nowMs + 3, Source: "edge", Level: "info"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	out := &mockOutput{}
	conn := newVercelConnector(t)
	p := New(conn, eng, out)
	defer p.Close()

	cfg := connector.ConnectorConfig{
		Provider: "vercel",
		APIKey:   "test-token",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_test"},
	}

	err := p.Query(context.Background(), cfg, connector.QueryParams{})
	if err != nil {
		t.Fatalf("query should not fail with edge case logs: %v", err)
	}

	// Pipeline should process all logs without crashing.
	// The real engine handles empty/binary content gracefully (Phase 2 edge case tests).
	// If any log causes an engine error, Section 3 skip-and-continue keeps the pipeline alive.
	events := out.Events()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (valid logs processed), got %d", len(events))
	}
}

// TestIntegration_DedupReducesCount sends 10 identical error logs and verifies
// that dedup merges them into fewer events with Count > 1.
func TestIntegration_DedupReducesCount(t *testing.T) {
	eng := newIntegrationEngine(t)

	nowMs := time.Now().UnixMilli()
	var entries []vercelLogEntry
	for i := 0; i < 10; i++ {
		entries = append(entries, vercelLogEntry{
			ID:        fmt.Sprintf("%d", i+1),
			Message:   "ERROR: connection refused to db-primary:5432 dial tcp timeout",
			Timestamp: nowMs + int64(i),
			Source:    "lambda",
			Level:     "error",
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(vercelResponse{Data: entries})
	}))
	defer srv.Close()

	out := &mockOutput{}
	conn := newVercelConnector(t)
	d := dedup.New(dedup.Config{Window: 10 * time.Second})
	p := New(conn, eng, out, WithDedup(d, time.Second))
	defer p.Close()

	cfg := connector.ConnectorConfig{
		Provider: "vercel",
		APIKey:   "test-token",
		Endpoint: srv.URL,
		Extra:    map[string]string{"project_id": "proj_test"},
	}

	if err := p.Query(context.Background(), cfg, connector.QueryParams{}); err != nil {
		t.Fatalf("query error: %v", err)
	}

	events := out.Events()
	if len(events) == 0 {
		t.Fatal("expected at least 1 event after dedup")
	}
	if len(events) >= 10 {
		t.Fatalf("expected dedup to reduce 10 identical logs, got %d events", len(events))
	}

	// All 10 logs should be accounted for via Count fields.
	var hasDeduped bool
	var totalCount int
	for _, e := range events {
		if e.Count > 1 {
			hasDeduped = true
		}
		if e.Count > 0 {
			totalCount += e.Count
		} else {
			totalCount++
		}
	}
	if !hasDeduped {
		t.Error("expected at least one event with Count > 1")
	}
	if totalCount != 10 {
		t.Errorf("expected total count of 10 (all logs accounted for), got %d", totalCount)
	}
}
