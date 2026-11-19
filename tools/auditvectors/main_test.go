package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedVectorsMatchGenerator(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("../..", fixturePath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, generate()) {
		t.Fatal("checkpoint vectors drifted. From the repository root, run go run ./tools/auditvectors")
	}
}

func TestGeneratorPreservesFixtureOnWriteFailure(t *testing.T) {
	const child = "OPENPLEXER_AUDITVECTOR_WRITE_FAILURE"
	if os.Getenv(child) == "1" {
		main()
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, fixturePath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := generate()
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Limit file writes in the child so even a failed write cannot affect other tests.
	cmd := exec.Command("sh", "-c", `ulimit -f 2 && exec "$1" -test.run '^TestGeneratorPreservesFixtureOnWriteFailure$'`, "sh", executable)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), child+"=1")
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "file too large") {
		t.Fatalf("generator should fail on the size limit: %v\n%s", err, output)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("fixture changed after the failed write: got %d bytes, want %d", len(got), len(original))
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("fixture directory should contain only the original file: %v", files)
	}
}
