# Phase 9: Distribution & Release Pipeline (Partial)

**Completed:** 2026-04-02

**Scope:** Implemented the distribution and release portions of Phase 9 — platform-aware library naming, version injection, multi-platform Makefile, GitHub Release workflow, and README install section. CI quality gate (lint, unit tests, integration tests) was explicitly deferred to a later phase.

**Plan:** `docs/executing/phase-9-distribution.md`

---

## What Was Done

### 1. Platform-Aware ONNX Runtime Library Name

**File:** `internal/engine/embedder/onnx.go`

**Problem:** Line 41 hardcoded `libonnxruntime.so` regardless of platform. macOS expects `.dylib`, Windows expects `.dll`. It worked on macOS only because `dlopen` happens to accept `.so` as a fallback — a fragile accident that would cause confusion in release tarballs where the library is named correctly per platform.

**Fix:** Added `ortLibraryName()` function that returns the correct library filename based on `runtime.GOOS`:
- `darwin` → `libonnxruntime.dylib`
- `windows` → `onnxruntime.dll`
- default (Linux) → `libonnxruntime.so`

Replaced the hardcoded path:
```go
// Before:
libPath := filepath.Join(modelDir, "libonnxruntime.so")

// After:
libPath := filepath.Join(modelDir, ortLibraryName())
```

**Why this is safe:** `ort.SetSharedLibraryPath()` passes the path to `dlopen()`/`LoadLibrary()`, which handles platform-native extensions. This just makes the default path correct rather than relying on macOS's `.so` fallback behavior.

---

### 2. Version Injection via ldflags

**File:** `internal/config/config.go`

**Problem:** `const Version = "0.6.0"` cannot be overridden at build time. Go's `-ldflags "-X pkg.Var=value"` only works with package-level `var` declarations, not `const`. Release binaries couldn't carry their actual version number.

**Fix:** Changed to:
```go
var Version = "0.8.1"
```

Build command becomes:
```bash
go build -ldflags "-s -w -X github.com/kaminocorp/lumber/internal/config.Version=0.8.1" -o bin/lumber ./cmd/lumber
```

- `-s -w` strips debug info and DWARF symbols (~30% smaller binary)
- `-X` injects the version string at link time
- Default `"0.8.1"` serves as fallback for local `go build` without ldflags

**Verified:** `go build -ldflags "-X ...Version=0.8.1-test"` → `lumber -version` prints `lumber 0.8.1-test`.

---

### 3. Multi-Platform Makefile

**File:** `Makefile`

**Problems solved:**
1. ORT download was ARM64-only, copying from Go module cache (`find $GOMODCACHE ... onnxruntime_arm64.so`). Didn't work on Linux x86_64 or produce correct filenames for macOS.
2. `build` target didn't inject version.
3. `test` target only set `LD_LIBRARY_PATH` (Linux). macOS ignores this; it needs `DYLD_LIBRARY_PATH`.

**Changes:**

**New `download-ort` target** — auto-detects platform via `go env GOOS`/`GOARCH`, downloads the correct ONNX Runtime binary from GitHub releases (`github.com/microsoft/onnxruntime/releases`), and copies it with the correct filename:

| Platform | ORT Archive | Installed As |
|----------|-------------|-------------|
| `linux-amd64` | `onnxruntime-linux-x64-1.24.1.tgz` | `libonnxruntime.so` |
| `linux-arm64` | `onnxruntime-linux-aarch64-1.24.1.tgz` | `libonnxruntime.so` |
| `darwin-arm64` | `onnxruntime-osx-arm64-1.24.1.tgz` | `libonnxruntime.dylib` |

Existence check looks for **both** `.so` and `.dylib` so it doesn't re-download on macOS when the `.dylib` already exists.

**`download-model` now depends on `download-ort`** — one command (`make download-model`) gets everything.

**`build` target injects version:**
```makefile
VERSION ?= dev
build:
    go build -ldflags "-s -w -X github.com/kaminocorp/lumber/internal/config.Version=$(VERSION)" ...
```

`make build` uses `"dev"`. `VERSION=0.8.1 make build` injects a real version.

**`test` target sets both library paths:**
```makefile
test:
    LD_LIBRARY_PATH=$(MODEL_DIR) DYLD_LIBRARY_PATH=$(MODEL_DIR) go test ./...
```

---

### 4. GitHub Release Workflow

**New file:** `.github/workflows/release.yml`

Triggered by pushing a `v*` tag. Two-job design:

**Job 1: `build`** — runs on 3 native GitHub Actions runners in parallel (can't cross-compile due to CGo/ONNX Runtime):

| Runner | Platform | ORT Library |
|--------|----------|-------------|
| `ubuntu-latest` | Linux x86_64 | `libonnxruntime.so` |
| `ubuntu-24.04-arm` | Linux ARM64 | `libonnxruntime.so` |
| `macos-14` | macOS ARM64 | `libonnxruntime.dylib` |

Each runner:
1. Downloads model files from HuggingFace (`MongoDB/mdbr-leaf-mt`)
2. Downloads platform-specific ORT library from `microsoft/onnxruntime` releases
3. Compiles natively with version injection via ldflags
4. Assembles a self-contained tarball:

```
lumber-v0.8.1-linux-amd64/
  bin/lumber                          # Go binary (~15MB)
  models/model_quantized.onnx        # ONNX model (~23MB)
  models/model_quantized.onnx_data   # model external data
  models/vocab.txt                   # tokenizer vocabulary
  models/2_Dense/model.safetensors   # projection weights (~1.6MB)
  models/2_Dense/config.json         # projection config
  models/libonnxruntime.so           # ORT shared library (~8-35MB)
```

**Job 2: `release`** — waits for all 3 builds, downloads all artifacts, generates SHA256 checksums (`sha256sum *.tar.gz > checksums.txt`), creates GitHub Release via `softprops/action-gh-release@v2` with auto-generated release notes.

**Why native builds, not GoReleaser:** GoReleaser relies on `GOOS`/`GOARCH` cross-compilation, which doesn't work with CGo (ONNX Runtime uses `#cgo LDFLAGS: -ldl` and `dlopen()`). GoReleaser also can't bundle different external assets per platform.

---

### 5. README Install Section

**File:** `README.md`

Added an "Install" section above the existing Quickstart with two paths:

1. **Pre-built binaries (recommended)** — download release tarball, extract, run. Links to GitHub Releases. Shows platform table (Linux x64, Linux ARM64, macOS ARM64).
2. **Build from source** — `git clone` → `make download-model` → `make build`. Updated Go version requirement from 1.23+ to 1.24+.

---

## Files Changed

| File | Action | What |
|------|--------|------|
| `internal/engine/embedder/onnx.go` | modified | Added `ortLibraryName()` function; replaced hardcoded `.so` with platform-aware name |
| `internal/config/config.go` | modified | `const Version = "0.6.0"` → `var Version = "0.8.1"` for ldflags injection |
| `Makefile` | modified | Version-injected build, `download-ort` target (auto-detect platform, download from GitHub releases), `DYLD_LIBRARY_PATH` for macOS |
| `.github/workflows/release.yml` | **new** | 3-platform native build + tarball assembly + GitHub Release creation |
| `README.md` | modified | Added Install section (pre-built binaries + build from source) |

**New files: 1. Modified files: 4. Total: 5.**

---

## What Was Deferred

### CI Quality Gate (Section 4 of the plan)

The `.github/workflows/ci.yml` workflow was intentionally skipped. This would have added:
- `lint` job: `gofmt` + `go vet` + `golangci-lint` (ubuntu only)
- `test` job: unit tests on 3 platforms (ONNX tests auto-skip via `skipWithoutModel`)
- `integration-test` job: full test suite with model files on Linux x64
- `build-check` job: compilation verification on 3 platforms

**Reason deferred:** Focus was on making Lumber downloadable/installable first. CI will be added in a future phase.

### Other Phase 9 Deferrals (unchanged from plan)

- **Docker image** — planned for Phase 12a (`docs/executing/phase-12-docker-homebrew.md`)
- **Homebrew formula** — planned for Phase 12b (`docs/executing/phase-12-docker-homebrew.md`)
- **Windows support** — no current demand
- **macOS Intel** — ORT 1.24.x dropped prebuilt binaries

---

## Verification Performed

- [x] `go build ./...` — compiles cleanly
- [x] `go vet ./...` — no issues
- [x] `go build -ldflags "-X ...Version=0.8.1-test"` → `lumber -version` prints `lumber 0.8.1-test`
- [ ] Push `v*` tag → GitHub Release with tarballs (not yet tested — requires push to remote)
- [ ] Download tarball, extract, run → prints version (blocked on above)
