# Scaffolding Implementation — Completion Notes

**Plan:** `docs/plans/scaffolding-implementation.md`
**Status:** Complete
**Date:** 2026-02-19

---

## What was built

All 20 files from the scaffolding plan were created. The project compiles (`go build ./...`) and passes `go vet` with zero issues.

### Files created

| Path | Purpose |
|------|---------|
| `.gitignore` | Ignores binaries, ONNX models, IDE files |
| `go.mod` | Module `github.com/kaminocorp/lumber`, Go 1.23 |
| `Makefile` | build, test, lint, clean, download-model targets |
| `models/.gitkeep` | Placeholder for ONNX model files |
| `internal/model/rawlog.go` | `RawLog` struct |
| `internal/model/event.go` | `CanonicalEvent` struct |
| `internal/model/taxonomy.go` | `TaxonomyNode` and `EmbeddedLabel` types |
| `internal/connector/connector.go` | `Connector` interface, `ConnectorConfig`, `QueryParams` |
| `internal/connector/registry.go` | Provider name → constructor registry |
| `internal/connector/vercel/vercel.go` | Vercel stub (returns `errNotImplemented`) |
| `internal/engine/embedder/embedder.go` | `Embedder` interface + `ONNXEmbedder` stub |
| `internal/engine/taxonomy/taxonomy.go` | Taxonomy manager (label pre-embedding, lookup) |
| `internal/engine/taxonomy/default.go` | Default taxonomy: 8 top-level categories, 34 leaf labels |
| `internal/engine/classifier/classifier.go` | Cosine similarity classifier with confidence threshold |
| `internal/engine/compactor/compactor.go` | Token-aware compaction with 3 verbosity levels |
| `internal/engine/engine.go` | Engine orchestrator: embed → classify → compact |
| `internal/output/output.go` | `Output` interface |
| `internal/output/stdout/stdout.go` | JSON-to-stdout output implementation |
| `internal/pipeline/pipeline.go` | Pipeline: connector → engine → output (stream + query modes) |
| `internal/config/config.go` | Env-based config loading with defaults |
| `cmd/lumber/main.go` | Binary entrypoint with graceful shutdown |

---

## Implementation decisions

- **Classifier has real logic.** `cosineSimilarity` is fully implemented (not stubbed) since it's pure math with no dependencies. Uses a Newton's method `sqrt` to avoid importing `math` — this will be swapped for `math.Sqrt` when the package grows.
- **Compactor has real logic.** Basic truncation and summarization are implemented against the three verbosity levels. Deduplication (counted summaries) is deferred to when we have real log streams to test against.
- **Vercel connector uses `init()` for self-registration.** The blank import in `main.go` (`_ "...vercel"`) triggers registration. New connectors follow the same pattern.
- **Default taxonomy ships 34 leaf labels** across 8 categories (ERROR, REQUEST, DEPLOY, SYSTEM, SECURITY, DATA, SCHEDULED, APPLICATION). This is slightly below the ~40-50 target from the vision doc — easy to expand once we see real classification results.
- **No external dependencies yet.** The entire scaffold uses only the standard library. `onnxruntime-go` will be the first external dep when the embedder is implemented.

---

## What's stubbed (returns `errNotImplemented` or no-ops)

- `embedder.ONNXEmbedder.Embed` / `EmbedBatch` — needs ONNX runtime integration
- `connector/vercel.Connector.Stream` / `Query` — needs Vercel API implementation
- `taxonomy.New` — skips pre-embedding (depends on working embedder)

---

## Next steps

1. **Embedder implementation** — integrate `onnxruntime-go`, add tokenizer, wire up model loading
2. **Taxonomy pre-embedding** — once embedder works, pre-embed all 34 leaf labels at startup
3. **Vercel connector** — implement REST API client for log drain / historical query
4. **Tests** — unit tests for classifier (cosine sim), compactor, registry, pipeline wiring
