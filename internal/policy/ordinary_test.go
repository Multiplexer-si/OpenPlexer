package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type globWorkload struct {
	bundle *Bundle
	query  Query
}

func ordinaryWorkloads(t testing.TB) (ignore, fixtures []globWorkload) {
	t.Helper()
	read := func(path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var patterns []string
	for _, file := range []string{"../../.gitignore", "testdata/reference-ignore.txt"} {
		for line := range strings.SplitSeq(string(read(file)), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}
	bundle := &Bundle{Areas: []Area{{Name: "ignore", Patterns: patterns}}}
	for _, path := range []string{"apps/dashboard/.next/manifest.json", ".env", "dist/bin/openplexer", "src/" + strings.Repeat("dir/", 61) + "main.go"} {
		ignore = append(ignore, globWorkload{bundle, Query{Action: Read, Path: path, Repo: "app"}})
	}
	files, err := filepath.Glob(filepath.Join(specDir, "fixtures", "evaluate-*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find evaluation fixtures: %v", err)
	}
	for _, file := range files {
		if filepath.Base(file) == "evaluate-class-syntax.json" {
			continue
		}
		fixture := readFixture(t, file)
		bundle, err := ParseBundle(fixture.Shared.Bundle)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range fixture.Cases {
			query, err := ParseQuery(c.Input)
			if err != nil {
				t.Fatal(err)
			}
			if query.Action == Read || query.Action == Write {
				fixtures = append(fixtures, globWorkload{bundle, query})
			}
		}
	}
	return ignore, fixtures
}

func BenchmarkOrdinaryGlobs(b *testing.B) {
	ignore, fixtures := ordinaryWorkloads(b)
	for _, corpus := range []struct {
		name  string
		cases []globWorkload
	}{{"ignore", ignore}, {"fixtures", fixtures}} {
		b.Run(corpus.name, func(b *testing.B) {
			for b.Loop() {
				for _, c := range corpus.cases {
					Evaluate(c.bundle, c.query)
				}
			}
		})
	}
}
