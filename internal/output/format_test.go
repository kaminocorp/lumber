package output

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/model"
)

func baseEvent() model.CanonicalEvent {
	return model.CanonicalEvent{
		Type:       "ERROR",
		Category:   "connection_failure",
		Severity:   "error",
		Timestamp:  time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC),
		Summary:    "connection refused",
		Confidence: 0.91,
		Raw:        `{"level":"error","msg":"connection refused"}`,
	}
}

func TestFormatEventMinimal(t *testing.T) {
	e := FormatEvent(baseEvent(), compactor.Minimal)

	if e.Raw != "" {
		t.Fatal("Raw should be empty at Minimal")
	}
	if e.Confidence != 0 {
		t.Fatal("Confidence should be 0 at Minimal")
	}
	if e.Type != "ERROR" {
		t.Fatal("Type should be preserved")
	}
	if e.Summary != "connection refused" {
		t.Fatal("Summary should be preserved")
	}
}

func TestFormatEventStandard(t *testing.T) {
	e := FormatEvent(baseEvent(), compactor.Standard)

	if e.Raw == "" {
		t.Fatal("Raw should be preserved at Standard")
	}
	if e.Confidence != 0.91 {
		t.Fatal("Confidence should be preserved at Standard")
	}
}

func TestFormatEventFull(t *testing.T) {
	e := FormatEvent(baseEvent(), compactor.Full)

	if e.Raw == "" {
		t.Fatal("Raw should be preserved at Full")
	}
	if e.Confidence != 0.91 {
		t.Fatal("Confidence should be preserved at Full")
	}
}

func TestFormatEventCount(t *testing.T) {
	e := baseEvent()
	e.Count = 5
	formatted := FormatEvent(e, compactor.Standard)

	data, err := json.Marshal(formatted)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if m["count"] != float64(5) {
		t.Fatalf("expected count=5, got %v", m["count"])
	}

	// Count == 0 should be omitted.
	e.Count = 0
	formatted = FormatEvent(e, compactor.Standard)
	data, _ = json.Marshal(formatted)
	m = nil
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := m["count"]; ok {
		t.Fatal("count=0 should be omitted from JSON")
	}
}

func TestJSONTagNames(t *testing.T) {
	e := baseEvent()
	e.Count = 3
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	expected := []string{"type", "category", "severity", "timestamp", "summary", "confidence", "raw", "count"}
	for _, key := range expected {
		if _, ok := m[key]; !ok {
			t.Fatalf("expected lowercase key %q in JSON", key)
		}
	}

	// Ensure no uppercase keys.
	for key := range m {
		if key != key {
			t.Fatalf("unexpected key format: %q", key)
		}
	}
}
