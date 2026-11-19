package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run([]string{"--version"}, &stdout, &stderr); status != 0 || stdout.String() != "dev\n" || stderr.Len() != 0 {
		t.Fatalf("--version: exit %d, stdout %q, stderr %q", status, &stdout, &stderr)
	}
}

func TestRunUsageAndReadErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	export, jwks := write("export.json"), write("jwks.json")
	missing := filepath.Join(dir, "missing")
	for _, tc := range []struct {
		name string
		args []string
		exit int
		want string
	}{
		{"help", []string{"--help"}, 0, "Usage: openplexer-verify"},
		{"missing args", nil, 2, "Usage: openplexer-verify"},
		{"unknown flag", []string{"--unknown"}, 2, "flag provided but not defined"},
		{"extra args", []string{"--export", export, "--jwks", jwks, "extra"}, 2, "Usage: openplexer-verify"},
		{"export read error", []string{"--export", missing, "--jwks", jwks}, 2, "read export"},
		{"key set read error", []string{"--export", export, "--jwks", missing}, 2, "read JWKS"},
		{"checkpoint read error", []string{"--export", export, "--jwks", jwks, "--checkpoint", missing}, 2, "read checkpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.args, &stdout, &stderr); got != tc.exit || stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit %d and %q on stderr", got, &stdout, &stderr, tc.exit, tc.want)
			}
		})
	}
}

func TestRunVerifiesCheckpointVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/audit/v1/fixtures/checkpoint.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Shared struct{ JWKS json.RawMessage }
		Cases  []struct {
			Name  string
			Input struct {
				Export      json.RawMessage
				Checkpoints []string
				JWKSJSON    *string `json:"jwks_json"`
			}
			Expected json.RawMessage
		}
	}
	decodeJSON(t, raw, &vectors)
	for _, vector := range vectors.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, data []byte) string {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			jwks := vectors.Shared.JWKS
			if vector.Input.JWKSJSON != nil {
				jwks = json.RawMessage(*vector.Input.JWKSJSON)
			}
			args := []string{"--export", write("export.json", vector.Input.Export), "--jwks", write("jwks.json", jwks)}
			for i, token := range vector.Input.Checkpoints {
				args = append(args, "--checkpoint", write(fmt.Sprintf("checkpoint-%d.jws", i+1), []byte(token)))
			}
			var want struct {
				OK           bool
				Rule         string
				RecordNumber int     `json:"record_number"`
				BrokenAt     *string `json:"broken_at"`
				Checkpoint   int
			}
			decodeJSON(t, vector.Expected, &want)
			exit := 0
			if !want.OK {
				exit = 1
			}
			var stdout, stderr bytes.Buffer
			if got := run(args, &stdout, &stderr); got != exit {
				t.Fatalf("exit %d, want %d; stdout %q, stderr %q", got, exit, &stdout, &stderr)
			}
			if want.OK {
				hint := "Completeness is proven through checkpoint count"
				if len(vector.Input.Checkpoints) == 0 {
					hint = "Completeness is not proven"
				}
				if !strings.Contains(stdout.String(), "Chain is consistent from genesis") || !strings.Contains(stdout.String(), hint) || stderr.Len() != 0 {
					t.Errorf("stdout %q, stderr %q", &stdout, &stderr)
				}
			} else {
				id := "<none>"
				if want.BrokenAt != nil {
					id = *want.BrokenAt
				}
				prefix := fmt.Sprintf("record %s (position %d): rule %s: ", id, want.RecordNumber, want.Rule)
				if want.Checkpoint > 0 {
					prefix += fmt.Sprintf("checkpoint %d: ", want.Checkpoint)
				}
				if !strings.HasPrefix(stderr.String(), prefix) || strings.Count(stderr.String(), "\n") != 1 || stdout.Len() != 0 {
					t.Errorf("want one diagnostic beginning %q; stdout %q, stderr %q", prefix, &stdout, &stderr)
				}
			}
			stdout.Reset()
			stderr.Reset()
			if got := run(append(slices.Clone(args), "--json"), &stdout, &stderr); got != exit || stderr.Len() != 0 {
				t.Fatalf("--json exit %d, want %d; stdout %q, stderr %q", got, exit, &stdout, &stderr)
			}
			var gotJSON, wantJSON any
			decodeJSON(t, stdout.Bytes(), &gotJSON)
			decodeJSON(t, vector.Expected, &wantJSON)
			if !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Errorf("--json result %s, want %s", &stdout, vector.Expected)
			}
		})
	}
}

func decodeJSON(t *testing.T, data []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatal(err)
	}
}
