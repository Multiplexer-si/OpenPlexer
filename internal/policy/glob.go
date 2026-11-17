package policy

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf16"
)

// The policy glob dialect of spec/policy/README.md. A glob and a path are
// matched as UTF-16 code units, because the contract counts one character
// as one code unit.
//
// The reference turns a glob into an ECMA-262 regular expression, and it
// copies the text of a class into that expression. So in a class whose first
// character is `]`, the rest of the class is regular expression syntax.

type node interface {
	ends(path []uint16, from []bool) []bool
}

type (
	one      func(unit uint16) bool
	sequence []node
	anyOf    []node
	repeat   struct {
		of        node
		low, high int
	}
	look struct {
		of              node
		behind, negated bool
	}
	edge int
)

const (
	pathStart edge = iota
	pathEnd
)

const unbounded = -1

func (holds one) ends(path []uint16, from []bool) []bool {
	to := make([]bool, len(from))
	for i, unit := range path {
		to[i+1] = from[i] && holds(unit)
	}
	return to
}

func (holds one) run(path []uint16, from []bool) []bool {
	to := make([]bool, len(from))
	inRun := false
	for i := range from {
		inRun = inRun || from[i]
		to[i] = inRun
		if i < len(path) && !holds(path[i]) {
			inRun = false
		}
	}
	return to
}

func (s sequence) ends(path []uint16, from []bool) []bool {
	for _, n := range s {
		from = n.ends(path, from)
	}
	return from
}

func (c anyOf) ends(path []uint16, from []bool) []bool {
	to := make([]bool, len(from))
	for _, n := range c {
		merge(to, n.ends(path, from))
	}
	return to
}

func merge(into, more []bool) (grew bool) {
	for i, reached := range more {
		if reached && !into[i] {
			into[i], grew = true, true
		}
	}
	return grew
}

func (r repeat) ends(path []uint16, from []bool) []bool {
	if holds, ok := r.of.(one); ok && r.low == 0 && r.high == unbounded {
		return holds.run(path, from)
	}
	current := from
	for round := 0; round < r.low; round++ {
		next := r.of.ends(path, current)
		if slices.Equal(next, current) {
			break
		}
		current = next
	}
	reached := slices.Clone(current)
	for round := r.low; r.high == unbounded || round < r.high; round++ {
		current = r.of.ends(path, current)
		if !merge(reached, current) {
			break
		}
	}
	return reached
}

func (l look) ends(path []uint16, from []bool) []bool {
	to := make([]bool, len(from))
	only := make([]bool, len(from))
	for at := range from {
		if !from[at] {
			continue
		}
		found := false
		if l.behind {
			for start := 0; start <= at && !found; start++ {
				only[start] = true
				found = l.of.ends(path, only)[at]
				only[start] = false
			}
		} else {
			only[at] = true
			found = slices.Contains(l.of.ends(path, only), true)
			only[at] = false
		}
		to[at] = found != l.negated
	}
	return to
}

func (e edge) ends(path []uint16, from []bool) []bool {
	to := make([]bool, len(from))
	at := 0
	if e == pathEnd {
		at = len(path)
	}
	to[at] = from[at]
	return to
}

func literal(unit uint16) one {
	form := canonical(unit)
	return func(other uint16) bool { return canonical(other) == form }
}

func notSlash(unit uint16) bool { return unit != '/' }

// `**`, and the `.` of a regular expression, match no line terminator of
// ECMA-262.
func notLineTerminator(unit uint16) bool {
	return unit != '\n' && unit != '\r' && unit != 0x2028 && unit != 0x2029
}

func anyNumberOf(holds one) node { return repeat{of: holds, low: 0, high: unbounded} }

type class struct {
	negated bool
	spans   []span
}

type span struct{ low, high uint16 }

func readClass(text []uint16) (class, error) {
	var c class
	if len(text) > 0 && text[0] == '^' {
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
	return c.negated != slices.ContainsFunc(sameLetters(unit), c.spansHold)
}

func (c class) spansHold(unit uint16) bool {
	return slices.ContainsFunc(c.spans, func(s span) bool { return s.low <= unit && unit <= s.high })
}

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

type part struct {
	node node
	text uint16
}

func compileGlob(glob string) (node, error) {
	units := utf16.Encode([]rune(glob))
	parts := []part{{text: '^'}}
	for i := 0; i < len(units); {
		switch unit := units[i]; unit {
		case '*':
			end := i
			for end < len(units) && units[end] == '*' {
				end++
			}
			switch {
			case end-i == 1:
				parts = append(parts, part{node: anyNumberOf(notSlash)})
			case (i == 0 || units[i-1] == '/') && end < len(units) && units[end] == '/':
				directory := sequence{anyNumberOf(notLineTerminator), literal('/')}
				parts = append(parts, part{node: repeat{of: directory, low: 0, high: 1}})
				end++
			default:
				parts = append(parts, part{node: anyNumberOf(notLineTerminator)})
			}
			i = end
		case '?':
			parts = append(parts, part{node: one(notSlash)})
			i++
		case '[':
			// A class has at least one character, so its `]` is not the
			// character right after the `[`.
			length := -1
			if i+2 <= len(units) {
				length = slices.Index(units[i+2:], ']')
			}
			if length == -1 {
				parts = append(parts, part{node: literal('[')})
				i++
				continue
			}
			end := i + 2 + length
			parts = append(parts, part{text: '['})
			for at, member := range units[i+1 : end] {
				if at == 0 && member == '!' {
					member = '^'
				}
				parts = append(parts, part{text: member})
			}
			parts = append(parts, part{text: ']'})
			i = end + 1
		default:
			parts = append(parts, part{node: literal(unit)})
			i++
		}
	}
	parts = append(parts, part{text: '$'})

	s := syntax{parts: parts}
	root, err := s.alternatives()
	if err == nil && s.at < len(parts) {
		err = errGroupNotOpen
	}
	return root, err
}

func globMatches(glob node, path []uint16) bool {
	anywhere := slices.Repeat([]bool{true}, len(path)+1)
	return slices.Contains(glob.ends(path, anywhere), true)
}

var (
	errRangeOutOfOrder = errors.New("a class range has its ends out of order")
	errNothingToRepeat = errors.New("a class that starts with ] holds a quantifier with nothing to repeat")
	errCountOutOfOrder = errors.New("a class that starts with ] holds a count with its bounds out of order")
	errGroupNotClosed  = errors.New("a class that starts with ] holds a group that is not closed")
	errGroupNotOpen    = errors.New("a class that starts with ] holds a ) that closes no group")
	errGroupNotRead    = errors.New("a class that starts with ] holds a group that starts with (? and is not (?:, (?=, (?!, (?<=, (?<!, or a group with a name of ASCII letters, digits, $, and _")
	errNameTwice       = errors.New("a class that starts with ] holds two groups with the same name")
	errTooManyGroups   = fmt.Errorf("a class that starts with ] holds more than %d groups", maxGroups)
)

const maxGroups = 1000

// syntax reads parts as a regular expression of ECMA-262 with the i flag and
// without the u flag, as Annex B extends it.
type syntax struct {
	parts  []part
	at     int
	groups int
	names  []string
}

func (s *syntax) text() (unit uint16, ok bool) {
	if s.at == len(s.parts) || s.parts[s.at].node != nil {
		return 0, false
	}
	return s.parts[s.at].text, true
}

func (s *syntax) take(unit uint16) bool {
	next, ok := s.text()
	if !ok || next != unit {
		return false
	}
	s.at++
	return true
}

func (s *syntax) alternatives() (node, error) {
	var branches anyOf
	for {
		branch, err := s.sequence()
		if err != nil {
			return nil, err
		}
		branches = append(branches, branch)
		if !s.take('|') {
			return branches, nil
		}
	}
}

func (s *syntax) sequence() (node, error) {
	var terms sequence
	for s.at < len(s.parts) {
		if unit, ok := s.text(); ok && (unit == '|' || unit == ')') {
			break
		}
		term, err := s.term()
		if err != nil {
			return nil, err
		}
		terms = append(terms, term)
	}
	return terms, nil
}

func (s *syntax) term() (node, error) {
	if next := s.parts[s.at].node; next != nil {
		s.at++
		return s.quantified(next)
	}
	if _, _, isQuantifier := s.quantifier(); isQuantifier {
		return nil, errNothingToRepeat
	}
	unit := s.parts[s.at].text
	s.at++
	switch unit {
	case '^':
		return pathStart, nil
	case '$':
		return pathEnd, nil
	case '.':
		return s.quantified(one(notLineTerminator))
	case '[':
		var members []uint16
		for {
			member, ok := s.text()
			if !ok || member == ']' {
				break
			}
			members = append(members, member)
			s.at++
		}
		s.take(']')
		c, err := readClass(members)
		if err != nil {
			return nil, err
		}
		return s.quantified(one(c.holds))
	case '(':
		return s.group()
	}
	return s.quantified(literal(unit))
}

func (s *syntax) group() (node, error) {
	if s.groups++; s.groups > maxGroups {
		return nil, errTooManyGroups
	}
	var assertion *look
	if s.take('?') {
		behind := s.take('<')
		switch {
		case s.take('='):
			assertion = &look{behind: behind}
		case s.take('!'):
			assertion = &look{behind: behind, negated: true}
		case behind:
			if err := s.name(); err != nil {
				return nil, err
			}
		case !s.take(':'):
			return nil, errGroupNotRead
		}
	}
	inside, err := s.alternatives()
	if err != nil {
		return nil, err
	}
	if !s.take(')') {
		return nil, errGroupNotClosed
	}
	if assertion == nil {
		return s.quantified(inside)
	}
	assertion.of = inside
	if assertion.behind {
		return *assertion, nil
	}
	return s.quantified(*assertion)
}

func (s *syntax) name() error {
	var name []byte
	for {
		unit, ok := s.text()
		if !ok {
			return errGroupNotRead
		}
		s.at++
		if unit == '>' {
			break
		}
		letter := unit == '$' || unit == '_' || 'a' <= unit && unit <= 'z' || 'A' <= unit && unit <= 'Z'
		if !letter && (len(name) == 0 || unit < '0' || unit > '9') {
			return errGroupNotRead
		}
		name = append(name, byte(unit))
	}
	if len(name) == 0 {
		return errGroupNotRead
	}
	if slices.Contains(s.names, string(name)) {
		return errNameTwice
	}
	s.names = append(s.names, string(name))
	return nil
}

func (s *syntax) quantified(of node) (node, error) {
	low, high, ok := s.quantifier()
	if !ok {
		return of, nil
	}
	if high != unbounded && low > high {
		return nil, errCountOutOfOrder
	}
	s.take('?')
	return repeat{of: of, low: low, high: high}, nil
}

// A `{` that starts no count is a literal character, and quantifier leaves it.
func (s *syntax) quantifier() (low, high int, ok bool) {
	switch {
	case s.take('*'):
		return 0, unbounded, true
	case s.take('+'):
		return 1, unbounded, true
	case s.take('?'):
		return 0, 1, true
	}
	start := s.at
	if s.take('{') {
		low, ok = s.number()
		high = low
		if ok && s.take(',') {
			if high, ok = s.number(); !ok {
				high, ok = unbounded, true
			}
		}
		if ok && s.take('}') {
			return low, high, true
		}
	}
	s.at = start
	return 0, 0, false
}

func (s *syntax) number() (n int, ok bool) {
	for {
		digit, isText := s.text()
		if !isText || digit < '0' || digit > '9' {
			return n, ok
		}
		n = int(min(int64(n)*10+int64(digit-'0'), math.MaxInt32))
		ok = true
		s.at++
	}
}
