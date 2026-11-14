// Package spectest checks the contracts under spec/ against themselves: every
// file is where the layout puts it, every schema compiles, every example
// validates against its schema, every hash in an example or a fixture follows
// from its documented rule, and every fixture file validates against the
// fixture-file schema. It reads the files in this process, so that the test
// cache sees every file the result depends on.
package spectest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	specRoot     = "../../spec"
	draft        = "https://json-schema.org/draft/2020-12/schema"
	idPrefix     = "urn:openplexer:spec:"
	schemaSuffix = ".schema.json"
)

// contracts holds every compiled schema under spec/, by $id.
type contracts map[string]*jsonschema.Schema

// readJSON reads every number as an IEEE 754 double, as spec/README.md says
// that a reader of the contracts does. A number outside the range of a double
// is read as infinity.
func readJSON(t *testing.T, path string) any {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := decode(text)
	if err != nil {
		t.Fatalf("%s: %v", display(path), err)
	}
	return value
}

func decode(text []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("text after the JSON value")
	}
	return doubles(value), nil
}

// doubles replaces every json.Number in a decoded value with its double.
func doubles(value any) any {
	switch value := value.(type) {
	case json.Number:
		n, _ := strconv.ParseFloat(string(value), 64)
		return n
	case []any:
		for i, element := range value {
			value[i] = doubles(element)
		}
	case map[string]any:
		for key, element := range value {
			value[key] = doubles(element)
		}
	}
	return value
}

// display is the path of a file relative to the module root.
func display(path string) string {
	rel, err := filepath.Rel(filepath.Dir(specRoot), path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// filesUnder lists the files under spec/ that match, in a stable order.
func filesUnder(t *testing.T, match func(rel string) bool) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(specRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(specRoot, path)
		if err != nil {
			return err
		}
		if match(filepath.ToSlash(rel)) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	return paths
}

// idOf is the $id that a schema file must declare: the path under spec/
// without the suffix, with ":" for "/". The rule lets a reader find the file
// of an $id without a lookup table.
func idOf(path string) string {
	rel, _ := filepath.Rel(specRoot, path)
	return idPrefix + strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(rel), schemaSuffix), "/", ":")
}

func schemaPaths(t *testing.T) []string {
	t.Helper()
	paths := filesUnder(t, func(rel string) bool { return strings.HasSuffix(rel, schemaSuffix) })
	if len(paths) == 0 {
		t.Fatal("found no schema under spec/")
	}
	return paths
}

func loadContracts(t *testing.T) contracts {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	paths := schemaPaths(t)
	for _, path := range paths {
		doc := readJSON(t, path)
		object, _ := doc.(map[string]any)
		if object["$schema"] != draft {
			t.Errorf("%s: $schema must be %s", display(path), draft)
		}
		if object["$id"] != idOf(path) {
			t.Errorf("%s: $id must be %s", display(path), idOf(path))
		}
		if err := compiler.AddResource(idOf(path), withoutAnnotations(doc)); err != nil {
			t.Fatalf("%s: %v", display(path), err)
		}
	}
	schemas := contracts{}
	for _, path := range paths {
		schema, err := compiler.Compile(idOf(path))
		if err != nil {
			t.Errorf("%s: %v", display(path), err)
			continue
		}
		schemas[idOf(path)] = schema
	}
	return schemas
}

// withoutAnnotations removes every "title" and "description" whose value is a
// text. The validator rejects infinity under any schema that is not empty,
// and spec/README.md says that infinity is valid where a schema accepts any
// value, such as a schema that has only a description. Without its
// annotations such a schema is empty. A property named "description" has a
// schema as its value, not a text, so it stays.
func withoutAnnotations(node any) any {
	switch node := node.(type) {
	case []any:
		for i, element := range node {
			node[i] = withoutAnnotations(element)
		}
	case map[string]any:
		for key, value := range node {
			if _, text := value.(string); text && (key == "title" || key == "description") {
				delete(node, key)
				continue
			}
			node[key] = withoutAnnotations(value)
		}
	}
	return node
}

// leaves lists the validation errors that have no cause of their own.
func leaves(err error) []*jsonschema.ValidationError {
	var failed *jsonschema.ValidationError
	if !errors.As(err, &failed) {
		return nil
	}
	if len(failed.Causes) == 0 {
		return []*jsonschema.ValidationError{failed}
	}
	var out []*jsonschema.ValidationError
	for _, cause := range failed.Causes {
		out = append(out, leaves(cause)...)
	}
	return out
}

// layout lists the places where spec/README.md allows a file, as paths under
// spec/. A file anywhere else is one that no test reads, so it could be wrong
// and nothing would say so.
var layout = []*regexp.Regexp{
	regexp.MustCompile(`^README\.md$`),
	regexp.MustCompile(`^fixture-file\.schema\.json$`),
	regexp.MustCompile(`^[a-z0-9-]+/README\.md$`),
	regexp.MustCompile(`^[a-z0-9-]+/v[1-9][0-9]*/[a-z0-9-]+\.schema\.json$`),
	regexp.MustCompile(`^[a-z0-9-]+/v[1-9][0-9]*/examples/[a-z0-9-]+/[a-z0-9.-]+\.json$`),
	regexp.MustCompile(`^[a-z0-9-]+/v[1-9][0-9]*/fixtures/[a-z0-9-]+\.json$`),
}

func TestEveryFileIsInTheLayout(t *testing.T) {
	files := filesUnder(t, func(string) bool { return true })
	for _, path := range files {
		rel, _ := filepath.Rel(specRoot, path)
		inLayout := slices.ContainsFunc(layout, func(place *regexp.Regexp) bool { return place.MatchString(filepath.ToSlash(rel)) })
		if !inLayout {
			t.Errorf("%s: the layout in spec/README.md has no place for this file", display(path))
		}
	}
	t.Logf("checked %d files", len(files))
}

func TestSchemasCompile(t *testing.T) {
	schemas := loadContracts(t)
	t.Logf("compiled %d schemas", len(schemas))
}

// escapable lists the characters that ECMA-262 lets "\" escape with the u
// flag: the syntax characters and "/".
const escapable = `^$\.*+?()[]{}|/`

// quantifierAt matches a counted quantifier at the start of the text.
var quantifierAt = regexp.MustCompile(`^\{([0-9]+)(?:,([0-9]*))?\}`)

// maxCount is the largest count, and the largest product of the counts of
// nested quantifiers, that RE2 accepts.
const maxCount = 1000

// countOf is the count of a counted quantifier that RE2 limits: m for {n,m},
// n for {n}, and n for {n,}, or 1 when n is 0, because RE2 passes the limit
// through {0,} unchanged. It says why the quantifier is outside the subset,
// or returns "".
func countOf(quantifier string) (int, string) {
	parts := quantifierAt.FindStringSubmatch(quantifier)
	n, err := strconv.Atoi(parts[1])
	if err != nil || n > maxCount {
		return 0, fmt.Sprintf("the quantifier %s, whose count is above %d", quantifier, maxCount)
	}
	most := n
	if strings.Contains(quantifier, ",") {
		if parts[2] == "" {
			return max(n, 1), ""
		}
		m, err := strconv.Atoi(parts[2])
		switch {
		case err != nil || m > maxCount:
			return 0, fmt.Sprintf("the quantifier %s, whose count is above %d", quantifier, maxCount)
		case m < n:
			return 0, fmt.Sprintf("the quantifier %s, whose second count is less than its first", quantifier)
		}
		most = m
	}
	if most == 0 {
		return 0, fmt.Sprintf("the quantifier %s, which repeats nothing", quantifier)
	}
	return most, ""
}

// notPortable says why a pattern is outside the portable subset that
// spec/README.md defines, or returns "". The subset is a grammar, and this
// scan accepts exactly its patterns. The validator of this test compiles
// patterns with the Go engine, and the contracts promise the ECMA-262
// meaning, so a construct outside the subset could pass here and mean
// something else to another implementation. The extractor applies the same
// scan before it writes a schema. The JSON reader of Go turns a lone
// surrogate into U+FFFD, so the scan rejects U+FFFD to see one.
//
// products holds, for each group that is open, the largest product of
// counts inside it. pending is the product of the atom just read, which a
// quantifier may still multiply, or -1.
func notPortable(pattern string) string {
	runes := []rune(pattern)
	at := func(i int) rune {
		if i < len(runes) {
			return runes[i]
		}
		return 0
	}
	unsafe := func(r rune) string {
		switch {
		case r > 0xFFFF:
			return "a character outside the Basic Multilingual Plane"
		case r == 0xFFFD:
			return "the replacement character U+FFFD"
		}
		return ""
	}
	before := "nothing"
	products := []int{1}
	pending := -1
	flush := func() {
		if pending >= 0 {
			products[len(products)-1] = max(products[len(products)-1], pending)
			pending = -1
		}
	}
	i := 0
	if at(0) == '^' {
		i = 1
	}
	for i < len(runes) {
		r := runes[i]
		quantifier := ""
		if strings.ContainsRune("*+?", r) {
			quantifier = string(r)
		} else if r == '{' {
			quantifier = quantifierAt.FindString(string(runes[i:]))
		}
		if reason := unsafe(r); reason != "" {
			return reason
		}
		if quantifier == "" {
			flush()
		}
		switch {
		case r == '$':
			if i != len(runes)-1 {
				return `a "$" that does not end the pattern`
			}
			i++
		case r == '^':
			return `a "^" that does not start the pattern`
		case r == '\\':
			if at(i+1) == 0 || !strings.ContainsRune(escapable, at(i+1)) {
				return fmt.Sprintf("the escape %q", string(runes[i:min(i+2, len(runes))]))
			}
			before = "atom"
			pending = 1
			i += 2
		case r == '.':
			return `a "." with no "\" before it`
		case r == '[':
			i++
			if at(i) == '^' {
				i++
			}
			if at(i) == ']' {
				return `a "]" as the first member of a class`
			}
			for ; at(i) != ']'; i++ {
				member := at(i)
				switch {
				case i >= len(runes):
					return `a class with no "]" at its end`
				case unsafe(member) != "":
					return unsafe(member)
				case member == '[':
					return `a "[" inside a class with no "\" before it`
				case member == '.':
					return `a "." with no "\" before it`
				case strings.ContainsRune("-&|~", member) && at(i+1) == member:
					return fmt.Sprintf("%q inside a class", string([]rune{member, member}))
				case member == '\\':
					if at(i+1) == 0 || !strings.ContainsRune(escapable+"-", at(i+1)) {
						return fmt.Sprintf("the escape %q", string(runes[i:min(i+2, len(runes))]))
					}
					i++
				}
			}
			before = "atom"
			pending = 1
			i++
		case r == '(':
			if at(i+1) == '?' && at(i+2) != ':' {
				return `a group that starts with "(?" and is not "(?:"`
			}
			products = append(products, 1)
			before = "nothing"
			if at(i+1) == '?' {
				i += 3
			} else {
				i++
			}
		case r == ')':
			if len(products) == 1 {
				return `a ")" that closes no group`
			}
			pending = products[len(products)-1]
			products = products[:len(products)-1]
			before = "atom"
			i++
		case r == '|':
			before = "nothing"
			i++
		case quantifier != "":
			switch before {
			case "quantifier":
				return fmt.Sprintf("the quantifier %s right after a quantifier", quantifier)
			case "nothing":
				return fmt.Sprintf("the quantifier %s with nothing before it to repeat", quantifier)
			}
			if quantifier[0] == '{' {
				count, reason := countOf(quantifier)
				if reason != "" {
					return reason
				}
				if pending *= count; pending > maxCount {
					return fmt.Sprintf("nested counts whose product is above %d", maxCount)
				}
			}
			flush()
			before = "quantifier"
			i += len([]rune(quantifier))
		case r == '{':
			return `a "{" that does not start a quantifier "{n}", "{n,}", or "{n,m}"`
		case r == '}' || r == ']':
			return fmt.Sprintf(`a %q with no "\" before it outside a class or a quantifier`, string(r))
		default:
			before = "atom"
			pending = 1
			i++
		}
	}
	if len(products) != 1 {
		return `a group with no ")" at its end`
	}
	return ""
}

// patternsIn lists every regular expression in a schema: the value of each
// "pattern" keyword and each key of a "patternProperties" object.
func patternsIn(node any) []string {
	var out []string
	switch node := node.(type) {
	case []any:
		for _, element := range node {
			out = append(out, patternsIn(element)...)
		}
	case map[string]any:
		for key, value := range node {
			if pattern, ok := value.(string); ok && key == "pattern" {
				out = append(out, pattern)
				continue
			}
			if properties, ok := value.(map[string]any); ok && key == "patternProperties" {
				for pattern := range properties {
					out = append(out, pattern)
				}
			}
			out = append(out, patternsIn(value)...)
		}
	}
	return out
}

func TestPatternsArePortable(t *testing.T) {
	total := 0
	for _, path := range schemaPaths(t) {
		for _, pattern := range patternsIn(readJSON(t, path)) {
			total++
			if reason := notPortable(pattern); reason != "" {
				t.Errorf("%s: the pattern %q is not portable: it has %s", display(path), pattern, reason)
			}
		}
	}
	if total == 0 {
		t.Fatal("found no pattern under spec/")
	}
	t.Logf("checked %d patterns", total)
}

// The count rule of the scan must accept only what RE2 compiles. The test
// nests counted quantifiers three deep and compiles each pattern with Go.
// The scan rejects {0} and {0,0} for another engine, so Go may compile a
// pattern with one of them that the scan rejects.
func TestCountRuleAgreesWithRE2(t *testing.T) {
	forms := []string{"*", "+", "?"}
	for _, n := range []int{0, 1, 2, 10, 100, 101, 1000, 1001} {
		forms = append(forms, fmt.Sprintf("{%d}", n), fmt.Sprintf("{%d,}", n), fmt.Sprintf("{0,%d}", n), fmt.Sprintf("{1,%d}", n))
	}
	for _, outer := range forms {
		for _, middle := range forms {
			for _, inner := range forms {
				pattern := "^(?:(?:a" + inner + ")" + middle + ")" + outer + "$"
				_, err := regexp.Compile(pattern)
				reason := notPortable(pattern)
				switch {
				case reason == "" && err != nil:
					t.Errorf("the scan accepts %q, and Go rejects it: %v", pattern, err)
				case reason != "" && err == nil && !strings.Contains(pattern, "{0}") && !strings.Contains(pattern, "{0,0}"):
					t.Errorf("the scan rejects %q, and Go compiles it: it has %s", pattern, reason)
				}
			}
		}
	}
}

// The scan rejects what an engine other than Go reads differently, which
// the Go engine cannot show.
func TestScanRejectsWhatOtherEnginesReadDifferently(t *testing.T) {
	for pattern, portable := range map[string]bool{
		`^[a&&b]$`: false, `^[+--]$`: false, `^[a||b]$`: false, `^[a~~b]$`: false,
		`^[a&b]$`: true, `^[+-]$`: true, `^[a|b~]$`: true, `^[\-\-]$`: true,
		`^a{0}b$`: false, `^a{0,0}b$`: false, `^a{0,}b$`: true, `^a{0,1}b$`: true,
	} {
		if reason := notPortable(pattern); (reason == "") != portable {
			t.Errorf("the scan gives %q the reason %q, want portable %v", pattern, reason, portable)
		}
	}
}

// A reader of the contracts holds every number as a double, so a number that
// differs from an integer by less than a double can show is that integer.
func TestNumbersAreDoubles(t *testing.T) {
	schema, ok := loadContracts(t)[idPrefix+"policy:v1:bundle"]
	if !ok {
		t.Fatal("no bundle schema")
	}
	var bundle any
	text := `{"version":1.0000000000000001,"project_id":"prj","repos":[],"areas":[],"roles":{},"members":{}}`
	if err := json.Unmarshal([]byte(text), &bundle); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(bundle); err != nil {
		t.Errorf("a version of 1.0000000000000001 must be the integer 1: %v", err)
	}
}

// A number outside the range of a double is infinity. The verdicts are those
// of the reference: infinity is valid where a schema accepts any value, and
// invalid where a schema asks for a number or an integer.
func TestInfinity(t *testing.T) {
	schemas := loadContracts(t)
	cases := []struct {
		schema, text string
		valid        bool
	}{
		{"room:v1:envelope", `{"v":1,"type":"resume","sender":{"id":"dmn-ana","kind":"daemon"},"payload":1e400}`, true},
		{"room:v1:envelope", `{"v":1e400,"type":"resume","sender":{"id":"dmn-ana","kind":"daemon"},"payload":{}}`, false},
		{"policy:v1:signed-bundle", `{"version":3,"bundle":1e400,"signature":"s"}`, true},
		{"policy:v1:signed-bundle", `{"version":-1e400,"bundle":{},"signature":"s"}`, false},
	}
	for _, c := range cases {
		value, err := decode([]byte(c.text))
		if err != nil {
			t.Fatal(err)
		}
		if got := schemas[idPrefix+c.schema].Validate(value) == nil; got != c.valid {
			t.Errorf("%s: the schema %s gives valid=%v, the reference gives valid=%v", c.text, c.schema, got, c.valid)
		}
	}
}

// An example lives at spec/<contract>/v1/examples/<schema>/<name>.json and
// must validate against spec/<contract>/v1/<schema>.schema.json.
func TestExamplesValidate(t *testing.T) {
	schemas := loadContracts(t)
	examples := filesUnder(t, func(rel string) bool {
		return strings.Contains(rel, "/examples/") && strings.HasSuffix(rel, ".json")
	})
	covered := map[string]int{}
	for _, path := range examples {
		dir := filepath.Dir(path)
		id := idOf(filepath.Join(filepath.Dir(filepath.Dir(dir)), filepath.Base(dir)+schemaSuffix))
		schema, ok := schemas[id]
		if !ok {
			t.Errorf("%s: no schema %s", display(path), id)
			continue
		}
		covered[id]++
		if err := schema.Validate(readJSON(t, path)); err != nil {
			for _, leaf := range leaves(err) {
				t.Errorf("%s: %v", display(path), leaf)
			}
		}
	}
	for _, path := range schemaPaths(t) {
		if filepath.Dir(path) != specRoot && covered[idOf(path)] == 0 {
			t.Errorf("%s: the schema has no example", display(path))
		}
	}
	t.Logf("validated %d examples against %d schemas", len(examples), len(covered))
}
