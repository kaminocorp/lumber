package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/engine/compactor"
	"github.com/kaminocorp/lumber/internal/model"
)

func testEvent(typ, cat string) model.CanonicalEvent {
	return model.CanonicalEvent{
		Type:       typ,
		Category:   cat,
		Severity:   "info",
		Timestamp:  time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC),
		Summary:    typ + "." + cat,
		Confidence: 0.95,
		Raw:        "raw log line",
	}
}

func TestWriteProducesValidNDJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	out, err := New(path, compactor.Standard)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := out.Write(context.Background(), testEvent("REQUEST", "success")); err != nil {
			t.Fatalf("Write error: %v", err)
		}
	}
	out.Close()

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5", len(lines))
	}
	for i, line := range lines {
		var ev model.CanonicalEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
		}
		if ev.Category != "success" {
			t.Errorf("line %d: category = %q, want success", i, ev.Category)
		}
	}
}

func TestRotationTriggersAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")

	// MaxSize of 200 bytes — each JSON line is ~130 bytes, so rotation after ~1 line.
	out, err := New(path, compactor.Standard, WithMaxSize(200))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := out.Write(context.Background(), testEvent("ERROR", "timeout")); err != nil {
			t.Fatalf("Write error: %v", err)
		}
	}
	out.Close()

	// Rotated file should exist.
	if _, err := os.Stat(path + ".1"); os.IsNotExist(err) {
		t.Error("expected rotated file .1 to exist")
	}

	// Current file should also exist and have data.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("current file stat error: %v", err)
	}
	if info.Size() == 0 {
		t.Error("current file is empty after rotation")
	}
}

func TestCloseFlushesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	out, err := New(path, compactor.Standard)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	out.Write(context.Background(), testEvent("DEPLOY", "build_succeeded"))
	out.Close()

	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Error("file is empty — Close did not flush buffered data")
	}
}

func TestVerbosityMinimalStripsFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	out, err := New(path, compactor.Minimal)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	out.Write(context.Background(), testEvent("ERROR", "timeout"))
	out.Close()

	data, _ := os.ReadFile(path)
	var ev map[string]any
	json.Unmarshal([]byte(strings.TrimSpace(string(data))), &ev)

	if _, ok := ev["raw"]; ok {
		t.Error("Minimal verbosity should strip 'raw' field")
	}
	if _, ok := ev["confidence"]; ok {
		t.Error("Minimal verbosity should strip 'confidence' field")
	}
}

func TestConcurrentWritesSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	out, err := New(path, compactor.Standard)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out.Write(context.Background(), testEvent("REQUEST", "success"))
		}()
	}
	wg.Wait()
	out.Close()

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 50 {
		t.Errorf("got %d lines, want 50", len(lines))
	}
}
