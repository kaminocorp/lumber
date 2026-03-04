package multi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/model"
)

// mockOutput records calls for test assertions.
type mockOutput struct {
	events []model.CanonicalEvent
	closed bool
	err    error // if set, Write returns this error
}

func (m *mockOutput) Write(_ context.Context, event model.CanonicalEvent) error {
	m.events = append(m.events, event)
	return m.err
}

func (m *mockOutput) Close() error {
	m.closed = true
	return m.err
}

func testEvent(typ, cat string) model.CanonicalEvent {
	return model.CanonicalEvent{
		Type:      typ,
		Category:  cat,
		Severity:  "info",
		Timestamp: time.Now(),
		Summary:   typ + "." + cat,
	}
}

func TestFanOutDeliversToAll(t *testing.T) {
	a := &mockOutput{}
	b := &mockOutput{}
	c := &mockOutput{}
	m := New(a, b, c)

	ev := testEvent("REQUEST", "success")
	if err := m.Write(context.Background(), ev); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, out := range []*mockOutput{a, b, c} {
		if len(out.events) != 1 {
			t.Errorf("output %d: got %d events, want 1", i, len(out.events))
		}
		if out.events[0].Category != "success" {
			t.Errorf("output %d: got category %q, want %q", i, out.events[0].Category, "success")
		}
	}
}

func TestErrorDoesNotPreventDelivery(t *testing.T) {
	failing := &mockOutput{err: errors.New("disk full")}
	healthy := &mockOutput{}
	m := New(failing, healthy)

	ev := testEvent("ERROR", "connection_failure")
	err := m.Write(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Healthy output still received the event despite earlier failure.
	if len(healthy.events) != 1 {
		t.Fatalf("healthy output got %d events, want 1", len(healthy.events))
	}

	// Failing output also received the call (error returned after).
	if len(failing.events) != 1 {
		t.Fatalf("failing output got %d events, want 1", len(failing.events))
	}
}

func TestCloseCallsAllOutputs(t *testing.T) {
	a := &mockOutput{}
	b := &mockOutput{}
	m := New(a, b)

	if err := m.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !a.closed || !b.closed {
		t.Errorf("Close not called on all outputs: a=%v b=%v", a.closed, b.closed)
	}
}

func TestCloseCollectsErrors(t *testing.T) {
	a := &mockOutput{err: errors.New("err-a")}
	b := &mockOutput{err: errors.New("err-b")}
	m := New(a, b)

	err := m.Close()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !a.closed || !b.closed {
		t.Error("Close should be called on all outputs even when errors occur")
	}
}

func TestSingleOutputIdentity(t *testing.T) {
	inner := &mockOutput{}
	m := New(inner)

	ev := testEvent("DEPLOY", "build_succeeded")
	if err := m.Write(context.Background(), ev); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(inner.events) != 1 || inner.events[0].Category != "build_succeeded" {
		t.Error("single-output Multi did not behave identically to wrapped output")
	}
	if !inner.closed {
		t.Error("single-output Multi did not close inner output")
	}
}
