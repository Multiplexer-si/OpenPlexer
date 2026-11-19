package main

import (
	"bytes"
	"os"
	"path/filepath"
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
