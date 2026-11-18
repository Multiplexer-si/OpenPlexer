package jsonedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Edit replaces src[Start:End) with Text.
type Edit struct {
	Start, End int
	Text       string
}

// Apply splices non-overlapping edits into the source and puts back the BOM.
func (d *Doc) Apply(edits ...Edit) ([]byte, error) {
	es := slices.Clone(edits)
	slices.SortStableFunc(es, func(a, b Edit) int { return a.Start - b.Start })
	var out []byte
	if d.bom {
		out = append(out, utf8BOM...)
	}
	at := 0
	for _, e := range es {
		if e.Start < at || e.End < e.Start || e.End > len(d.src) {
			return nil, fmt.Errorf("edit [%d,%d) overlaps another edit or lies outside the document", e.Start, e.End)
		}
		out = append(out, d.src[at:e.Start]...)
		out = append(out, e.Text...)
		at = e.End
	}
	return append(out, d.src[at:]...), nil
}

// AppendElem adds v as the last element of arr, in the layout of arr.
func (d *Doc) AppendElem(arr Node, v any) (Edit, error) {
	if arr.Kind != Array {
		return Edit{}, fmt.Errorf("cannot append an element to a %s", arr.Kind)
	}
	return d.appendChild(arr, "", false, v)
}

// AppendMember adds key: v as the last member of obj, in the layout of obj.
func (d *Doc) AppendMember(obj Node, key string, v any) (Edit, error) {
	if obj.Kind != Object {
		return Edit{}, fmt.Errorf("cannot append a member to a %s", obj.Kind)
	}
	return d.appendChild(obj, key, true, v)
}

// RemoveChild removes member or element i. A first child takes the separator
// after it, any other child the separator before it, and an only child leaves
// an empty container.
func (d *Doc) RemoveChild(container Node, i int) (Edit, error) {
	n := childCount(container)
	if (container.Kind != Object && container.Kind != Array) || i < 0 || i >= n {
		return Edit{}, errors.New("no such child")
	}
	switch {
	case n == 1:
		return Edit{container.Start, container.End, emptyText(container.Kind)}, nil
	case i > 0:
		_, prevEnd := childSpan(container, i-1)
		_, end := childSpan(container, i)
		return Edit{prevEnd, end, ""}, nil
	default:
		start, _ := childSpan(container, 0)
		nextStart, _ := childSpan(container, 1)
		return Edit{start, nextStart, ""}, nil
	}
}

// ReplaceValue replaces n with v in compact form.
func (d *Doc) ReplaceValue(n Node, v any) (Edit, error) {
	text, err := style{colon: d.colon(), sep: d.inlineSep(Node{})}.render(v)
	if err != nil {
		return Edit{}, err
	}
	return Edit{n.Start, n.End, text}, nil
}

func emptyText(k Kind) string {
	if k == Object {
		return "{}"
	}
	return "[]"
}

func childCount(c Node) int {
	if c.Kind == Object {
		return len(c.Members)
	}
	return len(c.Elems)
}

func childSpan(c Node, i int) (start, end int) {
	if c.Kind == Object {
		return c.Members[i].KeyStart, c.Members[i].Value.End
	}
	return c.Elems[i].Start, c.Elems[i].End
}

func (d *Doc) appendChild(c Node, key string, isMember bool, v any) (Edit, error) {
	n := childCount(c)
	opener, closer := emptyText(c.Kind)[:1], emptyText(c.Kind)[1:]
	st := style{colon: d.colon(), sep: d.inlineSep(c)}
	keyLit := ""
	if isMember {
		k, err := marshal(key)
		if err != nil {
			return Edit{}, err
		}
		keyLit = k + st.colon
	}
	if n == 0 {
		if bytes.IndexByte(d.src, '\n') < 0 {
			text, err := st.render(v)
			return Edit{c.Start, c.End, opener + keyLit + text + closer}, err
		}
		opening, unit := d.lineIndent(c.Start), d.indentUnit()
		st.multi, st.eol, st.unit, st.indent = true, d.eolNear(c.Start), unit, opening+unit
		text, err := st.render(v)
		return Edit{c.Start, c.End, opener + st.eol + st.indent + keyLit + text + st.eol + opening + closer}, err
	}
	lastStart, lastEnd := childSpan(c, n-1)
	if bytes.IndexByte(d.src[c.Start:lastStart], '\n') < 0 {
		text, err := st.render(v)
		return Edit{lastEnd, lastEnd, st.sep + keyLit + text}, err
	}
	st.multi, st.eol, st.unit, st.indent = true, d.eolNear(c.Start), d.indentUnit(), d.lineIndent(lastStart)
	text, err := st.render(v)
	return Edit{lastEnd, lastEnd, "," + st.eol + st.indent + keyLit + text}, err
}

func (d *Doc) lineIndent(pos int) string {
	ls := bytes.LastIndexByte(d.src[:pos], '\n') + 1
	j := ls
	for j < len(d.src) && (d.src[j] == ' ' || d.src[j] == '\t') {
		j++
	}
	return string(d.src[ls:j])
}

func (d *Doc) eolNear(pos int) string {
	nl := bytes.LastIndexByte(d.src[:pos], '\n')
	if i := bytes.IndexByte(d.src[pos:], '\n'); i >= 0 {
		nl = pos + i
	}
	if nl > 0 && d.src[nl-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

func walk(n Node, f func(Node) bool) bool {
	if n.Kind != Object && n.Kind != Array {
		return false
	}
	if f(n) {
		return true
	}
	for _, m := range n.Members {
		if walk(m.Value, f) {
			return true
		}
	}
	for _, e := range n.Elems {
		if walk(e, f) {
			return true
		}
	}
	return false
}

func (d *Doc) indentUnit() string {
	unit := "  "
	walk(d.root, func(n Node) bool {
		if childCount(n) == 0 {
			return false
		}
		first, _ := childSpan(n, 0)
		if bytes.IndexByte(d.src[n.Start:first], '\n') < 0 {
			return false
		}
		open, child := d.lineIndent(n.Start), d.lineIndent(first)
		if len(child) > len(open) && strings.HasPrefix(child, open) {
			unit = child[len(open):]
			return true
		}
		return false
	})
	return unit
}

func (d *Doc) colon() string {
	colon := ": "
	walk(d.root, func(n Node) bool {
		for _, m := range n.Members {
			if gap := d.src[m.KeyEnd:m.Value.Start]; bytes.IndexByte(gap, '\n') < 0 {
				colon = string(gap)
				return true
			}
		}
		return false
	})
	return colon
}

func (d *Doc) inlineSep(c Node) string {
	sep := ""
	pick := func(n Node) bool {
		if childCount(n) < 2 {
			return false
		}
		_, end := childSpan(n, 0)
		start, _ := childSpan(n, 1)
		if gap := d.src[end:start]; bytes.IndexByte(gap, '\n') < 0 {
			sep = string(gap)
			return true
		}
		return false
	}
	if pick(c) || walk(d.root, pick) {
		return sep
	}
	if strings.HasSuffix(d.colon(), " ") {
		return ", "
	}
	return ","
}

type style struct {
	multi  bool
	indent string
	unit   string
	eol    string
	colon  string
	sep    string
}

func marshal(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func (st style) render(v any) (string, error) {
	compact, err := marshal(v)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	depth := 0
	newline := func() {
		b.WriteString(st.eol + st.indent + strings.Repeat(st.unit, depth))
	}
	inString, escaped := false, false
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		if inString {
			b.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			b.WriteByte(c)
		case '{', '[':
			b.WriteByte(c)
			if next := compact[i+1]; next == '}' || next == ']' {
				b.WriteByte(next)
				i++
				continue
			}
			depth++
			if st.multi {
				newline()
			}
		case '}', ']':
			depth--
			if st.multi {
				newline()
			}
			b.WriteByte(c)
		case ',':
			if st.multi {
				b.WriteByte(',')
				newline()
			} else {
				b.WriteString(st.sep)
			}
		case ':':
			b.WriteString(st.colon)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}
