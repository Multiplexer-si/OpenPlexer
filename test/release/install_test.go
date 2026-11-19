package release_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const (
	root    = "../.."
	version = "v0.0.0-rc.1"
)

func TestReproducibleArchiveAndInstallGuide(t *testing.T) {
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("the shell install steps need a supported Linux or macOS host")
	}
	target := runtime.GOOS + "/" + runtime.GOARCH
	name := fmt.Sprintf("openplexer_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	first, second := filepath.Join(t.TempDir(), version), filepath.Join(t.TempDir(), version)
	for _, dir := range []string{first, second} {
		cmd := exec.CommandContext(t.Context(), "go", "run", "./tools/release", "-version", version, "-target", target, "-out", dir)
		cmd.Dir = root
		cmd.Env = append(cmd.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "SOURCE_DATE_EPOCH=1700000001")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build release: %v\n%s", err, output)
		}
	}
	for _, file := range []string{name, "SHA256SUMS"} {
		if !bytes.Equal(read(t, filepath.Join(first, file)), read(t, filepath.Join(second, file))) {
			t.Errorf("%s differs between builds", file)
		}
	}

	guide := string(read(t, root+"/docs/install.md"))
	_, section, ok := strings.Cut(guide, "\n## Linux and macOS\n")
	if !ok {
		t.Fatal("docs/install.md is missing its Linux and macOS section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	blocks := regexp.MustCompile("(?m)^```sh\\n([\\s\\S]*?)^```$").FindAllStringSubmatch(section, -1)
	if len(blocks) == 0 {
		t.Fatal("docs/install.md is missing its shell commands")
	}
	var script strings.Builder
	for _, block := range blocks {
		script.WriteString(block[1])
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-eu", "-c", script.String())
	cmd.Dir = first
	cmd.Env = append(cmd.Environ(), "HOME="+t.TempDir(), "PATH=/usr/bin:/bin", "LC_ALL=C", "OPENPLEXER_HOME=")
	output, err := cmd.CombinedOutput()
	want := name + ": OK\n" + strings.Repeat(version+"\n", 3)
	if err != nil || string(output) != want {
		t.Fatalf("docs/install.md commands: %v\ngot %q\nwant %q", err, output, want)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
