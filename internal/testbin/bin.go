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
	for _, app := range apps {
		path := filepath.Join(dir, app)
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("OPENPLEXER_BIN_DIR: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("OPENPLEXER_BIN_DIR: %s is not a regular file", path)
		}
	}
	return dir, nil
}
