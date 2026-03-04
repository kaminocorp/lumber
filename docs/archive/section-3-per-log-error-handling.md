# Section 3: Per-Log Error Handling — Completion Notes

**Completed:** 2026-02-23
**Phase:** 5 (Pipeline Integration & Resilience)
**Depends on:** Section 1 (Structured Logging) — uses `slog.Warn` for skip logging

## Summary

The single most critical reliability fix in Phase 5. Previously, one bad log killed the entire pipeline (`return fmt.Errorf(...)` in `streamDirect`, `streamWithDedup`, and `Query`). Now: `engine.Process()` failures log a warning, increment an atomic counter, and skip the log. `output.Write()` failures remain fatal — if we can't write, continuing is pointless. `Query` mode falls back from batch to individual processing when `ProcessBatch` fails.

## What Changed

### Modified Files

| File | Change | Why |
|------|--------|-----|
| `internal/pipeline/pipeline.go` | Introduced `Processor` interface, replaced `*engine.Engine` with `Processor`, added `atomic.Int64` skip counter, skip-and-continue in `streamDirect`/`streamWithDedup`, batch fallback + `processIndividual` helper in `Query`, skip reporting in `Close` | Decouples pipeline from concrete engine (enables mock testing), resilient to per-log failures |
| `internal/pipeline/pipeline_test.go` | Added `mockProcessor`, `categoryProcessor`, `mockConnector` mocks + 4 new tests | Full coverage of skip logic without ONNX dependency |

### Key Architectural Changes

**`Processor` interface** (new, in `pipeline.go`):
```go
type Processor interface {
    Process(raw model.RawLog) (model.CanonicalEvent, error)
    ProcessBatch(raws []model.RawLog) ([]model.CanonicalEvent, error)
}
```
- `*engine.Engine` satisfies this implicitly (Go structural typing)
- `main.go` required zero changes
- Tests can now inject mock processors for error injection

**Skip-and-continue pattern:**
- `streamDirect()`: `Process` error → `p.skippedLogs.Add(1)` + `slog.Warn(...)` + `continue`
- `streamWithDedup()`: identical pattern
- `Query()`: `ProcessBatch` error → falls back to `processIndividual()` which uses the same skip pattern per log

**Atomic counter:** `sync/atomic.Int64` — safe for concurrent access (goroutines in streaming mode). Reported at stream end and at `Close()`.

### Import Changes

- `pipeline.go`: removed `"github.com/kaminocorp/lumber/internal/engine"` import, added `"log/slog"` and `"sync/atomic"`
- `pipeline_test.go`: added `"fmt"` and `"github.com/kaminocorp/lumber/internal/connector"` imports

### Tests Added (4)

| Test | What it validates |
|------|-------------------|
| `TestStreamDirect_SkipsBadLog` | 3 logs (good/bad/good) → 2 events output, 1 skipped, pipeline returns nil |
| `TestStreamWithDedup_SkipsBadLog` | Same with dedup enabled. Uses `categoryProcessor` to produce distinct dedup keys (Type+Category) so good events aren't merged |
| `TestQuery_BatchFallback` | `ProcessBatch` fails → falls back to individual, skips 1, outputs 2 |
| `TestSkipCounter` | 5 logs with 3 bad → counter reports 3, 2 events output, Close succeeds |

### Test Infrastructure Added

| Mock | Purpose |
|------|---------|
| `mockProcessor` | Returns error when `raw.Raw == failOn`; fixed Type/Category for non-dedup tests |
| `categoryProcessor` | Same fail logic but uses `raw.Raw` as Category, producing unique dedup keys per input |
| `mockConnector` | Pre-loaded logs, sends all to channel then closes; also serves `Query()` |

## Design Decisions

- **`output.Write()` errors remain fatal** — if the output destination is broken (stdout closed, pipe broken), continuing would just accumulate undeliverable events. Better to fail fast.
- **Batch fallback in Query** — `ProcessBatch` is a single ONNX inference call. If it fails (e.g., tensor shape mismatch from one malformed log), falling back to individual `Process` calls isolates the bad log while still processing the rest.
- **Dedup key is `Type + "." + Category`**, not including Summary — this is by design in the dedup package. Tests that use dedup must account for this by producing distinct categories.

## Verification

```
go test ./internal/pipeline/...  # 7 tests pass (3 existing + 4 new)
go build ./cmd/lumber            # compiles cleanly
go test ./...                    # full suite passes
```
