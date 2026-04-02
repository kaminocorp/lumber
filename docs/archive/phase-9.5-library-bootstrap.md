# Phase 9.5: Library Bootstrap & Versioning

**Goal:** Make Lumber usable as a Go library dependency without manual model setup. After this phase: `go get github.com/kaminocorp/lumber` + `lumber.New(lumber.WithAutoDownload())` works out of the box — model files are fetched on first use, cached locally, and reused across runs. Proper semver tags give consumers a stable versioning contract.

**Scope:** This phase targets the **library embedding path only** (`pkg/lumber` imported via `go get`). The standalone CLI binary is already solved — Phase 9 release tarballs bundle all model files and the ONNX Runtime library, and `make download-model` handles the build-from-source path. The problem addressed here is specific to Go applications that `import "github.com/kaminocorp/lumber/pkg/lumber"` and call `lumber.New()` programmatically.

**Why the CLI doesn't have this problem:** `go get` only fetches Go source code. Unlike `pip` (Python) or `build.rs` (Rust), Go modules have no post-install hooks and no mechanism for bundling binary assets. So while the CLI user gets a self-contained tarball with everything included, the library consumer gets Go code with no model files and no ONNX Runtime — they must acquire those through out-of-band steps.

**Starting point:** Phase 9 (distribution) shipped release tarballs for the standalone binary. The public library API (`pkg/lumber`) works, but requires consumers to pre-download ~25MB of model files and the ONNX Runtime shared library through out-of-band steps (Dockerfile curl stages, manual download, or copying from the Lumber repo's Makefile). Every new consumer rediscovers and replicates this setup. The repo has no git tags — Go generates pseudo-versions from commit hashes.

**Problem observed in practice:** Heimdall (an internal consumer) integrated Lumber by pinning to a pseudo-version (`v0.0.0-20260304033652-4f6b6e878057`) and manually curling 6 model files + the ORT library in a Dockerfile build stage. This works in Docker but breaks in local dev (no model files → `New()` returns an error, forcing the consumer to implement their own fallback/passthrough logic). Every future library consumer would face the same onboarding friction.

---

## What Changes and Why

### Current gaps (library path only — CLI/binary path is solved by Phase 9)

1. **No model self-bootstrap** — `lumber.New()` expects pre-existing model files on disk. There is no programmatic way to acquire them. Every library integrator must copy download logic from the Makefile or invent their own (as Heimdall did in its Dockerfile). The CLI binary path doesn't have this problem — `make download-model` and release tarballs handle it.
2. **No standard cache location** — Each library consumer chooses where to put model files (`/opt/lumber/models`, `./models`, etc.). No convention, no shared cache across applications on the same machine.
3. **No ONNX Runtime self-bootstrap** — The shared library (`libonnxruntime.so`/`.dylib`) must also be pre-installed by the library consumer. The CLI release tarballs bundle it; library consumers are on their own.
4. **No semver tags** — Consumers pin to opaque commit hashes via pseudo-versions. No stability contract, no changelog anchoring, no `go get github.com/kaminocorp/lumber@v0.9.0`. This affects both CLI (build-from-source) and library consumers.
5. **No graceful degradation option** — If model files are missing, `New()` fails hard. There's no way for a library consumer to say "classify if you can, pass through if you can't" without wrapping Lumber in their own fallback layer.

### Design constraints

- Download must be **opt-in**, not default. `lumber.New(lumber.WithModelDir("./models"))` must continue to work exactly as today for consumers who manage their own files. Auto-download only activates with an explicit option.
- Downloads happen **once** per cache directory. Subsequent calls skip download if files exist and checksums match.
- Must work behind corporate proxies (standard `HTTP_PROXY`/`HTTPS_PROXY` env vars via Go's default `http.Transport`).
- Must not add heavy dependencies. `net/http` + `os` + `crypto/sha256` from stdlib are sufficient.
- ONNX Runtime download is **separate** from model download — they come from different sources (HuggingFace vs Microsoft GitHub Releases) and have different platform concerns.
- Cache directory follows OS conventions: `$XDG_CACHE_HOME/lumber` on Linux, `~/Library/Caches/lumber` on macOS, `%LOCALAPPDATA%\lumber` on Windows. Fallback: `~/.cache/lumber`.

### What auto-download fetches

| File | Source | Size | Purpose |
|------|--------|------|---------|
| `model_quantized.onnx` | HuggingFace `MongoDB/mdbr-leaf-mt` | ~23MB | ONNX model |
| `model_quantized.onnx_data` | HuggingFace `MongoDB/mdbr-leaf-mt` | ~1MB | External tensor data |
| `vocab.txt` | HuggingFace `MongoDB/mdbr-leaf-mt` | ~230KB | WordPiece vocabulary |
| `2_Dense/model.safetensors` | HuggingFace `MongoDB/mdbr-leaf-mt` | ~1.6MB | Projection layer weights |
| `2_Dense/config.json` | HuggingFace `MongoDB/mdbr-leaf-mt` | <1KB | Projection config |
| `libonnxruntime.{so,dylib}` | Microsoft GitHub Releases | ~8-35MB | ONNX Runtime shared library |

Total first-download: ~35-60MB depending on platform. Cached after first run.

---

## Implementation Plan

### Section 1: Cache Directory Resolution

**New file:** `pkg/lumber/cache.go`

Determine the platform-appropriate cache directory for model files. This is a pure utility — no downloads, no I/O.

```go
// defaultCacheDir returns the platform-appropriate cache directory.
// Precedence: $LUMBER_CACHE_DIR > OS-specific cache dir > ~/.cache/lumber
func defaultCacheDir() (string, error)
```

Resolution order:
1. `$LUMBER_CACHE_DIR` environment variable (explicit override)
2. `os.UserCacheDir()` + `/lumber` (Go stdlib, returns `~/Library/Caches` on macOS, `$XDG_CACHE_HOME` or `~/.cache` on Linux)
3. Error if home directory cannot be determined

The cache layout mirrors the existing model directory structure:

```
~/.cache/lumber/
├── model_quantized.onnx
├── model_quantized.onnx_data
├── vocab.txt
├── 2_Dense/
│   ├── model.safetensors
│   └── config.json
└── libonnxruntime.{so,dylib}
```

**Files:**

| File | Action |
|------|--------|
| `pkg/lumber/cache.go` | new |

---

### Section 2: Model Downloader

**New file:** `pkg/lumber/download.go`

Downloads model files from HuggingFace and the ONNX Runtime library from Microsoft's GitHub Releases. Designed to be called once — checks for existing files before downloading.

```go
// downloadModels downloads model files to destDir if they don't already exist.
// Uses SHA256 checksums embedded in the binary to verify integrity.
// Returns nil if all files already exist and pass checksum verification.
func downloadModels(destDir string) error

// downloadORT downloads the platform-specific ONNX Runtime library to destDir
// if it doesn't already exist.
func downloadORT(destDir string) error
```

**Download behavior:**
- Check if each file exists and matches expected SHA256 checksum
- If all files present and valid, return immediately (no network calls)
- If any file missing or corrupt, download all missing files
- Write to `<destDir>/.tmp-<random>` first, then atomic rename to final path (prevents partial files from being used if download is interrupted)
- Use `net/http.DefaultClient` (inherits proxy settings from environment)
- Log progress via `log/slog` (the same structured logger Lumber already uses)

**Checksum verification:**
- SHA256 checksums for the current model version are embedded as constants in `download.go`
- Checksums are verified after download and on subsequent cache hits
- If a cached file fails checksum (corrupt or stale), re-download it

**ORT platform detection reuses the same logic as the Makefile:**

| `runtime.GOOS` + `runtime.GOARCH` | ORT Archive | Installed As |
|------------------------------------|-------------|-------------|
| `linux` + `amd64` | `onnxruntime-linux-x64-{version}.tgz` | `libonnxruntime.so` |
| `linux` + `arm64` | `onnxruntime-linux-aarch64-{version}.tgz` | `libonnxruntime.so` |
| `darwin` + `arm64` | `onnxruntime-osx-arm64-{version}.tgz` | `libonnxruntime.dylib` |

Unsupported platforms return a clear error: `"lumber: auto-download not supported on {GOOS}/{GOARCH} — use WithModelDir() with manually downloaded files"`.

**Error handling:** All download errors are wrapped with context (`fmt.Errorf("lumber: downloading %s: %w", filename, err)`). Network errors, HTTP non-200 responses, checksum mismatches, and disk write failures all surface as actionable error messages.

**Files:**

| File | Action |
|------|--------|
| `pkg/lumber/download.go` | new |
| `pkg/lumber/download_test.go` | new |

---

### Section 3: New Options for `pkg/lumber`

**File:** `pkg/lumber/options.go`

Add two new options to the public API:

```go
// WithAutoDownload enables automatic model and ONNX Runtime download.
// On first call, downloads ~35-60MB of files to the OS cache directory
// (or the directory specified by WithCacheDir). Subsequent calls reuse
// the cached files. Requires network access on first run only.
func WithAutoDownload() Option

// WithCacheDir overrides the default cache directory for auto-downloaded files.
// Only relevant when WithAutoDownload is used. Default: ~/.cache/lumber (Linux),
// ~/Library/Caches/lumber (macOS).
func WithCacheDir(dir string) Option
```

**Interaction with existing options:**
- `WithAutoDownload()` alone → download to default cache dir, use cached files
- `WithAutoDownload()` + `WithCacheDir("/tmp/lumber")` → download to custom dir
- `WithModelDir("./models")` alone → use pre-existing files (existing behavior, unchanged)
- `WithModelDir("./models")` + `WithAutoDownload()` → `WithModelDir` takes precedence, no download (explicit path wins over auto)
- `WithModelPaths(...)` + `WithAutoDownload()` → `WithModelPaths` takes precedence, no download

The precedence chain: `WithModelPaths` > `WithModelDir` > `WithAutoDownload` > error ("no model files found").

**Files:**

| File | Action |
|------|--------|
| `pkg/lumber/options.go` | modified — add `WithAutoDownload`, `WithCacheDir`, update `options` struct |

---

### Section 4: Wire Auto-Download into `New()`

**File:** `pkg/lumber/lumber.go`

Modify `New()` to invoke the downloader when `WithAutoDownload` is set and no explicit model path is provided.

```go
func New(opts ...Option) (*Lumber, error) {
    o := defaultOptions()
    for _, opt := range opts {
        opt(&o)
    }

    // New: auto-download if requested and no explicit paths provided
    if o.autoDownload && o.modelDir == "" && o.modelPath == "" {
        cacheDir, err := resolveAutoDownloadDir(o)
        if err != nil {
            return nil, fmt.Errorf("lumber: %w", err)
        }
        if err := downloadModels(cacheDir); err != nil {
            return nil, fmt.Errorf("lumber: %w", err)
        }
        if err := downloadORT(cacheDir); err != nil {
            return nil, fmt.Errorf("lumber: %w", err)
        }
        o.modelDir = cacheDir
    }

    modelPath, vocabPath, projPath := resolvePaths(o)
    // ... rest unchanged ...
}
```

**Concurrency safety:** Multiple goroutines (or processes) calling `New(WithAutoDownload())` simultaneously must not corrupt the cache. The atomic-rename download pattern from Section 2 handles this — concurrent downloads of the same file race harmlessly, and the final rename is atomic on both Linux and macOS.

**Files:**

| File | Action |
|------|--------|
| `pkg/lumber/lumber.go` | modified — auto-download block before `resolvePaths` |

---

### Section 5: Semver Tagging Strategy

**No code changes.** This is a process change.

**Current state:** No tags exist. All consumers use pseudo-versions (`v0.0.0-YYYYMMDD-commit`).

**Action:** Tag the current `master` (after this phase lands) as `v0.9.0`. The version signals:
- `v0.x.x` — pre-1.0, breaking changes possible per Go semver convention
- `0.9.0` — close to feature-complete for a v1.0 library API, but reserving room for breaking changes found during real-world integration

**Tagging workflow:**
```bash
git tag -a v0.9.0 -m "v0.9.0: library bootstrap with auto-download, semver tagging"
git push origin v0.9.0
```

This triggers the existing `.github/workflows/release.yml` from Phase 9, producing platform tarballs automatically.

**Going forward:**
- Patch versions (`v0.9.1`) for bug fixes
- Minor versions (`v0.10.0`) for new features
- `v1.0.0` when the public API in `pkg/lumber/` is stable and validated by multiple consumers

**Update `internal/config/config.go`:**
```go
var Version = "0.9.0"
```

**Files:**

| File | Action |
|------|--------|
| `internal/config/config.go` | modified — version bump to `0.9.0` |

---

### Section 6: Documentation & Integration Guide

**Files:** `README.md`, `pkg/lumber/doc.go`, `pkg/lumber/example_test.go`

#### README

Add a "Use as a Go library" section showing both integration paths:

```markdown
## Use as a Go library

    go get github.com/kaminocorp/lumber@v0.9.0

### Auto-download (recommended for getting started)

    l, err := lumber.New(lumber.WithAutoDownload())
    // Downloads ~35-60MB of model files on first call.
    // Cached at ~/.cache/lumber — subsequent calls are instant.

### Pre-downloaded models (recommended for production/Docker)

    l, err := lumber.New(lumber.WithModelDir("/opt/lumber/models"))
    // Use make download-model or Dockerfile curl stage to prepare the directory.
```

#### doc.go

Update the package documentation quick-start to show `WithAutoDownload()` as the primary path.

#### example_test.go

Add a second example function showing auto-download usage (ONNX-gated with fallback, same pattern as existing example).

**Files:**

| File | Action |
|------|--------|
| `README.md` | modified — "Use as a Go library" section |
| `pkg/lumber/doc.go` | modified — updated quick-start |
| `pkg/lumber/example_test.go` | modified — add auto-download example |

---

## Summary

### Files created (2)

| File | Purpose |
|------|---------|
| `pkg/lumber/cache.go` | Cache directory resolution (OS-aware) |
| `pkg/lumber/download.go` | Model + ORT downloader with checksum verification |

### Files modified (6)

| File | Change |
|------|--------|
| `pkg/lumber/options.go` | `WithAutoDownload()`, `WithCacheDir()` options |
| `pkg/lumber/lumber.go` | Auto-download wiring in `New()` |
| `internal/config/config.go` | Version bump to `0.9.0` |
| `README.md` | Library usage section with auto-download |
| `pkg/lumber/doc.go` | Updated quick-start |
| `pkg/lumber/example_test.go` | Auto-download example |

### Tests added

| File | Tests | What |
|------|-------|------|
| `pkg/lumber/download_test.go` | ~8-10 | Cache dir resolution, checksum verification, skip-if-cached, atomic write, platform detection, HTTP error handling (httptest server), corrupt file re-download |

### Implementation order

1. **Section 1** — Cache directory resolution (no deps, pure logic)
2. **Section 2** — Downloader (depends on cache dir)
3. **Section 3** — New options (depends on nothing, can parallel with 1-2)
4. **Section 4** — Wire into `New()` (depends on 1-3)
5. **Section 5** — Version bump + tagging (after code lands)
6. **Section 6** — Documentation (after API finalized)

### Verification

1. `lumber.New(lumber.WithAutoDownload())` on a clean machine → downloads model files to `~/.cache/lumber/`, classifies a log line correctly
2. Second call with same cache → no network requests, instant startup
3. `lumber.New(lumber.WithModelDir("./models"))` → existing behavior unchanged, no download attempted
4. `go get github.com/kaminocorp/lumber@v0.9.0` → resolves cleanly
5. Corrupt a cached file → next `New(WithAutoDownload())` re-downloads it
6. Run without network + empty cache → clear error message

### Deferred

- **Auto-update mechanism** — checking for newer model versions. v1 uses a single pinned model version with embedded checksums. Model updates require a Lumber version bump.
- **Download progress callback** — `WithProgressFunc(func(downloaded, total int64))` for consumers who want to show download progress in their UI. Can be added later without breaking the API.
- **ONNX Runtime system-level install detection** — checking `ldconfig`, `pkg-config`, or `$LD_LIBRARY_PATH` for a system-installed ORT before downloading a private copy. Optimization, not required for correctness.
- **Windows support** — `download-ort` platform matrix doesn't include Windows. Deferred per Phase 9.
