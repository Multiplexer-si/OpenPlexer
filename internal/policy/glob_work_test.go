package policy

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestGlobWorkBoundsMatcherOperations(t *testing.T) {
	var scratch globScratch
	check := func(glob, path string, fold bool) {
		t.Helper()
		budget := NewBudget()
		count := 0
		_, err := scratch.matches(NormalizeGlob(glob), utf16.Encode([]rune(path)), fold, budget, &count)
		if err != nil {
			if count != 0 {
				t.Fatalf("%q: failed after %d matcher operations: %v", glob, count, err)
			}
			return
		}
		if charged := maxWork - budget.left; count == 0 || charged < count {
			t.Fatalf("%q: charged %d work units for %d matcher operations", glob, charged, count)
		}
	}
	checkQuery := func(w globWorkload) {
		path, ok := canonicalPath(w.query.Path)
		if !ok {
			return
		}
		for _, area := range w.bundle.Areas {
			for _, pattern := range area.Patterns {
				repo, glob, hasRepo := strings.Cut(pattern, ":")
				if !hasRepo {
					repo, glob = "*", pattern
				}
				if repo == "*" || lowercase(repo) == lowercase(w.query.Repo) {
					check(glob, path, true)
				}
			}
		}
	}
	ignore, fixtures := ordinaryWorkloads(t)
	for _, w := range append(ignore, fixtures...) {
		checkQuery(w)
	}
	bundlePath := filepath.Join(specDir, "fixtures", "evaluate-class-syntax.json")
	fixture := readFixture(t, bundlePath)
	bundle, err := ParseBundle(fixture.Shared.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		q, err := ParseQuery(c.Input)
		if err != nil {
			t.Fatal(err)
		}
		checkQuery(globWorkload{bundle, q})
	}
	for _, c := range slowGlobs {
		check(c.glob, c.path, true)
		check(c.glob, c.path, false)
	}
	for _, glob := range []string{
		slowestGlob,
		strings.Repeat("**/", 400) + "c",
		"[]|" + strings.Repeat("aZ|", 425) + "aZ]",
		"[]|(?:(?<=a)a|a?){0,200}]",
		"[]|(?:(?=(?<!z)a)){10000000}a*]",
		"[]|[" + strings.Repeat("x", 2989) + "]",
		"[]" + strings.Repeat("a", 100) + "[]|[!x-z]",
	} {
		for _, path := range []string{"", "AZ]", strings.Repeat("a", MaxGlobPath-1) + "]", strings.Repeat("Ι", MaxGlobPath)} {
			check(glob, path, true)
			check(glob, path, false)
		}
	}
}

func TestClassWorkIncludesEverySpanAndCaseVariant(t *testing.T) {
	const spans = 2989
	var form uint16
	variants := caseGroups().largest
	for unit, group := range caseGroups().byForm {
		if len(group) == variants {
			form = unit
			break
		}
	}
	glob := "[]|[" + strings.Repeat("0", spans) + "]"
	path := make([]uint16, MaxGlobPath)
	for i := range path {
		path[i] = form
	}
	var scratch globScratch
	budget := NewBudget()
	count := 0
	matched, err := scratch.matches(glob, path, true, budget, &count)
	if err != nil || matched || count < len(path)*spans*variants {
		t.Fatalf("matched %t with error %v after %d operations, want a full class scan", matched, err, count)
	}
	charged := maxWork - budget.left
	if charged < count {
		t.Fatalf("charged %d units for %d operations", charged, count)
	}
	budget.left, count = charged-1, 0
	_, err = scratch.matches(glob, path, true, budget, &count)
	if !errors.Is(err, errOverBudget) || count != 0 || budget.left != 0 {
		t.Fatalf("one unit short: err %v, ran %d operations with %d units left", err, count, budget.left)
	}
}
