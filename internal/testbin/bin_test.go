package testbin

import (
	"os"
	"path/filepath"
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
