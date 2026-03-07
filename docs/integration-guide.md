# Lumber — Library Integration Guide

Use Lumber as an in-process Go library to classify logs without running a CLI or sidecar. Load the ONNX model once at startup, then classify individual lines or batches on demand.

**Import path:**

```bash
go get github.com/kaminocorp/lumber
```

```go
import "github.com/kaminocorp/lumber/pkg/lumber"
```

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [API Reference](#api-reference)
  - [Constructor](#constructor)
  - [Options](#options)
  - [Classification](#classification)
  - [Types](#types)
  - [Taxonomy Introspection](#taxonomy-introspection)
  - [Cleanup](#cleanup)
- [Integration Patterns](#integration-patterns)
  - [Monitoring Agent](#monitoring-agent)
  - [HTTP Middleware](#http-middleware)
  - [Batch Processing Worker](#batch-processing-worker)
- [Performance](#performance)
- [Concurrency](#concurrency)
- [Model Management](#model-management)
- [Taxonomy Reference](#taxonomy-reference)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

1. **Go 1.23+**
2. **ONNX model files** — three files, co-located in a single directory:
   - `model_quantized.onnx` (~23MB, int8 quantized)
   - `vocab.txt` (WordPiece vocabulary, 30,522 tokens)
   - `2_Dense/model.safetensors` (projection weights)

Download them via the Lumber repo:

```bash
git clone https://github.com/kaminocorp/lumber.git
cd lumber && make download-model
# Files land in models/
```

Or fetch them directly from HuggingFace:

```bash
# model_quantized.onnx + vocab.txt
curl -L https://huggingface.co/onnx-community/mdbr-leaf-mt-ONNX/resolve/main/onnx/model_quantized.onnx -o models/model_quantized.onnx
curl -L https://huggingface.co/onnx-community/mdbr-leaf-mt-ONNX/resolve/main/vocab.txt -o models/vocab.txt

# projection weights
mkdir -p models/2_Dense
curl -L https://huggingface.co/Snowflake/mdbr-leaf-mt/resolve/main/2_Dense/model.safetensors -o models/2_Dense/model.safetensors
```

3. **ONNX Runtime shared library** — `libonnxruntime` must be available at runtime. The `make download-model` target handles this. If installing manually, set `ONNX_RUNTIME_LIB` or ensure the `.dylib`/`.so` is on your library path.

---

## Quick Start

```go
package main

import (
    "fmt"
    "log"

    "github.com/kaminocorp/lumber/pkg/lumber"
)

func main() {
    // Load model and pre-embed taxonomy (~100-300ms, do this once)
    l, err := lumber.New(lumber.WithModelDir("/app/models"))
    if err != nil {
        log.Fatal(err)
    }
    defer l.Close()

    // Classify a single log line
    event, err := l.Classify(`{"level":"error","msg":"connection refused","host":"db-primary"}`)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("%s.%s (%.2f) — %s\n", event.Type, event.Category, event.Confidence, event.Summary)
    // ERROR.connection_failure (0.87) — connection refused
}
```

---

## API Reference

### Constructor

```go
func New(opts ...Option) (*Lumber, error)
```

Creates a Lumber engine instance. This is the expensive call — it loads the ONNX model into memory, initializes the tokenizer, and pre-embeds all 42 taxonomy labels. Takes ~100–300ms depending on hardware.

**Call once at application startup.** Reuse the instance for the lifetime of your process.

Returns an error if model files are missing, corrupt, or if the ONNX runtime fails to initialize.

### Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithModelDir(dir)` | `"models"` | Directory containing `model_quantized.onnx`, `vocab.txt`, and `2_Dense/model.safetensors` |
| `WithModelPaths(model, vocab, proj)` | — | Explicit paths for each file. Use when files aren't co-located |
| `WithConfidenceThreshold(t)` | `0.5` | Minimum cosine similarity (0.0–1.0) for a classification to be accepted. Below this, the event is marked `UNCLASSIFIED` |
| `WithVerbosity(v)` | `"standard"` | Controls summary compaction: `"minimal"` (200 chars), `"standard"` (2000 chars), `"full"` (no truncation) |

```go
l, err := lumber.New(
    lumber.WithModelDir("/app/models"),
    lumber.WithConfidenceThreshold(0.6),
    lumber.WithVerbosity("minimal"),
)
```

### Classification

Four methods, covering raw text and structured input, single and batch:

#### `Classify` — single raw text

```go
func (l *Lumber) Classify(text string) (Event, error)
```

Classifies a single log line. The text is embedded, compared against the taxonomy, and returned as a canonical event. Empty or whitespace-only input returns an `UNCLASSIFIED` event (not an error).

```go
event, err := l.Classify("GET /api/health 200 OK 3ms")
// event.Type = "REQUEST", event.Category = "success"
```

#### `ClassifyBatch` — multiple raw texts

```go
func (l *Lumber) ClassifyBatch(texts []string) ([]Event, error)
```

Classifies multiple log lines in a **single batched ONNX inference call**. Significantly faster than calling `Classify` in a loop — the embedder processes all inputs in one forward pass.

```go
events, err := l.ClassifyBatch([]string{
    `ERROR: connection refused to db-primary:5432`,
    `GET /api/users 200 OK 12ms`,
    `Build v2.3.1 succeeded in 45s`,
    `panic: runtime error: index out of range [3] with length 2`,
})
// events[0]: ERROR.connection_failure
// events[1]: REQUEST.success
// events[2]: DEPLOY.build_succeeded
// events[3]: ERROR.runtime_exception
```

#### `ClassifyLog` — single structured input

```go
func (l *Lumber) ClassifyLog(log Log) (Event, error)
```

Use when you have timestamp, source, or metadata alongside the log text. The `Text` field is what gets classified; other fields are carried through to the event.

```go
event, err := l.ClassifyLog(lumber.Log{
    Text:      "connection refused (host=db-primary, port=5432)",
    Timestamp: time.Now(),
    Source:    "vercel",
    Metadata:  map[string]any{"project": "api-prod"},
})
```

#### `ClassifyLogs` — batch structured input

```go
func (l *Lumber) ClassifyLogs(logs []Log) ([]Event, error)
```

Batch version of `ClassifyLog`. Same batched ONNX inference benefits as `ClassifyBatch`.

### Types

#### `Event` — classification result

```go
type Event struct {
    Type       string    `json:"type"`               // Root category: ERROR, REQUEST, DEPLOY, SYSTEM, ACCESS, PERFORMANCE, DATA, SCHEDULED
    Category   string    `json:"category"`            // Leaf label: connection_failure, success, build_started, ...
    Severity   string    `json:"severity"`            // error, warning, info, debug (derived from taxonomy, not input)
    Timestamp  time.Time `json:"timestamp"`           // When the log was produced
    Summary    string    `json:"summary"`             // Compacted first line, <=120 runes
    Confidence float64   `json:"confidence,omitempty"` // Cosine similarity score (0.0–1.0)
    Raw        string    `json:"raw,omitempty"`        // Compacted original text (verbosity-dependent)
    Count      int       `json:"count,omitempty"`      // >0 when deduplicated
}
```

Key fields for most integrations:

- **`Type` + `Category`** — the full taxonomy path, e.g. `ERROR` + `connection_failure`
- **`Confidence`** — how sure the classifier is. Use this to set your own thresholds (e.g. escalate anything below 0.6 for human review)
- **`Severity`** — derived from the taxonomy leaf, not from parsing the log text. Consistent across all log formats

#### `Log` — structured input

```go
type Log struct {
    Text      string         // The log text to classify (required)
    Timestamp time.Time      // When the log was produced (zero = time.Now())
    Source    string         // Provider/origin name (optional)
    Metadata  map[string]any // Additional context (optional, not used in classification)
}
```

#### `Category` and `Label` — taxonomy types

```go
type Category struct {
    Name   string  // Root name: ERROR, REQUEST, DEPLOY, ...
    Labels []Label // Leaf labels under this root
}

type Label struct {
    Name     string // e.g. "connection_failure"
    Path     string // e.g. "ERROR.connection_failure"
    Severity string // error, warning, info, debug
}
```

### Taxonomy Introspection

```go
func (l *Lumber) Taxonomy() []Category
```

Returns the full taxonomy tree. Useful for documentation, validation, or building UI filters.

```go
for _, cat := range l.Taxonomy() {
    for _, label := range cat.Labels {
        fmt.Printf("%-30s  severity=%s\n", label.Path, label.Severity)
    }
}
// ERROR.connection_failure        severity=error
// ERROR.auth_failure              severity=error
// REQUEST.success                 severity=info
// ...42 labels total
```

### Cleanup

```go
func (l *Lumber) Close() error
```

Releases ONNX runtime resources and memory. **Must be called** when the Lumber instance is no longer needed. Use `defer l.Close()` after construction.

---

## Integration Patterns

### Monitoring Agent

A monitoring agent (like Heimdall) that classifies logs each monitoring cycle and escalates problematic ones:

```go
type Monitor struct {
    lumber *lumber.Lumber
}

func NewMonitor(modelDir string) (*Monitor, error) {
    l, err := lumber.New(
        lumber.WithModelDir(modelDir),
        lumber.WithConfidenceThreshold(0.5),
        lumber.WithVerbosity("minimal"),
    )
    if err != nil {
        return nil, fmt.Errorf("monitor: init lumber: %w", err)
    }
    return &Monitor{lumber: l}, nil
}

func (m *Monitor) ProcessCycle(rawLogs []string) (errors, warnings []lumber.Event) {
    events, err := m.lumber.ClassifyBatch(rawLogs)
    if err != nil {
        slog.Error("classification failed", "err", err)
        return nil, nil
    }

    for _, e := range events {
        switch e.Severity {
        case "error":
            errors = append(errors, e)
        case "warning":
            warnings = append(warnings, e)
        }
    }
    return errors, warnings
}

func (m *Monitor) Close() error {
    return m.lumber.Close()
}
```

### HTTP Middleware

Classify request logs inline and attach taxonomy metadata to your observability pipeline:

```go
func ClassifyMiddleware(l *lumber.Lumber) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            rec := &statusRecorder{ResponseWriter: w, statusCode: 200}
            next.ServeHTTP(rec, r)

            logLine := fmt.Sprintf("%s %s %d %s", r.Method, r.URL.Path, rec.statusCode, r.RemoteAddr)
            event, err := l.Classify(logLine)
            if err == nil {
                slog.Info("request",
                    "method", r.Method,
                    "path", r.URL.Path,
                    "status", rec.statusCode,
                    "lumber.type", event.Type,
                    "lumber.category", event.Category,
                    "lumber.confidence", event.Confidence,
                )
            }
        })
    }
}
```

### Batch Processing Worker

Process log files or queue messages in bulk:

```go
func processLogFile(l *lumber.Lumber, path string) error {
    f, err := os.Open(path)
    if err != nil {
        return err
    }
    defer f.Close()

    var batch []string
    scanner := bufio.NewScanner(f)
    for scanner.Scan() {
        batch = append(batch, scanner.Text())

        // Flush every 100 lines for optimal batch size
        if len(batch) >= 100 {
            events, err := l.ClassifyBatch(batch)
            if err != nil {
                return fmt.Errorf("classify batch: %w", err)
            }
            for _, e := range events {
                fmt.Printf("%s.%-22s %.2f  %s\n", e.Type, e.Category, e.Confidence, e.Summary)
            }
            batch = batch[:0]
        }
    }

    // Flush remaining
    if len(batch) > 0 {
        events, err := l.ClassifyBatch(batch)
        if err != nil {
            return fmt.Errorf("classify batch: %w", err)
        }
        for _, e := range events {
            fmt.Printf("%s.%-22s %.2f  %s\n", e.Type, e.Category, e.Confidence, e.Summary)
        }
    }
    return scanner.Err()
}
```

---

## Performance

### Benchmarks

| Operation | Time | Notes |
|-----------|------|-------|
| `New()` (initialization) | ~100–300ms | Model load + taxonomy pre-embedding. Once per process |
| `Classify()` (single) | ~5–10ms | Single ONNX forward pass |
| `ClassifyBatch()` (100 lines) | ~50–80ms | Batched inference, ~0.5–0.8ms per line |
| Memory footprint | ~80–120MB | ONNX model + runtime + taxonomy vectors |

Benchmarks measured on an M1 MacBook Air (8GB). CPU-only inference — no GPU required.

### Batch Size Guidance

- **1–10 lines:** `Classify` in a loop is fine. Overhead is minimal.
- **10–200 lines:** Use `ClassifyBatch`. Single ONNX call amortizes setup cost.
- **200+ lines:** Chunk into batches of ~100–200. Keeps memory predictable and latency consistent.

### Optimization Tips

- **Reuse the `Lumber` instance.** Construction is expensive (~200ms). Never create per-request.
- **Prefer `ClassifyBatch` over loops.** A batch of 100 lines is ~10x faster than 100 individual `Classify` calls.
- **Use `"minimal"` verbosity** if you only need `Type`, `Category`, `Severity`, and `Confidence`. This reduces compaction work and output size.
- **Empty/whitespace lines are handled gracefully.** They return `UNCLASSIFIED` without hitting the ONNX model, so you don't need to pre-filter.

---

## Concurrency

The `Lumber` instance is **safe for concurrent use** from multiple goroutines. The ONNX runtime session, taxonomy vectors, and classifier are all read-only after initialization.

```go
// Safe: shared across goroutines
l, _ := lumber.New(lumber.WithModelDir("models/"))
defer l.Close()

var wg sync.WaitGroup
for i := 0; i < 10; i++ {
    wg.Add(1)
    go func(id int) {
        defer wg.Done()
        event, _ := l.Classify(fmt.Sprintf("request %d completed 200 OK", id))
        fmt.Printf("[worker %d] %s.%s\n", id, event.Type, event.Category)
    }(i)
}
wg.Wait()
```

No mutexes, no pooling, no per-goroutine instances needed.

---

## Model Management

### File Layout

Lumber expects three files in a single directory:

```
models/
  model_quantized.onnx        # ONNX model (23MB)
  vocab.txt                    # WordPiece vocabulary
  2_Dense/
    model.safetensors          # Projection layer weights
```

### Custom Paths

If your deployment doesn't use this layout, specify each file explicitly:

```go
l, err := lumber.New(lumber.WithModelPaths(
    "/opt/ml/model.onnx",
    "/opt/ml/vocab.txt",
    "/opt/ml/projection.safetensors",
))
```

### Embedding in Docker

```dockerfile
FROM golang:1.24 AS builder
WORKDIR /app
COPY . .
RUN make build

FROM debian:bookworm-slim
COPY --from=builder /app/bin/lumber /usr/local/bin/
COPY --from=builder /app/models/ /app/models/
# Copy ONNX runtime library
COPY --from=builder /app/lib/libonnxruntime.so /usr/local/lib/
RUN ldconfig
```

For library consumers, bundle the model files alongside your application binary and point `WithModelDir` to their location.

---

## Taxonomy Reference

Lumber classifies every log into one of 42 leaf labels across 8 root categories. The confidence score reflects cosine similarity between the log's embedding and the best-matching label.

| Root | Leaf Labels | Severity |
|------|------------|----------|
| **ERROR** | `connection_failure`, `auth_failure`, `authorization_failure`, `timeout`, `runtime_exception`, `validation_error`, `out_of_memory`, `rate_limited`, `dependency_error` | error |
| **REQUEST** | `success` (info), `client_error` (warning), `server_error` (error), `redirect` (info), `slow_request` (warning) | mixed |
| **DEPLOY** | `build_started`, `build_succeeded`, `build_failed`, `deploy_started`, `deploy_succeeded`, `deploy_failed`, `rollback` | mixed |
| **SYSTEM** | `health_check`, `scaling_event`, `resource_alert`, `process_lifecycle`, `config_change` | mixed |
| **ACCESS** | `login_success`, `login_failure`, `session_expired`, `permission_change`, `api_key_event` | mixed |
| **PERFORMANCE** | `latency_spike`, `throughput_drop`, `queue_backlog`, `cache_event`, `db_slow_query` | warning |
| **DATA** | `query_executed`, `migration`, `replication` | info |
| **SCHEDULED** | `cron_started`, `cron_completed`, `cron_failed` | mixed |

Logs below the confidence threshold (default 0.5) are classified as `UNCLASSIFIED` with `Type: "UNCLASSIFIED"` and `Category: ""`.

### Programmatic Access

```go
for _, cat := range l.Taxonomy() {
    fmt.Printf("\n%s (%d labels):\n", cat.Name, len(cat.Labels))
    for _, label := range cat.Labels {
        fmt.Printf("  %-30s  %s\n", label.Path, label.Severity)
    }
}
```

---

## Troubleshooting

### `lumber: failed to load ONNX model`

The ONNX runtime shared library isn't found. Ensure `libonnxruntime.dylib` (macOS) or `libonnxruntime.so` (Linux) is on your library path:

```bash
# macOS
export DYLD_LIBRARY_PATH=/path/to/lumber/lib:$DYLD_LIBRARY_PATH

# Linux
export LD_LIBRARY_PATH=/path/to/lumber/lib:$LD_LIBRARY_PATH
```

Or run `make download-model` in the Lumber repo — it fetches both model files and the runtime library.

### `lumber: open models/model_quantized.onnx: no such file or directory`

Model files aren't at the expected path. Either:
- Set `WithModelDir()` to the correct directory
- Set `WithModelPaths()` for non-standard layouts
- Verify the model was downloaded: `ls -la models/`

### All events return `UNCLASSIFIED`

- **Confidence threshold too high.** Try lowering: `WithConfidenceThreshold(0.3)` and inspect the `Confidence` values on returned events.
- **Model files corrupt or incomplete.** Re-download with `make download-model`.
- **Input is empty/whitespace.** Empty input is intentionally returned as `UNCLASSIFIED`.

### High memory usage

The ONNX model and taxonomy vectors use ~80–120MB. This is a one-time allocation at `New()`. If you're seeing growth beyond that, ensure you're not creating multiple `Lumber` instances — one is enough for the entire process.

### Slow classification

- Use `ClassifyBatch` instead of looping `Classify` — batched inference is ~10x faster per line.
- Ensure you're reusing the `Lumber` instance, not recreating it per request.
- On first call after `New()`, the ONNX runtime may JIT-compile kernels. Subsequent calls are faster.
