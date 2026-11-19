// Package testbin locates the optional prebuilt binaries for test harnesses.
package testbin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Run reports a binary's exit status separately from execution errors.
func Run(cmd *exec.Cmd) (int, error) {
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// Dir returns an empty string when the harness should build from source.
func Dir(apps ...string) (string, error) {
	dir := os.Getenv("OPENPLEXER_BIN_DIR")
	if dir == "" {
		return "", nil
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("OPENPLEXER_BIN_DIR must be an absolute path: %q", dir)
	}
	realDir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("OPENPLEXER_BIN_DIR %q: %w", dir, err)
	}
	for _, app := range apps {
		path := filepath.Join(dir, app)
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("OPENPLEXER_BIN_DIR: resolve %s: %w", path, err)
		}
		rel, err := filepath.Rel(realDir, realPath)
		if err != nil || !filepath.IsLocal(rel) {
			return "", fmt.Errorf("OPENPLEXER_BIN_DIR: %s resolves outside %s to %s", path, realDir, realPath)
		}
		info, err := os.Stat(realPath)
		if err != nil {
			return "", fmt.Errorf("OPENPLEXER_BIN_DIR: stat %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("OPENPLEXER_BIN_DIR: %s is not a regular file", path)
		}
	}
	return dir, nil
}
