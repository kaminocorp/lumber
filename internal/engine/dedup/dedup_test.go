package dedup

import (
	"strings"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/model"
)

var t0 = time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC)

func event(typ, cat string, offset time.Duration) model.CanonicalEvent {
	return model.CanonicalEvent{
		Type:      typ,
		Category:  cat,
		Severity:  "error",
		Timestamp: t0.Add(offset),
		Summary:   typ + "." + cat,
	}
}

func TestDeduplicateBatchEmpty(t *testing.T) {
	d := New(Config{Window: 5 * time.Second})
	result := d.DeduplicateBatch(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestDeduplicateBatchNoDuplicates(t *testing.T) {
	d := New(Config{Window: 5 * time.Second})
	events := []model.CanonicalEvent{
		event("ERROR", "connection_failure", 0),
		event("ERROR", "timeout", time.Second),
		event("REQUEST", "success", 2*time.Second),
	}
	result := d.DeduplicateBatch(events)
	if len(result) != 3 {
		t.Fatalf("expected 3 events, got %d", len(result))
	}
	// Count should not be set on non-deduplicated events.
	for _, e := range result {
		if e.Count != 0 {
			t.Fatalf("expected Count=0 for non-deduped event, got %d", e.Count)
		}
	}
}

func TestDeduplicateBatchSimple(t *testing.T) {
	d := New(Config{Window: 5 * time.Second})
	var events []model.CanonicalEvent
	for i := 0; i < 5; i++ {
		events = append(events, event("ERROR", "connection_failure", time.Duration(i)*time.Second))
	}

	result := d.DeduplicateBatch(events)
	if len(result) != 1 {
		t.Fatalf("expected 1 deduplicated event, got %d", len(result))
	}
	if result[0].Count != 5 {
		t.Fatalf("expected Count=5, got %d", result[0].Count)
	}
}

func TestDeduplicateBatchMixed(t *testing.T) {
	d := New(Config{Window: 5 * time.Second})
	events := []model.CanonicalEvent{
		event("ERROR", "connection_failure", 0),              // A
		event("REQUEST", "success", 500*time.Millisecond),    // B
		event("ERROR", "connection_failure", time.Second),     // A
		event("ERROR", "connection_failure", 2*time.Second),   // A
		event("REQUEST", "success", 3*time.Second),            // B
	}

	result := d.DeduplicateBatch(events)
	if len(result) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(result))
	}
	// First group: A (x3)
	if result[0].Type != "ERROR" || result[0].Count != 3 {
		t.Fatalf("expected ERROR x3, got %s x%d", result[0].Type, result[0].Count)
	}
	// Second group: B (x2)
	if result[1].Type != "REQUEST" || result[1].Count != 2 {
		t.Fatalf("expected REQUEST x2, got %s x%d", result[1].Type, result[1].Count)
	}
}

func TestDeduplicateBatchWindowExpiry(t *testing.T) {
	d := New(Config{Window: 5 * time.Second})
	events := []model.CanonicalEvent{
		event("ERROR", "timeout", 0),
		event("ERROR", "timeout", 2*time.Second),
		event("ERROR", "timeout", 4*time.Second),
		// Gap > 5s from first event.
		event("ERROR", "timeout", 10*time.Second),
		event("ERROR", "timeout", 12*time.Second),
	}

	result := d.DeduplicateBatch(events)
	if len(result) != 2 {
		t.Fatalf("expected 2 groups (window expiry), got %d", len(result))
	}
	if result[0].Count != 3 {
		t.Fatalf("first group: expected Count=3, got %d", result[0].Count)
	}
	if result[1].Count != 2 {
		t.Fatalf("second group: expected Count=2, got %d", result[1].Count)
	}
}

func TestDeduplicateBatchSummaryFormat(t *testing.T) {
	d := New(Config{Window: 10 * time.Minute})
	var events []model.CanonicalEvent
	for i := 0; i < 47; i++ {
		e := event("ERROR", "connection_failure", time.Duration(i)*6*time.Second)
		e.Summary = "connection refused"
		events = append(events, e)
	}

	result := d.DeduplicateBatch(events)
	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	if !strings.Contains(result[0].Summary, "(x47") {
		t.Fatalf("expected summary with (x47...), got %q", result[0].Summary)
	}
	if !strings.Contains(result[0].Summary, "connection refused") {
		t.Fatalf("expected original summary preserved, got %q", result[0].Summary)
	}
}

func TestDeduplicateBatchPreservesTimestamp(t *testing.T) {
	d := New(Config{Window: 5 * time.Second})
	events := []model.CanonicalEvent{
		event("ERROR", "timeout", 0),
		event("ERROR", "timeout", time.Second),
		event("ERROR", "timeout", 2*time.Second),
	}

	result := d.DeduplicateBatch(events)
	// Should use first event's timestamp.
	if !result[0].Timestamp.Equal(t0) {
		t.Fatalf("expected first timestamp %v, got %v", t0, result[0].Timestamp)
	}
}
