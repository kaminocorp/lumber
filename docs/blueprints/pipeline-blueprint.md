# Pipeline Blueprint

## Overview

Lumber's pipeline layer is the orchestration surface — it connects connectors (ingestion), the classification engine (processing), and output (delivery) into a single running system. The pipeline handles two modes of operation (stream and query), per-log error resilience, event deduplication with bounded buffering, graceful shutdown, structured internal logging, CLI flags, and startup config validation.

Before this layer, the pieces worked in isolation: connectors fetched logs, the engine classified them, the output wrote JSON. The pipeline makes them work as a system — with proper error handling, backpressure, lifecycle management, and operational controls.

---

## Architecture

```
                    ┌──────────────────────────────────┐
                    │           cmd/lumber/main.go     │
                    │                                  │
                    │  Config ─→ Validate ─→ Init      │
                    │  Signal handling (graceful)       │
                    │  Mode dispatch (stream / query)   │
                    └──────────────┬───────────────────┘
                                   │
                                   ▼
┌──────────────────────────────────────────────────────────────────┐
│                        Pipeline                                  │
│                                                                  │
│  ┌───────────┐     ┌───────────┐     ┌────────────┐             │
│  │ Connector │────→│ Processor │────→│   Output   │             │
│  │ (Stream/  │     │ (Engine)  │     │  (stdout)  │             │
│  │  Query)   │     │           │     │            │             │
│  └───────────┘     └───────────┘     └────────────┘             │
│       │                  │                  ▲                    │
│       │            ┌─────┴─────┐            │                   │
│       │            │  skip +   │     ┌──────┴──────┐            │
│       │            │  continue │     │ streamBuffer│            │
│       │            └───────────┘     │  (dedup +   │            │
│       │                              │  bounded)   │            │
│       └──────────────────────────────┘─────────────┘            │
│                                                                  │
│  Atomic skip counter │ Per-log error isolation │ Batch fallback  │
└──────────────────────────────────────────────────────────────────┘
```

---

## Pipeline

The pipeline (`internal/pipeline/pipeline.go`) wires three components together through a `Processor` interface:

```go
type Processor interface {
    Process(raw model.RawLog) (model.CanonicalEvent, error)
    ProcessBatch(raws []model.RawLog) ([]model.CanonicalEvent, error)
}
```

The `Processor` interface is extracted from `*engine.Engine` to enable mock-based testing. The pipeline never imports the engine directly — it depends on the interface, allowing error injection and fast unit tests without ONNX model files.

```go
type Pipeline struct {
    connector     connector.Connector
    engine        Processor
    output        output.Output
    dedup         *dedup.Deduplicator
    window        time.Duration
    maxBufferSize int
    skippedLogs   atomic.Int64
}
```

### Stream mode

Stream mode processes logs as they arrive from the connector's channel. Two code paths exist:

**Without dedup (`streamDirect`):** Events flow straight from connector to engine to output. Each log is processed individually. On engine error, the log is skipped (warning logged, skip counter incremented) and the loop continues. Output errors are fatal — if writing fails, the pipeline stops.

**With dedup (`streamWithDedup`):** Events accumulate in a `streamBuffer` that flushes on either a timer or buffer-full signal. The `select` loop handles three cases:
1. `ctx.Done()` — shutdown: flush remaining events with `context.Background()` (the original context is cancelled, so a fresh context allows the final writes to complete; the shutdown timer in `main.go` provides the hard bound)
2. `raw` from channel — process and add to buffer; if `add()` returns true (buffer full), force-flush immediately
3. `flushCh()` fires — timer-based flush

### Query mode

Query mode is one-shot: fetch all logs, classify, optionally dedup, write.

```
connector.Query() → engine.ProcessBatch() → [dedup] → output.Write() per event
```

If `ProcessBatch` fails (e.g., one bad log in the batch triggers an ONNX error), the pipeline falls back to `processIndividual()` — processing logs one at a time with skip-and-continue. This ensures a single bad log doesn't discard an entire query result set.

### Per-log error resilience

The pipeline never crashes on a single bad log. In all modes:
- `engine.Process()` failure → log warning with error and source, increment atomic skip counter, continue
- The skip counter is reported on stream end, context cancel, and pipeline close
- `sync/atomic.Int64` is used for the counter — zero contention for the expected case (most logs succeed), simpler than channel-based counting

---

## Stream Buffer

The stream buffer (`internal/pipeline/buffer.go`) accumulates events between dedup flush cycles. It is a mutex-protected list with a lazy timer.

```go
type streamBuffer struct {
    dedup   *dedup.Deduplicator
    out     output.Output
    window  time.Duration
    maxSize int    // 0 = unlimited

    mu      sync.Mutex
    pending []model.CanonicalEvent
    timer   *time.Timer
}
```

### Timer lifecycle

The timer is **lazy** — it's only created when the first event arrives after a flush (or at startup). This means an idle pipeline consumes no timer resources.

- `add()` creates a new timer on the first event (`len(pending) == 1`)
- `flush()` stops the timer and nils it — the next `add()` creates a fresh one
- `flushCh()` returns `nil` when no timer is active, causing the `select` case to be skipped

### Bounded buffer

The buffer has a configurable `maxSize` (default 1000 via `LUMBER_MAX_BUFFER_SIZE`). When the buffer reaches `maxSize`, `add()` returns `true`, signaling the pipeline to force-flush immediately.

This prevents unbounded memory growth during log storms. No events are dropped — the buffer flushes early instead. Setting `maxSize` to 0 disables the bound for backward compatibility.

### Flush

`flush()` is atomic with respect to the buffer: it takes the lock, swaps the pending slice to `nil`, stops the timer, releases the lock, then deduplicates and writes without holding the lock. This means new events can accumulate during the write phase.

---

## Deduplication

The deduplicator (`internal/engine/dedup/dedup.go`) collapses identical event types within a configurable time window.

### Dedup key

Events are grouped by `Type + "." + Category` — e.g., `ERROR.connection_failure`. All events with the same key whose timestamps fall within `Window` of the group's first event are merged.

### Merge behavior

Merged events:
- Keep the first event's fields (Type, Category, Severity, Summary, Confidence, Raw)
- Set `Count` to the total number of merged events
- Rewrite `Summary` to include the count: `"connection timeout (x47 in 5s)"`
- Track `firstTS` and `latestTS` for duration calculation

### Duration formatting

The duration between first and last merged event is formatted human-readably:
- `< 1s` → `"450ms"`
- `< 1m` → `"12s"`
- `≥ 1m` → `"2m30s"` or `"5m"`

### Ordering

Results preserve first-occurrence order using an ordered map pattern (a slice of entries alongside the lookup map). Events that fall outside the window for their key start a new group rather than extending the old one.

---

## Graceful Shutdown

Shutdown is managed in `cmd/lumber/main.go` with a three-tier escalation:

```
Signal 1 (SIGINT/SIGTERM)
    │
    ├── Cancel context → pipeline begins draining
    ├── Start shutdown timer (LUMBER_SHUTDOWN_TIMEOUT, default 10s)
    │
    ▼
Signal 2 (another SIGINT/SIGTERM)  ──→  os.Exit(1) immediately
    │
    ▼
Timer expires                      ──→  os.Exit(1) with error log
```

**Signal channel is buffered at 2** — enough to catch both the first signal (triggers graceful shutdown) and a second signal (forces immediate exit) without blocking the OS signal delivery.

**Context cancellation flow:**
1. First signal cancels the root context
2. Stream mode: the pipeline's `select` loop sees `ctx.Done()`, performs a final dedup flush using `context.Background()` (the cancelled context would cause output writes to fail), then returns `context.Canceled`
3. Query mode: if the query is in progress, context cancellation propagates to the connector's HTTP calls
4. `defer p.Close()` runs, reporting total skipped logs
5. `defer emb.Close()` runs, cleaning up the ONNX session

---

## Structured Logging

Internal logging (`internal/logging/logging.go`) uses Go 1.21's `log/slog` — stdlib, no dependencies, structured by default.

### Handler selection

```go
func Init(outputIsStdout bool, level slog.Level)
```

When Lumber's data output goes to stdout (NDJSON events), internal logs use **JSONHandler on stderr** — avoiding mixing structured log data with diagnostic output. When output goes elsewhere, **TextHandler on stderr** provides human-readable diagnostics.

### Level parsing

`ParseLevel` converts strings to `slog.Level` with case-insensitive matching. Unknown strings default to `LevelInfo`. Accepts both `"warn"` and `"warning"`.

### Call-site usage

After `logging.Init()` calls `slog.SetDefault()`, all call sites use `slog.Info()`, `slog.Warn()`, etc. directly — no logger instance passing. All `fmt.Fprintf(os.Stderr, ...)` and `log.Printf` calls across the codebase were replaced with structured slog calls.

---

## Configuration

Configuration (`internal/config/config.go`) uses a two-layer approach: environment variables as the base, CLI flags as overrides.

### Config struct

```go
type Config struct {
    Connector       ConnectorConfig
    Engine          EngineConfig
    Output          OutputConfig
    LogLevel        string
    ShutdownTimeout time.Duration
    Mode            string        // "stream" or "query"
    QueryFrom       time.Time
    QueryTo         time.Time
    QueryLimit      int
    ShowVersion     bool
    parseErrors     []string      // collected during LoadWithFlags
}
```

### Loading

`Load()` reads all environment variables with sensible defaults. `LoadWithFlags()` calls `Load()` first, then defines 9 CLI flags and overlays only explicitly-set flags using `flag.Visit()`.

**`flag.Visit()` is critical.** Go's `flag` package doesn't distinguish "flag set to default" from "flag not provided." Without `flag.Visit()`, every flag's default value would silently override the env var. `flag.Visit()` only visits flags explicitly present on the command line, preserving env var values for unset flags.

### CLI flags

| Flag | Type | Description |
|------|------|-------------|
| `-version` | bool | Print version and exit |
| `-mode` | string | Pipeline mode: stream or query |
| `-connector` | string | Connector: vercel, flyio, supabase |
| `-from` | string | Query start time (RFC3339) |
| `-to` | string | Query end time (RFC3339) |
| `-limit` | int | Query result limit |
| `-verbosity` | string | Verbosity: minimal, standard, full |
| `-pretty` | bool | Pretty-print JSON output |
| `-log-level` | string | Log level: debug, info, warn, error |

### Parse error collection

Invalid `-from`/`-to` values don't fail immediately during `LoadWithFlags()`. Instead, the parse error is appended to `cfg.parseErrors` and surfaced later by `Validate()`. This ensures all config errors are reported together, not one at a time.

### Validation

`Validate()` checks all fields at once and returns **all** errors, not just the first:

| Check | Condition |
|-------|-----------|
| API key | Required when `Connector.Provider` is set |
| Model files | All three (model, vocab, projection) must exist on disk |
| Confidence threshold | Must be in `[0, 1]` |
| Verbosity | Must be `minimal`, `standard`, or `full` |
| Dedup window | Must be non-negative |
| Mode | Must be `stream` or `query` |
| Query time range | `-from` and `-to` required when mode is `query` |
| Parse errors | Any flag parse errors from `LoadWithFlags()` |

Errors are formatted as a bulleted list:
```
config validation failed:
  - LUMBER_API_KEY is required when a connector is configured
  - model file not found: models/model_quantized.onnx
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LUMBER_CONNECTOR` | `vercel` | Log provider |
| `LUMBER_API_KEY` | _(required)_ | Provider API key/token |
| `LUMBER_ENDPOINT` | _(provider default)_ | API endpoint override |
| `LUMBER_MODE` | `stream` | Pipeline mode |
| `LUMBER_MODEL_PATH` | `models/model_quantized.onnx` | ONNX model file |
| `LUMBER_VOCAB_PATH` | `models/vocab.txt` | Tokenizer vocabulary |
| `LUMBER_PROJECTION_PATH` | `models/2_Dense/model.safetensors` | Projection weights |
| `LUMBER_CONFIDENCE_THRESHOLD` | `0.5` | Min cosine similarity for classification |
| `LUMBER_VERBOSITY` | `standard` | Output verbosity |
| `LUMBER_DEDUP_WINDOW` | `5s` | Dedup window (0 disables) |
| `LUMBER_MAX_BUFFER_SIZE` | `1000` | Max events buffered before force flush |
| `LUMBER_LOG_LEVEL` | `info` | Internal log level |
| `LUMBER_SHUTDOWN_TIMEOUT` | `10s` | Max drain time on shutdown |
| `LUMBER_OUTPUT_PRETTY` | `false` | Pretty-print JSON |

### Helper functions

Four typed env var readers handle parsing with fallbacks:

| Helper | Parses | Invalid input |
|--------|--------|---------------|
| `getenv` | string | N/A |
| `getenvBool` | `"true"`, `"1"` (case-insensitive) | Falls back |
| `getenvInt` | `strconv.Atoi` | Falls back |
| `getenvFloat` | `strconv.ParseFloat` | Falls back |
| `getenvDuration` | `time.ParseDuration` (with `"0"` special case) | Falls back |

---

## Entrypoint

`cmd/lumber/main.go` is the startup sequence. Order matters:

```
1. LoadWithFlags()           Parse env vars + CLI flags
2. -version check            Print and exit if set
3. logging.Init()            Configure slog before any log calls
4. cfg.Validate()            Fail fast on bad config
5. embedder.New()            Load ONNX model (~100ms)
6. taxonomy.New()            Pre-embed 42 labels (~100-300ms)
7. classifier.New()          Configure confidence threshold
8. compactor.New()           Configure verbosity
9. engine.New()              Wire embed → classify → compact
10. stdout.New()             Configure output encoder
11. connector.Get() + ctor() Resolve and create connector
12. pipeline.New()           Wire connector → engine → output
13. Signal handler goroutine Start shutdown listener
14. Mode dispatch            Stream or Query
```

### Connector registration

Connectors are registered via blank imports:
```go
_ "github.com/kaminocorp/lumber/internal/connector/flyio"
_ "github.com/kaminocorp/lumber/internal/connector/supabase"
_ "github.com/kaminocorp/lumber/internal/connector/vercel"
```

Each connector's `init()` calls `connector.Register(name, constructor)`. At runtime, `connector.Get(provider)` retrieves the constructor. The main package never imports provider-specific types.

### Version output

`lumber -version` writes to stdout (not stderr) — POSIX convention for version output, allowing scripts to capture it: `VERSION=$(lumber -version)`.

---

## Output

The output interface (`internal/output/output.go`) is minimal:

```go
type Output interface {
    Write(ctx context.Context, event model.CanonicalEvent) error
    Close() error
}
```

The stdout implementation (`internal/output/stdout/stdout.go`) wraps `json.Encoder` writing to `os.Stdout`. It supports optional pretty-printing (`SetIndent`) and uses `output.FormatEvent()` for verbosity-aware field omission before encoding.

---

## Test Strategy

### Unit tests (no ONNX required)

The `Processor` interface enables fast, focused tests using mock implementations:

- **`mockProcessor`** — returns a fixed `CanonicalEvent` for all inputs; configurable `failOn` string triggers errors for specific inputs
- **`categoryProcessor`** — uses `raw.Raw` as the Category, producing distinct dedup keys per input (prevents accidental merging in dedup tests)
- **`mockConnector`** — sends pre-loaded logs via a buffered channel, immediately closes it
- **`mockOutput`** — collects events in a mutex-protected slice

| Package | Tests | What |
|---------|-------|------|
| `pipeline` | 8 | Buffer flush + timer, context cancel flush, no-dedup pass-through, skip bad log (direct), skip bad log (with dedup), query batch fallback, skip counter accuracy, bounded buffer |
| `pipeline` (buffer) | 3 | MaxSize signal, no data loss across flushes, unlimited backward compat |
| `config` | 24 | Defaults, env var loading, connector extras, pretty/dedup/mode/version/buffer/shutdown env vars, 7 validation rules, multi-error reporting, getenvInt, parse error surfacing, query mode from/to validation |
| `logging` | 3 | ParseLevel, JSON handler, text handler |

### Integration tests (ONNX required)

Four tests in `internal/pipeline/integration_test.go` exercise the full stack: httptest server → Vercel connector → real ONNX engine → mock output. Guarded by `skipWithoutModel(t)` so `go test ./...` always passes.

| Test | What |
|------|------|
| `TestIntegration_VercelStreamThroughPipeline` | 3 realistic logs streamed through the full pipeline; validates Type, Category, Severity, Summary, Confidence are populated |
| `TestIntegration_VercelQueryThroughPipeline` | 3 semantically distinct logs via query mode; validates complete classification |
| `TestIntegration_BadLogDoesNotCrash` | Mix of valid logs, empty string, and binary content; verifies pipeline survives without crashing |
| `TestIntegration_DedupReducesCount` | 10 identical error logs; verifies dedup merges them, Count > 1, total count preserved |

---

## File Layout

```
cmd/lumber/
└── main.go                         Entrypoint: config, init, signal handling, mode dispatch

internal/pipeline/
├── pipeline.go                     Pipeline struct, Stream/Query, per-log error handling
├── buffer.go                       streamBuffer: bounded accumulation, lazy timer, flush
├── pipeline_test.go                8 unit tests + mocks
└── integration_test.go             4 integration tests (ONNX-guarded)

internal/config/
├── config.go                       Config struct, Load, LoadWithFlags, Validate, env helpers
└── config_test.go                  24 tests

internal/logging/
├── logging.go                      Init (JSON/text handler), ParseLevel
└── logging_test.go                 3 tests

internal/engine/dedup/
└── dedup.go                        DeduplicateBatch, time-window grouping, summary rewriting

internal/output/
├── output.go                       Output interface (Write + Close)
└── stdout/
    └── stdout.go                   JSON encoder on os.Stdout, pretty-print option
```

---

## Key Constants

| Constant | Value | Location |
|----------|-------|----------|
| Default dedup window | 5s | `config.go` |
| Default max buffer size | 1000 | `config.go` |
| Default shutdown timeout | 10s | `config.go` |
| Default mode | `stream` | `config.go` |
| Default log level | `info` | `config.go` |
| Default confidence threshold | 0.5 | `config.go` |
| Default verbosity | `standard` | `config.go` |
| Signal channel buffer | 2 | `main.go` |
| Version | `0.5.1-beta` | `config.go` |

---

## Design Decisions

**`Processor` interface over concrete `*engine.Engine`.** The pipeline calls `Process()` and `ProcessBatch()` — that's all it needs. Extracting an interface enables mock-based tests for error injection, batch fallback, and skip counting without ONNX model files. Unit tests run in milliseconds, not seconds.

**Atomic skip counter over channel-based counting.** `sync/atomic.Int64` is simpler and has zero contention for the expected case (most logs succeed). A channel-based approach would need a goroutine to drain counts, adding complexity for no benefit. The counter is reported once on pipeline close.

**`context.Background()` for final dedup flush.** When the pipeline's context is cancelled on shutdown, the original context would cause `output.Write()` to fail immediately. Using `context.Background()` for the final flush allows writes to complete. The shutdown timer in `main.go` provides the hard bound — if the flush takes too long, the timer fires and `os.Exit(1)` kills the process.

**`flag.Visit()` for CLI override.** Go's `flag` package treats the default value and "not set" identically. `flag.Visit()` only visits flags explicitly passed on the command line. This prevents flag defaults from silently overriding env var values. Without this, `-limit 0` (the default) would always override `LUMBER_QUERY_LIMIT=100`.

**Collect-all-errors validation.** `Validate()` accumulates all errors into a slice and returns them as a single formatted error. This lets users fix all config issues in one pass instead of fixing one, re-running, hitting the next, and repeating. Parse errors from `LoadWithFlags()` are folded in via the `parseErrors` field.

**`log/slog` over third-party loggers.** Stdlib since Go 1.21, no dependencies, structured by default. `slog.SetDefault()` means all call sites use `slog.Info()` directly without passing logger instances through the call stack. JSON handler on stderr when stdout carries data prevents log/data interleaving.

**Batch fallback in query mode.** `ProcessBatch` is the fast path — one ONNX inference call for the entire query result. But if it fails (one bad log can crash the batch), the pipeline falls back to `processIndividual()`, which processes logs one at a time with skip-and-continue. This maximizes data recovery from partial failures.

**Lazy timer in streamBuffer.** The timer is only created when the first event arrives after a flush. An idle pipeline (no incoming logs) creates no timers. This avoids unnecessary timer goroutines during quiet periods.

**Bounded buffer with no-drop policy.** When the buffer hits `maxSize`, it force-flushes immediately rather than dropping events. This prevents unbounded memory growth during log storms while preserving data completeness. The bound is configurable and defaults to 1000 — large enough for normal bursts, small enough to cap memory usage.

---

## Dependencies

The pipeline layer adds zero external dependencies. Everything is standard library:

- `context` — cancellation propagation through the pipeline
- `sync` — mutex for stream buffer, atomic for skip counter
- `sync/atomic` — lock-free skip counter
- `time` — timers, durations, tickers
- `fmt` — error wrapping
- `log/slog` — structured logging
- `flag` — CLI flag parsing
- `os` — env vars, signal handling, file existence checks
- `os/signal` — SIGINT/SIGTERM handling
- `syscall` — signal constants
- `strconv` — env var parsing
- `strings` — string manipulation
- `encoding/json` — JSON output encoding
