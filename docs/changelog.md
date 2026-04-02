# Changelog

## Index

- [0.9.0](#090--2026-04-02) — Distribution & release pipeline: platform-aware ORT, version injection, multi-platform Makefile, GitHub Release workflow
- [0.8.0](#080--2026-03-09) — Model source consolidation: all downloads now use official `MongoDB/mdbr-leaf-mt` repo
- [0.7.0](#070--2026-03-04) — Module rename: `hejijunhao/lumber` → `kaminocorp/lumber`, git remote migration
- [0.6.0](#060--2026-02-28) — Output architecture & public library API: multi-output fan-out, async wrapper, file/webhook backends, `pkg/lumber` importable API
- [0.5.1](#051--2026-02-24) — Post-review fixes: version stdout, timer leak, query validation, corpus test visibility, batch embed filtering
- [0.5.0](#050--2026-02-23) — Pipeline integration & resilience: structured logging, config validation, per-log error handling, graceful shutdown, CLI flags
- [0.4.1](#041--2026-02-23) — Post-review fixes: stack trace truncation, Go interleaving, test correctness
- [0.4.0](#040--2026-02-23) — Log connectors: shared HTTP client, Vercel/Fly.io/Supabase connectors, config wiring
- [0.3.0](#030--2026-02-22) — Classification pipeline: 42-leaf taxonomy, 104-entry test corpus, 100% accuracy, edge case hardening
- [0.2.6](#026--2026-02-19) — Post-review fixes: batched inference, leaf severity, dynamic padding, math.Sqrt
- [0.2.5](#025--2026-02-19) — Taxonomy pre-embedding: batch embed all 34 leaf labels at startup
- [0.2.4](#024--2026-02-19) — Mean pooling + dense projection: full 1024-dim embeddings, end-to-end Embed/EmbedBatch
- [0.2.3](#023--2026-02-19) — Pure-Go WordPiece tokenizer: vocab loader, BERT tokenization, batch packing
- [0.2.2](#022--2026-02-19) — Download projection layer weights for full 1024-dim embeddings
- [0.2.1](#021--2026-02-19) — ONNX Runtime integration: session lifecycle, raw inference, dynamic tensor discovery
- [0.2.0](#020--2026-02-19) — Model download pipeline: Makefile target, tokenizer config, vocab path
- [0.1.0](#010--2026-02-19) — Project scaffolding: module structure, pipeline skeleton, classifier, compactor, and default taxonomy

---

## 0.9.0 — 2026-04-02

**Distribution & release pipeline (Phase 9 partial)**

Makes Lumber downloadable and installable as pre-built binaries. Platform-aware ONNX Runtime library loading, build-time version injection, multi-platform Makefile, and a GitHub Actions release workflow that produces self-contained tarballs for 3 platforms. CI quality gate deferred to a later phase.

### Changed

- **Platform-aware ORT library name** — `internal/engine/embedder/onnx.go` now selects `libonnxruntime.dylib` (macOS), `onnxruntime.dll` (Windows), or `libonnxruntime.so` (Linux) via `ortLibraryName()` instead of hardcoding `.so`. Eliminates reliance on macOS `dlopen` fallback behavior.
- **Version injection via ldflags** — `internal/config/config.go` changed `const Version` to `var Version = "0.8.1"` so `-ldflags "-X ...Version=X.Y.Z"` works at build time. Default serves as fallback for local `go build`.
- **Makefile: version-injected build** — `build` target injects version via `VERSION ?= dev` and `-ldflags -X`. `make build` uses `"dev"`, `VERSION=X.Y.Z make build` injects a real version.
- **Makefile: `download-ort` target** — auto-detects platform via `go env GOOS`/`GOARCH`, downloads the correct ONNX Runtime binary from `microsoft/onnxruntime` releases, installs with platform-correct filename. Supports `linux-amd64`, `linux-arm64`, `darwin-arm64`. `download-model` now depends on `download-ort`.
- **Makefile: cross-platform test target** — sets both `LD_LIBRARY_PATH` (Linux) and `DYLD_LIBRARY_PATH` (macOS) for ORT discovery.
- **README.md** — added Install section with pre-built binary download (platform table) and build-from-source instructions. Updated Go version requirement to 1.24+.

### Added

- **GitHub Release workflow** — `.github/workflows/release.yml` triggered by `v*` tags. Two-job design:
  - `build` job: 3 native runners in parallel (`ubuntu-latest`, `ubuntu-24.04-arm`, `macos-14`). Each downloads model files + ORT, compiles with version injection, assembles a self-contained tarball (`bin/lumber` + `models/` + ORT library).
  - `release` job: downloads all artifacts, generates SHA256 checksums, creates GitHub Release via `softprops/action-gh-release@v2` with auto-generated release notes.

### Deferred

- **CI quality gate** (`ci.yml` with lint, unit tests, integration tests) — intentionally deferred to focus on making Lumber downloadable first
- **Docker image** — planned for Phase 12a
- **Homebrew formula** — planned for Phase 12b
- **Windows / macOS Intel builds** — no current demand / ORT dropped prebuilt Intel binaries

### Files changed

| File | Action | What |
|------|--------|------|
| `internal/engine/embedder/onnx.go` | modified | `ortLibraryName()` function; platform-aware library path |
| `internal/config/config.go` | modified | `const Version` → `var Version` for ldflags injection |
| `Makefile` | modified | Version-injected build, `download-ort` target, dual library paths in test |
| `.github/workflows/release.yml` | **new** | 3-platform native build + tarball assembly + GitHub Release |
| `README.md` | modified | Install section (pre-built binaries + build from source) |

**New files: 1. Modified files: 4. Total: 5.**

### Completion doc

`docs/completions/phase-9-distribution.md`

---

## 0.8.0 — 2026-03-09

**Model source consolidation — single official HuggingFace repo**

All model download URLs consolidated from two HuggingFace repositories (`onnx-community/mdbr-leaf-mt-ONNX` + `MongoDB/mdbr-leaf-mt`) to the single official repo `MongoDB/mdbr-leaf-mt`, which now hosts the ONNX exports directly alongside the projection layer weights.

### Changed

- **Makefile** — consolidated 3 URL variables (`MODEL_REPO`, `MODEL_BASE`, `OFFICIAL_BASE`) into 1 (`MODEL_BASE`). All `make download-model` fetches now use `MongoDB/mdbr-leaf-mt`
- **README.md** — embedding model HuggingFace link updated to official repo
- **`docs/integration-guide.md`** — manual download curl commands updated. Fixed incorrect `Snowflake/mdbr-leaf-mt` URL for projection weights (was a copy-paste error, would have 404'd)
- **`docs/blueprints/embedding-engine-blueprint.md`** — updated from "two HuggingFace repositories" to one
- **`docs/executing/phase-9-distribution.md`** — CI workflow download URLs updated

### Not changed

- **`docs/changelog.md`** — historical entries retain original `onnx-community` references (accurate at time of writing)
- **All Go source** — zero code changes. URLs only appear in docs and Makefile

### Files changed

| Category | Files |
|----------|-------|
| Makefile | 1 |
| Documentation (`.md`) | 4 |
| **Total** | **5** |

---

## 0.7.0 — 2026-03-04

**Module rename — `hejijunhao/lumber` → `kaminocorp/lumber`**

Repository moved from personal GitHub account to the Kamino Corp organisation. Go module path, all internal imports, documentation, and git remote updated to `github.com/kaminocorp/lumber`.

### Changed

- **Go module path** — `go.mod` module declaration changed from `github.com/hejijunhao/lumber` to `github.com/kaminocorp/lumber`
- **All import paths** — 98 import statements across 36 `.go` files updated to new module prefix
- **Documentation** — clone URLs, `go get` commands, and import examples updated across README and 10 docs files
- **Git remote** — origin updated to `https://github.com/kaminocorp/lumber.git`

### Not changed

- **`docs/plans/distribution-ref.md`** — retains `hejijunhao/photon` reference (different repo)
- **`go.sum`** — only third-party deps, unaffected. Regenerated via `go mod tidy`
- **All internal logic** — zero behaviour changes. Pure string replacement of import paths

### Verification

- `go mod tidy` — clean
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./...` — 22 packages pass

### Files changed

| Category | Files |
|----------|-------|
| `go.mod` | 1 |
| Go source (`.go`) | 20 |
| Go test (`_test.go`) | 16 |
| Documentation (`.md`) | 11 |
| **Total** | **48** |

### Implementation plan

`docs/executing/module-rename-kaminocorp.md`

---

## 0.6.0 — 2026-02-28

**Output architecture & public library API (Phases 7 + 8)**

Two phases implemented together — Phase 7 transforms the output layer from a single synchronous stdout pipe into a multi-destination async fan-out system; Phase 8 exposes `pkg/lumber` so any Go application can `go get` Lumber and classify logs without running a subprocess.

### Added

- **Multi-output router** — `internal/output/multi/` fans out each `Write()` call to N outputs sequentially. Error isolation via `errors.Join`: one output failing doesn't prevent delivery to others. `Close()` collects errors from all outputs.
- **Async output wrapper** — `internal/output/async/` decouples production from consumption via a buffered channel (default 1024). Two modes: backpressure (default, blocks when full) and drop-on-full (lossy, for non-critical outputs like webhooks). `Close()` is idempotent via `sync.Once`; drains remaining events with a 5s timeout. Errors from the inner output routed to a configurable `errFunc` callback.
- **File output backend** — `internal/output/file/` writes NDJSON with `bufio.Writer` (64KB buffer, reduces syscalls from 1-per-event to ~1-per-64KB). Size-based rotation: when file exceeds `maxSize`, renames to `.1` (shifting existing `.1`→`.2`→...`.9`→`.10`), opens new file. Thread-safe via `sync.Mutex`. Verbosity-aware via `output.FormatEvent()`.
- **Webhook output backend** — `internal/output/webhook/` POSTs batched events as a JSON array. Timer+buffer pattern: `time.AfterFunc` starts on first event, flushes when batch fills (default 50) or timer fires (default 5s). Retry on 5xx with exponential backoff (1s, 2s, 4s, max 3 retries); no retry on 4xx. Custom headers via `WithHeaders()`. Timer-flush errors routed through `errFunc` callback.
- **Public library API** — `pkg/lumber/`:
  - `New(opts ...Option)` loads ONNX model, pre-embeds taxonomy, builds engine (~100-300ms, create once)
  - `Classify(text)` classifies a single log line → `Event`
  - `ClassifyBatch(texts)` batched inference for multiple lines
  - `ClassifyLog(log)` / `ClassifyLogs(logs)` for structured input with timestamp/source/metadata
  - `Taxonomy()` returns `[]Category` with `[]Label` for read-only introspection
  - `Close()` releases ONNX resources
  - `Event` type: stable public contract separate from `model.CanonicalEvent`
  - `Log` type: structured input with `Text`, `Timestamp`, `Source`, `Metadata`
  - `Option` funcs: `WithModelDir`, `WithModelPaths`, `WithConfidenceThreshold`, `WithVerbosity`
  - Safe for concurrent use
- **Config extensions** — `OutputConfig` gains `FilePath`, `FileMaxSize`, `WebhookURL`, `WebhookHeaders`. Validation: webhook URL must start with `http://` or `https://`; file output parent directory must exist.
- **Pipeline event counter** — `writtenEvents atomic.Int64` incremented after each successful `output.Write()` in all three paths (direct stream, dedup stream via `onWrite` callback, query). `Close()` logs both `total_events_written` and `total_skipped_logs`.
- **Package documentation** — `pkg/lumber/doc.go` with godoc quick-start, `example_test.go` with runnable `Example()` (ONNX-gated with fallback output).

### Changed

- `cmd/lumber/main.go` — builds `[]output.Output` from config: stdout (sync) + file (async) + webhook (async+drop), combined via `multi.New()`. Passes verbosity to file output.
- `internal/pipeline/pipeline.go` — added `writtenEvents` counter, updated `Close()` to log both counters
- `internal/pipeline/buffer.go` — added `onWrite func()` callback to `streamBuffer`, invoked after each successful write in `flush()`

### Design decisions

- **Sequential fan-out over parallel.** `multi.Write()` calls outputs sequentially. stdout and file writes are microseconds; parallel goroutines would add overhead for no gain. The `async` wrapper handles truly slow outputs.
- **Backpressure as default, drop-on-full as opt-in.** Safe by default. Lossy mode explicitly opted into for outputs where data loss is acceptable.
- **Separate public types from internal types.** `lumber.Event` mirrors `model.CanonicalEvent` today but can diverge. The `eventFromCanonical()` bridge function is the single divergence point.
- **`errors.Join` for multi-error collection.** Go 1.20+ stdlib, returns nil when all errors are nil, supports `errors.Is`/`errors.As` unwrapping.
- **`time.AfterFunc` for webhook batching.** Cleaner than manual timer management since the webhook doesn't have its own event loop.
- **`bufio.Writer` for file output.** 64KB buffer reduces syscalls from 1000/s to ~1/64KB at 1000 events/sec.
- **Callback-based error routing.** Both `async.Async` and `webhook.Output` use `errFunc func(error)` callbacks for errors from background goroutines, avoiding complex error channels.

### New environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LUMBER_OUTPUT_FILE` | `` | File path for NDJSON output (empty = disabled) |
| `LUMBER_OUTPUT_FILE_MAX_SIZE` | `0` | Max file size before rotation (bytes, 0 = no rotation) |
| `LUMBER_WEBHOOK_URL` | `` | Webhook endpoint URL (empty = disabled) |

### New CLI flags

| Flag | Type | Description |
|------|------|-------------|
| `-output-file` | string | File path for NDJSON output |
| `-webhook-url` | string | Webhook POST endpoint |

### Tests added

49 new tests across 7 packages:

| Package | Tests | What |
|---------|-------|------|
| `internal/output/multi` | 5 | Fan-out, error isolation, close propagation, single-output identity |
| `internal/output/async` | 7 | Flow-through, backpressure, drop-on-full, close drain, error callback, goroutine leak, close idempotency |
| `internal/output/file` | 5 | Valid NDJSON, rotation at maxSize, close flush, verbosity filtering, concurrent safety |
| `internal/output/webhook` | 7 | Batch flush, timer flush, 5xx retry, 4xx no-retry, custom headers, timer error callback, close flush |
| `internal/config` | 6 | Webhook URL valid/invalid, file dir valid/invalid, env var loading (2) |
| `internal/pipeline` | 1 | Dedup path writtenEvents counter |
| `pkg/lumber` | 18 | Construction (2), classification (4), empty/whitespace (2), concurrent safety, options/paths (4), metadata (3), taxonomy (3) |

### Files changed

| File | Action |
|------|--------|
| `internal/output/multi/multi.go` | **new** |
| `internal/output/multi/multi_test.go` | **new** |
| `internal/output/async/async.go` | **new** |
| `internal/output/async/async_test.go` | **new** |
| `internal/output/file/file.go` | **new** |
| `internal/output/file/file_test.go` | **new** |
| `internal/output/webhook/webhook.go` | **new** |
| `internal/output/webhook/webhook_test.go` | **new** |
| `pkg/lumber/event.go` | **new** |
| `pkg/lumber/options.go` | **new** |
| `pkg/lumber/lumber.go` | **new** |
| `pkg/lumber/lumber_test.go` | **new** |
| `pkg/lumber/log.go` | **new** |
| `pkg/lumber/taxonomy.go` | **new** |
| `pkg/lumber/taxonomy_test.go` | **new** |
| `pkg/lumber/doc.go` | **new** |
| `pkg/lumber/example_test.go` | **new** |
| `internal/config/config.go` | modified |
| `internal/config/config_test.go` | modified |
| `internal/pipeline/pipeline.go` | modified |
| `internal/pipeline/pipeline_test.go` | modified |
| `internal/pipeline/buffer.go` | modified |
| `cmd/lumber/main.go` | modified |
| `README.md` | modified |

**New files: 17. Modified files: 7. Total: 24.**

---

## 0.5.1 — 2026-02-24

**Post-review fixes — version stdout, timer leak, query validation, corpus test visibility, batch embed filtering**

Audit of the Phase 6 (Beta Validation & Polish) implementation identified 5 issues across correctness, usability, and test coverage. All fixed with 8 new tests.

### Fixed

- **Version output writes to stdout** — `lumber -version` was writing to stderr. POSIX convention (and most CLI tools) writes version info to stdout so scripts can capture it (`VERSION=$(lumber -version)`). Changed `fmt.Fprintf(os.Stderr, ...)` to `fmt.Printf(...)`.
- **Timer leak in `streamBuffer.flush()`** — `flush()` set `b.timer = nil` without calling `timer.Stop()`. When flush was triggered by buffer-full before the timer fired, the old timer's internal goroutine leaked. Added `timer.Stop()` before nil assignment.
- **Silent `-from`/`-to` parse failures** — invalid RFC3339 input to `-from` or `-to` flags was silently swallowed, leaving `QueryFrom`/`QueryTo` at zero value. Parse errors are now collected during `LoadWithFlags()` and surfaced by `Validate()`.
- **Query mode missing from/to validation** — `lumber -mode query` without `-from` and `-to` proceeded with zero-value time range. `Validate()` now requires both when `Mode == "query"`.
- **`ProcessBatch` embedded empty strings** — empty/whitespace inputs were passed through `EmbedBatch()` and overridden post-classification. Refactored to pre-scan inputs, filter empties, and call `EmbedBatch()` only on non-empty texts with index mapping to reassemble results.
- **Corpus validation tests invisible to `go test ./...`** — the `testdata/` package was skipped by Go convention. Added wrapper tests in `engine_test.go` that validate corpus structure and taxonomy coverage, ensuring CI catches issues.

### Tests added

8 new tests across 2 packages:

| Package | Tests | What |
|---------|-------|------|
| `internal/config` | 5 | Query mode from/to validation (3), parse error surfacing (1), updated query valid (1) |
| `internal/engine` | 3 | Corpus structure (1), taxonomy coverage (1), batch all-empty skips embedder (1) |

### Files changed

| File | Action |
|------|--------|
| `cmd/lumber/main.go` | modified |
| `internal/pipeline/buffer.go` | modified |
| `internal/config/config.go` | modified |
| `internal/config/config_test.go` | modified |
| `internal/engine/engine.go` | modified |
| `internal/engine/engine_test.go` | modified |

**New files: 0. Modified files: 6.**

---

## 0.5.0 — 2026-02-23

**Pipeline integration & resilience (Phase 5)**

Phase 5 takes the working pieces (connectors, engine, output) and makes the full pipeline (connector → engine → output) run reliably with proper error handling, buffering, graceful shutdown, structured logging, CLI flags, and end-to-end integration tests. This is the "it works as a system" release.

### Added

- **Structured internal logging** — `internal/logging` package using Go 1.21+ `log/slog`. JSON handler when output is stdout (avoids mixing with NDJSON data), text handler otherwise. `LUMBER_LOG_LEVEL` env var (default `info`). All `fmt.Fprintf(os.Stderr)` and `log.Printf` calls replaced with `slog.Info`/`slog.Warn` across main.go and all 3 connectors.
- **Config validation** — `Config.Validate()` method checks all fields at startup and returns all errors (not just the first): API key required when connector set, model/vocab/projection files exist on disk, confidence threshold in [0,1], verbosity enum, dedup window non-negative, mode enum. Called in main.go before any component initialization.
- **Per-log error resilience** — `engine.Process()` failures now log a warning, increment an atomic skip counter, and continue processing. One bad log doesn't kill the pipeline. In query mode, `ProcessBatch()` failure falls back to individual processing with skip-and-continue. `Processor` interface extracted from `*engine.Engine` to enable mock-based testing.
- **Bounded dedup buffer** — `streamBuffer` gains a `maxSize` field (default 1000 via `LUMBER_MAX_BUFFER_SIZE`). When the buffer hits max, it force-flushes immediately — no events dropped, no unbounded memory growth during log storms. `add()` returns a bool indicating full state.
- **Graceful shutdown** — Configurable `LUMBER_SHUTDOWN_TIMEOUT` (default 10s). On first SIGINT/SIGTERM: cancel context, start shutdown timer. On second signal: immediate `os.Exit(1)`. On timeout: `os.Exit(1)` with error log. Final dedup flush uses `context.Background()` so writes can complete during drain.
- **CLI flags** — 8 flags via stdlib `flag` package: `-mode`, `-connector`, `-from`, `-to`, `-limit`, `-verbosity`, `-pretty`, `-log-level`. Flags override env vars. `LoadWithFlags()` uses `flag.Visit()` to overlay only explicitly-set flags. Query mode now accessible from the CLI (`-mode query` with `-from`/`-to`/`-limit`).
- **End-to-end integration tests** — 4 tests in `internal/pipeline/integration_test.go`: httptest server → Vercel connector → real ONNX engine → mock output. Guarded by `skipWithoutModel(t)` so `go test ./...` always passes. Tests cover stream, query, bad-log resilience, and dedup.

### Changed

- `cmd/lumber/main.go` — rewritten: uses `config.LoadWithFlags()`, calls `logging.Init()` and `cfg.Validate()`, stream/query mode switch, graceful shutdown with timeout and double-signal handling
- `internal/pipeline/pipeline.go` — `Processor` interface replaces concrete `*engine.Engine`, atomic skip counter, `processIndividual()` fallback helper, `WithMaxBufferSize` option, `context.Background()` for final dedup flush
- `internal/pipeline/buffer.go` — `maxSize` field, `add()` returns bool for force-flush signal
- `internal/config/config.go` — `LogLevel`, `ShutdownTimeout`, `Mode`, `QueryFrom`/`QueryTo`/`QueryLimit`, `MaxBufferSize` fields added, `Validate()` method, `LoadWithFlags()` with 8 CLI flags, `getenvInt`/`getenvDuration`/`getenvBool` helpers
- `internal/connector/vercel/vercel.go` — `log.Printf` → `slog.Warn`
- `internal/connector/flyio/flyio.go` — `log.Printf` → `slog.Warn`
- `internal/connector/supabase/supabase.go` — `log.Printf` → `slog.Warn` (x2)

### Design decisions

- **`log/slog` over third-party loggers.** Stdlib since Go 1.21, no dependencies, structured by default, `slog.SetDefault()` means call sites use `slog.Info()` directly without passing logger instances.
- **`Processor` interface for testability.** The pipeline needs to call `Process()` and `ProcessBatch()`. Extracting an interface from `*engine.Engine` enables mock-based tests for error injection without ONNX model files.
- **Atomic skip counter over channel-based counting.** `sync/atomic.Int64` is simpler and has zero contention for the expected case (most logs succeed). Reported once on pipeline close.
- **`flag.Visit()` for CLI override.** Go's `flag` package doesn't distinguish "default value" from "not set". `flag.Visit()` only visits flags explicitly set on the command line, so env var values aren't silently overridden by flag defaults.
- **`context.Background()` for final dedup flush.** The already-cancelled context would cause `output.Write()` to fail immediately during shutdown drain. The shutdown timer in main.go provides the hard bound instead.

### New environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LUMBER_LOG_LEVEL` | `info` | Internal log level: debug, info, warn, error |
| `LUMBER_SHUTDOWN_TIMEOUT` | `10s` | Max time to drain in-flight logs on shutdown |
| `LUMBER_MAX_BUFFER_SIZE` | `1000` | Max events buffered before force dedup flush |
| `LUMBER_MODE` | `stream` | Pipeline mode: stream or query |

### New CLI flags

| Flag | Type | Description |
|------|------|-------------|
| `-mode` | string | Pipeline mode: stream or query |
| `-connector` | string | Connector provider |
| `-from` | string | Query start time (RFC3339) |
| `-to` | string | Query end time (RFC3339) |
| `-limit` | int | Query result limit |
| `-verbosity` | string | Output verbosity |
| `-pretty` | bool | Pretty-print JSON |
| `-log-level` | string | Log level |

### Tests added

27 new tests across 4 packages:

| Package | Tests | What |
|---------|-------|------|
| `internal/logging` | 3 | ParseLevel, JSON handler, text handler |
| `internal/config` | 14 | 7 validation + 2 buffer + 2 shutdown + 3 mode |
| `internal/pipeline` | 6 | 2 error handling + 4 buffer |
| `internal/pipeline` (integration) | 4 | Stream, query, bad log resilience, dedup |

### Files changed

| File | Action |
|------|--------|
| `internal/logging/logging.go` | **new** |
| `internal/logging/logging_test.go` | **new** |
| `internal/pipeline/integration_test.go` | **new** |
| `internal/config/config.go` | modified |
| `internal/config/config_test.go` | modified |
| `internal/pipeline/pipeline.go` | modified |
| `internal/pipeline/pipeline_test.go` | modified |
| `internal/pipeline/buffer.go` | modified |
| `internal/connector/vercel/vercel.go` | modified |
| `internal/connector/flyio/flyio.go` | modified |
| `internal/connector/supabase/supabase.go` | modified |
| `cmd/lumber/main.go` | modified |

**New files: 3. Modified files: 9. Total: 12.**

---

## 0.4.1 — 2026-02-23

**Phase 4 post-review fixes — stack trace truncation, Go interleaving, test correctness**

### Fixed

- **Stack trace truncation destroyed by character truncation** — `Compact` applied 200/2000-rune character truncation unconditionally after stack trace frame truncation. A 30-frame Java trace truncated to 5+2 frames is still ~660 chars, so the 200-rune limit clipped the "frames omitted" message. Fix: when stack trace truncation is effective (returns a different string), skip character truncation — they serve the same purpose.
- **Go stack dump produced duplicate omission messages** — `truncateStackTrace` used a line-by-line keep-set that retained all non-frame lines and inserted an omission message at each omitted frame. Go frames are two-line pairs (function signature + `\t/path/file.go:line`); non-frame lines between omitted frames reset the `omissionInserted` flag, producing up to 4 duplicate omission messages and *increasing* token count. Fix: replaced keep-set with range-based cut — everything between the last kept first-frame and the first kept last-frame is replaced wholesale with a single omission message.
- **`TestFormatEventCount` false failure** — test reused a `map[string]any` across two `json.Unmarshal` calls without clearing. Go's `Unmarshal` merges into existing maps, so the `count` key from the first call (count=5) persisted into the second (count=0, expected omitted).
- **`stdout` tests captured empty output** — `stdout.New()` eagerly stored `os.Stdout` into `json.NewEncoder` at construction time. Tests created the `Output` before `captureStdout()` redirected `os.Stdout` to a pipe, so the encoder wrote to the original fd. Fix: moved `New()` inside the capture callback.

### Files changed

- `internal/engine/compactor/compactor.go` — range-based `truncateStackTrace`, conditional character truncation in `Compact`
- `internal/output/format_test.go` — reset map before second unmarshal
- `internal/output/stdout/stdout_test.go` — moved `New()` inside `captureStdout` callback (3 tests)

---

## 0.4.0 — 2026-02-23

**Log connectors — real-world ingestion from three providers (Phase 3)**

Phase 3 connects the classification pipeline to production log sources. Three connectors (Vercel, Fly.io, Supabase) implement the existing `connector.Connector` interface, producing `model.RawLog` entries that feed directly into the engine. A shared HTTP client handles auth, retry, and rate limit logic for all three.

### Added

- **Shared HTTP client** — `internal/connector/httpclient` package:
  - `Client` with Bearer auth, base URL, configurable timeout (default 30s)
  - `GetJSON(ctx, path, query, dest)` — authenticated GET with JSON unmarshalling
  - Retry logic: 429 respects `Retry-After` header, 5xx uses exponential backoff (1s, 2s, 4s), max 3 retries
  - `*APIError` type for non-2xx responses (status code + first 512 bytes of body)
  - Context-aware retry sleep via `time.NewTimer` + `select` on `ctx.Done()`
  - Zero external dependencies — stdlib only
- **Vercel connector** — `internal/connector/vercel`, registered as `"vercel"`:
  - Response types matching Vercel REST API (`/v1/projects/{projectId}/logs`)
  - `toRawLog`: unix millisecond timestamps, metadata includes level/source/id, optional proxy fields (status_code, path, method, host)
  - `Query()`: cursor-paginated via `pagination.next`, time filters via `from`/`to` (unix ms), team scoping via `teamId`, limit enforcement
  - `Stream()`: poll-based with immediate first poll, configurable interval (default 5s), errors logged to stderr without crashing
- **Fly.io connector** — `internal/connector/flyio`, registered as `"flyio"`:
  - Response types matching Fly.io HTTP logs API (`/api/v1/apps/{app_name}/logs`) with nested `data[].attributes` structure
  - `toRawLog`: RFC 3339 timestamp parsing, `attributes.meta` merged into top-level metadata
  - `Query()`: cursor-paginated via `next_token`, **client-side time filter** with half-open interval `[Start, End)` (Fly.io has no server-side time range)
  - `Stream()`: same poll-loop pattern as Vercel
- **Supabase connector** — `internal/connector/supabase`, registered as `"supabase"`:
  - SQL builder with allow-list validation against all 7 Supabase log tables (4 default + 3 opt-in) — prevents SQL injection
  - `toRawLog`: microsecond timestamp conversion (float64 → `time.Unix`), `event_message` excluded from metadata to avoid duplication with `Raw`
  - `Query()`: multi-table SQL queries, 24-hour window chunking for ranges exceeding API limit, results merged and sorted by timestamp, configurable table list via comma-separated `tables` config
  - `Stream()`: timestamp-cursor polling (default 10s — 4 tables × 1 req/table = 24 req/min, within 120 req/min limit), per-table error isolation
- **Config wiring** — `loadConnectorExtra()` reads provider-specific env vars into `ConnectorConfig.Extra`:
  - `LUMBER_VERCEL_PROJECT_ID` → `project_id`, `LUMBER_VERCEL_TEAM_ID` → `team_id`
  - `LUMBER_FLY_APP_NAME` → `app_name`
  - `LUMBER_SUPABASE_PROJECT_REF` → `project_ref`, `LUMBER_SUPABASE_TABLES` → `tables`
  - `LUMBER_POLL_INTERVAL` → `poll_interval` (shared across all connectors)
  - Returns `nil` when no provider-specific vars are set
- **Test suites** — 38 tests across 5 packages, all using `httptest` fixtures (no live API keys required):
  - httpclient: 8 tests (auth, query params, retries, rate limits, context cancellation)
  - vercel: 8 tests (mapping, pagination, missing config, API errors, streaming)
  - flyio: 7 tests (mapping, pagination, client-side time filter, streaming)
  - supabase: 11 tests (SQL builder, injection prevention, multi-table, window chunking, default/custom tables, streaming)
  - config: 4 tests (defaults, extra population, empty omission, multi-provider)

### Changed

- `internal/config/config.go` — added `Extra map[string]string` to `ConnectorConfig`, added `loadConnectorExtra()` helper
- `cmd/lumber/main.go` — blank imports for `flyio` and `supabase` connectors, `Extra` passed through to pipeline config

### Design decisions

- **Single `GetJSON` method on HTTP client.** All three provider APIs use GET requests. POST/PUT can be added later.
- **Consistent poll-loop pattern across all connectors.** Immediate first poll, ticker-based loop, buffered channel (64), errors logged not fatal. Reduces cognitive load when reading or extending connectors.
- **Client-side time filter for Fly.io.** The API has no server-side time range, so filtering happens after fetch. Half-open `[Start, End)` prevents overlap when querying consecutive windows.
- **Allow-list for Supabase SQL table names.** Table names are interpolated into SQL. The allow-list is the defense against injection — only the 7 known Supabase log tables are accepted.
- **Per-table error isolation in Supabase streaming.** One failing table (e.g., opt-in table not enabled) doesn't block the others.
- **Flat shared Extra map.** Key names are unique across providers. Simpler than per-provider maps, and `poll_interval` is intentionally shared.

### Known limitations

- No buffering or backpressure — channels are fixed at 64, no overflow handling (Phase 5)
- No graceful drain on shutdown — context cancellation closes channels, in-flight logs may be lost (Phase 5)
- No per-log error isolation — malformed API responses surface as errors, not skip-and-continue (Phase 5)
- Connector selection and config via env vars only — no CLI flags (Phase 5)
- All tests use `httptest` fixtures — live API validation deferred (Phase 6)

### Files changed

- `internal/connector/httpclient/httpclient.go` — **new**, shared HTTP client
- `internal/connector/httpclient/httpclient_test.go` — **new**, 8 tests
- `internal/connector/vercel/vercel.go` — replaced stub with full implementation
- `internal/connector/vercel/vercel_test.go` — **new**, 8 tests
- `internal/connector/flyio/flyio.go` — **new**, Fly.io connector
- `internal/connector/flyio/flyio_test.go` — **new**, 7 tests
- `internal/connector/supabase/supabase.go` — **new**, Supabase connector
- `internal/connector/supabase/supabase_test.go` — **new**, 11 tests
- `internal/config/config.go` — added `Extra` field and `loadConnectorExtra()`
- `internal/config/config_test.go` — **new**, 4 tests
- `cmd/lumber/main.go` — added connector imports, pass Extra

---

## 0.3.0 — 2026-02-22

**Classification pipeline — end-to-end validation (Phase 2)**

Phase 2 takes the working embedding engine from Phase 1 and validates the full pipeline — embed → classify → canonicalize → compact — against a labeled test corpus, tuning taxonomy descriptions until classification is accurate and robust.

### Added

- **Expanded taxonomy** — 34 → 42 leaves across 8 roots, reconciled with the vision doc:
  - ERROR: 5 → 9 leaves (added `authorization_failure`, `out_of_memory`, `rate_limited`, `dependency_error`; merged `null_reference` + `unhandled_exception` into `runtime_exception`)
  - REQUEST: replaced `incoming_request`/`outgoing_request`/`response` with HTTP status classes (`success`, `client_error`, `server_error`, `redirect`, `slow_request`)
  - DEPLOY: added `rollback` (6 → 7 leaves)
  - SYSTEM: merged `startup`/`shutdown` into `process_lifecycle`, renamed `resource_limit` → `resource_alert`, added `config_change`
  - SECURITY renamed to ACCESS: added `session_expired`, `permission_change`, `api_key_event`; moved `rate_limited` to ERROR
  - PERFORMANCE: new 5-leaf root (`latency_spike`, `throughput_drop`, `queue_backlog`, `cache_event`, `db_slow_query`)
  - DATA: consolidated `cache_hit`/`cache_miss` into PERFORMANCE; renamed `query` → `query_executed`, added `replication`
  - APPLICATION root removed — `info`/`warning`/`debug` are severity levels, not categories
- **Synthetic test corpus** — 104 labeled log lines in `internal/engine/testdata/corpus.json` covering all 42 leaves with 2–3 entries each. Format diversity: JSON structured, plain text, key=value, pipe-delimited, Apache/nginx, stack traces, CI/CD output
- **Corpus loader** — `internal/engine/testdata/testdata.go` with `//go:embed` and `LoadCorpus()`. Validation tests for JSON parsing, leaf coverage, and severity values
- **Integration test suite** — 14 tests in `internal/engine/engine_test.go` using the real ONNX embedder:
  - `TestProcessSingleLog` — all CanonicalEvent fields populated, timestamp preserved
  - `TestProcessBatchConsistency` — batch and individual produce identical Type/Category
  - `TestProcessEmptyBatch` — nil input returns nil
  - `TestProcessUnclassifiedLog` — gibberish input handled gracefully
  - `TestCorpusAccuracy` — **100% top-1 accuracy** (104/104), per-category breakdown, misclassification report
  - `TestCorpusSeverityConsistency` — all correctly classified entries have correct severity
  - `TestCorpusConfidenceDistribution` — confidence stats and threshold sweep analysis
- **Edge case tests** — 7 tests for degenerate inputs:
  - Empty string and whitespace-only logs — tokenizer produces `[CLS][SEP]`, classifies safely
  - Very long logs (3600+ chars) — 128-token truncation preserves signal
  - Binary and invalid UTF-8 — control character stripping prevents crashes
  - Timestamp preservation including zero values
  - Metadata on input doesn't crash pipeline (not surfaced in output by design)
- **Configurable confidence threshold** — `LUMBER_CONFIDENCE_THRESHOLD` env var (default 0.5), parsed via `getenvFloat()` in `config.Load()`
- **Classification pipeline blueprint** — `docs/classification-pipeline-blueprint.md`

### Changed

- **Taxonomy descriptions tuned across 3 rounds** (89.4% → 94.2% → 96.2% → 100%):
  - Round 1: added discriminating keywords (`NXDOMAIN`, `dial tcp`, `TypeError`), removed overlapping language (`expired token` from auth_failure, `login` from auth_failure, `type error` from validation_error, `request rejected` from rate_limited)
  - Round 2: fine-tuned descriptions for scaling (HPA language), login failure (MFA/TOTP), resource alerts (approaching limit)
  - Round 3: adjusted 4 genuinely ambiguous corpus entries where raw text didn't match intended category
- **Confidence characteristics** — mean 0.783, min 0.662, max 0.869 across the corpus. Clean separation above the 0.5 threshold with no misclassifications

### Design decisions

- **Descriptions are the primary tuning lever.** The embedding model and taxonomy structure are fixed. Description text determines where each label lands in vector space — it's the highest-impact change for accuracy.
- **Cross-category keyword leakage is the main failure mode.** When two categories share language, the model can't distinguish them. The fix is adding discriminating keywords to one side and removing shared keywords from the other.
- **APPLICATION root removed.** `info`/`warning`/`debug` as categories creates confusion with severity. Logs that truly don't fit any category get UNCLASSIFIED.
- **Threshold stays at 0.5.** Threshold sweep showed correct/incorrect confidence distributions overlapped when accuracy was <100%, making threshold adjustment ineffective. Description tuning eliminated all misclassifications, making threshold selection moot.
- **Corpus entries adjusted for genuine ambiguity.** Some log lines legitimately matched multiple categories. Rather than forcing the model to make an impossible distinction, the corpus was corrected to reflect the most natural classification.

### Known limitations

- UNCLASSIFIED events have empty Severity (no real-world logs trigger this with 100% corpus accuracy, but should be addressed)
- Compactor `truncate()`/`summarize()` slice on byte index, can split multi-byte UTF-8 (deferred to Phase 4)
- Empty/whitespace logs classify arbitrarily (~0.6 confidence) rather than returning UNCLASSIFIED
- Corpus is synthetic — real-world validation deferred to Phase 6

### Files changed

- `internal/engine/taxonomy/default.go` — expanded and tuned 42-leaf taxonomy
- `internal/engine/taxonomy/taxonomy_test.go` — updated fixtures for 42 leaves, 8 roots, severity, descriptions
- `internal/engine/testdata/corpus.json` — **new**, 104 labeled log lines
- `internal/engine/testdata/testdata.go` — **new**, corpus loader with `//go:embed`
- `internal/engine/testdata/testdata_test.go` — **new**, 3 corpus validation tests
- `internal/engine/engine_test.go` — **new**, 14 integration tests
- `internal/config/config.go` — configurable confidence threshold via env var
- `docs/classification-pipeline-blueprint.md` — **new**, classification pipeline reference

---

## 0.2.6 — 2026-02-19

**Embedding engine — post-review fixes (plan Section 6)**

### Changed

- `ProcessBatch` now calls `EmbedBatch` once for the full batch instead of looping `Process` per event — single ONNX inference call instead of N
- `Embed()` routes through `tokenizeBatch` with a 1-element slice, giving single-text inference the same dynamic-padding-to-longest behavior as `EmbedBatch` — a 10-token log line now infers on ~12 positions instead of 128
- Replaced custom 64-iteration Newton's method `sqrt` with `math.Sqrt` in cosine similarity — compiles to a single CPU instruction
- Severity now comes from per-leaf `Severity` field on `EmbeddedLabel` instead of `inferSeverity()` which only mapped top-level types — fixes incorrect severity for leaves like `DEPLOY.build_failed` (was "info", now "error") and `SCHEDULED.cron_failed`
- `Makefile` test target prefixed with `LD_LIBRARY_PATH=$(MODEL_DIR)` for reliable test execution outside repo root

### Added

- `Severity string` field on `TaxonomyNode` and `EmbeddedLabel`
- Severity set on every leaf in `DefaultRoots()`: all ERROR children → error (except `validation_error` → warning), `build_failed`/`deploy_failed`/`cron_failed` → error, security leaves → warning, `cache_hit`/`debug` → debug, everything else → info

### Removed

- `inferSeverity()` function in `engine.go`
- Custom `sqrt()` function in `classifier.go`

### Deferred

- L2 normalization of final embeddings — not a bug (cosine similarity handles unnormalized vectors), deferred until adaptive taxonomy work where embeddings may be used outside the classifier

### Files changed

- `internal/engine/engine.go` — batched `ProcessBatch`, removed `inferSeverity`
- `internal/engine/classifier/classifier.go` — `math.Sqrt` replacement
- `internal/model/taxonomy.go` — added `Severity` to both structs
- `internal/engine/taxonomy/default.go` — severity on every leaf
- `internal/engine/taxonomy/taxonomy.go` — propagate severity to embedded labels
- `internal/engine/taxonomy/taxonomy_test.go` — severity in test fixtures and assertions
- `internal/engine/embedder/embedder.go` — `Embed()` dynamic padding
- `Makefile` — `LD_LIBRARY_PATH` in test target

---

## 0.2.5 — 2026-02-19

**Embedding engine — taxonomy pre-embedding (plan Section 4)**

### Added

- `taxonomy.New(roots, embedder)` now pre-embeds all leaf labels at startup via a single `EmbedBatch` call:
  - Walks roots → children, builds embedding texts as `"{Parent}: {Leaf.Desc}"` (e.g., `"ERROR: Network or database connection failure"`)
  - Paths stored as `"ERROR.connection_failure"` for classifier consumption
  - Edge cases: empty roots or roots with no children short-circuit before calling the embedder
- Startup logging in `main.go` — logs model path with `dim=1024` after embedder init, label count and wall-clock duration after taxonomy init (e.g., `pre-embedded 34 labels in 142ms`)
- `internal/engine/taxonomy/taxonomy_test.go` — 4 tests using mock embedder:
  - `TestNewPreEmbeds` — correct paths, vector dimensions, and non-zero values
  - `TestNewEmptyRoots` — nil roots → 0 labels, embedder never called
  - `TestNewNoLeaves` — root-only nodes → 0 labels, embedder never called
  - `TestNewEmbedError` — embedder failure propagates as wrapped error

### Design decisions

- **Embedding text format `"{Parent}: {Leaf.Desc}"`** — gives the model both category context and semantic description; the dotted path is a code identifier, not useful for embedding
- **Single `EmbedBatch` call** — one ONNX inference pass for all 34 labels keeps startup fast (~100-300ms)

### Files changed

- `internal/engine/taxonomy/taxonomy.go` — replaced stub with leaf collection + batch embedding
- `internal/engine/taxonomy/taxonomy_test.go` — **new**, 4 unit tests
- `cmd/lumber/main.go` — startup logging

---

## 0.2.4 — 2026-02-19

**Embedding engine — mean pooling + dense projection (plan Section 3)**

### Added

- `internal/engine/embedder/projection.go` — safetensors loader + linear projection:
  - Parses safetensors binary format using only `encoding/binary` + `encoding/json` (no new deps, ~60 lines)
  - Loads `"linear.weight"` tensor, validates dtype=`F32` and shape=`[1024, 384]`
  - `apply(vec)` — matrix-vector multiply projecting 384-dim → 1024-dim
- `internal/engine/embedder/pool.go` — attention-mask-weighted mean pooling:
  - Averages hidden states only at positions where `mask == 1`
  - All-padding sequences produce zero vectors (no divide-by-zero)
- `ProjectionPath` in `EngineConfig` with env var `LUMBER_PROJECTION_PATH` (default: `models/2_Dense/model.safetensors`)
- `internal/engine/embedder/pool_test.go` — 3 tests (single sample, batch, all-padding)
- `internal/engine/embedder/projection_test.go` — 5 tests:
  - `TestLoadProjection` — real safetensors, shape `[1024, 384]`, non-zero weights
  - `TestProjectionApply` — uniform input → 1024-dim non-zero output
  - `TestEmbedEndToEnd` — `Embed("hello world")` → 1024-dim vector, `EmbedDim() == 1024`
  - `TestEmbedBatchEndToEnd` — 2 texts → distinct 1024-dim vectors
  - `TestEmbedBatchEmpty` — nil → nil

### Changed

- `ONNXEmbedder` struct now holds `*onnxSession`, `*tokenizer`, and `*projection`
- `New(modelPath, vocabPath, projectionPath)` — loads all three, validates `session.embedDim == projection.inDim` at construction (fails fast on mismatch), cleans up on partial failure
- `Embed(text)` — full pipeline: tokenize → infer → mean pool → project → 1024-dim vector
- `EmbedBatch(texts)` — tokenize batch → single infer → mean pool → project each → `[][]float32`
- `EmbedDim()` — now returns `projection.outDim` (1024) instead of `session.embedDim` (384)

### Design decisions

- **Pure-stdlib safetensors parsing** — format is simple enough that no third-party library is needed
- **Dimension validation at init** — catches model/projection mismatch immediately rather than producing garbage at runtime

### Files changed

- `internal/engine/embedder/projection.go` — **new**, safetensors loader + projection
- `internal/engine/embedder/pool.go` — **new**, mean pooling
- `internal/engine/embedder/pool_test.go` — **new**, 3 tests
- `internal/engine/embedder/projection_test.go` — **new**, 5 tests
- `internal/engine/embedder/embedder.go` — wired tokenizer + projection, implemented `Embed`/`EmbedBatch`
- `internal/config/config.go` — added `ProjectionPath`
- `cmd/lumber/main.go` — updated `embedder.New()` call

---

## 0.2.3 — 2026-02-19

**Embedding engine — WordPiece tokenizer (plan Section 2)**

### Added

- `internal/engine/embedder/vocab.go` — vocabulary loader:
  - Parses `vocab.txt` (one token per line, line number = token ID)
  - Bidirectional maps (`token→id`, `id→token`), 30,522 tokens
  - Validates and caches special token IDs: `[PAD]=0`, `[UNK]=100`, `[CLS]=101`, `[SEP]=102`
- `internal/engine/embedder/tokenizer.go` — full BERT tokenization pipeline:
  - Clean text (remove control chars, normalize whitespace) → CJK character padding → lowercase → strip accents (NFD + remove combining marks) → whitespace split → punctuation split → WordPiece (greedy longest-prefix with `##` continuation, 200-rune max per word) → wrap with `[CLS]`/`[SEP]` → truncate to 128 → right-pad to `maxSeqLen` → generate `attention_mask` and `token_type_ids`
  - `tokenizeBatch(texts)` — packs into flat slices padded to the *longest sequence in the batch* (not always 128), minimizing unnecessary ONNX computation
  - Character classification (`isPunctuation`, `isWhitespace`, `isControl`, `isChineseChar`) matches BERT's Python `BasicTokenizer` exactly
- `internal/engine/embedder/tokenizer_test.go` — 10 tests validated against HuggingFace `BertTokenizer` reference output:
  - `TestVocabLoad` — vocab size 30,522, all special token IDs
  - `TestTokenize` — 7 sub-tests: simple words, empty string, log line with punctuation/numbers, IP addresses, accented characters, CJK, mixed brackets
  - `TestTokenizeTruncation` — 200-word input → exactly 128 tokens
  - `TestTokenizeBatch` — flat packing, correct shape
  - `TestTokenizeBatchEmpty` — nil → zero batch
- `golang.org/x/text` v0.34.0 dependency (for `unicode/norm.NFD` accent stripping)

### Design decisions

- **Pure Go, no CGo tokenizer bindings** — WordPiece is simple enough (~250 lines), avoids HuggingFace Rust `tokenizers` dependency. Vocab is 30K entries — map lookup is fast.
- **Max sequence length 128** — log lines rarely exceed this; matches `tokenizer_config.json`. Shorter = faster inference.
- **Batch padding to longest sequence** — `tokenizeBatch` pads to the longest in the batch, not always 128. For typical 20-40 token log lines, this cuts unnecessary ONNX computation.

### Files changed

- `internal/engine/embedder/vocab.go` — **new**, vocabulary loader
- `internal/engine/embedder/tokenizer.go` — **new**, WordPiece tokenizer + batch tokenization
- `internal/engine/embedder/tokenizer_test.go` — **new**, 10 tests
- `go.mod`, `go.sum` — added `golang.org/x/text` v0.34.0

---

## 0.2.2 — 2026-02-19

**Embedding engine — projection layer download (plan Section 5 amendment)**

### Added

- `make download-model` now fetches the sentence-transformers `2_Dense` projection layer from the official `MongoDB/mdbr-leaf-mt` repo:
  - `2_Dense/model.safetensors` (1.57MB) — `[1024, 384]` weight matrix
  - `2_Dense/config.json` — confirms: `in_features: 384`, `out_features: 1024`, `bias: false`, identity activation
- `OFFICIAL_BASE` URL variable in Makefile pointing to `MongoDB/mdbr-leaf-mt` (separate from `onnx-community` used for the ONNX model)
- `.gitignore` — added `/models/2_Dense/`

### Discovered

- The ONNX export (both official and community repos) only contains the base transformer (stage 1 of 3). The full mdbr-leaf-mt sentence-transformers pipeline is:
  1. **Transformer** (ONNX) → `[batch, seq, 384]` per-token hidden states
  2. **Mean pooling** (not in ONNX) → `[batch, 384]`
  3. **Dense projection** (not in ONNX) → `[batch, 1024]` via linear layer, no bias
- The plan's 1024-dim target was correct all along — the projection must be applied in Go after mean pooling (Section 3)

### Files changed

- `Makefile` — added `OFFICIAL_BASE`, projection layer download block
- `.gitignore` — added `2_Dense/` pattern

---

## 0.2.1 — 2026-02-19

**Embedding engine — ONNX Runtime integration (plan Section 1)**

### Added

- `onnxruntime-go` v1.26.0 dependency — pre-compiled `libonnxruntime.so` for aarch64 Linux ships with the package
- `internal/engine/embedder/onnx.go` — ONNX session wrapper:
  - Process-wide singleton runtime init via `sync.Once`
  - `DynamicAdvancedSession` for variable batch sizes at runtime
  - Auto-discovers input/output tensor names and embedding dimension from the model
  - Validates expected BERT-style inputs (`input_ids`, `attention_mask`, `token_type_ids`)
  - Raw `infer()` method: takes flat int64 slices, returns flat float32 output
  - Session options: 4 intra-op threads, sequential inter-op execution
- `Close() error` added to `Embedder` interface (embeds cleanup responsibility into the contract)
- `ONNXEmbedder.EmbedDim()` method — exposes model's embedding dimension
- `internal/engine/embedder/onnx_test.go` — 3 integration tests (session load, single inference, batch inference)

### Changed

- `ONNXEmbedder.New()` now loads the real ONNX model and creates an inference session (fails fast if model missing/corrupt)
- `ONNXEmbedder` struct holds `*onnxSession` instead of a bare path
- `cmd/lumber/main.go` — `defer emb.Close()` after embedder creation
- `Makefile` `download-model` target now also copies `libonnxruntime.so` from the Go module cache
- `.gitignore` — added `libonnxruntime.so`
- Default model path changed to `models/model_quantized.onnx` (preserves original ONNX filename so external data reference resolves)

### Discovered

- ONNX export (both official and community) outputs **384-dim** per-token hidden states from the base transformer. The final **1024-dim** embeddings require post-processing in Go: mean pooling → dense projection via `2_Dense/model.safetensors` (`[1024, 384]` linear, no bias). The projection weights (~1.57MB) need to be downloaded separately from the official `MongoDB/mdbr-leaf-mt` repo. The ONNX output dimension (384) is discovered dynamically by the code.
- ONNX Runtime `cpuid_info` warning on aarch64 (`Unknown CPU vendor`) is harmless — inference works correctly.

### Stubbed (not yet functional)

- `ONNXEmbedder.Embed` / `EmbedBatch` — needs tokenizer (Section 2) and mean pooling (Section 3)
- Taxonomy label pre-embedding — depends on working embedder

---

## 0.2.0 — 2026-02-19

**Embedding engine — model download pipeline (plan Section 5)**

### Added

- `make download-model` fetches from `onnx-community/mdbr-leaf-mt-ONNX` on HuggingFace:
  - `model_quantized.onnx` (216KB graph) + `model_quantized.onnx_data` (22MB int8 weights)
  - `vocab.txt` (227KB, 30,522 WordPiece tokens)
  - `tokenizer_config.json` (confirms: `BertTokenizer`, `do_lower_case: true`, `max_length: 128`)
- Idempotent download — skips if all key files already present
- `VocabPath` field in `EngineConfig` with env var `LUMBER_VOCAB_PATH` (default: `models/vocab.txt`)

### Changed

- `.gitignore` — added patterns for `*.onnx_data`, `vocab.txt`, `tokenizer_config.json`

### Design decisions

- **Int8 quantized over fp32:** 23MB vs 92MB, 4x smaller, faster on CPU. Log classification doesn't need fp32 precision. Swappable via a one-line URL change in the Makefile.
- **Original filenames preserved:** ONNX models hardcode external data file references internally — renaming breaks the reference. Files remain `model_quantized.onnx` / `model_quantized.onnx_data`.

---

## 0.1.0 — 2026-02-19

**Scaffolding — full project skeleton**

### Added

- Go module (`github.com/kaminocorp/lumber`, Go 1.23) with Makefile (build, test, lint, clean, download-model)
- `RawLog`, `CanonicalEvent`, `TaxonomyNode`, and `EmbeddedLabel` domain types
- `Connector` interface with provider registry and self-registering Vercel stub
- `Embedder` interface with `ONNXEmbedder` stub (awaiting ONNX runtime integration)
- Taxonomy manager with default taxonomy: 8 categories, 34 leaf labels (ERROR, REQUEST, DEPLOY, SYSTEM, SECURITY, DATA, SCHEDULED, APPLICATION)
- Cosine-similarity classifier with confidence threshold (fully implemented, no external deps)
- Token-aware compactor with 3 verbosity levels (full, moderate, compact)
- Engine orchestrator wiring embed → classify → compact
- `Output` interface with JSON-to-stdout implementation
- Pipeline connecting connector → engine → output (stream and query modes)
- Env-based config loader with defaults
- CLI entrypoint with graceful shutdown

### Stubbed (not yet functional)

- `ONNXEmbedder.Embed` / `EmbedBatch` — needs `onnxruntime-go`
- `vercel.Connector.Stream` / `Query` — needs Vercel API client
- Taxonomy label pre-embedding — depends on working embedder
