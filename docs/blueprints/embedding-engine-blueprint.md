# Embedding Engine Blueprint

## Overview

Lumber's embedding engine is the core value layer of the pipeline. It takes arbitrary raw log text and produces a 1024-dimensional vector representation using a local neural network — no cloud APIs, no network calls, no Python. The entire pipeline runs on-device via ONNX Runtime with pure-Go preprocessing.

The engine serves two purposes:

1. **Taxonomy pre-embedding** — at startup, embed the description text of every taxonomy leaf into the same vector space. This happens once.
2. **Log classification** — at runtime, embed each incoming log line and find the nearest taxonomy label by cosine similarity.

Both operations share the same pipeline: tokenize → infer → pool → project. Same model, same code path, same vector space. A log is classified by placing it in the same space as the labels and measuring distance.

---

## Model

**MongoDB MDBr-Leaf-MT** — a 23M-parameter transformer trained for information retrieval, ranked #1 on MTEB BEIR for models under 100M parameters. Apache 2.0 licensed.

| Property | Value |
|---|---|
| Parameters | ~23M |
| Architecture | BERT-base (6 layers, 12 heads, 384 hidden dim) |
| Vocabulary | 30,522 tokens (WordPiece, uncased) |
| Max sequence length | 128 tokens |
| ONNX file | `model_quantized.onnx` (~23MB, int8 quantized) |
| Inference runtime | ONNX Runtime via `onnxruntime_go` |
| Hardware | CPU only, no GPU required |

The model is a sentence-transformer with a three-stage pipeline. The ONNX export only contains stage 1 (the transformer). Stages 2 and 3 are implemented in Go:

```
Stage 1: Transformer (ONNX)     [batch, seq, 384]  per-token hidden states
Stage 2: Mean pooling (Go)      [batch, 384]        sentence vector
Stage 3: Dense projection (Go)  [batch, 1024]       final embedding
```

Model files are fetched via `make download-model` from HuggingFace:
- `MongoDB/mdbr-leaf-mt` — quantized ONNX model, vocab, tokenizer config, and projection layer weights (`2_Dense/model.safetensors`)

---

## Pipeline

The embedding pipeline converts text into a 1024-dimensional float32 vector. Each stage is a separate Go file in `internal/engine/embedder/`.

```
Raw text
  │
  ▼
┌─────────────────────────────────────────────────────────────┐
│ TOKENIZE (tokenizer.go)                                     │
│                                                             │
│   "ERROR: connection refused (host=db-primary)"             │
│      ↓ basicTokenize: clean → CJK pad → lowercase          │
│        → strip accents → whitespace split → punctuation split│
│      ↓ wordpiece: greedy longest-prefix subword matching    │
│      ↓ wrap: [CLS] + tokens + [SEP] + [PAD]...             │
│                                                             │
│   Output: input_ids [128], attention_mask [128],            │
│           token_type_ids [128]                              │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│ INFER (onnx.go)                                             │
│                                                             │
│   Three int64 tensors → ONNX Runtime forward pass           │
│   Model: MDBr-Leaf-MT (quantized int8)                     │
│   Threads: 4 intra-op, 1 inter-op                          │
│                                                             │
│   Output: [batch, seq, 384] float32 hidden states           │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│ MEAN POOL (pool.go)                                         │
│                                                             │
│   Average hidden states at non-padding positions:           │
│   pooled[d] = sum(hidden[t][d] for t where mask=1) / count │
│                                                             │
│   All-padding sequences → zero vector (no divide-by-zero)  │
│                                                             │
│   Output: [batch, 384] float32 pooled vectors               │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│ PROJECT (projection.go)                                     │
│                                                             │
│   Linear layer: W ∈ ℝ^{1024×384}, no bias                  │
│   output[i] = Σⱼ W[i,j] · input[j]                        │
│   Weights from safetensors file (1.57MB)                   │
│                                                             │
│   Output: [batch, 1024] float32 final embeddings            │
└─────────────────────────────────────────────────────────────┘
```

### Tokenization detail

The tokenizer reimplements HuggingFace's `BertTokenizer` in pure Go (~250 lines). No CGo, no Rust bindings.

**BasicTokenize** applies five transforms in order:
1. **Clean** — strip control characters (`\x00`, `\xFFFD`, Unicode Cc), normalize whitespace to ASCII space
2. **CJK isolation** — surround CJK Unified Ideographs (U+4E00–U+9FFF and extensions) with spaces so each character becomes its own token
3. **Lowercase** — `strings.ToLower`
4. **Strip accents** — NFD normalize (via `golang.org/x/text/unicode/norm`), then drop all combining marks (Unicode Mn category)
5. **Split** — whitespace split, then punctuation split (ASCII ranges 33–47, 58–64, 91–96, 123–126, plus Unicode Punct)

**WordPiece** decomposes each basic token into vocabulary subwords:
- Greedy left-to-right longest match against the 30,522-entry vocabulary
- Continuation tokens prefixed with `##` (e.g., `"embedding"` → `["em", "##bed", "##ding"]`)
- Tokens longer than 200 runes → `[UNK]`
- Unknown words (no valid decomposition) → `[UNK]`

**Sequence construction:**
```
[CLS] token₁ token₂ ... tokenₙ [SEP] [PAD] [PAD] ...
  101   id₁    id₂  ...  idₙ    102     0     0   ...
```

Truncated to 128 positions. Attention mask is 1 for real tokens, 0 for padding. Token type IDs are all zeros (single-segment).

### Batch tokenization and dynamic padding

`tokenizeBatch` pads to the **longest sequence in the batch**, not the fixed 128 maximum. A batch of 20-token log lines infers on ~22 positions instead of 128, cutting ONNX computation proportionally.

All three output arrays (input_ids, attention_mask, token_type_ids) are packed into contiguous flat slices of shape `[batchSize × seqLen]` for direct tensor construction.

### ONNX session

The ONNX Runtime environment is a process-wide singleton initialized once via `sync.Once`. The session uses `DynamicAdvancedSession` for variable batch sizes.

At session creation, the model is introspected via `ort.GetInputOutputInfo` to dynamically discover tensor names and validate the expected BERT-style signature:
- Inputs: `input_ids`, `attention_mask`, `token_type_ids` (all int64)
- Output: single 3D tensor `[batch, seq, dim]` (float32)
- The embedding dimension (384) is read from the output shape, not hardcoded

Thread configuration: 4 intra-op threads (parallelism within matrix operations), 1 inter-op thread (sequential operator execution for consistent latency).

The shared library (`libonnxruntime.so`) is expected alongside the model in the `models/` directory, sourced from the `onnxruntime_go` module cache during `make download-model`.

### Mean pooling

Attention-mask-weighted average over the sequence dimension. Only non-padding positions contribute:

```
pooled[d] = (1/N) × Σₜ hidden[t][d]   where mask[t] = 1, N = count of mask=1
```

Operates directly on the flat `[batchSize × seqLen × dim]` output buffer using offset arithmetic. No intermediate allocations per token.

### Projection

A single dense linear layer (no bias, identity activation) loaded from a safetensors file.

**Safetensors format** (parsed with `encoding/binary` + `encoding/json`, no third-party library):
```
[8 bytes: LE uint64 header_length]
[header_length bytes: JSON metadata with tensor name, dtype, shape, data_offsets]
[tensor data: raw float32 weights in little-endian byte order]
```

The weight matrix is `[1024, 384]` stored row-major. Projection is a matrix-vector multiply: `out = W · in`.

**Dimension validation at init:** The embedder constructor checks that `session.embedDim == projection.inDim` (both 384) and fails fast on mismatch, preventing silent garbage output if model and projection files are mismatched.

---

## Classification

Classification is a separate component (`internal/engine/classifier/`) that consumes embeddings produced by the embedder. It is not part of the embedder itself, but the two are designed as a unit.

### How it works

At startup, every taxonomy leaf is embedded into the same 1024-dim vector space using the same pipeline. The embedding text for each leaf is `"{RootName}: {LeafDescription}"` — for example, `"ERROR: TCP connection refused, DNS resolution failure, network unreachable, socket connection error"`.

At runtime, each log line is embedded and scored against all pre-embedded labels via cosine similarity:

```
similarity(log, label) = (log · label) / (‖log‖ × ‖label‖)
```

The label with the highest similarity wins. If the best score falls below the confidence threshold (default 0.5), the log is classified as `UNCLASSIFIED`.

### Why this works for logs

Log classification is a **text-to-text matching** problem, the same paradigm as CLIP/SigLIP for images: embed both sides into a shared vector space and compare distances. The model doesn't need to "understand" log formats — it needs to place semantically similar texts (connection errors, deployment events, auth failures) near each other in vector space. MDBr-Leaf-MT is trained for exactly this kind of information retrieval.

The taxonomy descriptions are the primary tuning lever. The embedding model is fixed. The taxonomy structure is fixed. But the description text determines where each label lands in embedding space, so writing semantically rich, unambiguous descriptions directly controls classification quality.

---

## Orchestration

The engine (`internal/engine/engine.go`) wires the components together:

```go
Engine{embedder, taxonomy, classifier, compactor}
```

**Single log processing** (`Process`):
```
RawLog.Raw → embedder.Embed() → classifier.Classify(vec, taxonomy.Labels()) → compactor.Compact() → CanonicalEvent
```

**Batch processing** (`ProcessBatch`):
```
[]RawLog → embedder.EmbedBatch() → per-vector classifier.Classify() → per-log compactor.Compact() → []CanonicalEvent
```

`ProcessBatch` makes a single ONNX inference call for the entire batch, then classifies and compacts each result individually. Classification and compaction are pure CPU operations with negligible cost compared to inference.

The output `CanonicalEvent` splits the label path (e.g., `"ERROR.connection_failure"`) into `Type` ("ERROR") and `Category` ("connection_failure"), and carries the leaf's assigned severity rather than inferring it from the type.

---

## Taxonomy

The taxonomy is a two-level tree: root categories containing leaf labels. Currently 42 leaves across 8 roots:

```
ERROR (9)       — connection_failure, auth_failure, authorization_failure, timeout,
                  runtime_exception, validation_error, out_of_memory, rate_limited,
                  dependency_error
REQUEST (5)     — success, client_error, server_error, redirect, slow_request
DEPLOY (7)      — build_started, build_succeeded, build_failed, deploy_started,
                  deploy_succeeded, deploy_failed, rollback
SYSTEM (5)      — health_check, scaling_event, resource_alert, process_lifecycle,
                  config_change
ACCESS (5)      — login_success, login_failure, session_expired, permission_change,
                  api_key_event
PERFORMANCE (5) — latency_spike, throughput_drop, queue_backlog, cache_event,
                  db_slow_query
DATA (3)        — query_executed, migration, replication
SCHEDULED (3)   — cron_started, cron_completed, cron_failed
```

Each leaf carries a `Desc` (free-text description for embedding) and a `Severity` (one of `error`, `warning`, `info`, `debug`).

Pre-embedding happens once at startup via a single `EmbedBatch` call for all 42 labels (~100–300ms). The resulting `EmbeddedLabel` structs (path + vector + severity) are stored in the `Taxonomy` and passed to the classifier on every `Classify` call.

---

## Data flow summary

```
                    STARTUP                              RUNTIME
                    ───────                              ───────

   TaxonomyNode[]                              RawLog{Raw: "..."}
        │                                            │
        ▼                                            ▼
   Collect 42 leaf                             embedder.Embed()
   descriptions                                      │
        │                                            ▼
        ▼                                     tokenize → infer
   embedder.EmbedBatch()                      → pool → project
        │                                            │
        ▼                                            ▼
   42 × EmbeddedLabel{                        [1024]float32 vector
     Path, Vector, Severity                          │
   }                                                 ▼
        │                                    classifier.Classify(
        └──────────────────────────────────►   vec, labels)
                                                     │
                                                     ▼
                                              Result{Label, Confidence}
                                                     │
                                                     ▼
                                              compactor.Compact()
                                                     │
                                                     ▼
                                              CanonicalEvent{
                                                Type, Category, Severity,
                                                Timestamp, Summary,
                                                Confidence, Raw
                                              }
```

---

## File layout

```
internal/engine/
├── embedder/
│   ├── embedder.go        Embedder interface, ONNXEmbedder struct, Embed/EmbedBatch
│   ├── onnx.go            ONNX Runtime session: init, infer, tensor management
│   ├── tokenizer.go       BERT WordPiece tokenizer, batch packing, dynamic padding
│   ├── vocab.go           Vocabulary loader (vocab.txt → bidirectional maps)
│   ├── pool.go            Attention-mask-weighted mean pooling
│   └── projection.go      Safetensors loader, dense linear projection (384→1024)
├── classifier/
│   └── classifier.go      Cosine similarity scoring, confidence thresholding
├── taxonomy/
│   ├── default.go         42-leaf taxonomy tree with descriptions and severities
│   └── taxonomy.go        Pre-embedding orchestration, label storage
├── compactor/
│   └── compactor.go       Verbosity-based truncation (200/2000/unlimited), summarization
└── engine.go              Pipeline orchestration: embed → classify → compact

internal/model/
├── rawlog.go              RawLog{Timestamp, Source, Raw, Metadata}
├── event.go               CanonicalEvent{Type, Category, Severity, ...}
└── taxonomy.go            TaxonomyNode{Name, Children, Desc, Severity}
                           EmbeddedLabel{Path, Vector, Severity}

models/                    (gitignored, fetched via make download-model)
├── model_quantized.onnx
├── model_quantized.onnx_data
├── vocab.txt
├── tokenizer_config.json
├── libonnxruntime.so
└── 2_Dense/
    ├── model.safetensors
    └── config.json
```

---

## Key constants

| Constant | Value | Location |
|---|---|---|
| Max sequence length | 128 tokens | `tokenizer.go:maxSeqLen` |
| Vocabulary size | 30,522 | `vocab.txt` |
| ONNX hidden dimension | 384 | Discovered from model at init |
| Final embedding dimension | 1024 | `projection.outDim` |
| Projection weight shape | [1024, 384] | `2_Dense/model.safetensors` |
| Special tokens | [PAD]=0, [UNK]=100, [CLS]=101, [SEP]=102 | `vocab.go` |
| Max WordPiece token length | 200 runes | `tokenizer.go:wordpieceToken` |
| Intra-op threads | 4 | `onnx.go:newONNXSession` |
| Inter-op threads | 1 | `onnx.go:newONNXSession` |
| Default confidence threshold | 0.5 | `config.go` |
| Summary length | 120 chars | `compactor.go:summarize` |
| Minimal truncation | 200 chars | `compactor.go` |
| Standard truncation | 2,000 chars | `compactor.go` |

---

## Dependencies

The embedding engine adds exactly two dependencies to the Go module:

- **`github.com/yalue/onnxruntime_go`** — Go bindings for ONNX Runtime. Provides `DynamicAdvancedSession`, tensor creation, model introspection. Links to `libonnxruntime.so` at runtime.
- **`golang.org/x/text`** — Unicode normalization (`norm.NFD`) for accent stripping in the tokenizer.

Everything else is standard library: `encoding/binary` and `encoding/json` for safetensors parsing, `math` for cosine similarity, `sync` for the ORT singleton, `strings` and `unicode` for text processing.

---

## Design decisions

**Pure-Go tokenizer over CGo bindings.** WordPiece is simple enough (~250 lines) that a native Go implementation avoids the complexity of binding HuggingFace's Rust `tokenizers` library. The vocabulary is 30K entries — map lookup is fast. The tokenizer has been validated against HuggingFace `BertTokenizer` reference output.

**Pure-stdlib safetensors parsing.** The safetensors binary format is straightforward: 8-byte header length, JSON metadata, raw float data. Two stdlib packages handle it. No third-party dependency needed.

**Dynamic tensor discovery over hardcoded names.** The ONNX session introspects the model to find input/output tensor names and dimensions rather than hardcoding them. This means the embedder would work with a different BERT-style model swapped into the same path (provided it has the same input signature).

**Dimension validation at construction.** The embedder constructor checks that the ONNX model's output dimension matches the projection layer's input dimension. If someone downloads a mismatched model and projection, it fails immediately with a clear error instead of silently producing garbage vectors.

**Batch padding to longest sequence, not fixed 128.** For typical log lines (20–40 tokens), this cuts ONNX computation by 3–6× compared to always padding to 128. The cost is a few extra lines in `tokenizeBatch`.

**Embedding text `"{Root}: {Desc}"` for taxonomy labels.** The root name provides category context and the description provides semantic content. The dotted path (`ERROR.connection_failure`) is a code identifier and would embed poorly. This format gives the model the best signal for placing labels in meaningful regions of the vector space.

**Severity on leaves, not inferred from type.** A naive mapping of `ERROR → "error"` fails for cases like `DEPLOY.build_failed` (should be "error", not "info"). Each leaf carries its own severity, set explicitly in the taxonomy definition.

**Single `EmbedBatch` call for taxonomy initialization.** One ONNX inference pass for all 42 labels rather than 42 individual calls. Startup cost is ~100–300ms.
