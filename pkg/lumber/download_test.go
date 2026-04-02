package lumber

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultCacheDir(t *testing.T) {
	// With LUMBER_CACHE_DIR set, it takes precedence.
	t.Setenv("LUMBER_CACHE_DIR", "/tmp/lumber-test-cache")
	dir, err := defaultCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/tmp/lumber-test-cache" {
		t.Errorf("expected /tmp/lumber-test-cache, got %s", dir)
	}

	// Without LUMBER_CACHE_DIR, falls back to os.UserCacheDir() + /lumber.
	t.Setenv("LUMBER_CACHE_DIR", "")
	dir, err = defaultCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := os.UserCacheDir()
	if dir != base+"/lumber" {
		t.Errorf("expected %s/lumber, got %s", base, dir)
	}
}

func TestFileValid(t *testing.T) {
	dir := t.TempDir()

	// Non-existent file.
	if fileValid(filepath.Join(dir, "nope"), "") {
		t.Error("expected false for non-existent file")
	}

	// Existing file, no checksum — existence is enough.
	path := filepath.Join(dir, "exists.txt")
	os.WriteFile(path, []byte("hello"), 0o644)
	if !fileValid(path, "") {
		t.Error("expected true for existing file with no checksum")
	}

	// Existing file, matching checksum.
	h := sha256.Sum256([]byte("hello"))
	goodHash := hex.EncodeToString(h[:])
	if !fileValid(path, goodHash) {
		t.Error("expected true for matching checksum")
	}

	// Existing file, wrong checksum.
	if fileValid(path, "0000000000000000000000000000000000000000000000000000000000000000") {
		t.Error("expected false for mismatched checksum")
	}
}

func TestDownloadFile(t *testing.T) {
	content := []byte("test model data")
	h := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "model.bin")

	// Download with checksum verification.
	if err := downloadFile(srv.URL+"/model.bin", dest, expectedHash); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != string(content) {
		t.Errorf("content mismatch: got %q", got)
	}
}

func TestDownloadFile_ChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("corrupted data"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "model.bin")

	err := downloadFile(srv.URL+"/model.bin", dest, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}

	// Temp file should be cleaned up.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected empty dir after checksum failure, got %d files", len(entries))
	}
}

func TestDownloadFile_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "model.bin")

	err := downloadFile(srv.URL+"/model.bin", dest, "")
	if err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestDownloadFile_SkipsIfCached(t *testing.T) {
	content := []byte("cached model")
	h := sha256.Sum256(content)
	hash := hex.EncodeToString(h[:])

	dir := t.TempDir()
	dest := filepath.Join(dir, "model.bin")
	os.WriteFile(dest, content, 0o644)

	// downloadModels should skip files that are already valid.
	// We test fileValid directly since downloadModels uses real URLs.
	if !fileValid(dest, hash) {
		t.Error("expected cached file to be valid")
	}
}

func TestDownloadFile_SubdirectoryCreated(t *testing.T) {
	content := []byte("nested file")
	h := sha256.Sum256(content)
	hash := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "dir", "model.bin")

	if err := downloadFile(srv.URL+"/model.bin", dest, hash); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != string(content) {
		t.Errorf("content mismatch: got %q", got)
	}
}

func TestDownloadFile_CorruptCacheRedownloaded(t *testing.T) {
	goodContent := []byte("good data")
	h := sha256.Sum256(goodContent)
	hash := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(goodContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "model.bin")

	// Write corrupt file.
	os.WriteFile(dest, []byte("corrupt"), 0o644)
	if fileValid(dest, hash) {
		t.Fatal("corrupt file should not be valid")
	}

	// Download should replace it.
	if err := downloadFile(srv.URL+"/model.bin", dest, hash); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(goodContent) {
		t.Errorf("expected good data after re-download, got %q", got)
	}
}

func TestOrtPlatform(t *testing.T) {
	arch, lib, err := ortPlatform()

	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "linux-amd64":
		if err != nil || arch != "linux-x64" || lib != "libonnxruntime.so" {
			t.Errorf("unexpected: arch=%q lib=%q err=%v", arch, lib, err)
		}
	case "linux-arm64":
		if err != nil || arch != "linux-aarch64" || lib != "libonnxruntime.so" {
			t.Errorf("unexpected: arch=%q lib=%q err=%v", arch, lib, err)
		}
	case "darwin-arm64":
		if err != nil || arch != "osx-arm64" || lib != "libonnxruntime.dylib" {
			t.Errorf("unexpected: arch=%q lib=%q err=%v", arch, lib, err)
		}
	default:
		// Unsupported platform should error.
		if err == nil {
			t.Error("expected error for unsupported platform")
		}
	}
}

func TestAtomicWriteFromReader(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	r := strings.NewReader("atomic write test")
	if err := atomicWriteFromReader(dest, r); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != "atomic write test" {
		t.Errorf("got %q", got)
	}
}
