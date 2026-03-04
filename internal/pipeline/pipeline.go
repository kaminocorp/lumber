package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/kaminocorp/lumber/internal/connector"
	"github.com/kaminocorp/lumber/internal/engine/dedup"
	"github.com/kaminocorp/lumber/internal/model"
	"github.com/kaminocorp/lumber/internal/output"
)

// Processor handles log classification and compaction.
type Processor interface {
	Process(raw model.RawLog) (model.CanonicalEvent, error)
	ProcessBatch(raws []model.RawLog) ([]model.CanonicalEvent, error)
}

// Pipeline connects a connector, engine, and output into a processing pipeline.
type Pipeline struct {
	connector     connector.Connector
	engine        Processor
	output        output.Output
	dedup         *dedup.Deduplicator
	window        time.Duration
	maxBufferSize int
	skippedLogs   atomic.Int64
	writtenEvents atomic.Int64
}

// Option configures a Pipeline.
type Option func(*Pipeline)

// WithDedup enables event deduplication with the given Deduplicator and window.
func WithDedup(d *dedup.Deduplicator, window time.Duration) Option {
	return func(p *Pipeline) {
		p.dedup = d
		p.window = window
	}
}

// WithMaxBufferSize sets the maximum number of events buffered before a force flush.
// 0 means unlimited (default).
func WithMaxBufferSize(n int) Option {
	return func(p *Pipeline) {
		p.maxBufferSize = n
	}
}

// New creates a Pipeline from the given components.
func New(conn connector.Connector, eng Processor, out output.Output, opts ...Option) *Pipeline {
	p := &Pipeline{
		connector: conn,
		engine:    eng,
		output:    out,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Stream starts the pipeline in streaming mode, processing logs as they arrive.
// Blocks until the context is cancelled or an error occurs.
func (p *Pipeline) Stream(ctx context.Context, cfg connector.ConnectorConfig) error {
	ch, err := p.connector.Stream(ctx, cfg)
	if err != nil {
		return fmt.Errorf("pipeline stream: %w", err)
	}

	if p.dedup != nil {
		return p.streamWithDedup(ctx, ch)
	}
	return p.streamDirect(ctx, ch)
}

// streamDirect writes events directly without dedup.
func (p *Pipeline) streamDirect(ctx context.Context, ch <-chan model.RawLog) error {
	for {
		select {
		case <-ctx.Done():
			if skipped := p.skippedLogs.Load(); skipped > 0 {
				slog.Info("stream stopped", "skipped_logs", skipped)
			}
			return ctx.Err()
		case raw, ok := <-ch:
			if !ok {
				if skipped := p.skippedLogs.Load(); skipped > 0 {
					slog.Info("stream ended", "skipped_logs", skipped)
				}
				return nil
			}
			event, err := p.engine.Process(raw)
			if err != nil {
				p.skippedLogs.Add(1)
				slog.Warn("skipping log", "error", err, "source", raw.Source)
				continue
			}
			if err := p.output.Write(ctx, event); err != nil {
				return fmt.Errorf("pipeline output: %w", err)
			}
			p.writtenEvents.Add(1)
		}
	}
}

// streamWithDedup buffers events and flushes deduplicated batches on a timer.
func (p *Pipeline) streamWithDedup(ctx context.Context, ch <-chan model.RawLog) error {
	buf := newStreamBuffer(p.dedup, p.output, p.window, p.maxBufferSize, func() {
		p.writtenEvents.Add(1)
	})

	for {
		select {
		case <-ctx.Done():
			if skipped := p.skippedLogs.Load(); skipped > 0 {
				slog.Info("stream stopped", "skipped_logs", skipped)
			}
			// Use background context so writes can complete during drain.
			// The shutdown timer in main.go provides the hard bound.
			if err := buf.flush(context.Background()); err != nil {
				return fmt.Errorf("pipeline flush on shutdown: %w", err)
			}
			return ctx.Err()
		case raw, ok := <-ch:
			if !ok {
				if skipped := p.skippedLogs.Load(); skipped > 0 {
					slog.Info("stream ended", "skipped_logs", skipped)
				}
				// Channel closed — flush remaining.
				return buf.flush(ctx)
			}
			event, err := p.engine.Process(raw)
			if err != nil {
				p.skippedLogs.Add(1)
				slog.Warn("skipping log", "error", err, "source", raw.Source)
				continue
			}
			if buf.add(event) {
				// Buffer full — force early flush.
				if err := buf.flush(ctx); err != nil {
					return fmt.Errorf("pipeline flush (buffer full): %w", err)
				}
			}
		case <-buf.flushCh():
			if err := buf.flush(ctx); err != nil {
				return fmt.Errorf("pipeline flush: %w", err)
			}
		}
	}
}

// Query runs the pipeline in one-shot query mode.
func (p *Pipeline) Query(ctx context.Context, cfg connector.ConnectorConfig, params connector.QueryParams) error {
	raws, err := p.connector.Query(ctx, cfg, params)
	if err != nil {
		return fmt.Errorf("pipeline query: %w", err)
	}

	events, err := p.engine.ProcessBatch(raws)
	if err != nil {
		slog.Warn("batch processing failed, falling back to individual", "error", err, "count", len(raws))
		events = p.processIndividual(raws)
	}

	if p.dedup != nil {
		events = p.dedup.DeduplicateBatch(events)
	}

	for _, event := range events {
		if err := p.output.Write(ctx, event); err != nil {
			return fmt.Errorf("pipeline output: %w", err)
		}
		p.writtenEvents.Add(1)
	}
	return nil
}

// processIndividual processes logs one at a time, skipping failures.
func (p *Pipeline) processIndividual(raws []model.RawLog) []model.CanonicalEvent {
	var events []model.CanonicalEvent
	for _, raw := range raws {
		event, err := p.engine.Process(raw)
		if err != nil {
			p.skippedLogs.Add(1)
			slog.Warn("skipping log in query", "error", err, "source", raw.Source)
			continue
		}
		events = append(events, event)
	}
	return events
}

// Close shuts down the output.
func (p *Pipeline) Close() error {
	written := p.writtenEvents.Load()
	skipped := p.skippedLogs.Load()
	if written > 0 || skipped > 0 {
		slog.Info("pipeline closing", "total_events_written", written, "total_skipped_logs", skipped)
	}
	return p.output.Close()
}
