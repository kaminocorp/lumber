package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
	"github.com/kaminocorp/lumber/internal/engine/dedup"
	"github.com/kaminocorp/lumber/internal/model"
)

// --- mocks ---

// mockProcessor returns a fixed CanonicalEvent for all inputs, except when
// raw.Raw matches failOn, in which case it returns an error.
type mockProcessor struct {
	failOn string
}

func (m *mockProcessor) Process(raw model.RawLog) (model.CanonicalEvent, error) {
	if raw.Raw == m.failOn {
		return model.CanonicalEvent{}, fmt.Errorf("mock: cannot process %q", raw.Raw)
	}
	return model.CanonicalEvent{
		Type:      "ERROR",
		Category:  "timeout",
		Severity:  "error",
		Timestamp: raw.Timestamp,
		Summary:   raw.Raw,
	}, nil
}

func (m *mockProcessor) ProcessBatch(raws []model.RawLog) ([]model.CanonicalEvent, error) {
	// If any raw matches failOn, fail the whole batch.
	for _, raw := range raws {
		if raw.Raw == m.failOn {
			return nil, fmt.Errorf("mock: batch failed on %q", raw.Raw)
		}
	}
	var events []model.CanonicalEvent
	for _, raw := range raws {
		e, _ := m.Process(raw)
		events = append(events, e)
	}
	return events, nil
}

// categoryProcessor uses raw.Raw as the Category, producing distinct dedup keys per input.
// Fails when raw.Raw matches failOn.
type categoryProcessor struct {
	failOn string
}

func (m *categoryProcessor) Process(raw model.RawLog) (model.CanonicalEvent, error) {
	if raw.Raw == m.failOn {
		return model.CanonicalEvent{}, fmt.Errorf("mock: cannot process %q", raw.Raw)
	}
	return model.CanonicalEvent{
		Type:      "ERROR",
		Category:  raw.Raw, // unique per input — distinct dedup key
		Severity:  "error",
		Timestamp: raw.Timestamp,
		Summary:   raw.Raw,
	}, nil
}

func (m *categoryProcessor) ProcessBatch(raws []model.RawLog) ([]model.CanonicalEvent, error) {
	var events []model.CanonicalEvent
	for _, raw := range raws {
		e, err := m.Process(raw)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

// mockConnector is a minimal connector that sends pre-loaded logs.
type mockConnector struct {
	logs []model.RawLog
}

func (m *mockConnector) Stream(_ context.Context, _ connector.ConnectorConfig) (<-chan model.RawLog, error) {
	ch := make(chan model.RawLog, len(m.logs))
	for _, raw := range m.logs {
		ch <- raw
	}
	close(ch)
	return ch, nil
}

func (m *mockConnector) Query(_ context.Context, _ connector.ConnectorConfig, _ connector.QueryParams) ([]model.RawLog, error) {
	return m.logs, nil
}

type mockOutput struct {
	mu     sync.Mutex
	events []model.CanonicalEvent
}

func (m *mockOutput) Write(_ context.Context, e model.CanonicalEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *mockOutput) Close() error { return nil }

func (m *mockOutput) Events() []model.CanonicalEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]model.CanonicalEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

// --- streamBuffer tests ---

func TestStreamBufferFlush(t *testing.T) {
	out := &mockOutput{}
	d := dedup.New(dedup.Config{Window: time.Second})
	buf := newStreamBuffer(d, out, 100*time.Millisecond, 0, nil)

	t0 := time.Now()
	// Add 10 identical events.
	for i := 0; i < 10; i++ {
		buf.add(model.CanonicalEvent{
			Type:      "ERROR",
			Category:  "timeout",
			Severity:  "error",
			Timestamp: t0.Add(time.Duration(i) * time.Millisecond),
			Summary:   "timeout",
		})
	}

	// Wait for timer to fire.
	select {
	case <-buf.flushCh():
	case <-time.After(time.Second):
		t.Fatal("flush timer didn't fire")
	}

	if err := buf.flush(context.Background()); err != nil {
		t.Fatalf("flush error: %v", err)
	}

	events := out.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 deduplicated event, got %d", len(events))
	}
	if events[0].Count != 10 {
		t.Fatalf("expected Count=10, got %d", events[0].Count)
	}
}

func TestStreamBufferContextCancel(t *testing.T) {
	out := &mockOutput{}
	d := dedup.New(dedup.Config{Window: 10 * time.Second})
	buf := newStreamBuffer(d, out, 10*time.Second, 0, nil) // Long window — won't fire.

	t0 := time.Now()
	buf.add(model.CanonicalEvent{
		Type:      "ERROR",
		Category:  "timeout",
		Severity:  "error",
		Timestamp: t0,
		Summary:   "timeout",
	})
	buf.add(model.CanonicalEvent{
		Type:      "ERROR",
		Category:  "timeout",
		Severity:  "error",
		Timestamp: t0.Add(time.Second),
		Summary:   "timeout",
	})

	// Flush immediately (simulating context cancel).
	if err := buf.flush(context.Background()); err != nil {
		t.Fatalf("flush error: %v", err)
	}

	events := out.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 deduplicated event on cancel flush, got %d", len(events))
	}
	if events[0].Count != 2 {
		t.Fatalf("expected Count=2, got %d", events[0].Count)
	}
}

func TestPipelineWithoutDedup(t *testing.T) {
	// Verify that a pipeline without dedup passes events directly.
	out := &mockOutput{}
	d := dedup.New(dedup.Config{Window: time.Second})
	buf := newStreamBuffer(d, out, 50*time.Millisecond, 0, nil)

	// Add 3 distinct events.
	t0 := time.Now()
	buf.add(model.CanonicalEvent{Type: "ERROR", Category: "timeout", Timestamp: t0, Summary: "a"})
	buf.add(model.CanonicalEvent{Type: "REQUEST", Category: "success", Timestamp: t0, Summary: "b"})
	buf.add(model.CanonicalEvent{Type: "DEPLOY", Category: "build_succeeded", Timestamp: t0, Summary: "c"})

	select {
	case <-buf.flushCh():
	case <-time.After(time.Second):
		t.Fatal("flush timer didn't fire")
	}

	if err := buf.flush(context.Background()); err != nil {
		t.Fatalf("flush error: %v", err)
	}

	events := out.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 distinct events, got %d", len(events))
	}
}

// --- per-log error handling tests ---

func TestStreamDirect_SkipsBadLog(t *testing.T) {
	t0 := time.Now()
	conn := &mockConnector{logs: []model.RawLog{
		{Timestamp: t0, Source: "test", Raw: "good log 1"},
		{Timestamp: t0, Source: "test", Raw: "BAD"},
		{Timestamp: t0, Source: "test", Raw: "good log 2"},
	}}
	out := &mockOutput{}
	proc := &mockProcessor{failOn: "BAD"}

	p := New(conn, proc, out)

	err := p.Stream(context.Background(), connector.ConnectorConfig{})
	if err != nil {
		t.Fatalf("expected nil error (channel close), got: %v", err)
	}

	events := out.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 skipped), got %d", len(events))
	}
	if events[0].Summary != "good log 1" {
		t.Errorf("expected first event 'good log 1', got %q", events[0].Summary)
	}
	if events[1].Summary != "good log 2" {
		t.Errorf("expected second event 'good log 2', got %q", events[1].Summary)
	}
	if p.skippedLogs.Load() != 1 {
		t.Errorf("expected 1 skipped log, got %d", p.skippedLogs.Load())
	}
}

func TestStreamWithDedup_SkipsBadLog(t *testing.T) {
	t0 := time.Now()
	// Dedup keys on Type+Category, so we use a processor that produces
	// distinct categories to ensure events aren't merged.
	conn := &mockConnector{logs: []model.RawLog{
		{Timestamp: t0, Source: "test", Raw: "connection timeout"},
		{Timestamp: t0, Source: "test", Raw: "BAD"},
		{Timestamp: t0, Source: "test", Raw: "disk full"},
	}}
	out := &mockOutput{}
	proc := &categoryProcessor{failOn: "BAD"}
	d := dedup.New(dedup.Config{Window: time.Second})

	p := New(conn, proc, out, WithDedup(d, 50*time.Millisecond))

	err := p.Stream(context.Background(), connector.ConnectorConfig{})
	if err != nil {
		t.Fatalf("expected nil error (channel close), got: %v", err)
	}

	events := out.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 skipped), got %d", len(events))
	}
	if p.skippedLogs.Load() != 1 {
		t.Errorf("expected 1 skipped log, got %d", p.skippedLogs.Load())
	}
}

func TestStreamWithDedup_WrittenEventsCounter(t *testing.T) {
	t0 := time.Now()
	conn := &mockConnector{logs: []model.RawLog{
		{Timestamp: t0, Source: "test", Raw: "event-a"},
		{Timestamp: t0, Source: "test", Raw: "event-b"},
		{Timestamp: t0, Source: "test", Raw: "event-c"},
	}}
	out := &mockOutput{}
	proc := &categoryProcessor{}
	d := dedup.New(dedup.Config{Window: time.Second})

	p := New(conn, proc, out, WithDedup(d, 50*time.Millisecond))

	err := p.Stream(context.Background(), connector.ConnectorConfig{})
	if err != nil {
		t.Fatalf("expected nil error (channel close), got: %v", err)
	}

	events := out.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if p.writtenEvents.Load() != 3 {
		t.Errorf("expected writtenEvents=3, got %d", p.writtenEvents.Load())
	}
}

func TestQuery_BatchFallback(t *testing.T) {
	t0 := time.Now()
	conn := &mockConnector{logs: []model.RawLog{
		{Timestamp: t0, Source: "test", Raw: "good log 1"},
		{Timestamp: t0, Source: "test", Raw: "BAD"},
		{Timestamp: t0, Source: "test", Raw: "good log 2"},
	}}
	out := &mockOutput{}
	proc := &mockProcessor{failOn: "BAD"}

	p := New(conn, proc, out)

	err := p.Query(context.Background(), connector.ConnectorConfig{}, connector.QueryParams{})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// ProcessBatch fails because of "BAD", falls back to individual processing.
	// Individual processing skips "BAD", keeps the 2 good ones.
	events := out.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events after fallback, got %d", len(events))
	}
	if p.skippedLogs.Load() != 1 {
		t.Errorf("expected 1 skipped log, got %d", p.skippedLogs.Load())
	}
}

func TestSkipCounter(t *testing.T) {
	t0 := time.Now()
	conn := &mockConnector{logs: []model.RawLog{
		{Timestamp: t0, Source: "test", Raw: "good"},
		{Timestamp: t0, Source: "test", Raw: "BAD"},
		{Timestamp: t0, Source: "test", Raw: "BAD"},
		{Timestamp: t0, Source: "test", Raw: "BAD"},
		{Timestamp: t0, Source: "test", Raw: "good"},
	}}
	out := &mockOutput{}
	proc := &mockProcessor{failOn: "BAD"}

	p := New(conn, proc, out)

	if err := p.Stream(context.Background(), connector.ConnectorConfig{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.skippedLogs.Load() != 3 {
		t.Fatalf("expected 3 skipped logs, got %d", p.skippedLogs.Load())
	}

	events := out.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 good events, got %d", len(events))
	}

	// Close reports skip count (tested via no error).
	if err := p.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}
}

// --- bounded buffer tests ---

func TestStreamBuffer_MaxSizeFlush(t *testing.T) {
	out := &mockOutput{}
	d := dedup.New(dedup.Config{Window: time.Second})
	buf := newStreamBuffer(d, out, 10*time.Second, 5, nil) // long timer, maxSize=5

	t0 := time.Now()
	for i := 0; i < 4; i++ {
		full := buf.add(model.CanonicalEvent{
			Type: "ERROR", Category: "timeout", Timestamp: t0, Summary: "x",
		})
		if full {
			t.Fatalf("add() returned true at %d events, expected false (maxSize=5)", i+1)
		}
	}
	// 5th event should signal full.
	full := buf.add(model.CanonicalEvent{
		Type: "ERROR", Category: "timeout", Timestamp: t0, Summary: "x",
	})
	if !full {
		t.Fatal("add() should return true when buffer reaches maxSize")
	}
}

func TestStreamBuffer_MaxSizeNoDataLoss(t *testing.T) {
	out := &mockOutput{}
	d := dedup.New(dedup.Config{Window: 10 * time.Second})
	buf := newStreamBuffer(d, out, 10*time.Second, 3, nil) // maxSize=3

	t0 := time.Now()
	// Add 3 distinct events — buffer full.
	buf.add(model.CanonicalEvent{Type: "ERROR", Category: "a", Timestamp: t0, Summary: "a"})
	buf.add(model.CanonicalEvent{Type: "ERROR", Category: "b", Timestamp: t0, Summary: "b"})
	buf.add(model.CanonicalEvent{Type: "ERROR", Category: "c", Timestamp: t0, Summary: "c"})

	// Force flush.
	if err := buf.flush(context.Background()); err != nil {
		t.Fatalf("flush error: %v", err)
	}

	// Add 2 more.
	buf.add(model.CanonicalEvent{Type: "ERROR", Category: "d", Timestamp: t0, Summary: "d"})
	buf.add(model.CanonicalEvent{Type: "ERROR", Category: "e", Timestamp: t0, Summary: "e"})

	if err := buf.flush(context.Background()); err != nil {
		t.Fatalf("flush error: %v", err)
	}

	events := out.Events()
	if len(events) != 5 {
		t.Fatalf("expected 5 total events (3 + 2), got %d", len(events))
	}
}

func TestStreamBuffer_UnlimitedBackcompat(t *testing.T) {
	out := &mockOutput{}
	d := dedup.New(dedup.Config{Window: time.Second})
	buf := newStreamBuffer(d, out, 10*time.Second, 0, nil) // maxSize=0 → unlimited

	t0 := time.Now()
	for i := 0; i < 10000; i++ {
		full := buf.add(model.CanonicalEvent{
			Type: "ERROR", Category: "timeout", Timestamp: t0, Summary: "x",
		})
		if full {
			t.Fatalf("add() returned true at %d events with maxSize=0 (unlimited)", i+1)
		}
	}
}
