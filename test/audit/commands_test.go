package audit_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/testbin"
)

const root = "../.."

func buildBinaries(t *testing.T, apps ...string) string {
	t.Helper()
	bin, err := testbin.Dir(apps...)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "" {
		return bin
	}
	bin = t.TempDir()
	args := []string{"build", "-o", bin + string(os.PathSeparator)}
	for _, app := range apps {
		args = append(args, "./apps/"+app)
	}
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, output)
	}
	return bin
}

func run(t *testing.T, dir, home, bin, program string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), program, args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + dir}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	status, err := testbin.Run(cmd)
	if err != nil {
		t.Fatalf("run %s: %v", program, err)
	}
	return status, stdout.String(), stderr.String()
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decode(t *testing.T, data []byte, value any) {
	t.Helper()
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
