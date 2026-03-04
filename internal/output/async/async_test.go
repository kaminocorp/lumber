package async

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/model"
)

type mockOutput struct {
	mu     sync.Mutex
	events []model.CanonicalEvent
	closed bool
	err    error         // if set, Write returns this
	delay  time.Duration // if >0, Write sleeps first
}

func (m *mockOutput) Write(_ context.Context, event model.CanonicalEvent) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
	return m.err
}

func (m *mockOutput) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}

func (m *mockOutput) eventCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func testEvent(cat string) model.CanonicalEvent {
	return model.CanonicalEvent{
		Type:     "REQUEST",
		Category: cat,
		Severity: "info",
	}
}

func TestEventsFlowThrough(t *testing.T) {
	inner := &mockOutput{}
	a := New(inner, WithBufferSize(16))

	for i := 0; i < 10; i++ {
		if err := a.Write(context.Background(), testEvent("success")); err != nil {
			t.Fatalf("Write error: %v", err)
		}
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	if inner.eventCount() != 10 {
		t.Errorf("got %d events, want 10", inner.eventCount())
	}
}

func TestBackpressureBlocks(t *testing.T) {
	// Inner output is slow; buffer size is 1.
	inner := &mockOutput{delay: 50 * time.Millisecond}
	a := New(inner, WithBufferSize(1))

	// First write fills the buffer.
	a.Write(context.Background(), testEvent("first"))

	// Second write should block until the drain goroutine consumes the first.
	done := make(chan struct{})
	go func() {
		a.Write(context.Background(), testEvent("second"))
		close(done)
	}()

	select {
	case <-done:
		// Unblocked eventually — that's correct.
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked indefinitely (expected eventual unblock via drain)")
	}

	a.Close()
}

func TestDropOnFull(t *testing.T) {
	// Slow inner output + tiny buffer + drop mode.
	inner := &mockOutput{delay: 100 * time.Millisecond}
	a := New(inner, WithBufferSize(1), WithDropOnFull())

	// Rapid-fire writes. Some will be dropped.
	for i := 0; i < 20; i++ {
		a.Write(context.Background(), testEvent("burst"))
	}

	a.Close()

	// Not all 20 events should have arrived (some were dropped).
	if inner.eventCount() == 20 {
		t.Error("expected some events to be dropped in drop-on-full mode")
	}
	if inner.eventCount() == 0 {
		t.Error("expected at least some events to be delivered")
	}
}

func TestCloseDrainsRemaining(t *testing.T) {
	inner := &mockOutput{}
	a := New(inner, WithBufferSize(100))

	for i := 0; i < 50; i++ {
		a.Write(context.Background(), testEvent("drain"))
	}

	a.Close()

	if inner.eventCount() != 50 {
		t.Errorf("after Close, got %d events, want 50 (drain incomplete)", inner.eventCount())
	}
}

func TestErrorCallbackInvoked(t *testing.T) {
	inner := &mockOutput{err: errors.New("write failed")}
	var errorCount atomic.Int64
	a := New(inner, WithBufferSize(16), WithOnError(func(err error) {
		errorCount.Add(1)
	}))

	for i := 0; i < 5; i++ {
		a.Write(context.Background(), testEvent("failing"))
	}

	a.Close()

	if errorCount.Load() != 5 {
		t.Errorf("error callback called %d times, want 5", errorCount.Load())
	}
}

func TestNoGoroutineLeakAfterClose(t *testing.T) {
	inner := &mockOutput{}
	a := New(inner, WithBufferSize(16))

	a.Write(context.Background(), testEvent("leak-check"))
	a.Close()

	// The done channel should be closed, indicating the drain goroutine exited.
	select {
	case <-a.done:
		// Good — goroutine finished.
	case <-time.After(time.Second):
		t.Fatal("drain goroutine did not exit after Close")
	}
}

func TestCloseIdempotent(t *testing.T) {
	inner := &mockOutput{}
	a := New(inner, WithBufferSize(16))

	a.Write(context.Background(), testEvent("idempotent"))

	// Close twice should not panic.
	if err := a.Close(); err != nil {
		t.Fatalf("first Close error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close error: %v", err)
	}
}
