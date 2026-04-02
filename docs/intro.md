# Lumber — Technical Introduction

**Version 0.8.0** | Go 1.24 | 2 external dependencies | ~69 source files

Lumber is a log normalization pipeline. Raw logs from any provider go in — structured, classified, token-efficient canonical events come out. Classification runs entirely on-device using a local embedding model. No cloud API calls in the processing path.

---

## The Problem

Logs are fragmented across two dimensions:

1. **Provider fragmentation** — Vercel, AWS, Fly.io, Datadog, and Grafana each expose different APIs, auth mechanisms, and response formats for the same fundamental thing: timestamped events from running software.

2. **Application-level chaos** — Even within a single provider, every application logs differently. Four services reporting a database connection failure will produce four structurally unrelated log lines.

This makes downstream consumption — whether by LLM agents, dashboards, or alerting systems — unreliable, token-wasteful, and brittle.

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      CONNECTORS                          │
│                                                          │
│    Vercel  │  Fly.io  │  Supabase  │  (extensible)      │
│                                                          │
│    Auth, pagination, rate limiting, polling, raw log     │
│    retrieval — one thin adapter per provider              │
└────────────────────────┬────────────────────────────────┘
                         │
                         ▼  []model.RawLog
┌─────────────────────────────────────────────────────────┐
│                 CLASSIFICATION ENGINE                     │
│                                                          │
│    1. Embed    — log text → 1024-dim vector (ONNX)       │
│    2. Classify — cosine similarity against taxonomy       │
│    3. Compact  — strip noise, truncate, summarize         │
│    4. Dedup    — collapse repeated events within window   │
└────────────────────────┬────────────────────────────────┘
                         │
                         ▼  []model.CanonicalEvent
┌─────────────────────────────────────────────────────────┐
│                        OUTPUT                            │
│                                                          │
│    Multi-router → stdout │ file │ webhook                │
│    Async wrapper for non-blocking delivery                │
└─────────────────────────────────────────────────────────┘
```

### Data Flow

Every log follows the same path regardless of provider or format:

```
RawLog{Timestamp, Source, Raw, Metadata}
  → embed Raw text into 1024-dim vector
  → cosine similarity against 42 pre-embedded taxonomy labels
  → best match becomes Type + Category + Severity + Confidence
  → compact Raw text (truncation, field stripping, stack trace reduction)
  → summarize first line (≤120 runes at word boundary)
  → CanonicalEvent{Type, Category, Severity, Timestamp, Summary, Confidence, Raw, Count}
```

---

## Core Components

### Connectors (`internal/connector/`)

Thin adapters that implement a common interface:

```go
type Connector interface {
    Stream(ctx context.Context, cfg ConnectorConfig) (<-chan RawLog, error)
    Query(ctx context.Context, cfg ConnectorConfig, params QueryParams) ([]RawLog, error)
}
```

- **Stream** — continuous polling with cursor-based deduplication
- **Query** — bounded historical fetch with pagination

Three connectors are implemented:

| Provider | API Style | Pagination | Time Filtering |
|----------|-----------|------------|----------------|
| Vercel | REST JSON | Cursor (`pagination.next`) | Server-side (`from`/`to` unix ms) |
| Fly.io | REST JSON | Token (`next_token`) | Client-side half-open `[start, end)` |
| Supabase | SQL over REST | Chunked (24h windows) | SQL `WHERE timestamp` |

All three use a shared HTTP client (`internal/connector/httpclient/`) that handles Bearer auth, retry with exponential backoff on 5xx, and `Retry-After` on 429.

New connectors register via `init()` into a global registry. The pipeline resolves them by name at startup.

### Embedding (`internal/engine/embedder/`)

The embedder converts text into 1024-dimensional vectors using a local ONNX model:

```
text → tokenize (WordPiece) → ONNX inference → mean pool → dense projection → []float32
```

| Component | What It Does |
|-----------|-------------|
| **Tokenizer** | WordPiece subword tokenization against a 30k vocabulary. Dynamic padding to actual sequence length. |
| **ONNX Session** | Runs the LEAF model (23M params, quantized) via `onnxruntime-go`. Produces per-token hidden states. |
| **Mean Pooling** | Averages token embeddings respecting the attention mask, so pad tokens don't contribute. |
| **Dense Projection** | Multiplies pooled output by a learned weight matrix (loaded from safetensors) to produce final 1024-dim vectors. |

The model is MongoDB's LEAF (`mdbr-leaf-ir`) — ranked #1 on MTEB BEIR for models under 100M parameters. It runs on CPU in ~5-10ms per log line.

`Embed()` handles single texts. `EmbedBatch()` packs multiple inputs into one ONNX call for throughput.

### Taxonomy (`internal/engine/taxonomy/`)

An opinionated tree of 42 leaf labels across 8 root categories:

| Category | Leaves | Examples |
|----------|--------|----------|
| **ERROR** | 9 | connection_failure, auth_failure, timeout, runtime_exception, rate_limited |
| **REQUEST** | 5 | success, client_error, server_error, redirect, slow_request |
| **DEPLOY** | 7 | build_started, build_succeeded, build_failed, deploy_started, deploy_failed, rollback |
| **SYSTEM** | 5 | health_check, scaling_event, resource_alert, process_lifecycle, config_change |
| **ACCESS** | 5 | login_success, login_failure, session_expired, permission_change, api_key_event |
| **PERFORMANCE** | 5 | latency_spike, throughput_drop, queue_backlog, cache_event, db_slow_query |
| **DATA** | 3 | query_executed, migration, replication |
| **SCHEDULED** | 3 | cron_started, cron_completed, cron_failed |

At startup, every leaf is embedded using its parent context and a tuned semantic description (e.g., `"ERROR: TCP connection refused, ECONNREFUSED, dial tcp, NXDOMAIN..."`). These 42 vectors are computed once and held in memory.

Classification is then a single cosine similarity comparison between a log's embedding and all 42 label vectors. Best match wins. If the best score falls below the confidence threshold (default 0.5), the event is marked `UNCLASSIFIED`.

The 153-entry labeled test corpus validates classification at 100% top-1 accuracy.

### Compactor (`internal/engine/compactor/`)

Reduces token footprint through three verbosity tiers:

| Verbosity | Text Truncation | Stack Traces | JSON Fields |
|-----------|----------------|--------------|-------------|
| **Minimal** | 200 runes | 5 frames + last 2 | Strip trace_id, span_id, request_id, etc. |
| **Standard** | 2000 runes | 10 frames + last 2 | Strip same high-cardinality fields |
| **Full** | No truncation | Preserve all | Preserve all |

Stack trace detection covers Java (`at ...`), Go (`goroutine`, `.go:\d+`), and Python patterns. Middle frames are replaced with `"(N frames omitted)"` to preserve the entry point and the crash site.

Summary extraction takes the first line of the log, truncated to 120 runes at a word boundary.

### Deduplication (`internal/engine/dedup/`)

Within a configurable time window (default 5s), events with the same `Type.Category` key are collapsed:

```
ERROR.connection_failure ×47 in last 5s
```

First-occurrence order is preserved. The `Count` field on `CanonicalEvent` tracks how many events were merged.

### Pipeline (`internal/pipeline/`)

Orchestrates the full flow from connector through engine to output. Supports two modes:

**Stream mode** — continuous processing with optional dedup buffering:
- Without dedup: events flow through immediately (process → write)
- With dedup: events accumulate in a bounded buffer, flushed on timer expiry or when the buffer hits max size (default 1000)

**Query mode** — one-shot batch processing:
- Fetches historical logs via `connector.Query()`
- Batch-embeds and classifies all logs
- Deduplicates the batch
- Writes all events to output

Error handling is per-log: one bad log increments an atomic skip counter and processing continues. In query mode, a failed `ProcessBatch()` falls back to individual processing with skip-and-continue.

### Output (`internal/output/`)

A multi-destination fan-out system. Each `Write()` call is delivered to all configured outputs. The `Output` interface:

```go
type Output interface {
    Write(ctx context.Context, event CanonicalEvent) error
    Close() error
}
```

Four backends are implemented:

| Backend | Package | Behavior |
|---------|---------|----------|
| **stdout** | `internal/output/stdout/` | NDJSON or pretty-printed JSON to stdout |
| **File** | `internal/output/file/` | NDJSON with 64KB buffered writes, size-based rotation (`.1`→`.2`→...`.10`) |
| **Webhook** | `internal/output/webhook/` | Batched HTTP POST (default 50 events / 5s timer), retry on 5xx with exponential backoff, custom headers |
| **Multi** | `internal/output/multi/` | Fan-out router — delivers each event to N outputs sequentially, error-isolated via `errors.Join` |

The **async wrapper** (`internal/output/async/`) decouples production from consumption via a buffered channel (default 1024). Two modes: backpressure (blocks when full, default) and drop-on-full (lossy, for non-critical outputs). Used to wrap file and webhook backends so slow I/O doesn't block the pipeline.

Field omission is verbosity-aware: at Minimal, `Raw` and `Confidence` are stripped from the JSON output.

### Public Library API (`pkg/lumber/`)

Lumber is importable as a Go library. The `pkg/lumber` package exposes a stable public API so other Go programs can embed the classification engine directly — no subprocess, no HTTP, no serialization overhead.

```go
l, _ := lumber.New(lumber.WithModelDir("./models"))
defer l.Close()

event, _ := l.Classify("ERROR [2026-02-19] UserService — connection refused")
// event.Type == "ERROR", event.Category == "connection_failure"

events, _ := l.ClassifyBatch([]string{"line1", "line2", "line3"})
```

| Function | Purpose |
|----------|---------|
| `New(opts ...Option)` | Load ONNX model, pre-embed taxonomy (~100-300ms). Create once, reuse. |
| `Classify(text)` | Classify a single log line → `Event` |
| `ClassifyBatch(texts)` | Batched inference for multiple lines (more efficient than looping `Classify`) |
| `ClassifyLog(log)` / `ClassifyLogs(logs)` | Structured input with timestamp, source, and metadata |
| `Taxonomy()` | Returns the taxonomy tree for read-only introspection |
| `Close()` | Release ONNX resources |

Options: `WithModelDir`, `WithModelPaths`, `WithConfidenceThreshold`, `WithVerbosity`. Safe for concurrent use.

### Configuration (`internal/config/`)

All settings load from environment variables with CLI flag overrides. Flags use `flag.Visit()` to overlay only explicitly-set values, avoiding silent override of env vars by flag defaults.

Validation runs at startup and collects all errors (not just the first): missing API keys, nonexistent model files, out-of-range thresholds, invalid enums, query mode without time bounds, invalid webhook URLs, nonexistent file output directories.

#### Output-related settings

| Environment Variable | CLI Flag | Default | Description |
|---------------------|----------|---------|-------------|
| `LUMBER_OUTPUT_FILE` | `-output-file` | `` | File path for NDJSON output (empty = disabled) |
| `LUMBER_OUTPUT_FILE_MAX_SIZE` | — | `0` | Max file size before rotation (bytes, 0 = no rotation) |
| `LUMBER_WEBHOOK_URL` | `-webhook-url` | `` | Webhook endpoint URL (empty = disabled) |

### Logging (`internal/logging/`)

Lumber's own internal logging (distinct from the logs it processes) uses Go 1.21+ `log/slog`. When output is stdout, the logger writes JSON to stderr to avoid mixing with NDJSON event data. Configurable via `LUMBER_LOG_LEVEL`.

---

## Operational Modes

### CLI

```bash
# Stream mode (continuous)
LUMBER_API_KEY=tok_xxx LUMBER_VERCEL_PROJECT_ID=prj_xxx lumber

# Query mode (historical)
lumber -mode query -connector vercel -from 2026-02-24T00:00:00Z -to 2026-02-24T01:00:00Z

# With options
lumber -verbosity minimal -pretty -log-level debug

# With file and webhook output
lumber -output-file /var/log/lumber/events.jsonl -webhook-url https://hooks.example.com/logs
```

### Lifecycle

1. Load config (env vars + CLI flags)
2. Validate config (fail fast)
3. Initialize embedder (load ONNX model, vocab, projection weights)
4. Pre-embed all 42 taxonomy labels
5. Create engine, build multi-output (stdout + async file + async webhook), create connector and pipeline
6. Run stream or query
7. On SIGINT/SIGTERM: cancel context, drain buffer within shutdown timeout, exit
8. On second signal or timeout: force exit

---

## Dependencies

| Dependency | Purpose |
|------------|---------|
| `github.com/yalue/onnxruntime_go` | Go bindings for ONNX Runtime (model inference) |
| `golang.org/x/text` | Unicode normalization for tokenizer |

Everything else is Go standard library.

---

## Model Files

Downloaded via `make download-model` from the official `MongoDB/mdbr-leaf-mt` HuggingFace repository and stored in `models/`:

| File | Size | Purpose |
|------|------|---------|
| `model_quantized.onnx` | ~50MB | LEAF embedding model (quantized) |
| `vocab.txt` | ~200KB | WordPiece vocabulary (30k tokens) |
| `2_Dense/model.safetensors` | ~4MB | Dense projection layer weights |

---

## Test Infrastructure

- **153-entry labeled corpus** (`internal/engine/testdata/corpus.json`) — real-world log samples spanning all 42 taxonomy categories, validated at 100% accuracy
- **httptest fixtures** for all three connectors — no live API keys needed
- **Mock `Processor` interface** — enables pipeline testing without ONNX model files
- **Integration tests** — full end-to-end: httptest server → connector → real ONNX engine → mock output (guarded by `skipWithoutModel`)

```bash
go test ./...                    # Unit tests (always pass, no model needed)
go test ./... -run Integration   # Integration tests (require model files)
```
