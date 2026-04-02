# Phase 12: Docker Image & Homebrew Distribution

**Goal:** Make Lumber installable via the two most common distribution channels beyond direct binary download: Docker (for servers, CI pipelines, and containerized deployments) and Homebrew (for macOS/Linux developers). After this phase: `docker run kaminocorp/lumber` and `brew install kaminocorp/tap/lumber` both work out of the box.

**Starting point:** v0.8.1. GitHub Release workflow (Phase 9) produces self-contained tarballs for 3 platforms. No Docker image exists. No Homebrew formula exists. The binary requires model files (~25MB) and the ONNX Runtime shared library to be co-located in a `models/` directory.

**Dependencies:** Phase 9 release workflow must be complete (it is). Docker and Homebrew builds reuse the same artifacts and build patterns.

---

## Phase 12a: Docker Image

### What and Why

A Docker image lets users run Lumber without installing anything locally — no Go toolchain, no model download, no library path configuration. It's also the standard deployment unit for servers, Kubernetes, and CI pipelines.

**Target:** `kaminocorp/lumber` on Docker Hub (and optionally `ghcr.io/kaminocorp/lumber` on GitHub Container Registry).

**Image size target:** ~150MB (binary ~15MB + model ~25MB + ORT library ~35MB + base image ~70MB).

---

### Section 1: Dockerfile

**New file:** `Dockerfile`

Multi-stage build: compile in a Go builder image, copy the binary + assets into a minimal runtime image.

```dockerfile
# ---- Build stage ----
FROM golang:1.24 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=1 go build \
    -ldflags "-s -w -X github.com/kaminocorp/lumber/internal/config.Version=${VERSION}" \
    -o /lumber ./cmd/lumber

# ---- Runtime stage ----
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary
COPY --from=builder /lumber /app/bin/lumber

# Copy model files (downloaded during image build or provided as build context)
COPY models/model_quantized.onnx       /app/models/
COPY models/model_quantized.onnx_data  /app/models/
COPY models/vocab.txt                  /app/models/
COPY models/2_Dense/model.safetensors  /app/models/2_Dense/
COPY models/2_Dense/config.json        /app/models/2_Dense/

# Copy ONNX Runtime shared library
# The correct platform library must be present in models/ before docker build.
# On Linux: libonnxruntime.so. On macOS (buildx): libonnxruntime.dylib.
COPY models/libonnxruntime.so          /app/models/

# Set library path so ONNX Runtime can be found at runtime.
ENV LD_LIBRARY_PATH=/app/models

# Default working directory for model path resolution.
WORKDIR /app

ENTRYPOINT ["/app/bin/lumber"]
```

**Design decisions:**

- **`debian:bookworm-slim` over Alpine** — ONNX Runtime requires glibc. Alpine uses musl, which is incompatible with the prebuilt ORT binaries. `bookworm-slim` is ~70MB, a reasonable base.
- **`ca-certificates` + `curl`** — `ca-certificates` is needed for HTTPS connections to cloud log providers. `curl` is included for debugging convenience inside the container but could be removed to shave ~5MB.
- **Model files in the image** — bundled at build time, not downloaded at runtime. This makes the image self-contained and avoids startup latency. Tradeoff: image is ~150MB instead of ~85MB, but this is standard for ML-adjacent tools.
- **`ENTRYPOINT` not `CMD`** — allows `docker run kaminocorp/lumber -version` to pass flags naturally.

---

### Section 2: .dockerignore

**New file:** `.dockerignore`

Keep the build context small and avoid leaking sensitive files into the image.

```
.git
bin/
*.tar.gz
docs/
.github/
*.md
!README.md
.env
*.log
```

**Why this matters:** Without `.dockerignore`, `docker build` sends the entire repo (including `.git/` which can be 100MB+) as build context. This file reduces context transfer to just source code + model files.

---

### Section 3: Docker Build & Publish Workflow

**New file:** `.github/workflows/docker.yml`

Builds and pushes the Docker image on every `v*` tag, alongside the existing release workflow.

```yaml
name: Docker

on:
  push:
    tags: ["v*"]

permissions:
  contents: read
  packages: write

env:
  DOCKER_HUB_IMAGE: kaminocorp/lumber
  GHCR_IMAGE: ghcr.io/kaminocorp/lumber

jobs:
  docker:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Extract version from tag
        id: version
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"

      - name: Download model files
        run: |
          mkdir -p models/2_Dense
          curl -fSL -o models/model_quantized.onnx \
            "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main/onnx/model_quantized.onnx"
          curl -fSL -o models/model_quantized.onnx_data \
            "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main/onnx/model_quantized.onnx_data"
          curl -fSL -o models/vocab.txt \
            "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main/vocab.txt"
          curl -fSL -o models/2_Dense/model.safetensors \
            "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main/2_Dense/model.safetensors"
          curl -fSL -o models/2_Dense/config.json \
            "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main/2_Dense/config.json"

      - name: Download ONNX Runtime (Linux x64)
        run: |
          curl -fSL "https://github.com/microsoft/onnxruntime/releases/download/v1.24.1/onnxruntime-linux-x64-1.24.1.tgz" \
            | tar xz -C /tmp
          cp "/tmp/onnxruntime-linux-x64-1.24.1/lib/libonnxruntime.so.1.24.1" models/libonnxruntime.so

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to Docker Hub
        uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKERHUB_USERNAME }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      - name: Log in to GitHub Container Registry
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          build-args: VERSION=${{ steps.version.outputs.version }}
          tags: |
            ${{ env.DOCKER_HUB_IMAGE }}:${{ steps.version.outputs.version }}
            ${{ env.DOCKER_HUB_IMAGE }}:latest
            ${{ env.GHCR_IMAGE }}:${{ steps.version.outputs.version }}
            ${{ env.GHCR_IMAGE }}:latest
```

**Secrets required:**
- `DOCKERHUB_USERNAME` — Docker Hub username (set in GitHub repo settings → Secrets)
- `DOCKERHUB_TOKEN` — Docker Hub access token (not password — generate at hub.docker.com/settings/security)
- `GITHUB_TOKEN` — automatically available, used for GHCR

**Tags pushed:**
- `kaminocorp/lumber:0.8.1` + `kaminocorp/lumber:latest` (Docker Hub)
- `ghcr.io/kaminocorp/lumber:0.8.1` + `ghcr.io/kaminocorp/lumber:latest` (GHCR)

**Why both registries:** Docker Hub is the default `docker pull` source (most discoverable). GHCR is free for public repos and lives next to the code. Publishing to both costs nothing extra — the build runs once, push is just an upload.

---

### Section 4: Multi-Architecture Docker (Optional, Deferred)

The initial Docker image is **linux/amd64 only**. Multi-arch (adding `linux/arm64`) requires either:

**Option A: QEMU emulation via `docker/setup-qemu-action`**
- Slow (~5-10x slower build) but simple.
- Works for Lumber because the Go compile step is fast and ONNX Runtime has prebuilt ARM64 binaries.

**Option B: Native ARM64 runner**
- Fast but requires a self-hosted runner or GitHub's ARM64 runners.
- More complex workflow (build on 2 runners, merge manifests).

**Recommendation:** Ship amd64-only first. Add ARM64 when there's demand, using QEMU (Option A) for simplicity:

```yaml
# Future addition to docker.yml:
- name: Set up QEMU
  uses: docker/setup-qemu-action@v3

# In build-push-action:
  platforms: linux/amd64,linux/arm64
```

This would require the Dockerfile to conditionally copy the correct ORT library (`libonnxruntime.so` vs the ARM64 variant). A build-arg or multi-stage approach with platform detection would handle this.

**Not implementing now** — this is noted for future work.

---

### Section 5: Docker Usage Documentation

**File:** `README.md` — add a Docker section after the Install section.

Content to add:

```markdown
### Docker

```bash
# Run with piped input
cat app.log | docker run -i kaminocorp/lumber -connector stdin

# Stream from a cloud provider
docker run -e LUMBER_CONNECTOR=vercel \
           -e LUMBER_API_KEY=your-token \
           -e LUMBER_VERCEL_PROJECT_ID=prj_xxx \
           kaminocorp/lumber

# Classify a local file (mount it into the container)
docker run -v /path/to/logs:/data kaminocorp/lumber -connector file -file /data/app.log

# Check version
docker run kaminocorp/lumber -version
```

The Docker image is self-contained — model files and ONNX Runtime are bundled.
Available on [Docker Hub](https://hub.docker.com/r/kaminocorp/lumber) and
[GitHub Container Registry](https://ghcr.io/kaminocorp/lumber).
```

**Note:** The `docker run -i` flag is required for stdin piping. Without it, Docker doesn't connect stdin to the container.

---

### Phase 12a File Summary

| File | Action |
|------|--------|
| `Dockerfile` | **new** |
| `.dockerignore` | **new** |
| `.github/workflows/docker.yml` | **new** |
| `README.md` | modified — add Docker section |

**New files: 3. Modified files: 1. Total: 4.**

### Phase 12a Verification

- [ ] `docker build -t lumber .` builds successfully (requires model files in `models/`)
- [ ] `docker run lumber -version` prints version
- [ ] `echo "ERROR connection refused" | docker run -i lumber -connector stdin` classifies the log
- [ ] Image size < 200MB
- [ ] Push `v*` tag → image appears on Docker Hub and GHCR
- [ ] `docker pull kaminocorp/lumber:latest` works

---

## Phase 12b: Homebrew Formula

### What and Why

Homebrew is the standard package manager for macOS developers and increasingly common on Linux. `brew install` is the expected installation path for CLI tools on macOS. It handles versioning, upgrades, and PATH management automatically.

**Distribution model:** Homebrew formulas can either:
1. **Build from source** — `brew` downloads source, compiles locally. Standard for open-source tools.
2. **Download pre-built bottles** — `brew` downloads a pre-compiled binary. Faster install, no build tools needed.

Lumber needs **option 2 (bottles)** because:
- Compilation requires CGo + ONNX Runtime headers (complex build environment)
- Model files (~25MB) must be bundled alongside the binary
- Users shouldn't need Go or curl installed to use Lumber

The approach: a **Homebrew tap** (`kaminocorp/tap`) with a formula that downloads the release tarball from GitHub Releases (the same tarballs Phase 9 produces).

---

### Section 1: Create Homebrew Tap Repository

**New repository:** `github.com/kaminocorp/homebrew-tap`

This is a separate Git repository that Homebrew reads formula definitions from. It must follow the naming convention `homebrew-tap` so that `brew tap kaminocorp/tap` discovers it.

Repository structure:
```
homebrew-tap/
  Formula/
    lumber.rb    # the formula
  README.md
```

**This repo is created once manually on GitHub.** The formula file is what gets automated.

---

### Section 2: Homebrew Formula

**New file (in homebrew-tap repo):** `Formula/lumber.rb`

```ruby
class Lumber < Formula
  desc "High-performance log normalization pipeline"
  homepage "https://github.com/kaminocorp/lumber"
  version "0.8.1"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/kaminocorp/lumber/releases/download/v#{version}/lumber-v#{version}-darwin-arm64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_DARWIN_ARM64"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/kaminocorp/lumber/releases/download/v#{version}/lumber-v#{version}-linux-arm64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_LINUX_ARM64"
    end
    on_intel do
      url "https://github.com/kaminocorp/lumber/releases/download/v#{version}/lumber-v#{version}-linux-amd64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_LINUX_AMD64"
    end
  end

  def install
    bin.install "bin/lumber"

    # Install model files alongside the binary in the Homebrew prefix.
    # Lumber resolves model paths relative to the binary or via env vars.
    (share/"lumber/models/2_Dense").mkpath
    (share/"lumber/models").install "models/model_quantized.onnx"
    (share/"lumber/models").install "models/model_quantized.onnx_data"
    (share/"lumber/models").install "models/vocab.txt"
    (share/"lumber/models/2_Dense").install "models/2_Dense/model.safetensors"
    (share/"lumber/models/2_Dense").install "models/2_Dense/config.json"

    # Install ONNX Runtime shared library.
    lib.install Dir["models/libonnxruntime.*"]
  end

  def caveats
    <<~EOS
      Model files are installed to:
        #{share}/lumber/models/

      To use Lumber, set the model paths or use the default:
        export LUMBER_MODEL_PATH=#{share}/lumber/models/model_quantized.onnx
        export LUMBER_VOCAB_PATH=#{share}/lumber/models/vocab.txt
        export LUMBER_PROJECTION_PATH=#{share}/lumber/models/2_Dense/model.safetensors
    EOS
  end

  test do
    assert_match "lumber", shell_output("#{bin}/lumber -version")
  end
end
```

**Design decisions:**

- **Platform-conditional URLs** — Homebrew's `on_macos`/`on_linux` + `on_arm`/`on_intel` blocks select the correct tarball per platform. No macOS Intel block because ORT 1.24.x doesn't ship prebuilt Intel macOS binaries.
- **SHA256 placeholders** — must be replaced with actual checksums when a release is cut. The update workflow (Section 4) automates this.
- **`share/lumber/models/`** — Homebrew convention: application data goes in `share/`. The binary goes in `bin/`. Libraries go in `lib/`.
- **Caveats** — printed after `brew install`. Tells users where model files live and how to configure paths. This is needed because Lumber's default model path (`models/model_quantized.onnx`) is relative to the working directory, not the install prefix.

---

### Section 3: Model Path Resolution for Homebrew

**File (in lumber repo):** `internal/config/config.go`

The current default model paths are relative: `models/model_quantized.onnx`. This works when running from the repo root or from an extracted tarball, but not after `brew install` where files are in `/opt/homebrew/share/lumber/models/`.

Add fallback path resolution that checks the Homebrew prefix:

```go
func resolveModelPath(configured string) string {
    // If the configured path exists, use it directly.
    if _, err := os.Stat(configured); err == nil {
        return configured
    }

    // Try relative to the executable (for tarball installs).
    if exe, err := os.Executable(); err == nil {
        exeDir := filepath.Dir(exe)
        candidate := filepath.Join(filepath.Dir(exeDir), "share", "lumber", configured)
        if _, err := os.Stat(candidate); err == nil {
            return candidate
        }
    }

    // Return the original path — validation will catch if it doesn't exist.
    return configured
}
```

Apply in `Load()`:
```go
Engine: EngineConfig{
    ModelPath:      resolveModelPath(getenv("LUMBER_MODEL_PATH", "models/model_quantized.onnx")),
    VocabPath:      resolveModelPath(getenv("LUMBER_VOCAB_PATH", "models/vocab.txt")),
    ProjectionPath: resolveModelPath(getenv("LUMBER_PROJECTION_PATH", "models/2_Dense/model.safetensors")),
    // ...
},
```

**Resolution order:**
1. Explicit path (env var or flag) → use as-is if file exists
2. Relative to executable's parent (`../share/lumber/...`) → catches Homebrew and standard FHS layouts
3. Fall through to original path → validation reports the error

This also benefits tarball installs — if the user moves the binary to `/usr/local/bin/` and models to `/usr/local/share/lumber/models/`, it resolves automatically.

**Also needed:** The ONNX Runtime library path in `embedder/onnx.go` needs the same treatment. Currently it resolves relative to the model directory. After Homebrew install, the `.dylib` is in `lib/` not `models/`. Add a fallback:

```go
func resolveORTLibrary(modelDir string) string {
    // Primary: alongside model files (tarball layout).
    primary := filepath.Join(modelDir, ortLibraryName())
    if _, err := os.Stat(primary); err == nil {
        return primary
    }

    // Fallback: Homebrew lib/ directory (../lib/ relative to share/lumber/models/).
    if exe, err := os.Executable(); err == nil {
        candidate := filepath.Join(filepath.Dir(exe), "..", "lib", ortLibraryName())
        if _, err := os.Stat(candidate); err == nil {
            return candidate
        }
    }

    return primary
}
```

**Tests:**

| Test | What |
|------|------|
| `resolveModelPath` with existing relative path | Returns path unchanged |
| `resolveModelPath` with nonexistent relative path + Homebrew-style layout | Returns Homebrew path |
| `resolveModelPath` with nonexistent path and no Homebrew | Returns original (validation catches it) |
| `resolveORTLibrary` with library in model dir | Returns model dir path |
| `resolveORTLibrary` with library in Homebrew lib dir | Returns lib dir path |

---

### Section 4: Automated Formula Update Workflow

**New file (in lumber repo):** `.github/workflows/update-homebrew.yml`

After a GitHub Release is published, automatically update the Homebrew formula with the new version and SHA256 checksums.

```yaml
name: Update Homebrew

on:
  release:
    types: [published]

jobs:
  update-formula:
    runs-on: ubuntu-latest
    steps:
      - name: Extract version
        id: version
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"

      - name: Download checksums
        run: |
          curl -fSL -o checksums.txt \
            "https://github.com/kaminocorp/lumber/releases/download/${GITHUB_REF_NAME}/checksums.txt"

      - name: Extract SHA256 values
        id: sha
        run: |
          echo "darwin_arm64=$(grep 'darwin-arm64' checksums.txt | awk '{print $1}')" >> "$GITHUB_OUTPUT"
          echo "linux_amd64=$(grep 'linux-amd64' checksums.txt | awk '{print $1}')" >> "$GITHUB_OUTPUT"
          echo "linux_arm64=$(grep 'linux-arm64' checksums.txt | awk '{print $1}')" >> "$GITHUB_OUTPUT"

      - name: Checkout homebrew-tap
        uses: actions/checkout@v4
        with:
          repository: kaminocorp/homebrew-tap
          token: ${{ secrets.HOMEBREW_TAP_TOKEN }}

      - name: Update formula
        run: |
          sed -i "s/version \".*\"/version \"${{ steps.version.outputs.version }}\"/" Formula/lumber.rb
          sed -i "s/PLACEHOLDER_SHA256_DARWIN_ARM64/${{ steps.sha.outputs.darwin_arm64 }}/" Formula/lumber.rb
          sed -i "s/PLACEHOLDER_SHA256_LINUX_AMD64/${{ steps.sha.outputs.linux_amd64 }}/" Formula/lumber.rb
          sed -i "s/PLACEHOLDER_SHA256_LINUX_ARM64/${{ steps.sha.outputs.linux_arm64 }}/" Formula/lumber.rb

      - name: Commit and push
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "github-actions[bot]@users.noreply.github.com"
          git add Formula/lumber.rb
          git commit -m "lumber ${{ steps.version.outputs.version }}"
          git push
```

**Secrets required:**
- `HOMEBREW_TAP_TOKEN` — a GitHub personal access token with `repo` scope for the `kaminocorp/homebrew-tap` repository. Set in the lumber repo's Secrets.

**Flow:**
1. Push `v0.9.0` tag → release workflow creates tarballs + checksums
2. Release published → `update-homebrew` workflow triggers
3. Downloads `checksums.txt` from the release
4. Extracts SHA256 for each platform
5. Checks out `homebrew-tap`, updates `Formula/lumber.rb` with new version + hashes
6. Commits and pushes → `brew update && brew upgrade lumber` picks up the new version

**Note on subsequent releases:** The `sed` commands replace the *current* SHA256 values (not just placeholders). For the first release, the placeholders are replaced. For subsequent releases, the previous SHA256 values are replaced. This works because SHA256 hashes are unique strings that `sed` can match reliably.

Actually, a more robust approach for subsequent releases is to regenerate the formula from a template. A simple improvement:

```bash
# Instead of sed on SHA256 values, sed on the version line + regenerate URLs.
# The SHA256 lines use a known pattern we can target:
sed -i "/darwin-arm64/s/sha256 \".*\"/sha256 \"${{ steps.sha.outputs.darwin_arm64 }}\"/" Formula/lumber.rb
```

But for v1, the simple `sed` approach works since each platform block has a unique SHA256 line.

---

### Section 5: Homebrew Usage Documentation

**File:** `README.md` — add Homebrew to the Install section.

Content to add (between pre-built binaries and build-from-source):

```markdown
### Homebrew (macOS / Linux)

```bash
brew install kaminocorp/tap/lumber
lumber -version
```

Homebrew installs the binary, model files, and ONNX Runtime library automatically.
Model files are stored in `$(brew --prefix)/share/lumber/models/`.
```

---

### Phase 12b File Summary

**In the lumber repo:**

| File | Action |
|------|--------|
| `internal/config/config.go` | modified — `resolveModelPath()` for Homebrew prefix |
| `internal/config/config_test.go` | modified — tests for `resolveModelPath()` |
| `internal/engine/embedder/onnx.go` | modified — `resolveORTLibrary()` for Homebrew lib dir |
| `.github/workflows/update-homebrew.yml` | **new** |
| `README.md` | modified — add Homebrew section |

**In the homebrew-tap repo (separate):**

| File | Action |
|------|--------|
| `Formula/lumber.rb` | **new** |
| `README.md` | **new** |

**Lumber repo: 1 new, 4 modified. Tap repo: 2 new. Total: 7.**

---

### Phase 12b Verification

- [ ] `brew tap kaminocorp/tap` succeeds
- [ ] `brew install kaminocorp/tap/lumber` downloads tarball, installs binary + models + ORT
- [ ] `lumber -version` works after install (binary in PATH)
- [ ] `lumber -connector stdin` works (model files found via prefix resolution)
- [ ] `brew upgrade lumber` picks up new version after a release
- [ ] Formula `test` block passes (`brew test lumber`)

---

## Phase 12 Pre-Requisites

Before implementing Phase 12, the following must be in place:

1. **Docker Hub account** for `kaminocorp` organization — create at hub.docker.com
2. **GitHub repo `kaminocorp/homebrew-tap`** — create empty repo
3. **GitHub Secrets** configured in the `lumber` repo:
   - `DOCKERHUB_USERNAME` — Docker Hub username
   - `DOCKERHUB_TOKEN` — Docker Hub access token
   - `HOMEBREW_TAP_TOKEN` — GitHub PAT with `repo` scope for `homebrew-tap`
4. **At least one GitHub Release** with tarballs — needed for Homebrew SHA256 values

---

## Implementation Order

```
Phase 12a: Docker                        Phase 12b: Homebrew
─────────────────                        ───────────────────

1. Dockerfile + .dockerignore            1. Create homebrew-tap repo (manual)
2. Docker workflow                       2. Model path resolution in config.go
3. Docker Hub account + secrets          3. ORT library resolution in onnx.go
4. Test: docker build + run              4. Formula (lumber.rb)
5. README Docker section                 5. Update-homebrew workflow + secrets
                                         6. Test: brew install + lumber -version
                                         7. README Homebrew section
```

**12a and 12b can be implemented in parallel** — they share no code dependencies. The only shared prerequisite is a published GitHub Release to provide tarball URLs and checksums.

---

## Deferred

- **Multi-arch Docker** (linux/amd64 + linux/arm64) — add when ARM64 demand materializes. QEMU emulation approach documented in Section 4 of Phase 12a.
- **Homebrew core submission** — getting into the main Homebrew repository (not a tap) requires stable release history and community review. Target after v1.0 with proven adoption.
- **Scoop (Windows)** — Windows package manager. Add if Windows support is implemented.
- **APT/YUM repositories** — native Linux package managers. Significant infrastructure overhead. Docker covers most Linux server use cases.
