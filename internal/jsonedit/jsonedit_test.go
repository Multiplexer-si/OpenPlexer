package jsonedit_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/jsonedit"
)

type layout struct {
	unit   string
	eol    string
	colon  string
	sep    string
	final  bool
	inline func() bool
}

type gen struct {
	r *rand.Rand
	l layout
}

type field struct {
	key string
	val any
}

func (g *gen) scalar() (text string, want any) {
	switch g.r.IntN(8) {
	case 0:
		return "1.0", 1.0
	case 1:
		return "1e3", 1000.0
	case 2:
		return `"caf\u00e9 <&>"`, "café <&>"
	case 3:
		return "true", true
	case 4:
		return "null", nil
	case 5:
		return `"a\"b\\c"`, `a"b\c`
	}
	n := g.r.IntN(100)
	return fmt.Sprint(n), float64(n)
}

func (g *gen) value(depth int, level int) (text string, want any) {
	if depth == 0 || g.r.IntN(3) == 0 {
		return g.scalar()
	}
	count := g.r.IntN(4)
	isObj := g.r.IntN(2) == 0
	open, closer := "[", "]"
	if isObj {
		open, closer = "{", "}"
	}
	if count == 0 {
		if isObj {
			return "{}", map[string]any{}
		}
		return "[]", []any{}
	}
	multi := !g.l.inline()
	var parts []string
	var wantArr []any
	wantObj := map[string]any{}
	for i := range count {
		t, w := g.value(depth-1, level+1)
		if isObj {
			key := fmt.Sprintf("k%d", i)
			parts = append(parts, `"`+key+`"`+g.l.colon+t)
			wantObj[key] = w
		} else {
			parts = append(parts, t)
			wantArr = append(wantArr, w)
		}
	}
	var sb strings.Builder
	sb.WriteString(open)
	pad := strings.Repeat(g.l.unit, level+1)
	for i, p := range parts {
		if multi {
			sb.WriteString(g.l.eol + pad)
		}
		sb.WriteString(p)
		if i < len(parts)-1 {
			if multi {
				sb.WriteString(",")
			} else {
				sb.WriteString(g.l.sep)
			}
		}
	}
	if multi {
		sb.WriteString(g.l.eol + strings.Repeat(g.l.unit, level))
	}
	sb.WriteString(closer)
	if isObj {
		return sb.String(), wantObj
	}
	return sb.String(), wantArr
}

func newDoc(seed uint64) (src string, want any) {
	r := rand.New(rand.NewPCG(seed, 1))
	g := &gen{r: r}
	g.l.unit = []string{"\t", "  ", "    "}[r.IntN(3)]
	g.l.eol = []string{"\n", "\r\n"}[r.IntN(2)]
	g.l.colon = []string{": ", ":"}[r.IntN(2)]
	g.l.sep = []string{", ", ","}[r.IntN(2)]
	g.l.final = r.IntN(2) == 0
	mode := r.IntN(3)
	g.l.inline = func() bool { return mode == 0 || (mode == 2 && r.IntN(2) == 0) }
	var text string
	for {
		text, want = g.value(4, 0)
		if text[0] == '{' || text[0] == '[' {
			break
		}
	}
	if g.l.final {
		text += g.l.eol
	}
	return text, want
}

func check(t *testing.T, d *jsonedit.Doc, n jsonedit.Node, want any) {
	t.Helper()
	var got any
	if err := json.Unmarshal(d.Text(n), &got); err != nil {
		t.Fatalf("node bytes %q do not parse: %v", d.Text(n), err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("node bytes %q decode to %#v, want %#v", d.Text(n), got, want)
	}
	switch w := want.(type) {
	case map[string]any:
		if n.Kind != jsonedit.Object || len(n.Members) != len(w) {
			t.Fatalf("node %q: kind %v with %d members, want object of %d", d.Text(n), n.Kind, len(n.Members), len(w))
		}
		for _, m := range n.Members {
			check(t, d, m.Value, w[m.Key])
		}
	case []any:
		if n.Kind != jsonedit.Array || len(n.Elems) != len(w) {
			t.Fatalf("node %q: kind %v with %d elements, want array of %d", d.Text(n), n.Kind, len(n.Elems), len(w))
		}
		for i, e := range n.Elems {
			check(t, d, e, w[i])
		}
	}
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("not valid JSON: %v\n%q", err, b)
	}
	return v
}

func containers(n jsonedit.Node, path []int, out *[][]int) {
	switch n.Kind {
	case jsonedit.Object:
		*out = append(*out, slicesClone(path))
		for i, m := range n.Members {
			containers(m.Value, append(path, i), out)
		}
	case jsonedit.Array:
		*out = append(*out, slicesClone(path))
		for i, e := range n.Elems {
			containers(e, append(path, i), out)
		}
	}
}

func slicesClone(p []int) []int { return append([]int(nil), p...) }

func at(n jsonedit.Node, path []int) jsonedit.Node {
	for _, i := range path {
		if n.Kind == jsonedit.Object {
			n = n.Members[i].Value
		} else {
			n = n.Elems[i]
		}
	}
	return n
}

type pair struct {
	A string `json:"matcher"`
	B []int  `json:"hooks"`
}

func TestRandomDocuments(t *testing.T) {
	for seed := range uint64(400) {
		src, want := newDoc(seed)
		d, err := jsonedit.Parse([]byte(src))
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		check(t, d, d.Root(), want)

		var paths [][]int
		containers(d.Root(), nil, &paths)
		r := rand.New(rand.NewPCG(seed, 2))
		for range 4 {
			path := paths[r.IntN(len(paths))]
			c := at(d.Root(), path)
			var e jsonedit.Edit
			if c.Kind == jsonedit.Array {
				e, err = d.AppendElem(c, pair{"Bash|Edit&&<x>", []int{1, 2}})
			} else {
				e, err = d.AppendMember(c, "zz", pair{"Bash|Edit&&<x>", []int{1, 2}})
			}
			if err != nil {
				t.Fatalf("seed %d path %v: %v", seed, path, err)
			}
			added, err := d.Apply(e)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(src, "\r\n") && strings.Count(string(added), "\n") != strings.Count(string(added), "\r\n") {
				t.Fatalf("seed %d: a bare LF in a CRLF file\n%q", seed, added)
			}
			if !strings.Contains(string(added), "Bash|Edit&&<x>") {
				t.Fatalf("seed %d: value escaped or lost\n%q", seed, added)
			}
			d2, err := jsonedit.Parse(added)
			if err != nil {
				t.Fatalf("seed %d path %v: result is not JSON: %v\n%q", seed, path, err, added)
			}
			c2 := at(d2.Root(), path)
			last := len(c2.Elems) - 1
			if c2.Kind == jsonedit.Object {
				last = len(c2.Members) - 1
			}
			rm, err := d2.RemoveChild(c2, last)
			if err != nil {
				t.Fatal(err)
			}
			back, err := d2.Apply(rm)
			if err != nil {
				t.Fatal(err)
			}
			if string(back) != src {
				t.Fatalf("seed %d path %v: append then remove changed the bytes\nbefore %q\nafter  %q", seed, path, src, back)
			}

			if n := len(c.Elems) + len(c.Members); n > 0 {
				i := r.IntN(n)
				rm, err := d.RemoveChild(c, i)
				if err != nil {
					t.Fatal(err)
				}
				cut, err := d.Apply(rm)
				if err != nil {
					t.Fatal(err)
				}
				decode(t, cut)
			}
		}
	}
}

func TestRemoveChildTakesOneSeparator(t *testing.T) {
	cases := []struct {
		name, src string
		path      []int
		i         int
		want      string
	}{
		{"first of three", "[\n\t1,\n\t2,\n\t3\n]", nil, 0, "[\n\t2,\n\t3\n]"},
		{"middle of three", "[\n\t1,\n\t2,\n\t3\n]", nil, 1, "[\n\t1,\n\t3\n]"},
		{"last of three", "[\n\t1,\n\t2,\n\t3\n]", nil, 2, "[\n\t1,\n\t2\n]"},
		{"only child", "{\"a\": [\n  1\n]}", []int{0}, 0, "{\"a\": []}"},
		{"first member inline", `{"a":1, "b":2}`, nil, 0, `{"b":2}`},
	}
	for _, c := range cases {
		d, err := jsonedit.Parse([]byte(c.src))
		if err != nil {
			t.Fatal(err)
		}
		e, err := d.RemoveChild(at(d.Root(), c.path), c.i)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := d.Apply(e)
		if string(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAppendFollowsTheStyleOfTheContainer(t *testing.T) {
	v := pair{"a&&b", []int{1}}
	cases := []struct {
		name, src, want string
		member          bool
	}{
		{"tabs and CRLF", "[\r\n\t1\r\n]", "[\r\n\t1,\r\n\t{\r\n\t\t\"matcher\": \"a&&b\",\r\n\t\t\"hooks\": [\r\n\t\t\t1\r\n\t\t]\r\n\t}\r\n]", false},
		{"four spaces", "{\n    \"a\": 1\n}", "{\n    \"a\": 1,\n    \"k\": {\n        \"matcher\": \"a&&b\",\n        \"hooks\": [\n            1\n        ]\n    }\n}", true},
		{"compact", `{"a":1}`, `{"a":1,"k":{"matcher":"a&&b","hooks":[1]}}`, true},
		{"inline copies the separator", `[1,  2]`, `[1,  2,  {"matcher": "a&&b",  "hooks": [1]}]`, false},
		{"empty without any newline stays compact", `[]`, `[{"matcher": "a&&b", "hooks": [1]}]`, false},
		{"empty in a file with lines expands", "{\n\t\"a\": []\n}", "{\n\t\"a\": [\n\t\t{\n\t\t\t\"matcher\": \"a&&b\",\n\t\t\t\"hooks\": [\n\t\t\t\t1\n\t\t\t]\n\t\t}\n\t]\n}", false},
	}
	for _, c := range cases {
		d, err := jsonedit.Parse([]byte(c.src))
		if err != nil {
			t.Fatal(err)
		}
		target := d.Root()
		if c.name == "empty in a file with lines expands" {
			target = target.Members[0].Value
		}
		var e jsonedit.Edit
		if c.member {
			e, err = d.AppendMember(target, "k", v)
		} else {
			e, err = d.AppendElem(target, v)
		}
		if err != nil {
			t.Fatal(err)
		}
		got, _ := d.Apply(e)
		if string(got) != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestParseRefusesWhatIsNotStrictJSON(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"trailing comma", "{\n  \"a\": 1,\n}", "line 3"},
		{"comment", "{\n// no\n}", "line 2"},
		{"truncated", `{"a": [1, 2`, "line 1"},
		{"empty", ``, "invalid JSON"},
	}
	for _, c := range cases {
		_, err := jsonedit.Parse([]byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one naming %q", c.name, err, c.want)
		}
	}
}

func TestGetRefusesDuplicateKeysAndBOMSurvives(t *testing.T) {
	d, err := jsonedit.Parse([]byte("\ufeff{\"a\": 1, \"\\u0061\": 2}"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Root().Get("a"); err == nil {
		t.Error("Get on a duplicated key returned no error")
	}
	e, err := d.RemoveChild(d.Root(), 1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := d.Apply(e)
	if !bytes.Equal(got, []byte("\ufeff{\"a\": 1}")) {
		t.Errorf("got %q", got)
	}
}

func TestApplyRefusesOverlappingEdits(t *testing.T) {
	d, _ := jsonedit.Parse([]byte(`[1, 2, 3]`))
	if _, err := d.Apply(jsonedit.Edit{Start: 1, End: 5, Text: ""}, jsonedit.Edit{Start: 4, End: 6, Text: ""}); err == nil {
		t.Error("overlapping edits were applied")
	}
}
