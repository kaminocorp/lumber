package stdout

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/model"
)

func testEvent() model.CanonicalEvent {
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

// captureStdout redirects os.Stdout to capture output.
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestOutputCompactJSON(t *testing.T) {
	result := captureStdout(func() {
		out := New(compactor.Standard, false)
		out.Write(context.Background(), testEvent())
	})

	// Should be single line (NDJSON).
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	// Should be valid JSON with lowercase keys.
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["type"] != "ERROR" {
		t.Fatalf("expected type=ERROR, got %v", m["type"])
	}
}

func TestOutputPrettyJSON(t *testing.T) {
	result := captureStdout(func() {
		out := New(compactor.Standard, true)
		out.Write(context.Background(), testEvent())
	})

	// Pretty JSON should have multiple lines with indentation.
	if !strings.Contains(result, "  ") {
		t.Fatal("expected indented output for pretty mode")
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected multi-line pretty output, got %d lines", len(lines))
	}
}

func TestOutputMinimalOmitsFields(t *testing.T) {
	result := captureStdout(func() {
		out := New(compactor.Minimal, false)
		out.Write(context.Background(), testEvent())
	})

	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result)), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Raw and Confidence should be omitted at Minimal.
	if _, ok := m["raw"]; ok {
		t.Fatal("raw should be omitted at Minimal")
	}
	if _, ok := m["confidence"]; ok {
		t.Fatal("confidence should be omitted at Minimal")
	}
	// Core fields should be present.
	if m["type"] != "ERROR" {
		t.Fatalf("type should be preserved, got %v", m["type"])
	}
}
