package policy

import "testing"

func TestEvaluateDeniesWithoutABundle(t *testing.T) {
	now, _ := ParseTimestamp("2026-01-15T12:00:00Z")
	queries := map[string]Query{
		"read":  {Repo: "api", Path: "src/x", Action: Read, Member: "m", Now: now},
		"write": {Repo: "api", Path: "src/x", Action: Write, Member: "m", Now: now},
		"exec":  {Repo: "api", Command: "npm test", Action: Exec, Member: "m", Now: now},
		"push":  {Repo: "api", Action: GitPush, Member: "m", Now: now},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			result := Evaluate(nil, query)
			if result.Decision != Deny || result.Rule == "" || result.Area != nil {
				t.Fatalf("result = %+v, want deny with a rule and no area", result)
			}
		})
	}
}
