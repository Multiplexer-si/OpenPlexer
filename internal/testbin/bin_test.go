package testbin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openplexer"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, value, want string
		apps              []string
		wantError         bool
	}{
		{"unset", "", "", []string{"openplexer"}, false},
		{"prebuilt", dir, dir, []string{"openplexer"}, false},
		{"missing binary", dir, "", []string{"openplexer", "openplexer-hub"}, true},
		{"directory in place of binary", dir, "", []string{"."}, true},
		{"relative", "bin", "", []string{"openplexer"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENPLEXER_BIN_DIR", tc.value)
			if got, err := Dir(tc.apps...); got != tc.want || (err != nil) != tc.wantError {
				t.Errorf("Dir = %q, %v; want %q, error %t", got, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestDirRejectsBinarySymlinkOutsideSelection(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "selected")
	sibling := dir + "-outside"
	for _, path := range []string{dir, sibling} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(sibling, "openplexer-verify")
	if err := os.WriteFile(binary, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(dir, "openplexer-verify")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENPLEXER_BIN_DIR", dir)
	if _, err := Dir("openplexer-verify"); err == nil || !strings.Contains(err.Error(), "outside") ||
		!strings.Contains(err.Error(), "openplexer-verify") {
		t.Fatalf("Dir: got %v, want an error identifying the verifier outside the selected directory", err)
	}
}

func TestDirAcceptsBinarySymlinkInsideSelection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "verifier"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("verifier", filepath.Join(dir, "openplexer-verify")); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(dir, selected); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENPLEXER_BIN_DIR", selected)
	if got, err := Dir("openplexer-verify"); err != nil || got != selected {
		t.Fatalf("Dir = %q, %v; want %q", got, err, selected)
	}
}
