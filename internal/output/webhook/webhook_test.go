package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaminocorp/lumber/internal/model"
)

func testEvent(cat string) model.CanonicalEvent {
	return model.CanonicalEvent{
		Type:      "REQUEST",
		Category:  cat,
		Severity:  "info",
		Timestamp: time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC),
		Summary:   "test." + cat,
	}
}

func TestBatchFlushAtBatchSize(t *testing.T) {
	var mu sync.Mutex
	var received [][]model.CanonicalEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []model.CanonicalEvent
		json.Unmarshal(body, &batch)
		mu.Lock()
		received = append(received, batch)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	out := New(srv.URL, WithBatchSize(3), WithFlushInterval(10*time.Second))

	for i := 0; i < 3; i++ {
		out.Write(context.Background(), testEvent("success"))
	}

	// Give the POST a moment to complete.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(received))
	}
	if len(received[0]) != 3 {
		t.Errorf("batch size = %d, want 3", len(received[0]))
	}
}

func TestTimerFlushBeforeBatchSize(t *testing.T) {
	var mu sync.Mutex
	var received [][]model.CanonicalEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []model.CanonicalEvent
		json.Unmarshal(body, &batch)
		mu.Lock()
		received = append(received, batch)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	out := New(srv.URL, WithBatchSize(100), WithFlushInterval(100*time.Millisecond))

	out.Write(context.Background(), testEvent("timer"))

	// Wait for the timer to fire.
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 timer-triggered batch, got %d", len(received))
	}
	if len(received[0]) != 1 {
		t.Errorf("batch size = %d, want 1", len(received[0]))
	}
}

func TestRetryOn5xx(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	out := New(srv.URL, WithBatchSize(1))
	out.Write(context.Background(), testEvent("retry"))

	// Wait for retries to complete.
	time.Sleep(5 * time.Second)

	if attempts.Load() < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts.Load())
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(400)
	}))
	defer srv.Close()

	out := New(srv.URL, WithBatchSize(1))
	err := out.Write(context.Background(), testEvent("client-error"))

	time.Sleep(200 * time.Millisecond)

	if err == nil {
		t.Error("expected error for 400 response")
	}
	if attempts.Load() != 1 {
		t.Errorf("expected exactly 1 attempt for 4xx, got %d", attempts.Load())
	}
}

func TestCustomHeaders(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Custom-Auth")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	out := New(srv.URL,
		WithBatchSize(1),
		WithHeaders(map[string]string{"X-Custom-Auth": "secret123"}),
	)

	out.Write(context.Background(), testEvent("headers"))
	time.Sleep(100 * time.Millisecond)

	if gotAuth != "secret123" {
		t.Errorf("custom header = %q, want secret123", gotAuth)
	}
}

func TestTimerFlushErrorCallbackInvoked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()

	var errCount atomic.Int64
	out := New(srv.URL,
		WithBatchSize(100),
		WithFlushInterval(50*time.Millisecond),
		WithOnError(func(err error) { errCount.Add(1) }),
	)

	out.Write(context.Background(), testEvent("timer-error"))

	// Wait for timer-triggered flush + HTTP round-trip.
	time.Sleep(300 * time.Millisecond)

	if errCount.Load() != 1 {
		t.Errorf("expected error callback called 1 time, got %d", errCount.Load())
	}

	out.Close()
}

func TestCloseFlushesRemaining(t *testing.T) {
	var mu sync.Mutex
	var received [][]model.CanonicalEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []model.CanonicalEvent
		json.Unmarshal(body, &batch)
		mu.Lock()
		received = append(received, batch)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	out := New(srv.URL, WithBatchSize(100), WithFlushInterval(10*time.Second))

	out.Write(context.Background(), testEvent("close-flush"))
	out.Write(context.Background(), testEvent("close-flush"))

	out.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 batch on Close, got %d", len(received))
	}
	if len(received[0]) != 2 {
		t.Errorf("batch size = %d, want 2", len(received[0]))
	}
}
