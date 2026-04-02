# Phase 7 + 8 Completion: Output Architecture & Public Library API

**Completed:** 2026-02-28

**Scope:** Two phases implemented simultaneously — Phase 7 (Output Architecture) transforms the output layer from single stdout to multi-destination async fan-out; Phase 8 (Public Library API) exposes a `pkg/lumber` package for direct Go library usage.

---

## What Was Built

### Phase 7: Output Architecture

#### Section 1: Multi-Output Router (`internal/output/multi/`)
- `Multi` struct wraps `[]output.Output` and fans out each `Write()` call sequentially
- Error isolation: one output failing doesn't prevent delivery to others (uses `errors.Join`)
- `Close()` calls Close on all outputs, collecting errors
- **5 tests**: fan-out delivery, error isolation, close propagation, close error collection, single-output identity

#### Section 2: Async Output Wrapper (`internal/output/async/`)
- Channel-based decoupling: pipeline writes into buffered channel, background goroutine drains to inner output
- Two modes: backpressure (default, blocks when full) and drop-on-full (lossy, for non-critical outputs)
- `WithBufferSize(n)`, `WithOnError(f)`, `WithDropOnFull()` options
- `Close()` is idempotent via `sync.Once`; drains remaining events with timeout
- **7 tests**: flow-through, backpressure blocking, drop-on-full, close drain, error callback, goroutine leak check, close idempotency

#### Section 3: File Output Backend (`internal/output/file/`)
- NDJSON writer with `bufio.Writer` (64KB default buffer, reduces syscalls from 1-per-event to ~1-per-64KB)
- Size-based rotation: when file exceeds `maxSize`, renames to `.1` (shifting existing), opens new file
- Verbosity-aware via `output.FormatEvent()` (same as stdout)
- Thread-safe via `sync.Mutex`
- **5 tests**: valid NDJSON output, rotation at maxSize, close flushes, verbosity filtering, concurrent safety

#### Section 4: Webhook Output Backend (`internal/output/webhook/`)
- Batched HTTP POST as JSON array with configurable batch size (default 50) and flush interval (default 5s)
- Timer+buffer pattern: `time.AfterFunc` starts on first event, flushes when batch fills or timer fires
- Retry on 5xx with exponential backoff (1s, 2s, 4s, max 3 retries); no retry on 4xx
- Custom headers support
- **6 tests**: batch flush at size, timer flush, 5xx retry, 4xx no-retry, custom headers, close flushes remaining

#### Section 5: Config & CLI Wiring
- `OutputConfig` extended with `FilePath`, `FileMaxSize`, `WebhookURL`, `WebhookHeaders`
- New env vars: `LUMBER_OUTPUT_FILE`, `LUMBER_OUTPUT_FILE_MAX_SIZE`, `LUMBER_WEBHOOK_URL`
- New CLI flags: `-output-file`, `-webhook-url`
- Validation: webhook URL must start with `http://` or `https://`; file output parent directory must exist
- **6 new config tests**: webhook URL valid/invalid, file dir valid/invalid, env var loading
- `main.go` wiring: stdout (sync) + file (async) + webhook (async+drop) combined via `multi.New()`

#### Section 6: Pipeline Error Handling Update
- Added `writtenEvents atomic.Int64` counter to `Pipeline`
- Incremented after each successful `output.Write()` in `streamDirect`, `streamWithDedup` (via buffer), and `Query`
- `Close()` logs both `total_events_written` and `total_skipped_logs`

### Phase 8: Public Library API

#### Section 1-2: Public Types & Core API (`pkg/lumber/`)
- `Event` type: stable public contract mirroring `model.CanonicalEvent`
- `Option` funcs: `WithModelDir`, `WithModelPaths`, `WithConfidenceThreshold`, `WithVerbosity`
- `New(opts ...Option)` loads ONNX model, pre-embeds taxonomy, builds engine
- `Classify(text)` classifies a single log line
- `ClassifyBatch(texts)` classifies multiple lines in one batched inference call
- `Close()` releases ONNX resources
- `eventFromCanonical()` bridge function for internal→public type conversion
- **11 tests**: construction, bad path error, known log classification, batch matches individual, empty input, whitespace input, concurrent safety, defaults, path resolution (3 variants)

#### Section 3-4: Metadata Input & Taxonomy Introspection
- `Log` type: structured input with `Text`, `Timestamp`, `Source`, `Metadata`
- `ClassifyLog(log)` preserves timestamp and source; zero timestamp defaults to `time.Now()`
- `ClassifyLogs(logs)` batch variant
- `Taxonomy()` returns `[]Category` with `[]Label` for introspection (read-only)
- **6 tests**: timestamp preservation, zero timestamp default, batch logs, taxonomy root count (8), taxonomy introspection (42 leaves), path format

#### Section 5-6: Examples & Documentation
- `doc.go` with package-level godoc
- `example_test.go` with runnable `Example()` (ONNX-gated with fallback output)
- README updated: Library Usage section (basic, batch, structured input, taxonomy), Output Destinations section, updated project structure tree, updated status checklist

---

## Files Changed

### New files (14)

| File | Package | What |
|------|---------|------|
| `internal/output/multi/multi.go` | multi | Fan-out router |
| `internal/output/multi/multi_test.go` | multi | 5 tests |
| `internal/output/async/async.go` | async | Channel-based async wrapper |
| `internal/output/async/async_test.go` | async | 7 tests |
| `internal/output/file/file.go` | file | NDJSON file writer |
| `internal/output/file/file_test.go` | file | 5 tests |
| `internal/output/webhook/webhook.go` | webhook | Batched HTTP POST |
| `internal/output/webhook/webhook_test.go` | webhook | 6 tests |
| `pkg/lumber/event.go` | lumber | Public Event type |
| `pkg/lumber/options.go` | lumber | Option funcs and path resolution |
| `pkg/lumber/lumber.go` | lumber | Core API: New, Classify, ClassifyBatch, ClassifyLog, ClassifyLogs, Close |
| `pkg/lumber/log.go` | lumber | Log input type |
| `pkg/lumber/taxonomy.go` | lumber | Category, Label, Taxonomy() |
| `pkg/lumber/doc.go` | lumber | Package documentation |

### New test files (4)

| File | Tests |
|------|-------|
| `pkg/lumber/lumber_test.go` | 14 tests (construction, classification, options, paths, metadata) |
| `pkg/lumber/taxonomy_test.go` | 3 tests (root count, introspection, path format) |
| `pkg/lumber/example_test.go` | 1 example test |

### Modified files (5)

| File | What changed |
|------|------|
| `internal/config/config.go` | Added OutputConfig fields, env vars, CLI flags, validation |
| `internal/config/config_test.go` | 6 new tests for output config |
| `cmd/lumber/main.go` | Multi-output wiring (stdout + file + webhook via multi/async) |
| `internal/pipeline/pipeline.go` | Added writtenEvents counter, updated Close logging |
| `README.md` | Library Usage section, Output Destinations section, updated structure + status |

**New files: 18. Modified files: 5. Total: 23.**

---

## Test Summary

| Package | New Tests | Total |
|---------|-----------|-------|
| `internal/output/multi` | 5 | 5 |
| `internal/output/async` | 7 | 7 |
| `internal/output/file` | 5 | 5 |
| `internal/output/webhook` | 6 | 6 |
| `internal/config` | 6 | 39 |
| `internal/pipeline` | 0 (counter change only) | existing |
| `pkg/lumber` | 18 | 18 |
| **Total new tests** | **47** | |

All tests pass. Race detector clean on async and file packages. ONNX-gated tests skip gracefully without model files.

---

## Design Decisions

1. **Sequential fan-out over parallel**: `multi.Write()` calls outputs sequentially. stdout and file writes are microseconds; parallel goroutines would add overhead for no gain. The `async` wrapper handles truly slow outputs (webhook).

2. **Backpressure as default, drop-on-full as opt-in**: The async wrapper blocks when the channel is full by default (safe). Drop-on-full is explicitly opted into for lossy-acceptable outputs like webhooks.

3. **`sync.Once` for Close()**: Prevents double-close panics on channels. Idempotent close is a defensive pattern for resources used in concurrent contexts.

4. **Separate public types from internal types**: `lumber.Event` mirrors `model.CanonicalEvent` today but can diverge. The `eventFromCanonical()` bridge function is where that divergence would happen.

5. **`errors.Join` for multi-error collection**: Go 1.20+ stdlib function, returns nil when all errors are nil. Supports `errors.Is`/`errors.As` unwrapping.

6. **`bufio.Writer` for file output**: Reduces syscalls from 1-per-event to ~1-per-64KB. Critical for throughput at 1000+ events/sec.

7. **`time.AfterFunc` for webhook batching**: Cleaner than manual timer management since the webhook doesn't have its own event loop (unlike the pipeline's `streamWithDedup`).

---

## Dependency Graph

```
Phase 7:
  §1 Multi Router ──┐
                     ├──→ §5 Config & CLI ──→ §6 Pipeline Update
  §2 Async Wrapper ──┤
                     │
  §3 File Backend  ──┤
                     │
  §4 Webhook Backend ┘

Phase 8:
  §1-2 Types + Core API ──→ §3-4 Metadata + Taxonomy
                       └──→ §5-6 Examples + Docs

Cross-phase: No dependencies. Phases touch disjoint code paths.
```
