package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseRejectsInvalidVersion(t *testing.T) {
	for _, version := range []string{
		"v1.2.3$(invalid)", "v1.2.3$$", "v1.2.3`invalid`", "v1.2.3\n", "v1", "v1.2.3+meta",
	} {
		err := release(version, "", "invalid/target")
		if err == nil || !strings.Contains(err.Error(), "invalid version") {
			t.Errorf("release(%q) did not reject the version: %v", version, err)
		}
	}
}

func TestReleaseBuildFailureLeavesNoOutput(t *testing.T) {
	dir := fakeGoBuild(t, `
if [ "$GOARCH" = arm64 ]; then
	exit 1
fi
for app in openplexer openplexer-hub openplexer-verify; do
	printf 'binary\n' > "$out$app"
done
`)
	out := filepath.Join(dir, "v1.2.3")
	err := release("v1.2.3", out, "")
	if err == nil || !strings.Contains(err.Error(), "build darwin/arm64") {
		t.Fatalf("want failure on the second target, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "go" {
		t.Errorf("failed build left files in its output parent: %v", entries)
	}
}

func TestReleaseRefusesExistingOutput(t *testing.T) {
	dir := fakeGoBuild(t, `
for app in openplexer openplexer-hub openplexer-verify; do
	printf 'binary\n' > "$out$app"
done
`)
	for _, name := range []string{"empty", "previous"} {
		out := filepath.Join(dir, name)
		if err := os.Mkdir(out, 0755); err != nil {
			t.Fatal(err)
		}
		if name == "previous" {
			if err := os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte("previous manifest\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		err := release("v1.2.3", out, "darwin/amd64")
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("release did not refuse existing output %s: %v", name, err)
		}
		entries, err := os.ReadDir(out)
		if err != nil {
			t.Fatal(err)
		}
		if name == "empty" && len(entries) != 0 {
			t.Errorf("release wrote to an existing empty directory: %v", entries)
		}
		if name == "previous" {
			manifest, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
			if err != nil || string(manifest) != "previous manifest\n" || len(entries) != 1 {
				t.Errorf("release modified the previous output: %v, %q, %v", entries, manifest, err)
			}
		}
	}
}

func fakeGoBuild(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake Go build uses a POSIX shell")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SOURCE_DATE_EPOCH", "1700000001")
	t.Chdir("../..")
	script := `#!/bin/sh
set -eu
while [ "$1" != -o ]; do
	shift
done
out=$2
` + body
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}
