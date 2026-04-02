# Phase 7 + 8 Post-Review Fixes

**Completed:** 2026-02-28

**Scope:** Audit of Phase 7 (Output Architecture) and Phase 8 (Public Library API) implementation identified 3 bugs. All fixed with 2 new tests.

---

## Fixed

### 1. `writtenEvents` counter not incremented in dedup streaming path (HIGH)

**File:** `internal/pipeline/pipeline.go`, `internal/pipeline/buffer.go`

**Problem:** The `writtenEvents` atomic counter was incremented after each successful `output.Write()` in `streamDirect` (line 105) and `Query` (line 175), but not in `streamWithDedup`. In the dedup path, events are written through `streamBuffer.flush()` → `output.Write()` in `buffer.go`, which had no way to increment the pipeline's counter. Since dedup is enabled by default (`LUMBER_DEDUP_WINDOW=5s`), `Close()` always logged `total_events_written=0` in the most common configuration.

**Fix:** Added an `onWrite func()` callback parameter to `streamBuffer`. The pipeline passes `func() { p.writtenEvents.Add(1) }` when constructing the buffer. The callback is invoked after each successful `Write()` in `flush()`. Test callers pass `nil` (callback is guarded with a nil check).

### 2. Webhook timer-flush errors silently lost (MEDIUM)

**File:** `internal/output/webhook/webhook.go`

**Problem:** The `time.AfterFunc` callback that triggers timer-based batch flushes called `flushLocked()` and discarded the returned error (`//nolint:errcheck`). When the webhook is wrapped in `async.New()`, the async error callback only sees errors from `Write()` — timer-triggered flushes happen in `AfterFunc`'s goroutine, bypassing the async wrapper entirely. HTTP failures on timer-triggered batches were silently swallowed.

**Fix:** Added an `errFunc func(error)` field to the webhook `Output` (same pattern as `async.Async`). Default: `slog.Warn("webhook flush error", ...)`. The timer callback now checks and routes errors through `errFunc`. Added `WithOnError(f func(error)) Option` for custom error handling.

### 3. Typo in async constant name (LOW)

**File:** `internal/output/async/async.go`

**Problem:** `defaultDrainTimout` was missing a letter — should be `defaultDrainTimeout`. Unexported constant, so no external impact, but incorrect.

**Fix:** Renamed to `defaultDrainTimeout`.

---

## Tests Added

2 new tests across 2 packages:

| Package | Test | What |
|---------|------|------|
| `internal/pipeline` | `TestStreamWithDedup_WrittenEventsCounter` | 3 distinct events through dedup path, verifies `writtenEvents` counter equals 3 |
| `internal/output/webhook` | `TestTimerFlushErrorCallbackInvoked` | Timer-triggered flush to 400 endpoint, verifies error callback is invoked |

---

## Files Changed

| File | Action | What |
|------|--------|------|
| `internal/pipeline/buffer.go` | modified | Added `onWrite func()` field and callback to `newStreamBuffer`; invoke after each successful write in `flush()` |
| `internal/pipeline/pipeline.go` | modified | Pass `writtenEvents.Add(1)` callback to `newStreamBuffer` in `streamWithDedup` |
| `internal/pipeline/pipeline_test.go` | modified | Updated all `newStreamBuffer` calls with `nil` onWrite; added `TestStreamWithDedup_WrittenEventsCounter` |
| `internal/output/webhook/webhook.go` | modified | Added `errFunc` field, `WithOnError` option, default `slog.Warn`; timer callback routes errors through `errFunc` |
| `internal/output/webhook/webhook_test.go` | modified | Added `TestTimerFlushErrorCallbackInvoked` |
| `internal/output/async/async.go` | modified | Renamed `defaultDrainTimout` → `defaultDrainTimeout` |

**New files: 0. Modified files: 6.**
