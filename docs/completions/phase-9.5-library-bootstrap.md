# Phase 9.5: Library Bootstrap & Versioning

**Completed:** 2026-04-02

**Scope:** Implemented auto-download for model files and ONNX Runtime when Lumber is used as a Go library dependency. After this phase: `go get github.com/kaminocorp/lumber` + `lumber.New(lumber.WithAutoDownload())` works out of the box — model files are fetched on first use, cached locally, and reused across runs. Version bumped to 0.9.0 for semver tagging.

**Plan:** `docs/executing/phase-9.5-library-bootstrap.md`

---

## What Was Done

### 1. Cache Directory Resolution

**New file:** `pkg/lumber/cache.go`

**Problem:** Library consumers need a standard, predictable location for cached model files. Without a convention, every consumer invents their own path (`/opt/lumber/models`, `./models`, etc.), with no shared cache across applications.

**Implementation:** Single function `defaultCacheDir()` with a two-level resolution chain:

1. `$LUMBER_CACHE_DIR` environment variable — explicit override for CI, Docker, or custom layouts
2. `os.UserCacheDir()` + `/lumber` — Go stdlib, returns `~/Library/Caches/lumber` on macOS, `$XDG_CACHE_HOME/lumber` or `~/.cache/lumber` on Linux

Cache layout mirrors the existing model directory structure that `resolvePaths()` expects:

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

**Why `os.UserCacheDir()` not `os.UserConfigDir()`:** Model files are a cache (derivable, re-downloadable), not configuration. Cache dirs can be wiped without data loss, which matches the semantics — next `New()` call simply re-downloads.

---

### 2. Model + ORT Downloader

**New file:** `pkg/lumber/download.go`

**Problem:** `go get` only fetches Go source. Unlike `pip` (Python) or `build.rs` (Rust), Go modules have no post-install hooks. Library consumers must acquire ~35-60MB of binary assets (ONNX model + runtime) through out-of-band steps. Every new consumer rediscovers and replicates this setup.

**Implementation:** Two top-level functions:

#### `downloadModels(destDir string) error`

Downloads 5 model files from HuggingFace (`MongoDB/mdbr-leaf-mt`):

| File | Size | SHA256 (first 16 chars) |
|------|------|------------------------|
| `model_quantized.onnx` | ~220KB (stub, data in separate file) | `2a3541f3f156bc42...` |
| `model_quantized.onnx_data` | ~23MB | `65dc11dae54946d5...` |
| `vocab.txt` | ~230KB | `07eced375cec144d...` |
| `2_Dense/model.safetensors` | ~1.6MB | `dfe95933b7511...` |
| `2_Dense/config.json` | <1KB | `5d4010b4ce5194...` |

For each file:
1. Check if file exists and SHA256 matches → skip (no network call)
2. If missing or corrupt → download via `net/http.Get` (inherits `HTTP_PROXY`/`HTTPS_PROXY`)
3. Write to temp file (`.lumber-download-*`) in same directory
4. Compute SHA256 while writing via `io.MultiWriter(file, hasher)` — single-pass, no re-read
5. Verify checksum → atomic `os.Rename` to final path

SHA256 checksums are embedded as constants in the `modelFiles` slice. Sourced from HuggingFace's `paths-info` API (LFS files) and direct download + `shasum` (non-LFS files).

#### `downloadORT(destDir string) error`

Downloads the platform-specific ONNX Runtime shared library from Microsoft's GitHub Releases.

Platform detection via `runtime.GOOS` + `runtime.GOARCH`:

| Platform | Archive | Installed as |
|----------|---------|-------------|
| `linux-amd64` | `onnxruntime-linux-x64-1.24.1.tgz` | `libonnxruntime.so` |
| `linux-arm64` | `onnxruntime-linux-aarch64-1.24.1.tgz` | `libonnxruntime.so` |
| `darwin-arm64` | `onnxruntime-osx-arm64-1.24.1.tgz` | `libonnxruntime.dylib` |

Unsupported platforms get a clear error: `"auto-download not supported on {GOOS}/{GOARCH} — use WithModelDir() with manually downloaded files"`.

**ORT extraction logic (`downloadAndExtractORT`):** The ORT `.tgz` archive contains headers, static libs, and other files we don't need. Extraction streams the response body through `gzip.NewReader` → `tar.NewReader` and selectively extracts only the versioned shared library by matching:
- Path prefix `{archiveName}/lib/`
- Filename prefix `libonnxruntime`
- Not a symlink or directory
- File size > 1MB (the real library is 8-35MB; skips small metadata files)

The extracted library is written via `atomicWriteFromReader` — temp file + rename pattern, same as model files.

**Why separate from model download:** Model files come from HuggingFace, ORT comes from Microsoft GitHub Releases. Different sources, different archive formats (raw files vs `.tgz`), different platform concerns (models are platform-agnostic, ORT is per-OS/arch).

**Concurrency safety:** Multiple goroutines or processes calling `New(WithAutoDownload())` simultaneously race harmlessly. The temp-file + atomic-rename pattern means only complete files are ever visible at the final path. Worst case: two processes download the same file, but the last `Rename` wins with a valid copy.

---

### 3. New Public API Options

**Modified file:** `pkg/lumber/options.go`

Added two fields to the `options` struct:

```go
type options struct {
    // ... existing fields ...
    autoDownload bool
    cacheDir     string
}
```

Two new `Option` functions:

#### `WithAutoDownload() Option`

Enables automatic model and ORT download on first `New()` call. Opt-in only — default behavior is unchanged. Downloads ~35-60MB to the OS cache directory, cached for subsequent calls.

#### `WithCacheDir(dir string) Option`

Overrides the default cache directory. Only relevant when `WithAutoDownload` is used.

**Precedence chain** (unchanged from plan):

```
WithModelPaths > WithModelDir > WithAutoDownload > error ("no model files")
```

The auto-download guard in `New()` checks `o.modelDir == "" && o.modelPath == ""` — if either explicit option was set, auto-download is skipped entirely.

---

### 4. Wiring Auto-Download into `New()`

**Modified file:** `pkg/lumber/lumber.go`

Added an auto-download block at the top of `New()`, between option parsing and `resolvePaths()`:

```go
// Auto-download models + ORT if requested and no explicit paths provided.
if o.autoDownload && o.modelDir == "" && o.modelPath == "" {
    cacheDir := o.cacheDir
    if cacheDir == "" {
        var err error
        cacheDir, err = defaultCacheDir()
        if err != nil {
            return nil, fmt.Errorf("lumber: %w", err)
        }
    }
    if err := os.MkdirAll(cacheDir, 0o755); err != nil {
        return nil, fmt.Errorf("lumber: creating cache dir: %w", err)
    }
    if err := downloadModels(cacheDir); err != nil {
        return nil, fmt.Errorf("lumber: %w", err)
    }
    if err := downloadORT(cacheDir); err != nil {
        return nil, fmt.Errorf("lumber: %w", err)
    }
    o.modelDir = cacheDir
}
```

**Key detail:** After successful download, `o.modelDir` is set to the cache directory. This means `resolvePaths(o)` — which is called next — resolves model/vocab/projection paths relative to the cache dir, using the exact same path-joining logic as manual `WithModelDir`. Zero special-casing downstream.

Added `"os"` to imports for `os.MkdirAll`.

---

### 5. Version Bump

**Modified file:** `internal/config/config.go`

```go
// Before:
var Version = "0.8.1"

// After:
var Version = "0.9.0"
```

This aligns the default version with the planned semver tag. When `git tag -a v0.9.0` is created and the release workflow runs, the build injects the same version via `-ldflags`.

---

### 6. Documentation Updates

#### `pkg/lumber/doc.go`

Updated the package-level godoc quick-start to show `WithAutoDownload()` as the primary path:

```go
// Quick start (auto-download, recommended for getting started):
//
//   l, err := lumber.New(lumber.WithAutoDownload())
```

Added a note about cache location and a second example for pre-downloaded models (production/Docker).

#### `pkg/lumber/example_test.go`

Added `Example_autoDownload()` — a runnable godoc example gated behind `LUMBER_TEST_AUTODOWNLOAD` env var (since it requires network + ONNX Runtime). Falls back to hardcoded output in CI/test environments without network access, same pattern as the existing `Example()`.

#### `README.md`

Rewrote the "Library Usage" section:
- Version-pinned `go get` command: `go get github.com/kaminocorp/lumber@v0.9.0`
- **Auto-download** subsection (recommended for getting started) showing `WithAutoDownload()`
- **Pre-downloaded models** subsection (recommended for production/Docker) showing `WithModelDir()`
- Existing batch, structured input, and taxonomy introspection examples preserved unchanged

---

## Design Decisions

### Opt-in download, not default

`lumber.New()` without options still expects local model files (defaulting to `./models/`). Auto-download activates only with an explicit `WithAutoDownload()`. Rationale: implicit network calls in a constructor are surprising and can break in air-gapped environments. The consumer must consciously opt in to "fetch files from the internet on first run."

### SHA256 embedded in binary, not fetched from remote

Checksums are hardcoded constants in `download.go`, not fetched from HuggingFace at runtime. This means:
- No TOCTOU between "check remote checksum" and "download file"
- Works offline if cache is populated
- Model version is pinned to the Lumber version — updating models requires a Lumber version bump

Trade-off: updating the model requires updating checksums in code and releasing a new Lumber version. This is intentional — it prevents silent model drift and keeps classification deterministic per Lumber version.

### Streaming ORT extraction

The ORT `.tgz` is ~8-35MB. Rather than downloading to disk, decompressing, and then extracting, we stream: `http.Get` → `gzip.NewReader` → `tar.NewReader` → `atomicWriteFromReader`. This uses constant memory regardless of archive size and avoids temporary files for the archive itself.

### `io.MultiWriter` for hash-while-write

Computing SHA256 during download (not after) eliminates a second full read of the file. For the 23MB `model_quantized.onnx_data`, this saves ~23MB of disk I/O.

### No ORT checksum verification

Unlike model files, the ORT library doesn't have an embedded SHA256 check. Rationale: the ORT archive URL includes the exact version, and corrupted downloads would fail at `ort.InitializeEnvironment()` with a clear error. Adding checksums would require maintaining a per-platform, per-version checksum table — complexity not justified by the risk.

---

## Files Created (3)

| File | Lines | Purpose |
|------|-------|---------|
| `pkg/lumber/cache.go` | 22 | Cache directory resolution (`LUMBER_CACHE_DIR` > `os.UserCacheDir()/lumber`) |
| `pkg/lumber/download.go` | 279 | Model + ORT downloader: checksum verification, atomic writes, tar extraction |
| `pkg/lumber/download_test.go` | 189 | 10 tests covering all download behaviors |

## Files Modified (5)

| File | What Changed |
|------|-------------|
| `pkg/lumber/options.go` | Added `autoDownload`, `cacheDir` fields; `WithAutoDownload()`, `WithCacheDir()` option funcs |
| `pkg/lumber/lumber.go` | Auto-download block in `New()` before `resolvePaths`; added `"os"` import |
| `internal/config/config.go` | Version `"0.8.1"` → `"0.9.0"` |
| `pkg/lumber/doc.go` | Quick-start updated to show `WithAutoDownload()` as primary path |
| `pkg/lumber/example_test.go` | Added `Example_autoDownload()` |
| `README.md` | Library section rewritten with auto-download + pre-downloaded model paths |

**New files: 3. Modified files: 6. Total: 9.**

## Tests Added

| File | Tests | What |
|------|-------|------|
| `pkg/lumber/download_test.go` | `TestDefaultCacheDir` | `LUMBER_CACHE_DIR` override and OS fallback |
| | `TestFileValid` | Non-existent, no-checksum, matching, mismatched |
| | `TestDownloadFile` | Happy path with checksum verification via httptest |
| | `TestDownloadFile_ChecksumMismatch` | Corrupt download rejected, temp file cleaned up |
| | `TestDownloadFile_HTTPError` | HTTP 404 returns error |
| | `TestDownloadFile_SkipsIfCached` | Valid cached file not re-downloaded |
| | `TestDownloadFile_SubdirectoryCreated` | `os.MkdirAll` for nested paths (e.g., `2_Dense/`) |
| | `TestDownloadFile_CorruptCacheRedownloaded` | Corrupt file detected and replaced |
| | `TestOrtPlatform` | Platform detection matches current `GOOS`/`GOARCH` |
| | `TestAtomicWriteFromReader` | Temp file + rename produces correct output |

All tests use `httptest.NewServer` for download tests (no real network calls). ORT platform test adapts to the host platform.

## Verification

```
go build ./...   — clean (0 errors)
go vet ./...     — clean (0 warnings)
go test ./...    — 22 packages pass (0 failures)
```

## What's Left (Deferred Per Plan)

- **Semver tag** — `git tag -a v0.9.0 -m "..."` + `git push origin v0.9.0`. Manual step, ready to execute.
- **Auto-update mechanism** — checking for newer model versions. v1 uses pinned checksums.
- **Download progress callback** — `WithProgressFunc(func(downloaded, total int64))` for UI consumers.
- **ORT system-level detection** — checking `ldconfig`/`pkg-config` before downloading a private copy.
- **Windows support** — ORT platform matrix doesn't include Windows.
