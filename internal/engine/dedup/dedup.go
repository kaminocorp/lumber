package dedup

import (
	"fmt"
	"time"

	"github.com/kaminocorp/lumber/internal/model"
)

// Config controls deduplication behavior.
type Config struct {
	Window time.Duration // grouping window (default 5s)
}

// Deduplicator collapses identical event types within a time window.
type Deduplicator struct {
	cfg Config
}

// New creates a Deduplicator with the given config.
func New(cfg Config) *Deduplicator {
	return &Deduplicator{cfg: cfg}
}

// group accumulates events with the same dedup key.
type group struct {
	event    model.CanonicalEvent
	count    int
	firstTS  time.Time
	latestTS time.Time
}

// DeduplicateBatch collapses events with identical Type+Category within Window
// of each other. Returns events in first-occurrence order.
// Sets Count on merged events and rewrites Summary to include count.
func (d *Deduplicator) DeduplicateBatch(events []model.CanonicalEvent) []model.CanonicalEvent {
	if len(events) == 0 {
		return nil
	}

	// Ordered map: preserve first-occurrence order.
	type groupEntry struct {
		key string
		grp *group
	}
	var order []groupEntry
	groups := make(map[string]*groupEntry)

	for _, e := range events {
		key := e.Type + "." + e.Category

		entry, exists := groups[key]
		if exists && e.Timestamp.Sub(entry.grp.firstTS) <= d.cfg.Window {
			// Within window — merge.
			entry.grp.count++
			if e.Timestamp.After(entry.grp.latestTS) {
				entry.grp.latestTS = e.Timestamp
			}
			continue
		}

		// New group: either new key or outside window.
		g := &group{
			event:    e,
			count:    1,
			firstTS:  e.Timestamp,
			latestTS: e.Timestamp,
		}
		ge := &groupEntry{key: key, grp: g}
		groups[key] = ge
		order = append(order, *ge)
	}

	result := make([]model.CanonicalEvent, 0, len(order))
	for _, entry := range order {
		e := entry.grp.event
		if entry.grp.count > 1 {
			e.Count = entry.grp.count
			dur := entry.grp.latestTS.Sub(entry.grp.firstTS)
			e.Summary = fmt.Sprintf("%s (x%d in %s)", e.Summary, e.Count, formatDuration(dur))
		}
		result = append(result, e)
	}
	return result
}

// formatDuration produces a human-readable short duration string.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm%ds", mins, secs)
}
