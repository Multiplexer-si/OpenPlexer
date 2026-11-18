package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type object = map[string]any

const maxSafeInteger = 1<<53 - 1

func parse[T any](text []byte, read func(*reader, any) T) (T, error) {
	var none T
	doc, err := decodeJSON(text)
	if err != nil {
		return none, err
	}
	r := &reader{}
	value := read(r, doc)
	if len(r.issues) > 0 {
		return none, &ValidationError{Issues: r.issues}
	}
	return value, nil
}

// decodeJSON decodes into plain values, so the last of a repeated key wins and
// a number keeps its spelling until a reader asks for it.
func decodeJSON(text []byte) (any, error) {
	if !utf8.Valid(text) {
		return nil, &ValidationError{Issues: []Issue{{Reason: "is not valid UTF-8"}}}
	}
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return nil, &ValidationError{Issues: []Issue{{Reason: "is not valid JSON"}}}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, &ValidationError{Issues: []Issue{{Reason: "has text after the JSON value"}}}
	}
	return doc, nil
}

func compact(text []byte) json.RawMessage {
	var out bytes.Buffer
	if err := json.Compact(&out, text); err != nil {
		return nil
	}
	return out.Bytes()
}

type reader struct {
	issues []Issue
}

func (r *reader) fail(path, reason string) {
	r.issues = append(r.issues, Issue{Path: path, Reason: reason})
}

func member(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

const (
	required = true
	optional = false
)

func field[T any](r *reader, path string, o object, key string, need bool, read func(path string, value any) T) (T, bool) {
	value, present := o[key]
	if !present {
		if need {
			r.fail(member(path, key), "is required")
		}
		var none T
		return none, false
	}
	return read(member(path, key), value), true
}

func (r *reader) object(path string, value any) (object, bool) {
	o, ok := value.(object)
	if !ok {
		r.fail(path, "must be an object")
	}
	return o, ok
}

func (r *reader) text(path string, value any) string {
	text, ok := value.(string)
	if !ok {
		r.fail(path, "must be a string")
	}
	return text
}

func (r *reader) nonEmptyText(path string, value any) string {
	text, ok := value.(string)
	if !ok || text == "" {
		r.fail(path, "must be a string that is not empty")
	}
	return text
}

// isSpace is the white space of spec/README.md.
func isSpace(c rune) bool {
	switch {
	case c >= 0x09 && c <= 0x0d, c == 0x20, c == 0xa0, c == 0x1680, c >= 0x2000 && c <= 0x200a:
		return true
	}
	switch c {
	case 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return false
}

func (r *reader) nonBlankText(path string, value any) string {
	text, ok := value.(string)
	if !ok || strings.IndexFunc(text, func(c rune) bool { return !isSpace(c) }) < 0 {
		r.fail(path, "must be a string with a character that is not white space")
	}
	return text
}

func (r *reader) flag(path string, value any) bool {
	flag, ok := value.(bool)
	if !ok {
		r.fail(path, "must be true or false")
	}
	return flag
}

// number reads a JSON number as an IEEE 754 double, as spec/README.md says, so
// 1.0000000000000001 is the integer 1 and 1e400 is infinity.
func number(value any) (float64, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	double, _ := strconv.ParseFloat(string(n), 64)
	return double, true
}

func (r *reader) integerFrom(low int64) func(path string, value any) int64 {
	return func(path string, value any) int64 {
		double, ok := number(value)
		if ok && double == math.Trunc(double) && double >= float64(low) && double <= maxSafeInteger {
			return int64(double)
		}
		r.fail(path, fmt.Sprintf("must be an integer from %d to %d", low, int64(maxSafeInteger)))
		return 0
	}
}

func choice(r *reader, allowed ...string) func(path string, value any) string {
	return func(path string, value any) string {
		text, ok := value.(string)
		if !ok || !slices.Contains(allowed, text) {
			quoted := make([]string, len(allowed))
			for i, option := range allowed {
				quoted[i] = strconv.Quote(option)
			}
			r.fail(path, "must be one of "+strings.Join(quoted, ", "))
		}
		return text
	}
}

func list[T any](r *reader, read func(path string, value any) T) func(path string, value any) []T {
	return func(path string, value any) []T {
		elements, ok := value.([]any)
		if !ok {
			r.fail(path, "must be a list")
			return nil
		}
		out := make([]T, len(elements))
		for i, element := range elements {
			out[i] = read(fmt.Sprintf("%s[%d]", path, i), element)
		}
		return out
	}
}
