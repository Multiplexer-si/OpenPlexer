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
	emit(b *builder, code []instr) ([]instr, error)
	minUnits() int
}

type (
	one struct {
		kind  predicateKind
		unit  uint16
		class *class
	}
	sequence []node
	anyOf    []node
	repeat   struct {
		of        node
		low, high int
		least     int
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
	negated bool
	one
}

type predicateKind uint8

const (
	isLiteral predicateKind = iota
	notSlash
	notLineTerminator
	inClass
	never
)

type assertion struct {
	code     []instr
	backward bool
}

const maxCost = 3000

// MaxGlobPath is the longest path a glob can match, in UTF-16 code units.
const MaxGlobPath = 4096

var errPathTooLong = fmt.Errorf("path is longer than %d characters", MaxGlobPath)

var errTooLarge = fmt.Errorf("the glob is too large: its length plus the states of its automaton is more than %d", maxCost)

type builder struct {
	n          int
	limit      int
	backward   bool
	assertions []assertion
	budget     *Budget
}

func (b *builder) add(code []instr, more ...instr) ([]instr, error) {
	if b.limit -= len(more); b.limit < 0 {
		return nil, errTooLarge
	}
	if err := b.budget.spend(len(more)); err != nil {
		return nil, err
	}
	return append(code, more...), nil
}

func (holds one) emit(b *builder, code []instr) ([]instr, error) {
	return b.add(code, instr{op: opUnit, one: holds})
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
	low, high, least := r.low, r.high, r.least
	if high == 0 {
		return code, nil
	}
	body, err := r.of.emit(b, nil)
	if err != nil {
		return nil, err
	}
	// Copies and passes over a temporary body still cost work even if its
	// states are later refunded for the size limit.
	if err := b.budget.spend(2 * len(body)); err != nil {
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
			return one{kind: never}.emit(b, code)
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
		if code, err = b.add(code, skip); err == nil {
			code, err = b.add(code, body...)
		}
		if err != nil {
			return nil, err
		}
	}
	return code, nil
}

func (b *builder) star(code, body []instr) ([]instr, error) {
	if len(body) == 1 && body[0].op == opUnit {
		return b.add(code, instr{op: opRun, one: body[0].one})
	}
	var err error
	if code, err = b.add(code, instr{op: opFork, offset: int32(len(body) + 2)}); err == nil {
		code, err = b.add(code, body...)
	}
	if err != nil {
		return nil, err
	}
	return b.add(code, instr{op: opJump, offset: -int32(len(body) + 1)})
}

func (l look) emit(b *builder, code []instr) ([]instr, error) {
	outer := b.backward
	b.backward = !l.behind
	body, err := l.of.emit(b, nil)
	b.backward = outer
	if err != nil {
		return nil, err
	}
	test := instr{op: opTest, offset: int32(len(b.assertions)), negated: l.negated}
	b.assertions = append(b.assertions, assertion{append(body, instr{op: opMatch}), !l.behind})
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

func (r repeat) minUnits() int { return int(min(int64(r.low)*int64(r.least), math.MaxInt32)) }

func (look) minUnits() int { return 0 }

func (edge) minUnits() int { return 0 }

func consumes(in instr) bool { return in.op == opUnit || in.op == opRun }

// runWork bounds the loops in run, live.add, and one.holds. Each position
// seeds at most once. Every state is added at most once at that position.
// After a state is added, each of its outgoing epsilon edges can check a
// state already seen, and each live state can be visited by the step loop.
func runWork(code []instr, n int, fold bool) int64 {
	work := int64(3) // step, case folding, and a seed that may already be seen
	for _, in := range code {
		switch in.op {
		case opFork:
			work += 3 // add and two epsilon edges
		case opUnit:
			work += int64(2 + in.one.work(fold)) // add, step, predicate
		case opRun:
			work += int64(3 + in.one.work(fold)) // also an epsilon edge
		default:
			work += 2 // add and an epsilon edge or step
		}
	}
	return int64(n+1)*work + int64(2*len(code)) // initial marks for both live sets
}

type machine struct {
	path  []uint16
	tests [][]bool
	fold  bool
	count *int // enabled only by tests
}

// A file query can run many small programs against the same path. Reuse
// their instruction and state buffers, including across lookarounds.
type globScratch struct {
	code   [64]instr
	states []int32
}

func (m machine) run(code []instr, backward bool, found []bool, scratch *globScratch) bool {
	n := len(m.path)
	if m.count != nil {
		*m.count += 2 * len(code)
	}
	size := len(code)
	if len(scratch.states) < 5*size {
		scratch.states = make([]int32, 5*max(64, size))
	}
	states := scratch.states
	clear(states[:2*size])
	now := live{seen: states[:size], states: states[2*size : 2*size : 3*size], stamp: 1}
	next := live{seen: states[size : 2*size], states: states[3*size : 3*size : 4*size], stamp: 1}
	stack := states[4*size : 4*size : 5*size]
	// The main program only needs one match. With an initial anchor it
	// cannot start again after position zero.
	anchored := found == nil && code[0].op == opStart
	for step := 0; step <= n; step++ {
		if m.count != nil {
			*m.count++
		}
		at, to := step, step+1
		if backward {
			at, to = n-step, n-step-1
		}
		if !anchored || step == 0 {
			stack = now.add(code, 0, at, n, m.tests, stack, m.count)
		}
		if anchored && len(now.states) == 0 {
			return false
		}
		next.clear()
		var unit, form uint16
		if step < n {
			unit = m.path[min(at, to)]
			form = unit
			if m.fold {
				if m.count != nil {
					*m.count++
				}
				form = canonical(unit)
			}
		}
		for _, pc := range now.states {
			if m.count != nil {
				*m.count++
			}
			in := &code[pc]
			if in.op == opMatch {
				if found == nil {
					return true
				}
				found[at] = true
				continue
			}
			if step == n || !in.one.holds(unit, form, m.fold, m.count) {
				continue
			}
			if in.op == opUnit {
				pc++
			}
			if next.seen[pc] != next.stamp {
				stack = next.add(code, pc, to, n, m.tests, stack, m.count)
			}
		}
		now, next = next, now
	}
	return false
}

type live struct {
	seen   []int32
	stamp  int32
	states []int32
}

func (s *live) clear() {
	s.stamp++
	s.states = s.states[:0]
}

func (s *live) add(code []instr, pc int32, at, n int, tests [][]bool, stack []int32, count *int) []int32 {
	stack = stack[:0]
	for {
		if count != nil {
			*count++
		}
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
				if in.op == opTest && tests[in.offset][at] != in.negated || in.op == opStart && at == 0 || in.op == opEnd && at == n {
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

func literal(unit uint16, fold bool) one {
	if fold {
		unit = canonical(unit)
	}
	return one{unit: unit}
}

func (p one) work(fold bool) int {
	if p.kind != inClass || len(p.class.spans) == 0 {
		return 1
	}
	variants := 1
	if fold {
		variants = caseGroups().largest
	}
	return 1 + variants*(1+len(p.class.spans))
}

func (p one) holds(unit, form uint16, fold bool, count *int) bool {
	if count != nil {
		*count++
	}
	switch p.kind {
	case isLiteral:
		return p.unit == form
	case notSlash:
		return unit != '/'
	case notLineTerminator:
		// ECMA-262 wildcards across directories exclude line terminators.
		return unit != '\n' && unit != '\r' && unit != 0x2028 && unit != 0x2029
	case inClass:
		c := p.class
		if len(c.spans) == 0 {
			return c.negated
		}
		if fold {
			if variants := caseGroups().byForm[form]; variants != nil {
				for _, other := range variants {
					if c.spansHold(other, count) {
						return !c.negated
					}
				}
				return c.negated
			}
		}
		return c.negated != c.spansHold(unit, count)
	default:
		return false
	}
}

func anyNumberOf(kind predicateKind) node {
	return repeat{of: one{kind: kind}, high: unbounded, least: 1}
}

type class struct {
	negated bool
	spans   []span
}

type span struct{ low, high uint16 }

func readClass(text []uint16) (class, error) {
	negated := len(text) > 0 && text[0] == '^'
	if negated {
		text = text[1:]
	}
	return readClassMembers(text, negated)
}

func readClassMembers(text []uint16, negated bool) (class, error) {
	c := class{negated: negated}
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

func (c class) spansHold(unit uint16, count *int) bool {
	if count != nil {
		*count++
	}
	for _, s := range c.spans {
		if count != nil {
			*count++
		}
		if s.low <= unit && unit <= s.high {
			return true
		}
	}
	return false
}

// NormalizeGlob applies the path spelling rules shared by policy and claims.
func NormalizeGlob(glob string) string {
	glob = strings.ReplaceAll(glob, `\`, "/")
	for strings.HasPrefix(glob, "./") {
		glob = glob[2:]
	}
	if !strings.Contains(glob, "//") {
		return glob
	}
	var out strings.Builder
	out.Grow(len(glob))
	var slash bool
	for i := 0; i < len(glob); i++ {
		if glob[i] != '/' || !slash {
			out.WriteByte(glob[i])
		}
		slash = glob[i] == '/'
	}
	return out.String()
}

type part struct {
	node node
	text uint16
}

func parseGlob(units []uint16, fold bool) (node, error) {
	parts := []part{{text: '^'}}
	noCloser := false
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
				directory := sequence{anyNumberOf(notLineTerminator), literal('/', fold)}
				parts = append(parts, part{node: repeat{of: directory, high: 1, least: 1}})
				end++
			default:
				parts = append(parts, part{node: anyNumberOf(notLineTerminator)})
			}
			i = end
		case '?':
			parts = append(parts, part{node: one{kind: notSlash}})
			i++
		case '[':
			// A class has at least one character, so its `]` is not the
			// character right after the `[`.
			length := -1
			if !noCloser && i+2 <= len(units) {
				length = slices.Index(units[i+2:], ']')
				noCloser = length == -1
			}
			if length == -1 {
				parts = append(parts, part{node: literal('[', fold)})
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
			parts = append(parts, part{node: literal(unit, fold)})
			i++
		}
	}
	parts = append(parts, part{text: '$'})

	s := syntax{parts: parts, fold: fold}
	root, err := s.alternatives()
	if err == nil && s.at < len(parts) {
		err = errGroupNotOpen
	}
	return root, err
}

// ordinaryCode reads the anchored dialect directly. A class that starts with
// ] is the only way a glob can introduce regular expression syntax.
func ordinaryCode(code []instr, units []uint16, fold bool) ([]instr, error) {
	code = append(code, instr{op: opStart})
	noCloser := false
	for i := 0; i < len(units); i++ {
		in := instr{op: opUnit, one: literal(units[i], fold)}
		switch units[i] {
		case '*':
			end := i + 1
			for end < len(units) && units[end] == '*' {
				end++
			}
			in = instr{op: opRun, one: one{kind: notSlash}}
			if end-i > 1 {
				in.kind = notLineTerminator
				if (i == 0 || units[i-1] == '/') && end < len(units) && units[end] == '/' {
					code = append(code, instr{op: opFork, offset: 3}, in)
					in = instr{op: opUnit, one: literal('/', fold)}
					end++
				}
			}
			i = end - 1
		case '?':
			in.kind = notSlash
		case '[':
			if !noCloser && i+2 <= len(units) {
				if length := slices.Index(units[i+2:], ']'); length >= 0 {
					end := i + 2 + length
					members := units[i+1 : end]
					negated := members[0] == '!' || members[0] == '^'
					if negated {
						members = members[1:]
					}
					c, err := readClassMembers(members, negated)
					if err != nil {
						return nil, err
					}
					in.one = one{kind: inClass, class: &c}
					i = end
				} else {
					noCloser = true
				}
			}
		}
		code = append(code, in)
	}
	code = append(code, instr{op: opEnd})
	if len(units)+len(code) > maxCost {
		return nil, errTooLarge
	}
	return append(code, instr{op: opMatch}), nil
}

func (s *globScratch) matches(glob string, path []uint16, fold bool, budget *Budget, count *int) (bool, error) {
	if len(glob) > maxCost && utf16Length(glob) > maxCost {
		return false, errTooLarge
	}
	units := utf16.Encode([]rune(glob))
	var code []instr
	var assertions []assertion
	if !strings.Contains(glob, "[]") {
		var err error
		code, err = ordinaryCode(s.code[:0], units, fold)
		if err != nil {
			return false, err
		}
		if err := budget.spend(len(code)); err != nil {
			return false, err
		}
	} else {
		root, err := parseGlob(units, fold)
		if err != nil {
			return false, err
		}
		b := builder{n: MaxGlobPath, limit: maxCost - len(units), budget: budget}
		code, err = root.emit(&b, s.code[:0])
		if err != nil {
			return false, err
		}
		if len(path) != MaxGlobPath {
			b = builder{n: len(path), limit: maxCost, budget: budget}
			code, err = root.emit(&b, s.code[:0])
			if err != nil {
				return false, err
			}
		}
		code, assertions = append(code, instr{op: opMatch}), b.assertions
	}
	work := runWork(code, len(path), fold)
	for _, a := range assertions {
		work += int64(len(path) + 1) // allocate and clear the assertion's positional results
		work += runWork(a.code, len(path), fold)
	}
	if err := budget.spend(int(min(work, maxWork+1))); err != nil {
		return false, err
	}
	m := machine{path: path, fold: fold, count: count}
	if len(assertions) != 0 {
		m.tests = make([][]bool, len(assertions))
	}
	for i, a := range assertions {
		found := make([]bool, len(path)+1)
		if count != nil {
			*count += len(found)
		}
		m.run(a.code, a.backward, found, s)
		m.tests[i] = found
	}
	return m.run(code, false, nil, s), nil
}

// MatchClaimGlob matches a normalized Room claim glob against a normalized
// path. Room compares with case and treats a matcher error as an overlap.
func MatchClaimGlob(glob, path string) (bool, error) {
	if utf16Length(path) > MaxGlobPath {
		return false, errPathTooLong
	}
	budget := NewBudget()
	if err := budget.spend(len(glob)); err != nil {
		return false, err
	}
	var scratch globScratch
	return scratch.matches(glob, utf16.Encode([]rune(path)), false, budget, nil)
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
	names  map[string]struct{}
	fold   bool
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
		return s.quantified(one{kind: notLineTerminator})
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
		return s.quantified(one{kind: inClass, class: &c})
	case '(':
		return s.group()
	}
	return s.quantified(literal(unit, s.fold))
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
	if _, exists := s.names[string(name)]; exists {
		return errNameTwice
	}
	if s.names == nil {
		s.names = make(map[string]struct{})
	}
	s.names[string(name)] = struct{}{}
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
	return repeat{of: of, low: low, high: high, least: of.minUnits()}, nil
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
