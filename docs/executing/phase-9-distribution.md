# Phase 9: Distribution & CI/CD

**Goal:** Take Lumber from "works on my machine" to automated testing and one-tag releases. After this phase: every push runs a quality gate (lint + test across 3 platforms), and pushing a `v*` tag produces self-contained release tarballs on GitHub — binary + model files + ONNX Runtime library, download-extract-run.

**Starting point:** No CI/CD exists. No `.github/workflows/` directory. The Makefile handles local builds and model downloads but is ARM64-only for ONNX Runtime. The version string is a compile-time constant.

**Reference:** Modeled on the Photon project's distribution setup (`docs/plans/distribution-ref.md`), adapted from Rust/PyPI to Go/GitHub Releases.

---

## What Changes and Why

### Current gaps

1. **No CI** — contributors can push broken code to `master` with no automated feedback. No lint, no test, no build verification.
2. **No release automation** — shipping a binary requires manual compilation on each platform, manual model file assembly, manual GitHub Release creation.
3. **ARM64-only ORT** — the Makefile's `download-model` target hardcodes `onnxruntime_arm64.so` from the Go module cache. Doesn't work on x86_64 Linux or produce correct library names for macOS.
4. **Hardcoded library name** — `internal/engine/embedder/onnx.go` line 41 uses `libonnxruntime.so` regardless of platform. macOS expects `.dylib`, Windows expects `.dll`. (macOS's `dlopen` happens to accept `.so`, but this is a fragile accident.)
5. **Constant version** — `const Version = "0.6.0"` in `config.go` can't be overridden at build time. Release binaries can't have the git tag injected.

### Design constraints

- ONNX Runtime's prebuilt binaries prevent cross-compilation — must use native GitHub Actions runners per platform (same lesson as Photon's 10-version CI saga).
- CGo is required (`onnxruntime-go` uses `#cgo LDFLAGS: -ldl` and `dlopen()`). `CGO_ENABLED=0` is not an option.
- Model files (~25MB) + ONNX Runtime library (~8-35MB depending on platform) must be bundled in release tarballs — user should download, extract, and run with zero additional steps.
- Existing `skipWithoutModel(t)` / `skipIfNoModel(t)` test patterns already support two-tier testing (unit without ONNX, full with ONNX). CI should leverage this.

### Platform matrix

| Platform | GitHub Runner | ORT Archive | Ship? |
|----------|--------------|-------------|-------|
| Linux x86_64 | `ubuntu-latest` | `onnxruntime-linux-x64-1.24.1.tgz` | **Yes** — primary server target |
| Linux ARM64 | `ubuntu-24.04-arm` | `onnxruntime-linux-aarch64-1.24.1.tgz` | **Yes** — ARM servers, Graviton |
| macOS ARM64 | `macos-14` | `onnxruntime-osx-arm64-1.24.1.tgz` | **Yes** — developer laptops |
| macOS Intel | — | Dropped in ORT 1.24.x | **No** — no prebuilt available |
| Windows | — | Available but deferred | **No** — no current demand |

### Why not GoReleaser?

GoReleaser's cross-compilation relies on `GOOS`/`GOARCH` flags, which doesn't work with CGo. It also can't bundle different external assets per platform. Custom GitHub Actions gives full control over native builds and platform-specific ORT library handling.

---

## Implementation Plan

### Section 1: Platform-Aware Library Name

**File:** `internal/engine/embedder/onnx.go`

The hardcoded `.so` suffix on line 41 works on macOS by accident (`dlopen` accepts both `.so` and `.dylib`), but is incorrect and will cause confusion in release tarballs. Fix by selecting the library name based on `runtime.GOOS`.

```go
import "runtime"

// ortLibraryName returns the platform-specific ONNX Runtime shared library filename.
func ortLibraryName() string {
    switch runtime.GOOS {
    case "darwin":
        return "libonnxruntime.dylib"
    case "windows":
        return "onnxruntime.dll"
    default:
        return "libonnxruntime.so"
    }
}
```

Replace line 41:
```go
// Before:
libPath := filepath.Join(modelDir, "libonnxruntime.so")

// After:
libPath := filepath.Join(modelDir, ortLibraryName())
```

**Why this is safe:** `ort.SetSharedLibraryPath()` passes the path directly to `dlopen()`, which handles both `.so` and `.dylib` on macOS. We're just making the default correct per platform.

**Tests:** No new tests needed — this is a path construction change. Existing ONNX-gated tests validate the full load path.

**Files:**
| File | Action |
|------|--------|
| `internal/engine/embedder/onnx.go` | modified — `ortLibraryName()` + updated `newONNXSession` |

---

### Section 2: Version Injection via ldflags

**File:** `internal/config/config.go`

Go's `ldflags -X` can only override package-level `var`, not `const`. Change the version declaration so release builds can inject the git tag.

```go
// Before (line 12):
const Version = "0.6.0"

// After:
// Version is set at build time via ldflags for release builds.
var Version = "0.7.0"
```

The build command becomes:
```bash
go build -ldflags "-s -w -X github.com/crimson-sun/lumber/internal/config.Version=0.7.0" -o bin/lumber ./cmd/lumber
```

- `-s -w` strips debug info and DWARF symbols (shrinks binary ~30%)
- `-X` injects the version string at link time

**Tests:** No new tests — `lumber -version` already prints `config.Version`.

**Files:**
| File | Action |
|------|--------|
| `internal/config/config.go` | modified — `const` → `var`, bump to `0.7.0` |

---

### Section 3: Multi-Platform Makefile Updates

**File:** `Makefile`

Three changes: (a) version-injected build target, (b) multi-platform ORT download, (c) clean up the `.so`-only assumption.

#### 3a. Version-injected build

```makefile
VERSION ?= dev

build:
	go build -ldflags "-s -w -X github.com/crimson-sun/lumber/internal/config.Version=$(VERSION)" \
		-o bin/lumber ./cmd/lumber
```

Running `make build` uses `dev`. Running `VERSION=0.7.0 make build` injects the version.

#### 3b. Multi-platform ORT download

Replace lines 39-47 (the ARM64-only Go module cache copy) with a target that downloads from ORT's GitHub releases:

```makefile
ORT_VERSION := 1.24.1

download-ort:
	@if [ ! -f $(MODEL_DIR)/$(ORT_LIB_NAME) ]; then \
		OS=$$(go env GOOS); ARCH=$$(go env GOARCH); \
		case "$$OS-$$ARCH" in \
			linux-amd64)  ORT_ARCH="linux-x64";       ORT_LIB="libonnxruntime.so.$(ORT_VERSION)";        DEST="libonnxruntime.so" ;; \
			linux-arm64)  ORT_ARCH="linux-aarch64";    ORT_LIB="libonnxruntime.so.$(ORT_VERSION)";        DEST="libonnxruntime.so" ;; \
			darwin-arm64) ORT_ARCH="osx-arm64";        ORT_LIB="libonnxruntime.$(ORT_VERSION).dylib";     DEST="libonnxruntime.dylib" ;; \
			*) echo "Unsupported platform: $$OS/$$ARCH"; exit 1 ;; \
		esac; \
		echo "Downloading ONNX Runtime $$ORT_ARCH $(ORT_VERSION)..."; \
		curl -fSL "https://github.com/microsoft/onnxruntime/releases/download/v$(ORT_VERSION)/onnxruntime-$$ORT_ARCH-$(ORT_VERSION).tgz" \
			| tar xz -C /tmp; \
		cp "/tmp/onnxruntime-$$ORT_ARCH-$(ORT_VERSION)/lib/$$ORT_LIB" "$(MODEL_DIR)/$$DEST"; \
		rm -rf "/tmp/onnxruntime-$$ORT_ARCH-$(ORT_VERSION)"; \
		echo "ONNX Runtime library installed: $(MODEL_DIR)/$$DEST"; \
	fi
```

Update `download-model` to call `download-ort` instead of the Go module cache copy:

```makefile
download-model: download-ort
	# ... existing model download logic (lines 20-38) unchanged ...
```

Also update the `test` target — on macOS, `LD_LIBRARY_PATH` is ignored; use `DYLD_LIBRARY_PATH` instead:

```makefile
test:
	LD_LIBRARY_PATH=$(MODEL_DIR) DYLD_LIBRARY_PATH=$(MODEL_DIR) go test ./...
```

**Tests:** Run `make download-model && make test` locally to verify.

**Files:**
| File | Action |
|------|--------|
| `Makefile` | modified — version build, `download-ort` target, updated `download-model` dependency |

---

### Section 4: CI Quality Gate Workflow

**New file:** `.github/workflows/ci.yml`

Two tiers of testing, matching the existing `skipWithoutModel` pattern:
- **All 3 platforms**: Unit tests only (ONNX-gated tests auto-skip). Validates compilation, logic, and cross-platform correctness.
- **Linux x64 only**: Full integration tests with model files + ORT library. Validates the 153-entry corpus accuracy test and end-to-end pipeline. One platform is sufficient because the ONNX model produces identical vectors everywhere — it's pure math.

```yaml
name: CI

on:
  push:
    branches: [master]
  pull_request:
    branches: [master]

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - name: Check formatting
        run: |
          unformatted=$(gofmt -l .)
          if [ -n "$unformatted" ]; then
            echo "Files not formatted:"
            echo "$unformatted"
            exit 1
          fi
      - name: Vet
        run: go vet ./...
      - name: Lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: latest

  test:
    strategy:
      matrix:
        os: [ubuntu-latest, ubuntu-24.04-arm, macos-14]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - name: Unit tests
        run: go test ./...
        # ONNX-gated tests skip automatically via skipWithoutModel(t)

  integration-test:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - name: Download model files
        run: |
          make download-model
        # download-model now includes download-ort
      - name: Full test suite (with ONNX)
        run: LD_LIBRARY_PATH=models go test ./... -count=1

  build-check:
    strategy:
      matrix:
        os: [ubuntu-latest, ubuntu-24.04-arm, macos-14]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"
      - name: Build
        run: go build -o /dev/null ./cmd/lumber
```

**Files:**
| File | Action |
|------|--------|
| `.github/workflows/ci.yml` | new |

---

### Section 5: Release Workflow

**New file:** `.github/workflows/release.yml`

Triggered by pushing a `v*` tag. Builds natively on 3 platforms, bundles self-contained tarballs, creates GitHub Release.

```yaml
name: Release

on:
  push:
    tags: ["v*"]

permissions:
  contents: write  # Required for creating GitHub Releases

jobs:
  build:
    strategy:
      matrix:
        include:
          - os: ubuntu-latest
            goos: linux
            goarch: amd64
            ort_arch: linux-x64
            ort_lib_src: "libonnxruntime.so.1.24.1"
            ort_lib_dest: libonnxruntime.so
          - os: ubuntu-24.04-arm
            goos: linux
            goarch: arm64
            ort_arch: linux-aarch64
            ort_lib_src: "libonnxruntime.so.1.24.1"
            ort_lib_dest: libonnxruntime.so
          - os: macos-14
            goos: darwin
            goarch: arm64
            ort_arch: osx-arm64
            ort_lib_src: "libonnxruntime.1.24.1.dylib"
            ort_lib_dest: libonnxruntime.dylib
    runs-on: ${{ matrix.os }}

    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"

      - name: Extract version from tag
        id: version
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"

      - name: Download model files
        run: |
          mkdir -p models/2_Dense
          curl -fSL -o models/model_quantized.onnx \
            "https://huggingface.co/onnx-community/mdbr-leaf-mt-ONNX/resolve/main/onnx/model_quantized.onnx"
          curl -fSL -o models/model_quantized.onnx_data \
            "https://huggingface.co/onnx-community/mdbr-leaf-mt-ONNX/resolve/main/onnx/model_quantized.onnx_data"
          curl -fSL -o models/vocab.txt \
            "https://huggingface.co/onnx-community/mdbr-leaf-mt-ONNX/resolve/main/vocab.txt"
          curl -fSL -o models/2_Dense/model.safetensors \
            "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main/2_Dense/model.safetensors"
          curl -fSL -o models/2_Dense/config.json \
            "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main/2_Dense/config.json"

      - name: Download ONNX Runtime
        run: |
          curl -fSL "https://github.com/microsoft/onnxruntime/releases/download/v1.24.1/onnxruntime-${{ matrix.ort_arch }}-1.24.1.tgz" \
            | tar xz -C /tmp
          cp "/tmp/onnxruntime-${{ matrix.ort_arch }}-1.24.1/lib/${{ matrix.ort_lib_src }}" \
            "models/${{ matrix.ort_lib_dest }}"

      - name: Build binary
        run: |
          CGO_ENABLED=1 go build \
            -ldflags "-s -w -X github.com/crimson-sun/lumber/internal/config.Version=${{ steps.version.outputs.version }}" \
            -o staging/bin/lumber ./cmd/lumber

      - name: Assemble tarball
        run: |
          TARBALL="lumber-v${{ steps.version.outputs.version }}-${{ matrix.goos }}-${{ matrix.goarch }}"
          mkdir -p "staging/models/2_Dense"
          cp models/model_quantized.onnx       staging/models/
          cp models/model_quantized.onnx_data  staging/models/
          cp models/vocab.txt                  staging/models/
          cp models/2_Dense/model.safetensors  staging/models/2_Dense/
          cp models/2_Dense/config.json        staging/models/2_Dense/
          cp "models/${{ matrix.ort_lib_dest }}" staging/models/
          mv staging "$TARBALL"
          tar czf "${TARBALL}.tar.gz" "$TARBALL"

      - name: Upload artifact
        uses: actions/upload-artifact@v4
        with:
          name: lumber-${{ matrix.goos }}-${{ matrix.goarch }}
          path: "*.tar.gz"

  release:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - name: Download all artifacts
        uses: actions/download-artifact@v4
        with:
          merge-multiple: true

      - name: Generate checksums
        run: sha256sum *.tar.gz > checksums.txt

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          files: |
            *.tar.gz
            checksums.txt
          generate_release_notes: true
```

**Tarball layout (what users get):**
```
lumber-v0.7.0-linux-amd64/
  bin/lumber                          # Go binary (~15MB)
  models/model_quantized.onnx        # ONNX model (~23MB)
  models/model_quantized.onnx_data   # model external data
  models/vocab.txt                   # tokenizer vocabulary
  models/2_Dense/model.safetensors   # projection weights (~1.6MB)
  models/2_Dense/config.json         # projection config
  models/libonnxruntime.so           # ORT shared library (~8-35MB)
```

Users run:
```bash
tar xzf lumber-v0.7.0-linux-amd64.tar.gz
cd lumber-v0.7.0-linux-amd64
LD_LIBRARY_PATH=models bin/lumber -version
```

(On macOS, no `LD_LIBRARY_PATH` needed — the code passes the absolute path to `dlopen` via `SetSharedLibraryPath`.)

**Files:**
| File | Action |
|------|--------|
| `.github/workflows/release.yml` | new |

---

### Section 6: README Updates

**File:** `README.md`

Add a new "Install" section before the existing "Prerequisites" / "Quickstart" section:

```markdown
## Install

### Pre-built binaries (recommended)

Download the latest release for your platform from
[GitHub Releases](https://github.com/kaminocorp/lumber/releases):

| Platform | Archive |
|----------|---------|
| Linux x86_64 | `lumber-vX.Y.Z-linux-amd64.tar.gz` |
| Linux ARM64 | `lumber-vX.Y.Z-linux-arm64.tar.gz` |
| macOS Apple Silicon | `lumber-vX.Y.Z-darwin-arm64.tar.gz` |

Extract and run:

    tar xzf lumber-vX.Y.Z-linux-amd64.tar.gz
    cd lumber-vX.Y.Z-linux-amd64
    bin/lumber -version

The release tarball is self-contained — binary, model files, and ONNX
Runtime library are all included. No additional downloads required.

### Build from source

Requires Go 1.24+ and curl.

    git clone https://github.com/kaminocorp/lumber.git
    cd lumber
    make download-model
    make build
    bin/lumber -version
```

**Files:**
| File | Action |
|------|--------|
| `README.md` | modified — add Install section with release tarball instructions |

---

## Summary

### Files created (2)

| File | Purpose |
|------|---------|
| `.github/workflows/ci.yml` | CI quality gate: lint, unit tests (3 platforms), integration test (1 platform), build check |
| `.github/workflows/release.yml` | Release: native builds on 3 platforms, bundled tarballs, GitHub Release |

### Files modified (4)

| File | Change |
|------|--------|
| `internal/engine/embedder/onnx.go` | `ortLibraryName()` for cross-platform library name |
| `internal/config/config.go` | `const Version` → `var Version` for ldflags injection |
| `Makefile` | `download-ort` target, version-injected build, macOS lib path |
| `README.md` | Install section with release tarball instructions |

### Implementation order

1. **Section 1** — Platform-aware library name (prerequisite for everything)
2. **Section 2** — Version injection (needed by release workflow)
3. **Section 3** — Makefile updates (needed by CI workflow)
4. **Section 4** — CI workflow (push branch, verify green)
5. **Section 5** — Release workflow (test with `v0.7.0-rc.1` tag)
6. **Section 6** — README updates

### Verification

1. `make download-model && make build && bin/lumber -version` prints `0.7.0`
2. Push a branch → CI runs lint + unit tests on 3 platforms + integration tests on Linux x64
3. Push `v0.7.0` tag → 3 tarballs + checksums appear on GitHub Release
4. Download tarball, extract, run `bin/lumber -version` → prints `0.7.0`

### Deferred

- **Docker image** — will add in a later phase when ready
- **Homebrew formula** — after first stable release (v1.0)
- **Windows support** — if demand materializes
