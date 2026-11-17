package policy

import (
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"
)

// The policy glob dialect of spec/policy/README.md. A glob and a path are
// matched as UTF-16 code units, because the contract counts one character
// as one code unit.

type tokenKind int

const (
	// literal matches its unit without case.
	literal tokenKind = iota
	// oneOfClass matches one unit of its class.
	oneOfClass
	// oneInSegment is `?`.
	oneInSegment
	// runInSegment is `*`.
	runInSegment
	// runOfDirectories is `**` at the start of a segment and before `/`. It
	// takes the `/` after it.
	runOfDirectories
	// runAcrossSegments is every other run of two or more `*`.
	runAcrossSegments
)

type token struct {
	kind  tokenKind
	unit  uint16
	class class
}

// class matches a unit that one of its spans holds, or that none holds if
// the class is negated. A class with no span and no negation matches
// nothing.
type class struct {
	negated bool
	spans   []span
}

type span struct{ low, high uint16 }

var errRangeOutOfOrder = errors.New("a class range has its ends out of order")

// normalizeGlob makes the separators of a glob those of a canonical path.
func normalizeGlob(glob string) string {
	glob = strings.ReplaceAll(glob, `\`, "/")
	for strings.HasPrefix(glob, "./") {
		glob = glob[2:]
	}
	for strings.Contains(glob, "//") {
		glob = strings.ReplaceAll(glob, "//", "/")
	}
	return glob
}

// compileGlob reads a normalized glob. The only glob that it rejects is one
// with a class range whose ends are out of order, such as [z-a]. The
// reference fails on that glob, and the contract does not define its result.
func compileGlob(glob string) ([]token, error) {
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
			members, err := readClass(units[i+1 : end])
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: oneOfClass, class: members})
			i = end + 1
		default:
			tokens = append(tokens, token{kind: literal, unit: unit})
			i++
		}
	}
	return tokens, nil
}

// readClass reads the text between `[` and `]`. A class whose first
// character is `]` matches nothing, as in the reference.
func readClass(text []uint16) (class, error) {
	if text[0] == ']' {
		return class{}, nil
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
			return class{}, errRangeOutOfOrder
		}
		c.spans = append(c.spans, s)
	}
	return c, nil
}

func (c class) holds(unit uint16) bool {
	return c.negated != (c.spansHold(unit) || c.spansHoldAnotherCaseOf(unit))
}

func (c class) spansHold(unit uint16) bool {
	return slices.ContainsFunc(c.spans, func(s span) bool { return s.low <= unit && unit <= s.high })
}

// A span holds a letter if it holds the same letter in another case. The
// letters that fold together are the candidates, and canonical decides which
// of them the reference counts as the same letter.
func (c class) spansHoldAnotherCaseOf(unit uint16) bool {
	for other := unicode.SimpleFold(rune(unit)); other != rune(unit); other = unicode.SimpleFold(other) {
		if other <= 0xFFFF && canonical(uint16(other)) == canonical(unit) && c.spansHold(uint16(other)) {
			return true
		}
	}
	return false
}

func isSlash(unit uint16) bool { return unit == '/' }

// `**` does not match a line terminator of ECMA-262.
func isLineTerminator(unit uint16) bool {
	return unit == '\n' || unit == '\r' || unit == 0x2028 || unit == 0x2029
}

func (t token) takes(unit uint16) bool {
	switch t.kind {
	case literal:
		return canonical(t.unit) == canonical(unit)
	case oneOfClass:
		return t.class.holds(unit)
	default:
		return !isSlash(unit)
	}
}

// globMatches reports whether the tokens match the whole path. It tracks
// the set of positions in the path that the tokens so far can end at, so the
// time is the number of tokens times the length of the path.
func globMatches(tokens []token, path []uint16) bool {
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

// spread marks every position that a run can reach from a marked position
// without a unit that stops the run.
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
