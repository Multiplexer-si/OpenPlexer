package layering

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShippedModulesHaveNotices(t *testing.T) {
	const filename = "THIRD_PARTY_NOTICES.md"
	notices, err := os.ReadFile(filepath.Join(moduleRoot, filename))
	if err != nil {
		t.Fatal(err)
	}
	_, shipped, found := strings.Cut(string(notices), "\n### External Go modules\n")
	if !found {
		t.Fatalf("%s is missing the External Go modules section", filename)
	}
	shipped, _, _ = strings.Cut(shipped, "\n#")
	listed := map[string]bool{}
	// Use a bullet with the module path in backticks for each external module.
	for line := range strings.SplitSeq(shipped, "\n") {
		if entry, ok := strings.CutPrefix(line, "- `"); ok {
			path, _, closed := strings.Cut(entry, "`")
			if closed {
				listed[path] = true
			}
		}
	}

	// The dependency graph is read in a subprocess, so run with -count=1 as make check does.
	const moduleName = "{{if not .Standard}}{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}{{end}}"
	cmd := exec.CommandContext(t.Context(), "go", "list", "-buildvcs=false", "-deps", "-f", moduleName, "./apps/...")
	cmd.Dir = moduleRoot
	output, err := cmd.Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list: %v\n%s", err, failure.Stderr)
		}
		t.Fatal(err)
	}
	for _, path := range strings.Fields(string(output)) {
		if !listed[path] {
			t.Errorf("add %s and its license to the External Go modules section of %s", path, filename)
			listed[path] = true
		}
	}
}
