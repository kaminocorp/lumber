# Phase 7: Output Architecture — Fan-Out, Async, and New Backends

**Goal:** Transform Lumber's output layer from a single synchronous stdout pipe into a multi-destination, async-capable output system. After this phase, the pipeline can simultaneously write classified events to stdout, to a file, and to a webhook — each at its own pace, with independent error handling.

**Starting point:** `output.Output` interface with a single implementation (`stdout.Output`). The pipeline takes exactly one `Output` and calls `Write()` synchronously per event.

---

## What Changes and Why

### Current bottlenecks

1. **Single destination** — `pipeline.New()` accepts one `output.Output`. No fan-out.
2. **Synchronous write** — `pipeline.go:101` blocks on `output.Write()`. A slow output stalls the entire pipeline.
3. **Fatal on error** — any `Write()` error kills the pipeline. No retry, no skip, no graceful degradation.
4. **No batching contract** — the interface is single-event (`Write(ctx, event)`). Webhook/file outputs need batching for efficiency, but must implement it internally with no interface support.

### Design constraints

- The `Output` interface should remain simple — avoid bloating it with methods that only some backends need.
- Existing `stdout.Output` must continue to work identically — no regressions.
- The pipeline should not need to know how many outputs exist or how they behave internally.
- Backpressure: a slow output should not drop events by default, but should not block ingestion indefinitely either.

---

## Implementation Plan

### Section 1: Multi-Output Router

**Package:** `internal/output/multi/`

A `Multi` output that wraps multiple `output.Output` implementations and fans out each event to all of them.

```go
// multi.go
type Multi struct {
    outputs []output.Output
}

func New(outputs ...output.Output) *Multi

func (m *Multi) Write(ctx context.Context, event model.CanonicalEvent) error
func (m *Multi) Close() error
```

**Semantics:**
- `Write()` calls each wrapped output's `Write()` sequentially. If any fails, the error is collected but the remaining outputs still receive the event. Returns a combined error if any output failed.
- `Close()` calls `Close()` on all outputs, collecting errors.

**Why sequential, not parallel?** For the initial implementation, simplicity wins. stdout writes are ~microseconds, file writes are ~microseconds with buffering. Parallel fan-out adds goroutine overhead and ordering complexity for negligible gain at this stage. The async wrapper (Section 2) handles the truly slow outputs.

**Wire-up in main.go:**
```go
// Before (current):
out := stdout.New(verbosity, pretty)

// After:
var outputs []output.Output
outputs = append(outputs, stdout.New(verbosity, pretty))
if cfg.Output.FilePath != "" {
    f, _ := file.New(cfg.Output.FilePath, file.WithRotation(...))
    outputs = append(outputs, f)
}
out := multi.New(outputs...)
```

**Tests:**
- Fan-out delivers to all outputs
- One output error doesn't prevent delivery to others
- Close calls Close on all outputs
- Single-output Multi behaves identically to the wrapped output

**Files:**
| File | Action |
|------|--------|
| `internal/output/multi/multi.go` | new |
| `internal/output/multi/multi_test.go` | new |

---

### Section 2: Async Output Wrapper

**Package:** `internal/output/async/`

A wrapper that decouples event production from consumption via a buffered channel. The pipeline writes into the channel (non-blocking up to buffer capacity) and a background goroutine drains it to the wrapped output.

```go
type Async struct {
    inner   output.Output
    ch      chan model.CanonicalEvent
    done    chan struct{}
    errFunc func(error) // called on write errors (log, metric, etc.)
}

func New(inner output.Output, opts ...Option) *Async

// Options:
func WithBufferSize(n int) Option    // default: 1024
func WithOnError(f func(error)) Option  // default: slog.Warn
func WithDropOnFull() Option         // default: block (backpressure)
```

**Semantics:**
- `Write()` sends the event into the channel. By default, blocks if the channel is full (backpressure). With `WithDropOnFull()`, returns immediately and logs a drop warning — for outputs where lossiness is acceptable (e.g., a non-critical webhook).
- A background goroutine reads from the channel and calls `inner.Write()`. Errors are passed to `errFunc` rather than propagated — the pipeline never sees them.
- `Close()` closes the channel, waits for the drain goroutine to finish (with a timeout), then calls `inner.Close()`.

**Why a channel, not a ring buffer?** Go channels provide blocking semantics, select support, and goroutine-safe access out of the box. A ring buffer would need a mutex and manual signaling. Channels are the idiomatic tool here.

**Composition with Multi:**
```go
// stdout: synchronous (fast, never fails meaningfully)
stdoutOut := stdout.New(verbosity, pretty)

// file: async (buffered writes, rotation might pause briefly)
fileOut := async.New(file.New(path), async.WithBufferSize(4096))

// webhook: async + lossy (network failures shouldn't stall anything)
webhookOut := async.New(webhook.New(url), async.WithDropOnFull(), async.WithBufferSize(256))

out := multi.New(stdoutOut, fileOut, webhookOut)
```

**Tests:**
- Events flow through async wrapper to inner output
- Backpressure: Write blocks when buffer full (default)
- Drop mode: Write returns immediately when buffer full
- Close drains remaining events before returning
- Error callback invoked on inner Write failure
- Goroutine leak check: no goroutines after Close

**Files:**
| File | Action |
|------|--------|
| `internal/output/async/async.go` | new |
| `internal/output/async/async_test.go` | new |

---

### Section 3: File Output Backend

**Package:** `internal/output/file/`

Writes NDJSON to a file with buffered I/O and optional size-based rotation.

```go
type Output struct {
    w       *bufio.Writer
    f       *os.File
    mu      sync.Mutex
    path    string
    maxSize int64 // 0 = no rotation
    written int64
}

func New(path string, opts ...Option) (*Output, error)

func WithMaxSize(bytes int64) Option  // rotate when file exceeds this size
func WithBufSize(bytes int) Option    // bufio buffer size, default 64KB
```

**Semantics:**
- `Write()` JSON-encodes the event (after `FormatEvent()` verbosity filtering) into the buffered writer. Appends a newline (NDJSON).
- When `written` exceeds `maxSize`, the current file is renamed to `{path}.1` (shifting existing rotated files up) and a new file is opened. Simple numbered rotation — no timestamp suffixes, no compression. Keep it minimal.
- `Close()` flushes the buffer and closes the file.

**Why bufio, not direct writes?** `json.Encoder` on a raw `*os.File` does one `write()` syscall per event. At 1000 events/sec, that's 1000 syscalls/sec. A 64KB `bufio.Writer` batches these into ~1 syscall per 64KB (~hundreds of events), which is dramatically more efficient.

**Tests:**
- Write produces valid NDJSON (one JSON object per line)
- Rotation triggers at maxSize boundary
- Close flushes buffered data
- Verbosity filtering applied (Minimal strips Raw/Confidence)
- Concurrent Write calls are safe (mutex)

**Files:**
| File | Action |
|------|--------|
| `internal/output/file/file.go` | new |
| `internal/output/file/file_test.go` | new |

---

### Section 4: Webhook Output Backend

**Package:** `internal/output/webhook/`

POSTs batched events to a configurable HTTP endpoint.

```go
type Output struct {
    client    *http.Client
    url       string
    headers   map[string]string
    batchSize int
    flushInterval time.Duration
    mu        sync.Mutex
    pending   []model.CanonicalEvent
    timer     *time.Timer
}

func New(url string, opts ...Option) *Output

func WithHeaders(h map[string]string) Option
func WithBatchSize(n int) Option          // default: 50
func WithFlushInterval(d time.Duration) Option  // default: 5s
func WithTimeout(d time.Duration) Option  // HTTP client timeout, default 10s
```

**Semantics:**
- `Write()` appends to an internal batch. When `batchSize` is reached or `flushInterval` elapses, the batch is POSTed as a JSON array. This is the same timer+buffer pattern as `streamBuffer` in the pipeline.
- POST body: `[{event1}, {event2}, ...]` as JSON array.
- Retry: 3 attempts with exponential backoff on 5xx. No retry on 4xx (client error — likely a config problem). Uses the same retry pattern as `httpclient`.
- `Close()` flushes any remaining batch.

**Note:** The webhook output should almost always be wrapped in `async.New()` — network I/O in the synchronous write path would stall the pipeline. The implementation itself is synchronous (flush blocks on HTTP), and the async wrapper handles decoupling.

**Tests:**
- Batch accumulation and flush at batchSize
- Timer-based flush before batchSize reached
- Retry on 5xx (httptest)
- No retry on 4xx
- Custom headers sent
- Close flushes remaining events

**Files:**
| File | Action |
|------|--------|
| `internal/output/webhook/webhook.go` | new |
| `internal/output/webhook/webhook_test.go` | new |

---

### Section 5: Config & CLI Wiring

Extend `Config` and CLI flags to support multiple output destinations.

**New config fields:**
```go
type OutputConfig struct {
    Format   string // "stdout" (kept for backward compat)
    Pretty   bool
    FilePath string // NDJSON file output path; empty = disabled
    FileMaxSize int64 // rotation size in bytes; 0 = no rotation
    WebhookURL string // POST endpoint; empty = disabled
    WebhookHeaders map[string]string
}
```

**New env vars:**
| Variable | Default | Description |
|----------|---------|-------------|
| `LUMBER_OUTPUT_FILE` | `` | File path for NDJSON output (empty = disabled) |
| `LUMBER_OUTPUT_FILE_MAX_SIZE` | `0` | Max file size before rotation (bytes, 0 = no rotation) |
| `LUMBER_WEBHOOK_URL` | `` | Webhook endpoint URL (empty = disabled) |

**New CLI flags:**
| Flag | Type | Description |
|------|------|-------------|
| `-output-file` | string | File path for NDJSON output |
| `-webhook-url` | string | Webhook POST endpoint |

**Validation additions:**
- If `WebhookURL` is set, validate it's a parseable URL
- If `FilePath` is set, validate the parent directory exists

**Main.go wiring:**
Build a `[]output.Output` slice based on config, wrap slow outputs in `async.New()`, combine with `multi.New()`. stdout is always included unless explicitly disabled (future consideration).

**Files:**
| File | Action |
|------|--------|
| `internal/config/config.go` | modified |
| `internal/config/config_test.go` | modified |
| `cmd/lumber/main.go` | modified |

---

### Section 6: Pipeline Error Handling Update

Update the pipeline to handle `multi.Multi` errors gracefully.

**Current behavior (`pipeline.go:101`):**
```go
if err := p.output.Write(ctx, event); err != nil {
    return fmt.Errorf("pipeline output: %w", err)
}
```

**New behavior:** When the output is a `Multi` with async wrappers, `Write()` errors from individual backends are handled by the async error callbacks — they never reach the pipeline. The pipeline only sees errors from synchronous outputs (stdout), which are genuinely fatal (broken pipe = consumer disconnected).

This means the pipeline code itself barely changes — the error isolation is handled by the `async` and `multi` layers. The pipeline remains a simple loop.

**One addition:** Log total events written on pipeline close, alongside the existing `skippedLogs` counter.

**Files:**
| File | Action |
|------|--------|
| `internal/pipeline/pipeline.go` | modified (minor) |

---

## Dependency Graph

```
Section 1: Multi Router ──┐
                           ├──→ Section 5: Config & CLI ──→ Section 6: Pipeline Update
Section 2: Async Wrapper ──┤
                           │
Section 3: File Backend  ──┤
                           │
Section 4: Webhook Backend ┘
```

Sections 1–4 are independent of each other and can be built in any order. Section 5 wires them together. Section 6 is a small follow-up.

---

## Summary

| Section | Package | New files | What |
|---------|---------|-----------|------|
| 1 | `output/multi` | 2 | Fan-out to N outputs |
| 2 | `output/async` | 2 | Channel-based decoupling |
| 3 | `output/file` | 2 | NDJSON file writer with rotation |
| 4 | `output/webhook` | 2 | Batched HTTP POST with retry |
| 5 | `config`, `cmd/lumber` | 0 (modified) | Config + CLI + wiring |
| 6 | `pipeline` | 0 (modified) | Error handling adjustment |

**New files: 8. Modified files: 3. Total: 11.**
