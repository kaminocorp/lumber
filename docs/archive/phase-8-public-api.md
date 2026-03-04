# Phase 8: Public Library API

**Goal:** Make Lumber importable as a Go library. After this phase, any Go application can `go get github.com/kaminocorp/lumber` and classify log text into canonical events — no subprocess, no stdout parsing, no CLI.

**Starting point:** Everything lives under `internal/`. The only entry point is `cmd/lumber/main.go`. Lumber cannot be used as a library.

---

## What Changes and Why

### The `internal/` barrier

Go enforces that packages under `internal/` can only be imported by code within the same module. This is a compile-time guarantee — there's no workaround, no build flag, no escape hatch. Every package Lumber has (`engine`, `embedder`, `classifier`, `compactor`, `taxonomy`, `model`, `output`, etc.) is behind this wall.

To make Lumber importable, we need a **public API surface** — a `pkg/` package that external code can import. This package wraps the internal machinery and exposes a stable, minimal contract.

### Design principle: thin public surface, fat internals

The public API should expose **what Lumber does** (classify log text), not **how it does it** (ONNX sessions, tokenizers, taxonomy trees, cosine similarity). Consumers don't need to know about embedding dimensions, projection layers, or WordPiece vocabularies. They need:

1. Create a Lumber instance (pointing it at model files)
2. Classify text → get a structured event
3. Close when done

Everything else stays internal. This means we can freely refactor internals (swap models, change the classifier, restructure packages) without breaking consumers.

---

## Implementation Plan

### Section 1: Public Types

**Package:** `pkg/lumber/`

Define the public-facing types that consumers interact with. These are deliberately separate from `internal/model` — the public types are the stable contract, while internal types can evolve freely.

```go
// pkg/lumber/event.go

// Event is a classified, normalized log event.
type Event struct {
    Type       string    `json:"type"`               // Root category: ERROR, REQUEST, DEPLOY, etc.
    Category   string    `json:"category"`            // Leaf label: connection_failure, success, etc.
    Severity   string    `json:"severity"`            // error, warning, info, debug
    Timestamp  time.Time `json:"timestamp"`
    Summary    string    `json:"summary"`             // First line, ≤120 runes
    Confidence float64   `json:"confidence,omitempty"` // Cosine similarity score
    Raw        string    `json:"raw,omitempty"`        // Compacted original text
    Count      int       `json:"count,omitempty"`      // >0 when deduplicated
}
```

```go
// pkg/lumber/options.go

// Option configures a Lumber instance.
type Option func(*options)

type options struct {
    modelDir            string
    modelPath           string
    vocabPath           string
    projectionPath      string
    confidenceThreshold float64
    verbosity           string
}

// WithModelDir sets the directory containing model files.
// Expects: model_quantized.onnx, vocab.txt, 2_Dense/model.safetensors.
// This is the simplest way to configure — just point at the models/ directory.
func WithModelDir(dir string) Option

// WithModelPaths sets explicit paths for each model file.
// Use this when model files aren't in the default layout.
func WithModelPaths(model, vocab, projection string) Option

// WithConfidenceThreshold sets the minimum cosine similarity for classification.
// Below this threshold, events are marked UNCLASSIFIED. Default: 0.5.
func WithConfidenceThreshold(t float64) Option

// WithVerbosity sets the compaction verbosity: "minimal", "standard", "full".
// Default: "standard".
func WithVerbosity(v string) Option
```

**Why separate types from `internal/model`?** Two reasons:
1. **Stability** — `internal/model.CanonicalEvent` might gain fields, change tags, or restructure. The public `Event` type is the stable contract.
2. **Simplicity** — the public type doesn't need to expose every internal field. We can add derived/computed fields later without changing internals.

In practice, for now, `Event` and `CanonicalEvent` are structurally identical. A simple conversion function bridges them.

**Files:**
| File | Action |
|------|--------|
| `pkg/lumber/event.go` | new |
| `pkg/lumber/options.go` | new |

---

### Section 2: Core API

**Package:** `pkg/lumber/`

The main entry point. Wraps the internal engine behind a clean constructor and two methods.

```go
// pkg/lumber/lumber.go

// Lumber is a log classification engine.
// It embeds log text into vectors and classifies against a 42-label taxonomy.
// Safe for concurrent use.
type Lumber struct {
    engine   *engine.Engine
    embedder embedder.Embedder
}

// New creates a Lumber instance, loading model files and pre-embedding
// the taxonomy. This is an expensive operation (~100-300ms) — create once,
// reuse across requests.
func New(opts ...Option) (*Lumber, error)

// Classify classifies a single log line and returns a canonical event.
func (l *Lumber) Classify(text string) (Event, error)

// ClassifyBatch classifies multiple log lines in a single batched inference call.
// More efficient than calling Classify in a loop.
func (l *Lumber) ClassifyBatch(texts []string) ([]Event, error)

// Close releases model resources (ONNX runtime, memory).
// Must be called when the Lumber instance is no longer needed.
func (l *Lumber) Close() error
```

**Internal wiring in `New()`:**
```go
func New(opts ...Option) (*Lumber, error) {
    o := defaultOptions()
    for _, opt := range opts {
        opt(&o)
    }

    // Resolve model paths.
    modelPath, vocabPath, projPath := resolvePaths(o)

    // Initialize embedder (ONNX + tokenizer + projection).
    emb, err := embedder.New(modelPath, vocabPath, projPath)
    if err != nil {
        return nil, fmt.Errorf("lumber: %w", err)
    }

    // Pre-embed taxonomy.
    tax, err := taxonomy.New(taxonomy.DefaultRoots(), emb)
    if err != nil {
        emb.Close()
        return nil, fmt.Errorf("lumber: %w", err)
    }

    // Build engine.
    cls := classifier.New(o.confidenceThreshold)
    cmp := compactor.New(parseVerbosity(o.verbosity))
    eng := engine.New(emb, tax, cls, cmp)

    return &Lumber{engine: eng, embedder: emb}, nil
}
```

**`Classify()` implementation:**
```go
func (l *Lumber) Classify(text string) (Event, error) {
    raw := model.RawLog{
        Timestamp: time.Now(),
        Raw:       text,
    }
    ce, err := l.engine.Process(raw)
    if err != nil {
        return Event{}, err
    }
    return eventFromCanonical(ce), nil
}
```

**`ClassifyBatch()` implementation:**
```go
func (l *Lumber) ClassifyBatch(texts []string) ([]Event, error) {
    raws := make([]model.RawLog, len(texts))
    now := time.Now()
    for i, t := range texts {
        raws[i] = model.RawLog{Timestamp: now, Raw: t}
    }
    ces, err := l.engine.ProcessBatch(raws)
    if err != nil {
        return nil, err
    }
    events := make([]Event, len(ces))
    for i, ce := range ces {
        events[i] = eventFromCanonical(ce)
    }
    return events, nil
}
```

**Concurrency:** The underlying `engine.Engine` calls `embedder.Embed()` which calls `onnxSession.infer()`. ONNX Runtime sessions are thread-safe for concurrent `Run()` calls (with some caveats around session options). The engine itself is stateless — no mutexes needed at the engine level. We should document that `Lumber` is safe for concurrent use and validate with a concurrent test.

**Tests:**
- `New()` with `WithModelDir()` loads successfully (ONNX-gated)
- `New()` with bad path returns clear error
- `Classify()` returns correct type/category for known log lines
- `ClassifyBatch()` matches individual `Classify()` results
- Empty/whitespace input returns UNCLASSIFIED
- Concurrent `Classify()` calls are safe (race detector)
- `Close()` releases resources (no goroutine leak)

**Files:**
| File | Action |
|------|--------|
| `pkg/lumber/lumber.go` | new |
| `pkg/lumber/lumber_test.go` | new |

---

### Section 3: Classify with Metadata

Add an overload that accepts timestamps and source metadata, for when the consumer has structured log data (not just raw text).

```go
// ClassifyLog classifies a structured log entry. Use this when you have
// timestamp and source information. For raw text, use Classify().
func (l *Lumber) ClassifyLog(log Log) (Event, error)

// ClassifyLogs classifies a batch of structured log entries.
func (l *Lumber) ClassifyLogs(logs []Log) ([]Event, error)

// Log is a raw log entry with optional metadata.
type Log struct {
    Text      string            // The log text to classify
    Timestamp time.Time         // When the log was produced (zero = time.Now())
    Source    string             // Provider/origin name (optional)
    Metadata map[string]any     // Additional context (optional, not used in classification)
}
```

**Why a separate type from `Event`?** `Log` is input; `Event` is output. They have different fields and different purposes. `Log.Text` maps to `RawLog.Raw`; `Log.Timestamp` maps to `RawLog.Timestamp`. The separation makes the API self-documenting.

**Tests:**
- `ClassifyLog()` preserves timestamp and source
- Zero timestamp defaults to `time.Now()`
- Metadata passes through (if/when CanonicalEvent gains a Metadata field)

**Files:**
| File | Action |
|------|--------|
| `pkg/lumber/lumber.go` | modified (add methods) |
| `pkg/lumber/log.go` | new |
| `pkg/lumber/lumber_test.go` | modified (add tests) |

---

### Section 4: Taxonomy Introspection

Expose the taxonomy so consumers can discover available categories, build UIs, or validate classifications.

```go
// pkg/lumber/taxonomy.go

// Category represents a taxonomy category with its leaves.
type Category struct {
    Name     string   // Root name: ERROR, REQUEST, DEPLOY, etc.
    Labels   []Label  // Leaf labels under this root
}

// Label represents a single taxonomy leaf.
type Label struct {
    Name     string // e.g. "connection_failure"
    Path     string // e.g. "ERROR.connection_failure"
    Severity string // error, warning, info, debug
}

// Taxonomy returns the current taxonomy tree.
func (l *Lumber) Taxonomy() []Category
```

**This is read-only.** Consumers can inspect the taxonomy but not modify it. Modification (adaptive taxonomy) is a future concern.

**Tests:**
- Returns all 8 root categories
- Returns all 42 leaf labels
- Paths are correctly formatted

**Files:**
| File | Action |
|------|--------|
| `pkg/lumber/taxonomy.go` | new |
| `pkg/lumber/taxonomy_test.go` | new |

---

### Section 5: Example and Documentation

A runnable example that demonstrates the library API end-to-end.

```go
// pkg/lumber/example_test.go

func Example() {
    l, err := lumber.New(lumber.WithModelDir("../../models"))
    if err != nil {
        log.Fatal(err)
    }
    defer l.Close()

    event, err := l.Classify("ERROR [2026-02-28] UserService — connection refused (host=db-primary, port=5432)")
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Type: %s, Category: %s, Severity: %s\n", event.Type, event.Category, event.Severity)
    fmt.Printf("Confidence: %.3f\n", event.Confidence)
    fmt.Printf("Summary: %s\n", event.Summary)
    // Output:
    // Type: ERROR, Category: connection_failure, Severity: error
    // Confidence: 0.XXX
    // Summary: ERROR [2026-02-28] UserService — connection refused (host=db-primary, port=5432)
}
```

**Also:** A `doc.go` with package-level documentation explaining what Lumber is, the minimal setup, and a pointer to the full README.

**Files:**
| File | Action |
|------|--------|
| `pkg/lumber/example_test.go` | new |
| `pkg/lumber/doc.go` | new |

---

### Section 6: Consumer Integration Patterns

This section doesn't produce code in Lumber itself — it's about documenting how consumers actually use the library. Captured in the README or a separate guide.

**Pattern A: Simple classification (your use case (a) — store)**
```go
l, _ := lumber.New(lumber.WithModelDir("models/"))
defer l.Close()

// In your log handler:
event, _ := l.Classify(logLine)
db.Insert(event) // store for persistence
```

**Pattern B: Classify and forward (your use case (b) — funnel)**
```go
l, _ := lumber.New(lumber.WithModelDir("models/"))
defer l.Close()

event, _ := l.Classify(logLine)
eventJSON, _ := json.Marshal(event)
natsConn.Publish("classified-logs", eventJSON) // forward to another node
```

**Pattern C: Async fan-out (your use case (c) — both, asynchronously)**
```go
l, _ := lumber.New(lumber.WithModelDir("models/"))
defer l.Close()

events := make(chan lumber.Event, 1000)

// Producer
go func() {
    for logLine := range incomingLogs {
        event, err := l.Classify(logLine)
        if err != nil {
            continue
        }
        events <- event
    }
}()

// Consumer A: store
go func() {
    for event := range events {
        db.Insert(event)
    }
}()

// Consumer B: forward
go func() {
    for event := range events {
        otherNode.Send(event)
    }
}()
```

**Note on Pattern C:** The channel fan-out shown above has a problem — only one consumer receives each event. For true fan-out (both consumers get every event), the user needs a broadcast pattern or two separate channels. We should document this clearly, possibly with a small helper or by recommending existing libraries.

**The key insight for consumers:** Lumber's library API gives you `Event` values. *You* own the routing, storage, and forwarding. Lumber classifies; you decide what happens next. This is intentional — the CLI tool (`cmd/lumber`) handles the full pipeline for standalone use, while the library gives you the classification engine to embed in your own pipeline.

**Files:**
| File | Action |
|------|--------|
| README.md | modified (add Library Usage section) |

---

## Dependency Graph

```
Section 1: Public Types ──→ Section 2: Core API ──→ Section 3: Classify with Metadata
                                     │
                                     ├──→ Section 4: Taxonomy Introspection
                                     │
                                     └──→ Section 5: Examples ──→ Section 6: Docs
```

Sections 1 and 2 are the critical path. Sections 3–6 build on them independently.

---

## Open Questions

### 1. Module path for the public package

Currently the module is `github.com/kaminocorp/lumber`. The public API would be imported as:

```go
import "github.com/kaminocorp/lumber/pkg/lumber"
```

This is slightly redundant (`lumber/pkg/lumber`). Alternative: put the public API at the module root — but that requires moving internal code or restructuring the module. The `pkg/lumber` path is the Go convention and avoids disruption.

### 2. Model file discovery

Consumers need model files on disk. Three options:

| Approach | Pros | Cons |
|----------|------|------|
| **Explicit path** (`WithModelDir`) | Clear, no magic | Consumer must know where files are |
| **Embed in binary** (`//go:embed`) | Zero config, just `go get` and use | Adds ~55MB to every binary that imports Lumber |
| **Auto-download** (`lumber.EnsureModel()`) | One-time setup, explicit | Requires network, caching logic |

**Recommendation:** Start with explicit paths (`WithModelDir`). Add `EnsureModel()` as a convenience helper later. Never embed 55MB in a library.

### 3. Versioning and stability

The public API is a compatibility promise. Once published, breaking changes require a major version bump. We should:
- Start at v0.x (pre-1.0) to preserve flexibility
- Use the existing `config.Version` for the CLI, separate API versioning for the library
- Consider a `lumber.Version()` function returning the library version

### 4. Language bindings (npm/pip)

The user mentioned npm/pnpm/pip. Go libraries aren't directly installable via npm or pip. Options for non-Go consumers:

| Approach | What it gives you | Effort |
|----------|-------------------|--------|
| **CLI binary** (current) | `lumber` as a subprocess; any language can shell out | Already works |
| **HTTP server mode** (Phase 7 or later) | Language-agnostic REST API; `POST /classify` | Requires Phase 3.2 from proposals |
| **CGo shared library** | `.so`/`.dylib` callable from Python/Node via FFI | High effort, fragile |
| **WASM** | Run in browser/Node.js | ONNX Runtime doesn't support WASM target |

**Recommendation:** For non-Go consumers, the HTTP server mode (from post-beta proposals 3.2) is the right path. A `POST /classify` endpoint is universally consumable — Python, Node, Ruby, anything with an HTTP client. This becomes a natural follow-up to this phase.

---

## Summary

| Section | Files | What |
|---------|-------|------|
| 1. Public Types | 2 new | `Event`, `Option`, public type definitions |
| 2. Core API | 2 new | `New()`, `Classify()`, `ClassifyBatch()`, `Close()` |
| 3. Structured Input | 1 new, 1 modified | `ClassifyLog()`, `Log` type |
| 4. Taxonomy | 2 new | `Taxonomy()`, `Category`, `Label` |
| 5. Examples | 2 new | Runnable example + package doc |
| 6. Docs | 1 modified | README library usage section |

**New files: 9. Modified files: 2. Total: 11.**
