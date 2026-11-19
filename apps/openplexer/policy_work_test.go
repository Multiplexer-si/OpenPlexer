package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

type policyWorkCase struct {
	name, path string
	bundle     json.RawMessage
	overBudget bool
	wantArea   bool
}

func policyWorkCases(t testing.TB) []policyWorkCase {
	t.Helper()
	long := strings.Repeat("a", policy.MaxGlobPath)
	oneRepo := []string{"app"}
	makeBundle := func(repos []string, areas []map[string]any) json.RawMessage {
		t.Helper()
		bundle := map[string]any{
			"project_id": "prj-work", "version": 1, "repos": repos, "areas": areas,
			"roles":   map[string]any{},
			"members": map[string]string{},
		}
		raw, err := json.Marshal(bundle)
		if err != nil || len(raw) > policy.MaxBundleBytes {
			t.Fatalf("policy with %d bytes: %v", len(raw), err)
		}
		return raw
	}
	area := func(patterns []string) []map[string]any {
		return []map[string]any{{"name": "stress", "patterns": patterns}}
	}
	repeated := func(pattern string, n int) []string {
		patterns := make([]string, n)
		for i := range patterns {
			patterns[i] = pattern
		}
		return patterns
	}
	// Use the remaining byte allowance on a pattern for another repository.
	// This measures verification and parsing at the byte cap along with matching.
	full := func(patterns []string) json.RawMessage {
		for i := range patterns {
			patterns[i] = "app:" + patterns[i]
		}
		patterns = append(patterns, "unrelated:")
		base := makeBundle(oneRepo, area(patterns))
		patterns[len(patterns)-1] += strings.Repeat("x", policy.MaxBundleBytes-len(base))
		return makeBundle(oneRepo, area(patterns))
	}
	largeClass := "[" + strings.Repeat("x", 2989) + "]"
	alternation := "[]|" + strings.Repeat("aZ|", 425) + "aZ]"
	lookbehind := "[]|(?<=(?:.*){2972})]"
	// Exercise both applicable and non-applicable patterns across repositories.
	repos := make([]string, 211)
	for i := range repos {
		repos[i] = fmt.Sprintf("repo%03d", i)
	}
	areas := make([]map[string]any, 25_400)
	for i := range areas {
		areas[i] = map[string]any{"name": "many", "patterns": []string{repos[i%len(repos)] + ":*"}}
	}
	scope := strings.Repeat("Σ\u0301", 127) + ":*"
	nested := "[]|" + strings.Repeat("(?:", 200) + strings.Repeat("a", 700) + strings.Repeat(")?", 200) + "]"
	dir := strings.TrimSuffix(scope, ":*")
	return []policyWorkCase{
		{name: "150-classes", path: long, bundle: makeBundle(oneRepo, area(repeated(largeClass, 150))), overBudget: true},
		{name: "300-classes", path: long, bundle: makeBundle(oneRepo, area(repeated(largeClass, 300))), overBudget: true},
		{name: "case-variants", path: strings.Repeat("Ι", policy.MaxGlobPath), bundle: full([]string{"[]|" + largeClass, "[]|[" + strings.Repeat("x", 590) + "]"})},
		{name: "long-alternations", path: long, bundle: full(repeated(alternation, 3))},
		{name: "long-lookbehind", path: long[:len(long)-1] + "]", bundle: full([]string{lookbehind, "[]|(?<=(?:.*){600})]"}), wantArea: true},
		{name: "many-areas-and-repos", path: "a", bundle: makeBundle(repos, areas), wantArea: true},
		{name: "many-short-globs", path: "a", bundle: makeBundle(oneRepo, area(repeated("*", 250_000))), wantArea: true},
		{name: "unicode-repo-scans", path: "a", bundle: makeBundle(repos[:50], area(repeated(scope, 2000)))},
		{name: "unicode-directory-roots", path: dir, bundle: makeBundle(repos[:50], area(repeated(dir+"/**", 2000))), wantArea: true},
		{name: "nested-count-compilation", path: "a", bundle: full(repeated(nested, 60))},
	}
}

// Run with OPENPLEXER_BIN_DIR pointing to a release build. The check includes
// process startup, signature verification, evaluation, and audit recording.
// Keep wall time out of CI assertions because machine load and -race vary.
func BenchmarkPolicyCheckWork(b *testing.B) {
	if prebuiltCLI == "" {
		b.Skip("set OPENPLEXER_BIN_DIR to measure the built CLI")
	}
	for _, c := range policyWorkCases(b) {
		b.Run(c.name, func(b *testing.B) {
			keys, message := signedPolicy(b, c.bundle)
			cli := newCLI(b)
			cli.must(exitOK, "configure", "--keys", keys, "--member", "mem-work")
			cli.must(exitOK, "policy", "load", message)
			exit := exitOK
			if c.overBudget {
				exit = exitDenied
			}
			for b.Loop() {
				out := cli.must(exit, "policy", "check", "read", c.path)
				if c.overBudget != strings.HasPrefix(out, "deny: the evaluation is over its budget") ||
					c.wantArea != strings.HasPrefix(out, "allow: area ") {
					b.Fatalf("unexpected result: %s", out)
				}
			}
			b.ReportMetric(float64(len(c.bundle)), "payload-B")
		})
	}
}
