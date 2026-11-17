package policy

import (
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
