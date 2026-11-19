package policy

import (
	"slices"
	"strings"
	"testing"
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

func TestEvaluateDeniesAGlobThatIsTooLarge(t *testing.T) {
	globs := map[string]string{
		"a glob of 2048 stars":                                "api:" + strings.Repeat("*a", 2048),
		"a large count of a group that can match empty":       "api:[]|(?:a?){4000000000}]",
		"a count inside a count":                              "api:[]|(?:(?:x?){2000}$|x){2000}y]",
		"a count of 1000 of a group of five states":           "api:[]|(?:ab|c){1000}]",
		"a large count inside a count that matches no path":   "api:[]|(?:(?<!(|a){4000000000})a){2147483647,}]",
		"a large count inside a lookahead":                    "api:[]|(?=(?:a*){10000000})]",
		"more lookarounds than the limit, each in the former": "api:[]|" + strings.Repeat("(?<=", 900) + "a" + strings.Repeat(")", 900) + "]",
	}
	for name, glob := range globs {
		t.Run(name, func(t *testing.T) {
			bundle := &Bundle{Areas: []Area{{Name: "private", Patterns: []string{glob}, Private: true}, {Name: "open", Patterns: []string{"**"}}}}
			requireTooLarge(t, bundle, "src/x")
		})
	}
}

// The slowest glob found within the size limit.
const slowestGlob = "[]|(?<=(?:.*){2972})]"

func TestEvaluateDeniesAPathLongerThanTheLimit(t *testing.T) {
	now, _ := ParseTimestamp("2026-01-15T12:00:00Z")
	bundles := map[string]*Bundle{
		"no area":          {},
		"the slowest glob": {Areas: []Area{{Name: "private", Patterns: []string{"api:" + slowestGlob}, Private: true}}},
	}
	for name, bundle := range bundles {
		for _, length := range []int{MaxGlobPath + 1, 1 << 20} {
			for _, action := range []Action{Read, Write} {
				result := Evaluate(bundle, Query{Repo: "api", Path: strings.Repeat("a", length-1) + "]", Action: action, Member: "m", Now: now})
				if result.Decision != Deny || result.Area != nil || !strings.Contains(result.Rule, "path is longer than") {
					t.Fatalf("%s, %s of %d characters: result = %+v, want deny with no area, because the path is too long", name, action, length, result)
				}
			}
		}
	}
	at := Evaluate(bundles["the slowest glob"], Query{Repo: "api", Path: strings.Repeat("a", MaxGlobPath-1) + "]", Action: Read, Member: "m", Now: now})
	if at.Decision != Deny || at.Area == nil || *at.Area != "private" {
		t.Fatalf("a path at the limit: result = %+v, want deny in the area private", at)
	}
}

func TestEvaluateDeniesAQueryOverItsBudget(t *testing.T) {
	now, _ := ParseTimestamp("2026-01-15T12:00:00Z")
	bundle := &Bundle{Areas: []Area{{Name: "private", Patterns: slices.Repeat([]string{"api:" + slowestGlob}, 100), Private: true}}}
	result := Evaluate(bundle, Query{Repo: "api", Path: strings.Repeat("a", MaxGlobPath-1) + "]", Action: Write, Member: "m", Now: now})
	if result.Decision != Deny || result.Area != nil || result.Rule != errOverBudget.Error() {
		t.Fatalf("result = %+v, want deny with no area, because the query is over its budget", result)
	}
}

func requireTooLarge(t *testing.T, bundle *Bundle, path string) {
	t.Helper()
	now, _ := ParseTimestamp("2026-01-15T12:00:00Z")
	for _, action := range []Action{Read, Write} {
		result := Evaluate(bundle, Query{Repo: "api", Path: path, Action: action, Member: "m", Now: now})
		if result.Decision != Deny || result.Area != nil || !strings.Contains(result.Rule, "is not a glob: the glob is too large") {
			t.Fatalf("%s: result = %+v, want deny with no area, because the glob is too large", action, result)
		}
	}
}

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
			result := evaluateSlowGlob(c.glob, c.path)
			if strings.Contains(result.Rule, "is not a glob") || result.Rule == errOverBudget.Error() {
				t.Fatalf("result = %+v, want a result of the glob", result)
			}
		})
	}
}

func BenchmarkEvaluate(b *testing.B) {
	globs := append(slowGlobs, []struct{ name, glob, path string }{
		{"the slowest glob found within the limit", slowestGlob, strings.Repeat("a", 4095) + "]"},
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
