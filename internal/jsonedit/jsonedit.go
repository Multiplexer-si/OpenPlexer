// Package jsonedit edits JSON text in place. It records where every value sits
// in the source bytes and applies splices, so that bytes it did not touch (key
// order, indentation, number spelling, escapes, line endings) stay as they were.
package jsonedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type Kind uint8

const (
	Object Kind = iota
	Array
	String
	Number
	Bool
	Null
)

func (k Kind) String() string {
	return [...]string{"object", "array", "string", "number", "bool", "null"}[k]
}

// Node is one value. Its bytes are src[Start:End).
type Node struct {
	Kind       Kind
	Start, End int
	Members    []Member // Object only, in source order
	Elems      []Node   // Array only, in source order
}

// Member is one key and value of an object. The member spans [KeyStart, Value.End).
type Member struct {
	Key      string // decoded
	KeyStart int
	KeyEnd   int
	Value    Node
}

// Doc is a parsed document. Offsets in its nodes and edits count from after a
// leading UTF-8 byte order mark, which Apply puts back.
type Doc struct {
	src  []byte
	bom  bool
	root Node
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parse reads strict JSON: no comments, no trailing commas.
func Parse(src []byte) (*Doc, error) {
	body, bom := bytes.CutPrefix(src, utf8BOM)
	body = slices.Clone(body)
	if !json.Valid(body) {
		var v any
		return nil, syntaxError(body, json.Unmarshal(body, &v))
	}
	s := scanner{b: body}
	return &Doc{src: body, bom: bom, root: s.value()}, nil
}

func syntaxError(b []byte, err error) error {
	var se *json.SyntaxError
	if !errors.As(err, &se) {
		return errors.New("invalid JSON")
	}
	pos := min(max(int(se.Offset)-1, 0), len(b))
	line := 1 + bytes.Count(b[:pos], []byte("\n"))
	col := pos - bytes.LastIndexByte(b[:pos], '\n')
	return fmt.Errorf("invalid JSON at line %d, column %d: %s", line, col, se.Error())
}

func (d *Doc) Root() Node { return d.root }

// Text returns the source bytes of n.
func (d *Doc) Text(n Node) []byte { return d.src[n.Start:n.End] }

// Decode unmarshals n into v.
func (d *Doc) Decode(n Node, v any) error { return json.Unmarshal(d.Text(n), v) }

// Get returns the member named key. It is an error for the key to appear twice,
// because readers disagree on which value wins.
func (n Node) Get(key string) (Node, bool, error) {
	i, err := n.MemberIndex(key)
	if i < 0 || err != nil {
		return Node{}, false, err
	}
	return n.Members[i].Value, true, nil
}

// MemberIndex returns the position of the member named key, or -1.
func (n Node) MemberIndex(key string) (int, error) {
	found := -1
	for i, m := range n.Members {
		if m.Key != key {
			continue
		}
		if found >= 0 {
			return -1, fmt.Errorf("duplicate key %q", key)
		}
		found = i
	}
	return found, nil
}

// DuplicateKey reports a key that appears twice in the object n.
func (n Node) DuplicateKey() (string, bool) {
	seen := make(map[string]bool, len(n.Members))
	for _, m := range n.Members {
		if seen[m.Key] {
			return m.Key, true
		}
		seen[m.Key] = true
	}
	return "", false
}

type scanner struct {
	b []byte
	i int
}

func (s *scanner) skip() {
	for s.i < len(s.b) && (s.b[s.i] == ' ' || s.b[s.i] == '\t' || s.b[s.i] == '\n' || s.b[s.i] == '\r') {
		s.i++
	}
}

func (s *scanner) str() {
	s.i++
	for s.b[s.i] != '"' {
		if s.b[s.i] == '\\' {
			s.i++
		}
		s.i++
	}
	s.i++
}

func (s *scanner) value() Node {
	s.skip()
	n := Node{Start: s.i}
	switch c := s.b[s.i]; c {
	case '{':
		n.Kind = Object
		s.i++
		for {
			s.skip()
			switch s.b[s.i] {
			case '}':
				s.i++
				n.End = s.i
				return n
			case ',':
				s.i++
				continue
			}
			m := Member{KeyStart: s.i}
			s.str()
			m.KeyEnd = s.i
			_ = json.Unmarshal(s.b[m.KeyStart:m.KeyEnd], &m.Key)
			s.skip()
			s.i++
			m.Value = s.value()
			n.Members = append(n.Members, m)
		}
	case '[':
		n.Kind = Array
		s.i++
		for {
			s.skip()
			switch s.b[s.i] {
			case ']':
				s.i++
				n.End = s.i
				return n
			case ',':
				s.i++
				continue
			}
			n.Elems = append(n.Elems, s.value())
		}
	case '"':
		n.Kind = String
		s.str()
	default:
		switch c {
		case 't', 'f':
			n.Kind = Bool
		case 'n':
			n.Kind = Null
		default:
			n.Kind = Number
		}
		for s.i < len(s.b) && !bytes.ContainsRune([]byte(",]} \t\r\n"), rune(s.b[s.i])) {
			s.i++
		}
	}
	n.End = s.i
	return n
}
