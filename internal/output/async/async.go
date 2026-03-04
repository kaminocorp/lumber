package async

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kaminocorp/lumber/internal/model"
	"github.com/kaminocorp/lumber/internal/output"
)

const (
	defaultBufferSize  = 1024
	defaultDrainTimeout = 5 * time.Second
)

// Option configures an Async wrapper.
type Option func(*Async)

// WithBufferSize sets the channel buffer capacity. Default: 1024.
func WithBufferSize(n int) Option {
	return func(a *Async) { a.bufSize = n }
}

// WithOnError sets the callback invoked when the inner output's Write fails.
// Default: logs a warning via slog.
func WithOnError(f func(error)) Option {
	return func(a *Async) { a.errFunc = f }
}

// WithDropOnFull makes Write return immediately (dropping the event) when the
// buffer is full, instead of blocking. Use for outputs where lossiness is
// acceptable (e.g., a non-critical webhook).
func WithDropOnFull() Option {
	return func(a *Async) { a.dropOnFull = true }
}

// Async decouples event production from consumption via a buffered channel.
// The pipeline writes into the channel; a background goroutine drains it
// to the wrapped output. Errors from the inner output are passed to errFunc
// rather than propagated to the caller.
type Async struct {
	inner      output.Output
	ch         chan model.CanonicalEvent
	done       chan struct{}
	errFunc    func(error)
	bufSize    int
	dropOnFull bool
	closeOnce  sync.Once
}

// New wraps an output.Output in an async channel-based writer.
// The background drain goroutine starts immediately.
func New(inner output.Output, opts ...Option) *Async {
	a := &Async{
		inner:   inner,
		bufSize: defaultBufferSize,
		errFunc: func(err error) { slog.Warn("async output write error", "error", err) },
	}
	for _, opt := range opts {
		opt(a)
	}
	a.ch = make(chan model.CanonicalEvent, a.bufSize)
	a.done = make(chan struct{})
	go a.drain()
	return a
}

// Write sends the event into the channel. By default, blocks if the channel
// is full (backpressure). With WithDropOnFull, returns nil immediately and
// the event is lost.
func (a *Async) Write(_ context.Context, event model.CanonicalEvent) error {
	if a.dropOnFull {
		select {
		case a.ch <- event:
		default:
			slog.Warn("async output buffer full, dropping event",
				"type", event.Type, "category", event.Category)
		}
		return nil
	}
	a.ch <- event
	return nil
}

// Close closes the channel, waits for the drain goroutine to finish
// (with a timeout), then closes the inner output.
func (a *Async) Close() error {
	var err error
	a.closeOnce.Do(func() {
		close(a.ch)
		select {
		case <-a.done:
		case <-time.After(defaultDrainTimeout):
			slog.Warn("async output drain timed out")
		}
		err = a.inner.Close()
	})
	return err
}

// drain reads events from the channel and writes them to the inner output.
func (a *Async) drain() {
	defer close(a.done)
	for event := range a.ch {
		if err := a.inner.Write(context.Background(), event); err != nil {
			a.errFunc(err)
		}
	}
}
