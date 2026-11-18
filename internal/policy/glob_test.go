package policy

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateDeniesAGroupThatItDoesNotRead(t *testing.T) {
	globs := map[string]string{
		"a name outside ASCII":  "[]|(?<é>a)]",
		"a group with flags":    "[]|(?i:a)]",
		"a group with only (?":  "[]|(?)]",
		"more than 1000 groups": "[]|" + strings.Repeat("()", 1001) + "]",
	}
	now, _ := ParseTimestamp("2026-01-15T12:00:00Z")
	for name, glob := range globs {
		t.Run(name, func(t *testing.T) {
			bundle := &Bundle{Areas: []Area{{Name: "private", Patterns: []string{"api:" + glob}, Private: true}}}
			result := Evaluate(bundle, Query{Repo: "api", Path: "src/x", Action: Read, Member: "m", Now: now})
			if result.Decision != Deny || result.Area != nil || !strings.Contains(result.Rule, "is not a glob") {
				t.Fatalf("result = %+v, want deny with no area, because the pattern is not a glob", result)
			}
		})
	}
}

// A Codex hook that runs past its timeout lets the call run, so a slow match
// fails open. The budget is far below that timeout.
const matchBudget = 50 * time.Millisecond

var slowGlobs = []struct{ name, glob, path string }{
	{"a lookbehind", "[]|(?<=b)]", strings.Repeat("ab/", 1333) + "c]"},
	{"a negative lookbehind", "[]|(?<!x)c]", strings.Repeat("ab/", 1333) + "c]"},
	{"twelve lookbehinds, each inside the one before", "[]|" + strings.Repeat("(?<=", 12) + "a" + strings.Repeat(")", 12) + "]", strings.Repeat("a", 4095) + "]"},
	{"sixteen lookaheads, each inside the one before", "[]|" + strings.Repeat("(?=", 16) + "a" + strings.Repeat(")", 16) + "a*]", strings.Repeat("a", 4095) + "]"},
	{"a lookbehind inside a repeated group", "[]|(?:(?<=a)b|(?=a)a|/)*c]", strings.Repeat("ab/", 1333) + "c]"},
	{"a repeated group inside a repeated group", "[]|^(?:(?:x?)*$|x)*y]", strings.Repeat("x", 4096)},
}

func evaluateSlowGlob(glob, path string) Result {
	now, _ := ParseTimestamp("2026-01-15T12:00:00Z")
	bundle := &Bundle{Areas: []Area{{Name: "private", Patterns: []string{"api:" + glob}, Private: true}}}
	return Evaluate(bundle, Query{Repo: "api", Path: path, Action: Read, Member: "m", Now: now})
}

func TestEvaluateAnswersWithinTheBudget(t *testing.T) {
	for _, c := range slowGlobs {
		t.Run(c.name, func(t *testing.T) {
			var took time.Duration
			for range 3 {
				done := make(chan Result, 1)
				start := time.Now()
				go func() { done <- evaluateSlowGlob(c.glob, c.path) }()
				select {
				case result := <-done:
					if strings.Contains(result.Rule, "is not a glob") {
						t.Fatalf("result = %+v, want a result of the glob", result)
					}
				case <-time.After(20 * matchBudget):
					t.Fatalf("no result after %v, want one within %v", 20*matchBudget, matchBudget)
				}
				if took = time.Since(start); took <= matchBudget {
					return
				}
			}
			t.Fatalf("the result took %v, want at most %v", took, matchBudget)
		})
	}
}

func BenchmarkEvaluate(b *testing.B) {
	globs := append(slowGlobs, []struct{ name, glob, path string }{
		{"the slowest glob found within the limit", "[]|(?<=(?:.*){2900})]", strings.Repeat("a", 4095) + "]"},
		{"a glob of a source tree", "src/**/*.ts", "src/" + strings.Repeat("dir/", 20) + "file.tsx"},
	}...)
	for _, c := range globs {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				evaluateSlowGlob(c.glob, c.path)
			}
		})
	}
}
