package room

import (
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

func isGlob(pattern string) bool { return strings.ContainsAny(pattern, "*?[") }

func literalPrefix(pattern string) string {
	if i := strings.IndexAny(pattern, "*?["); i >= 0 {
		return pattern[:i]
	}
	return pattern
}

// PatternsOverlap reports whether two claim entries overlap, as
// spec/room/README.md says. Two globs report more overlaps than exist, because
// a claim is advice and not a lock. A glob that the reference cannot read, or
// that is too large to match, overlaps every entry, and so does an entry of
// more than the policy path limit in UTF-16 units.
func PatternsOverlap(a, b string) bool {
	if tooLong(a) || tooLong(b) {
		return true
	}
	a, b = policy.NormalizeGlob(a), policy.NormalizeGlob(b)
	switch globA, globB := isGlob(a), isGlob(b); {
	case !globA && !globB:
		return a == b
	case globA && !globB:
		return matchesOrFails(a, b)
	case !globA && globB:
		return matchesOrFails(b, a)
	}
	prefixA, prefixB := literalPrefix(a), literalPrefix(b)
	return matchesOrFails(a, prefixB) || matchesOrFails(b, prefixA) ||
		strings.HasPrefix(prefixA, prefixB) || strings.HasPrefix(prefixB, prefixA)
}

// PathsOverlap reports whether an entry of a overlaps an entry of b. A list
// with no entry overlaps nothing.
func PathsOverlap(a, b []string) bool {
	return slices.ContainsFunc(a, func(x string) bool {
		return slices.ContainsFunc(b, func(y string) bool { return PatternsOverlap(x, y) })
	})
}

func tooLong(entry string) bool {
	return len(entry) > policy.MaxGlobPath && len(utf16.Encode([]rune(entry))) > policy.MaxGlobPath
}

func matchesOrFails(glob, path string) bool {
	matches, err := policy.MatchClaimGlob(glob, path)
	return err != nil || matches
}
