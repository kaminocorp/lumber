package lumber

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// modelFile describes a file to download with its expected checksum.
type modelFile struct {
	// URL to download from.
	url string
	// Relative path within the destination directory.
	relPath string
	// Expected SHA256 hex digest. Empty means skip verification (small config files).
	sha256 string
}

const (
	hfBase     = "https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main"
	ortVersion = "1.24.1"
)

// modelFiles lists all files required by the embedding engine.
var modelFiles = []modelFile{
	{
		url:     hfBase + "/onnx/model_quantized.onnx",
		relPath: "model_quantized.onnx",
		sha256:  "2a3541f3f156bc420d593fe8bcde37597980f0780035e6d0fb9b6a2f949d8855",
	},
	{
		url:     hfBase + "/onnx/model_quantized.onnx_data",
		relPath: "model_quantized.onnx_data",
		sha256:  "65dc11dae54946d5c18390e52b0f92ed04215d0965b7f0ea6fef71cf4bfce907",
	},
	{
		url:     hfBase + "/vocab.txt",
		relPath: "vocab.txt",
		sha256:  "07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3",
	},
	{
		url:     hfBase + "/2_Dense/model.safetensors",
		relPath: filepath.Join("2_Dense", "model.safetensors"),
		sha256:  "dfe95933b75110ca0c1650dc0a78f06d0a05a028892ac74ffc5aa3644283f16f",
	},
	{
		url:     hfBase + "/2_Dense/config.json",
		relPath: filepath.Join("2_Dense", "config.json"),
		sha256:  "5d4010b4ce519411f3d09a8eb2c757c18877a727c29a53be0c23f53ab3c951a1",
	},
}

// downloadModels downloads model files to destDir if they don't already exist
// or if existing files fail checksum verification. Returns nil if all files
// are present and valid.
func downloadModels(destDir string) error {
	for _, mf := range modelFiles {
		dest := filepath.Join(destDir, mf.relPath)
		if fileValid(dest, mf.sha256) {
			continue
		}
		slog.Info("downloading model file", "file", mf.relPath)
		if err := downloadFile(mf.url, dest, mf.sha256); err != nil {
			return fmt.Errorf("downloading %s: %w", mf.relPath, err)
		}
	}
	return nil
}

// ortPlatform returns the ORT archive suffix and installed library filename
// for the current platform, or an error if unsupported.
func ortPlatform() (archiveSuffix, libName string, err error) {
	key := runtime.GOOS + "-" + runtime.GOARCH
	switch key {
	case "linux-amd64":
		return "linux-x64", "libonnxruntime.so", nil
	case "linux-arm64":
		return "linux-aarch64", "libonnxruntime.so", nil
	case "darwin-arm64":
		return "osx-arm64", "libonnxruntime.dylib", nil
	default:
		return "", "", fmt.Errorf("auto-download not supported on %s/%s — use WithModelDir() with manually downloaded files", runtime.GOOS, runtime.GOARCH)
	}
}

// downloadORT downloads the platform-specific ONNX Runtime shared library
// to destDir if it doesn't already exist.
func downloadORT(destDir string) error {
	archSuffix, libName, err := ortPlatform()
	if err != nil {
		return err
	}

	dest := filepath.Join(destDir, libName)
	if _, err := os.Stat(dest); err == nil {
		return nil // already exists
	}

	archiveName := fmt.Sprintf("onnxruntime-%s-%s", archSuffix, ortVersion)
	url := fmt.Sprintf("https://github.com/microsoft/onnxruntime/releases/download/v%s/%s.tgz", ortVersion, archiveName)

	slog.Info("downloading ONNX Runtime", "platform", archSuffix, "version", ortVersion)

	// The ORT archive is a .tgz containing lib/<versioned-lib-name>.
	// We need to extract the library file and rename it.
	return downloadAndExtractORT(url, destDir, archiveName, libName)
}

// downloadAndExtractORT downloads a .tgz archive and extracts the ORT library from it.
func downloadAndExtractORT(url, destDir, archiveName, libName string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("downloading ORT: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading ORT: HTTP %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("decompressing ORT archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// Look for the library file inside the archive's lib/ directory.
	// Versioned names: libonnxruntime.so.1.24.1 (Linux), libonnxruntime.1.24.1.dylib (macOS)
	libPrefix := archiveName + "/lib/"

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading ORT archive: %w", err)
		}

		// Match the versioned library file in the lib/ subdirectory.
		if !strings.HasPrefix(hdr.Name, libPrefix) {
			continue
		}
		baseName := filepath.Base(hdr.Name)
		if !strings.HasPrefix(baseName, "libonnxruntime") {
			continue
		}
		// Skip symlinks — we want the actual versioned file.
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			continue
		}
		// Skip directories and tiny files (< 1MB is not the real library).
		if hdr.Typeflag == tar.TypeDir || hdr.Size < 1_000_000 {
			continue
		}

		dest := filepath.Join(destDir, libName)
		if err := atomicWriteFromReader(dest, tr); err != nil {
			return fmt.Errorf("extracting ORT library: %w", err)
		}
		slog.Info("ONNX Runtime installed", "path", dest)
		return nil
	}

	return fmt.Errorf("ORT library not found in archive")
}

// fileValid returns true if the file at path exists and its SHA256 matches
// the expected digest. If expectedSHA256 is empty, only existence is checked.
func fileValid(path, expectedSHA256 string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	if expectedSHA256 == "" {
		return true
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == expectedSHA256
}

// downloadFile downloads url to dest with atomic write and optional checksum verification.
func downloadFile(url, dest, expectedSHA256 string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	// Write to a temp file, then atomic rename to prevent partial files.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".lumber-download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	// Hash while writing when checksum verification is needed.
	var w io.Writer = tmp
	h := sha256.New()
	if expectedSHA256 != "" {
		w = io.MultiWriter(tmp, h)
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if expectedSHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != expectedSHA256 {
			os.Remove(tmpPath)
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", filepath.Base(dest), expectedSHA256, got)
		}
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// atomicWriteFromReader writes data from r to dest via a temp file + rename.
func atomicWriteFromReader(dest string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".lumber-download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

