package room

import (
	"slices"
	"strings"
	"unicode/utf16"
)

// The claims glob dialect of spec/room/README.md. It is the policy glob dialect
// with two differences: letters compare with case, and dir/** does not match
// dir. A glob and a path are matched as UTF-16 code units, because the
// contract counts one character as one code unit.

type tokenKind int

const (
	literal tokenKind = iota
	oneOfClass
	oneInSegment
	runInSegment
	runOfDirectories
	runAcrossSegments
)

type token struct {
	kind  tokenKind
	unit  uint16
	class class
}

type class struct {
	negated bool
	spans   []span
}

type span struct{ low, high uint16 }

func normalize(pattern string) string {
	pattern = strings.ReplaceAll(pattern, `\`, "/")
	for strings.HasPrefix(pattern, "./") {
		pattern = pattern[2:]
	}
	for strings.Contains(pattern, "//") {
		pattern = strings.ReplaceAll(pattern, "//", "/")
	}
	return pattern
}

func isGlob(pattern string) bool { return strings.ContainsAny(pattern, "*?[") }

func literalPrefix(pattern string) string {
	if i := strings.IndexAny(pattern, "*?["); i >= 0 {
		return pattern[:i]
	}
	return pattern
}

// PatternsOverlap reports whether two claim entries overlap, as
// spec/room/README.md says. Two globs report more overlaps than exist, because
// a claim is advice and not a lock.
func PatternsOverlap(a, b string) bool {
	a, b = normalize(a), normalize(b)
	switch globA, globB := isGlob(a), isGlob(b); {
	case !globA && !globB:
		return a == b
	case globA && !globB:
		return globMatches(a, b)
	case !globA && globB:
		return globMatches(b, a)
	}
	prefixA, prefixB := literalPrefix(a), literalPrefix(b)
	return strings.HasPrefix(prefixA, prefixB) || strings.HasPrefix(prefixB, prefixA)
}

// PathsOverlap reports whether an entry of a overlaps an entry of b. A list
// with no entry overlaps nothing.
func PathsOverlap(a, b []string) bool {
	return slices.ContainsFunc(a, func(x string) bool {
		return slices.ContainsFunc(b, func(y string) bool { return PatternsOverlap(x, y) })
	})
}

// A glob with a class range whose ends are out of order, such as [z-a],
// matches no path.
func globMatches(glob, path string) bool {
	tokens, ok := compileGlob(glob)
	return ok && matchTokens(tokens, utf16.Encode([]rune(path)))
}

func compileGlob(glob string) ([]token, bool) {
	units := utf16.Encode([]rune(glob))
	var tokens []token
	for i := 0; i < len(units); {
		switch unit := units[i]; unit {
		case '*':
			end := i
			for end < len(units) && units[end] == '*' {
				end++
			}
			switch {
			case end-i == 1:
				tokens = append(tokens, token{kind: runInSegment})
			case (i == 0 || units[i-1] == '/') && end < len(units) && units[end] == '/':
				tokens = append(tokens, token{kind: runOfDirectories})
				end++
			default:
				tokens = append(tokens, token{kind: runAcrossSegments})
			}
			i = end
		case '?':
			tokens = append(tokens, token{kind: oneInSegment})
			i++
		case '[':
			// A class has at least one character, so its `]` is not the
			// character right after the `[`.
			length := -1
			if i+2 <= len(units) {
				length = slices.Index(units[i+2:], ']')
			}
			if length == -1 {
				tokens = append(tokens, token{kind: literal, unit: '['})
				i++
				continue
			}
			end := i + 2 + length
			members, ok := readClass(units[i+1 : end])
			if !ok {
				return nil, false
			}
			tokens = append(tokens, token{kind: oneOfClass, class: members})
			i = end + 1
		default:
			tokens = append(tokens, token{kind: literal, unit: unit})
			i++
		}
	}
	return tokens, true
}

// A class whose first character is `]` matches nothing.
func readClass(text []uint16) (class, bool) {
	if text[0] == ']' {
		return class{}, true
	}
	var c class
	if text[0] == '!' || text[0] == '^' {
		c.negated = true
		text = text[1:]
	}
	for i := 0; i < len(text); {
		s := span{low: text[i], high: text[i]}
		if i+2 < len(text) && text[i+1] == '-' {
			s.high = text[i+2]
			i += 2
		}
		i++
		if s.low > s.high {
			return class{}, false
		}
		c.spans = append(c.spans, s)
	}
	return c, true
}

func (c class) holds(unit uint16) bool {
	inSpans := slices.ContainsFunc(c.spans, func(s span) bool { return s.low <= unit && unit <= s.high })
	return c.negated != inSpans
}

func isSlash(unit uint16) bool { return unit == '/' }

// `**` does not match a line terminator of ECMA-262.
func isLineTerminator(unit uint16) bool {
	return unit == '\n' || unit == '\r' || unit == 0x2028 || unit == 0x2029
}

func (t token) takes(unit uint16) bool {
	switch t.kind {
	case literal:
		return t.unit == unit
	case oneOfClass:
		return t.class.holds(unit)
	default:
		return !isSlash(unit)
	}
}

func matchTokens(tokens []token, path []uint16) bool {
	at := make([]bool, len(path)+1)
	next := make([]bool, len(path)+1)
	at[0] = true
	for _, t := range tokens {
		clear(next)
		switch t.kind {
		case literal, oneOfClass, oneInSegment:
			for i, unit := range path {
				next[i+1] = at[i] && t.takes(unit)
			}
		case runInSegment:
			spread(at, next, path, isSlash)
		case runAcrossSegments:
			spread(at, next, path, isLineTerminator)
		case runOfDirectories:
			inRun := false
			for i := range at {
				inRun = inRun || at[i]
				next[i] = next[i] || at[i]
				if i == len(path) {
					break
				}
				if isLineTerminator(path[i]) {
					inRun = false
				} else if isSlash(path[i]) && inRun {
					next[i+1] = true
				}
			}
		}
		at, next = next, at
	}
	return at[len(path)]
}

func spread(at, next []bool, path []uint16, stops func(uint16) bool) {
	inRun := false
	for i := range at {
		inRun = inRun || at[i]
		next[i] = inRun
		if i < len(path) && stops(path[i]) {
			inRun = false
		}
	}
}
