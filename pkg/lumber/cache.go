package lumber

import (
	"fmt"
	"os"
)

// defaultCacheDir returns the platform-appropriate cache directory for
// auto-downloaded model files.
//
// Precedence:
//  1. $LUMBER_CACHE_DIR environment variable (explicit override)
//  2. os.UserCacheDir() + "/lumber" (~/Library/Caches/lumber on macOS,
//     $XDG_CACHE_HOME/lumber or ~/.cache/lumber on Linux)
func defaultCacheDir() (string, error) {
	if dir := os.Getenv("LUMBER_CACHE_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine cache directory: %w", err)
	}
	return base + "/lumber", nil
}
