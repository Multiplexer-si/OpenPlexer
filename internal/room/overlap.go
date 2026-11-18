package room

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf16"
)

// The claims glob dialect of spec/room/README.md. It is the policy glob dialect
// with two differences: letters compare with case, and dir/** does not match
// dir. A glob and a path are matched as UTF-16 code units, because the
// contract counts one character as one code unit.
//
// The reference turns a glob into an ECMA-262 regular expression, and it
// copies the text of a class into that expression. So in a class whose first
// character is `]`, the rest of the class is regular expression syntax.
//
// The matcher is a copy of the matcher of internal/policy in pull request 55,
// without the comparison that ignores case.

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
// a claim is advice and not a lock. A glob that the reference cannot read, or
// that is too large to match, overlaps every entry.
func PatternsOverlap(a, b string) bool {
	a, b = normalize(a), normalize(b)
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

// The reference throws where matchesOrFails finds an error, and a claim must
// fail closed there.
func matchesOrFails(glob, path string) bool {
	compiled, err := compileGlob(glob)
	if err != nil {
		return true
	}
	matches, err := globMatches(compiled, utf16.Encode([]rune(path)))
	return err != nil || matches
}

type node interface {
	emit(b *builder, code []instr) ([]instr, error)
	minUnits() int
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

type op uint8

const (
	opUnit op = iota
	opRun
	opFork
	opJump
	opTest
	opStart
	opEnd
	opMatch
)

type instr struct {
	op      op
	offset  int32
	holdsAt []bool
}

const (
	maxCost     = 3000
	longestPath = 4096
)

var errTooLarge = fmt.Errorf("the glob is too large: its length plus the states of its automaton is more than %d", maxCost)

type builder struct {
	path     []uint16
	n        int
	limit    int
	backward bool
}

func (b *builder) add(code []instr, more ...instr) ([]instr, error) {
	if b.limit -= len(more); b.limit < 0 {
		return nil, errTooLarge
	}
	return append(code, more...), nil
}

func (holds one) emit(b *builder, code []instr) ([]instr, error) {
	unit := instr{op: opUnit}
	if b.path != nil {
		unit.holdsAt = make([]bool, len(b.path))
		for at := range b.path {
			unit.holdsAt[at] = holds(b.path[at])
		}
	}
	return b.add(code, unit)
}

func (s sequence) emit(b *builder, code []instr) ([]instr, error) {
	for i := range s {
		term := s[i]
		if b.backward {
			term = s[len(s)-1-i]
		}
		var err error
		if code, err = term.emit(b, code); err != nil {
			return nil, err
		}
	}
	return code, nil
}

func (c anyOf) emit(b *builder, code []instr) ([]instr, error) {
	var jumps []int
	for i, branch := range c {
		fork := len(code)
		var err error
		if i < len(c)-1 {
			if code, err = b.add(code, instr{op: opFork}); err != nil {
				return nil, err
			}
		}
		if code, err = branch.emit(b, code); err != nil {
			return nil, err
		}
		if i < len(c)-1 {
			jumps = append(jumps, len(code))
			if code, err = b.add(code, instr{op: opJump}); err != nil {
				return nil, err
			}
			code[fork].offset = int32(len(code) - fork)
		}
	}
	for _, at := range jumps {
		code[at].offset = int32(len(code) - at)
	}
	return code, nil
}

// A large count of a body that reads a unit and can also match empty is
// refused, and it must be: the reference fails on it when it runs out of
// stack.
func (r repeat) emit(b *builder, code []instr) ([]instr, error) {
	low, high, least := r.low, r.high, r.of.minUnits()
	if high == 0 {
		return code, nil
	}
	body, err := r.of.emit(b, nil)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(body, consumes) {
		if low == 0 {
			return code, nil
		}
		return append(code, body...), nil
	}
	b.limit += len(body)
	most := b.n + 1
	if least > 0 {
		if most = b.n / least; low > most {
			return one(never).emit(b, code)
		}
	}
	if high > most {
		high = unbounded
	}
	for range low {
		if code, err = b.add(code, body...); err != nil {
			return nil, err
		}
	}
	if high == unbounded {
		return b.star(code, body)
	}
	for left := high - low; left > 0; left-- {
		skip := instr{op: opFork, offset: int32(left * (len(body) + 1))}
		if code, err = b.add(code, append([]instr{skip}, body...)...); err != nil {
			return nil, err
		}
	}
	return code, nil
}

func (b *builder) star(code, body []instr) ([]instr, error) {
	if len(body) == 1 && body[0].op == opUnit {
		return b.add(code, instr{op: opRun, holdsAt: body[0].holdsAt})
	}
	loop := append([]instr{{op: opFork, offset: int32(len(body) + 2)}}, body...)
	return b.add(code, append(loop, instr{op: opJump, offset: -int32(len(body) + 1)})...)
}

func (l look) emit(b *builder, code []instr) ([]instr, error) {
	outer := b.backward
	b.backward = !l.behind
	body, err := l.of.emit(b, nil)
	b.backward = outer
	if err != nil {
		return nil, err
	}
	test := instr{op: opTest}
	if b.path != nil {
		test.holdsAt = run(append(body, instr{op: opMatch}), len(b.path), !l.behind)
		if l.negated {
			for at, holds := range test.holdsAt {
				test.holdsAt[at] = !holds
			}
		}
	}
	return b.add(code, test)
}

func (e edge) emit(b *builder, code []instr) ([]instr, error) {
	if e == pathStart {
		return b.add(code, instr{op: opStart})
	}
	return b.add(code, instr{op: opEnd})
}

func (one) minUnits() int { return 1 }

func (s sequence) minUnits() int {
	sum := 0
	for _, term := range s {
		sum = min(sum+term.minUnits(), math.MaxInt32)
	}
	return sum
}

func (c anyOf) minUnits() int {
	fewest := math.MaxInt32
	for _, branch := range c {
		fewest = min(fewest, branch.minUnits())
	}
	return fewest
}

func (r repeat) minUnits() int { return int(min(int64(r.low)*int64(r.of.minUnits()), math.MaxInt32)) }

func (look) minUnits() int { return 0 }

func (edge) minUnits() int { return 0 }

func never(uint16) bool { return false }

func consumes(in instr) bool { return in.op == opUnit || in.op == opRun }

func run(code []instr, n int, backward bool) []bool {
	found := make([]bool, n+1)
	now, next := live{seen: make([]uint32, len(code)), stamp: 1}, live{seen: make([]uint32, len(code)), stamp: 1}
	var stack []int32
	for step := 0; step <= n; step++ {
		at, to := step, step+1
		if backward {
			at, to = n-step, n-step-1
		}
		stack = now.add(code, 0, at, n, stack)
		next.clear()
		unit := min(at, to)
		for _, pc := range now.states {
			in := &code[pc]
			if in.op == opMatch {
				found[at] = true
				continue
			}
			if step == n || !in.holdsAt[unit] {
				continue
			}
			if in.op == opUnit {
				pc++
			}
			if next.seen[pc] != next.stamp {
				stack = next.add(code, pc, to, n, stack)
			}
		}
		now, next = next, now
	}
	return found
}

type live struct {
	seen   []uint32
	stamp  uint32
	states []int32
}

func (s *live) clear() {
	s.stamp++
	s.states = s.states[:0]
}

func (s *live) add(code []instr, pc int32, at, n int, stack []int32) []int32 {
	stack = stack[:0]
	for {
		if s.seen[pc] != s.stamp {
			s.seen[pc] = s.stamp
			in := &code[pc]
			switch in.op {
			case opFork:
				stack = append(stack, pc+in.offset)
				pc++
				continue
			case opJump:
				pc += in.offset
				continue
			case opTest, opStart, opEnd:
				if in.op == opTest && in.holdsAt[at] || in.op == opStart && at == 0 || in.op == opEnd && at == n {
					pc++
					continue
				}
			case opRun:
				s.states = append(s.states, pc)
				pc++
				continue
			default:
				s.states = append(s.states, pc)
			}
		}
		if len(stack) == 0 {
			return stack
		}
		pc, stack = stack[len(stack)-1], stack[:len(stack)-1]
	}
}

func literal(want uint16) one {
	return func(unit uint16) bool { return unit == want }
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
	inSpans := slices.ContainsFunc(c.spans, func(s span) bool { return s.low <= unit && unit <= s.high })
	return c.negated != inSpans
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
	if err != nil {
		return nil, err
	}
	b := builder{n: longestPath, limit: maxCost - len(units)}
	if _, err := root.emit(&b, nil); err != nil {
		return nil, err
	}
	return root, nil
}

func globMatches(glob node, path []uint16) (bool, error) {
	b := builder{path: path, n: len(path), limit: math.MaxInt}
	if len(path) > longestPath {
		b.limit = maxCost
	}
	code, err := glob.emit(&b, nil)
	if err != nil {
		return false, err
	}
	return slices.Contains(run(append(code, instr{op: opMatch}), len(path), false), true), nil
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

// syntax reads parts as a regular expression of ECMA-262 without the i flag
// and without the u flag, as Annex B extends it.
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
