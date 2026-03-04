package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/kaminocorp/lumber/internal/engine/dedup"
	"github.com/kaminocorp/lumber/internal/model"
	"github.com/kaminocorp/lumber/internal/output"
)

// streamBuffer accumulates events and flushes deduplicated batches on a timer.
type streamBuffer struct {
	dedup   *dedup.Deduplicator
	out     output.Output
	window  time.Duration
	maxSize int // 0 means unlimited (backward compat)
	onWrite func() // called after each successful Write

	mu      sync.Mutex
	pending []model.CanonicalEvent
	timer   *time.Timer
}

func newStreamBuffer(d *dedup.Deduplicator, out output.Output, window time.Duration, maxSize int, onWrite func()) *streamBuffer {
	return &streamBuffer{
		dedup:   d,
		out:     out,
		window:  window,
		maxSize: maxSize,
		onWrite: onWrite,
	}
}

// add appends an event to the buffer. If this is the first event, starts the flush timer.
// Returns true if the buffer is full and needs flushing.
func (b *streamBuffer) add(event model.CanonicalEvent) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pending = append(b.pending, event)
	if len(b.pending) == 1 {
		// First event — start timer.
		b.timer = time.NewTimer(b.window)
	}
	return b.maxSize > 0 && len(b.pending) >= b.maxSize
}

// flushCh returns the timer's channel, or nil if no timer is active.
func (b *streamBuffer) flushCh() <-chan time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer == nil {
		return nil
	}
	return b.timer.C
}

// flush deduplicates and writes all pending events.
func (b *streamBuffer) flush(ctx context.Context) error {
	b.mu.Lock()
	events := b.pending
	b.pending = nil
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()

	if len(events) == 0 {
		return nil
	}

	deduped := b.dedup.DeduplicateBatch(events)
	for _, e := range deduped {
		if err := b.out.Write(ctx, e); err != nil {
			return err
		}
		if b.onWrite != nil {
			b.onWrite()
		}
	}
	return nil
}
