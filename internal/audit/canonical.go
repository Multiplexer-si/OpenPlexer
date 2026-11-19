package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonical returns the canonical JSON of a JSON text, under the rules of
// spec/canonical-json/README.md.
func Canonical(text []byte) ([]byte, error) {
	v, err := parse(text)
	if err != nil {
		return nil, err
	}
	return v.appendCanonical(nil), nil
}

type kind int

const (
	kindNull kind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// value is a JSON value as canonical JSON reads it. A string is a sequence
// of UTF-16 code units, because a JSON text can hold half of a surrogate
// pair and a Go string cannot. The members of an object are sorted by key,
// and no key repeats.
type value struct {
	kind      kind
	flag      bool
	number    float64
	units     []uint16
	items     []value
	members   []member
	duplicate string
}

type member struct {
	key   []uint16
	value value
}

func text(s string) value {
	return value{kind: kindString, units: utf16.Encode([]rune(s))}
}

func (v value) member(key string) (value, bool) {
	units := utf16.Encode([]rune(key))
	for _, m := range v.members {
		if slices.Equal(m.key, units) {
			return m.value, true
		}
	}
	return value{}, false
}

// str returns the string of v, with U+FFFD for half of a surrogate pair. It
// reports false when v is not a string.
func (v value) str() (string, bool) {
	return string(utf16.Decode(v.units)), v.kind == kindString
}

func object(members ...member) value {
	slices.SortFunc(members, func(a, b member) int { return slices.Compare(a.key, b.key) })
	return value{kind: kindObject, members: members}
}

func parse(text []byte) (value, error) {
	if !utf8.Valid(text) || !json.Valid(text) {
		return value{}, errors.New("the text is not JSON")
	}
	r := reader{text: text}
	return r.value(), nil
}

// reader reads a text that json.Valid accepted, so it checks no syntax.
type reader struct {
	text []byte
	at   int
}

func (r *reader) space() {
	for r.at < len(r.text) && strings.IndexByte(" \t\n\r", r.text[r.at]) >= 0 {
		r.at++
	}
}

func (r *reader) value() value {
	r.space()
	switch c := r.text[r.at]; c {
	case 'n':
		r.at += len("null")
		return value{}
	case 't':
		r.at += len("true")
		return value{kind: kindBool, flag: true}
	case 'f':
		r.at += len("false")
		return value{kind: kindBool}
	case '"':
		return value{kind: kindString, units: r.units()}
	case '[':
		return r.array()
	case '{':
		return r.object()
	}
	start := r.at
	for r.at < len(r.text) && strings.IndexByte("+-.eE0123456789", r.text[r.at]) >= 0 {
		r.at++
	}
	// A number beyond the range of a double is infinity, and ParseFloat
	// returns infinity together with its range error.
	number, _ := strconv.ParseFloat(string(r.text[start:r.at]), 64)
	return value{kind: kindNumber, number: number}
}

// more steps over the opening bracket or the comma before an element, and
// reports false at the closing bracket.
func (r *reader) more(closing byte) bool {
	r.at++
	r.space()
	if r.text[r.at] == closing {
		r.at++
		return false
	}
	return true
}

func (r *reader) array() value {
	v := value{kind: kindArray}
	for r.more(']') {
		v.items = append(v.items, r.value())
		r.space()
		if r.text[r.at] == ']' {
			r.at++
			break
		}
	}
	return v
}

func (r *reader) object() value {
	var members []member
	var duplicate string
	for r.more('}') {
		key := r.units()
		r.space()
		r.at++
		m := member{key: key, value: r.value()}
		if i := slices.IndexFunc(members, func(m member) bool { return slices.Equal(m.key, key) }); i >= 0 {
			members[i] = m
			if duplicate == "" {
				duplicate = string(appendString(nil, key))
			}
		} else {
			members = append(members, m)
		}
		r.space()
		if r.text[r.at] == '}' {
			r.at++
			break
		}
	}
	v := object(members...)
	v.duplicate = duplicate
	return v
}

var escapes = map[byte]uint16{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

func (r *reader) units() []uint16 {
	units := []uint16{}
	r.at++
	for {
		c := r.text[r.at]
		switch {
		case c == '"':
			r.at++
			return units
		case c != '\\':
			char, size := utf8.DecodeRune(r.text[r.at:])
			units = utf16.AppendRune(units, char)
			r.at += size
		case r.text[r.at+1] == 'u':
			unit, _ := strconv.ParseUint(string(r.text[r.at+2:r.at+6]), 16, 16)
			units = append(units, uint16(unit))
			r.at += 6
		default:
			units = append(units, escapes[r.text[r.at+1]])
			r.at += 2
		}
	}
}

func (v value) appendCanonical(out []byte) []byte {
	switch v.kind {
	case kindNull:
		return append(out, "null"...)
	case kindBool:
		return strconv.AppendBool(out, v.flag)
	case kindNumber:
		return appendNumber(out, v.number)
	case kindString:
		return appendString(out, v.units)
	case kindArray:
		out = append(out, '[')
		for i, item := range v.items {
			if i > 0 {
				out = append(out, ',')
			}
			out = item.appendCanonical(out)
		}
		return append(out, ']')
	}
	out = append(out, '{')
	for i, m := range v.members {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendString(out, m.key)
		out = append(out, ':')
		out = m.value.appendCanonical(out)
	}
	return append(out, '}')
}

var shortEscapes = map[uint16]string{'"': `\"`, '\\': `\\`, '\b': `\b`, '\t': `\t`, '\n': `\n`, '\f': `\f`, '\r': `\r`}

func appendString(out []byte, units []uint16) []byte {
	out = append(out, '"')
	for i := 0; i < len(units); i++ {
		unit := units[i]
		if short, ok := shortEscapes[unit]; ok {
			out = append(out, short...)
			continue
		}
		if utf16.IsSurrogate(rune(unit)) {
			if i+1 < len(units) {
				if char := utf16.DecodeRune(rune(unit), rune(units[i+1])); char != utf8.RuneError {
					out = utf8.AppendRune(out, char)
					i++
					continue
				}
			}
			out = fmt.Appendf(out, `\u%04x`, unit)
			continue
		}
		if unit < 0x20 {
			out = fmt.Appendf(out, `\u%04x`, unit)
			continue
		}
		out = utf8.AppendRune(out, rune(unit))
	}
	return append(out, '"')
}

// appendNumber writes a double as Number::toString of ECMA-262 does.
func appendNumber(out []byte, number float64) []byte {
	if number == 0 {
		return append(out, '0')
	}
	if math.IsInf(number, 0) {
		return append(out, "null"...)
	}
	if number < 0 {
		out = append(out, '-')
		number = -number
	}
	// The shortest digits that give the same double back, as d.ddde±xx.
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(number, 'e', -1, 64), "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e, _ := strconv.Atoi(exponent)
	count, point := len(digits), e+1
	switch {
	case count <= point && point <= 21:
		out = append(out, digits...)
		return append(out, strings.Repeat("0", point-count)...)
	case 0 < point && point <= 21:
		out = append(out, digits[:point]...)
		out = append(out, '.')
		return append(out, digits[point:]...)
	case -6 < point && point <= 0:
		out = append(out, "0."...)
		out = append(out, strings.Repeat("0", -point)...)
		return append(out, digits...)
	}
	out = append(out, digits[0])
	if count > 1 {
		out = append(out, '.')
		out = append(out, digits[1:]...)
	}
	out = append(out, 'e')
	if e >= 0 {
		out = append(out, '+')
	}
	return strconv.AppendInt(out, int64(e), 10)
}
