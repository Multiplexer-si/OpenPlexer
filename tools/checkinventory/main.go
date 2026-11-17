// Command checkinventory checks docs/inventory.md against a checkout of the
// existing TypeScript implementation.
//
// The page must have every section of layout, each with a table that has the
// expected header and at least the expected number of rows. A table under any
// other heading, an HTML table, and a line that contains a pipe outside a
// table are errors.
//
// In every row, Scope is in, out, or undecided, and Kind, where the table has
// that column, is limitation, accident, or possible bug. A row of a table with
// an Evidence column needs evidence. A path is a code span whose first
// directory is a top-level directory of the reference, and it must be a file
// at the commit. In Evidence, a later code span is a symbol of at least three
// characters that must occur as a word in the file of the path before it. A
// double-quoted string must be part of the title of exactly one test or
// describe block in that file that is not skipped, and must not name a
// tracker id, an environment variable, the product, or its command. A test
// file in Evidence needs at least one title. Paths and titles in Notes are
// checked the same way.
//
// Usage:
//
//	go run ./tools/checkinventory -reference /path/to/checkout [-commit 7d80045a] [-file docs/inventory.md]
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

type section struct {
	name    string
	header  string
	minRows int
}

const (
	component    = "Behavior | Evidence | Scope | Notes"
	undocumented = "Behavior | Evidence | Scope | Kind"
)

var layout = []section{
	{"Policy evaluator and glob dialect", component, 53},
	{"Signed policy distribution", component, 39},
	{"Daemon gate, overrides, offline and stale policy", component, 36},
	{"Claude Code adapter", component, 41},
	{"Codex adapter", component, 16},
	{"Hook install and uninstall", component, 16},
	{"Local stripping of secrets", component, 9},
	{"Room event format and transport envelope", component, 38},
	{"Claims", component, 20},
	{"Decisions", component, 17},
	{"Hub Room sequencing", component, 17},
	{"Audit chain and verification", component, 23},
	{"Audit export", component, 8},
	{"Behavior that is undocumented or accidental", undocumented, 25},
	{"Out of scope", "Component | Scope", 11},
}

var (
	separator = regexp.MustCompile(`^\|?\s*:?-{3,}`)
	citation  = regexp.MustCompile("`([^`]+)`|\"([^\"]+)\"")
	escaped   = regexp.MustCompile(`\\([[:punct:]])`)
	declared  = regexp.MustCompile("(?:^|[^\\w.$])(it|test|describe)((?:\\.\\w+|\\([^()]*\\))*)\\(\\s*([\"'`])")
	skips     = regexp.MustCompile(`\.(skip|skipIf|runIf|todo)\b`)
	eachRow   = regexp.MustCompile("(?m)^\\s*\\[\\s*([\"'`])")
	private   = regexp.MustCompile(`#\d|\b[A-Z]+-?\d+\b|\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b|(?i:multiplexer|\bmpx)`)
	scopes    = []string{"in", "out", "undecided"}
	kinds     = []string{"limitation", "accident", "possible bug"}
)

func main() {
	dir := flag.String("reference", "", "path to a checkout of the existing implementation (required)")
	commit := flag.String("commit", "7d80045a", "commit of the existing implementation that the inventory records")
	file := flag.String("file", "docs/inventory.md", "inventory to check")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "checkinventory: -reference is required")
		os.Exit(2)
	}
	ref, err := open(*dir, *commit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkinventory:", err)
		os.Exit(2)
	}
	doc, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkinventory:", err)
		os.Exit(2)
	}
	c := check(*file, string(doc), ref, layout)
	for _, f := range c.failures {
		fmt.Println(f)
	}
	if len(c.failures) > 0 {
		os.Exit(1)
	}
	fmt.Printf("%s: %d rows ok (in %d, out %d, undecided %d; limitation %d, accident %d, possible bug %d)\n",
		*file, c.scopes["in"]+c.scopes["out"]+c.scopes["undecided"], c.scopes["in"], c.scopes["out"], c.scopes["undecided"],
		c.kinds["limitation"], c.kinds["accident"], c.kinds["possible bug"])
}

type reference struct {
	dir, commit string
	roots       map[string]bool
	files       map[string]*source
}

type source struct {
	text   string
	titles []title
}

type title struct {
	text    string
	skipped bool
}

func open(dir, commit string) (*reference, error) {
	out, err := exec.Command("git", "-C", dir, "ls-tree", "-d", "--name-only", commit).Output()
	if err != nil {
		return nil, fmt.Errorf("cannot list the directories of %s at %s: %v", dir, commit, err)
	}
	r := &reference{dir: dir, commit: commit, roots: map[string]bool{}, files: map[string]*source{}}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		r.roots[name] = true
	}
	return r, nil
}

func (r *reference) isPath(s string) bool {
	root, _, ok := strings.Cut(s, "/")
	return ok && r.roots[root]
}

// file returns the file at the commit, or nil when the path is not a file there.
func (r *reference) file(path string) *source {
	if s, ok := r.files[path]; ok {
		return s
	}
	var s *source
	if out, err := exec.Command("git", "-C", r.dir, "cat-file", "blob", r.commit+":"+path).Output(); err == nil {
		s = &source{text: string(out), titles: titles(string(out))}
	}
	r.files[path] = s
	return s
}

// titles returns the title of every test and describe block in a vitest file.
// A title is skipped when its block, or a describe block around it, is
// declared with skip, skipIf, runIf, or todo. When the file has an each block
// whose title is a format such as "%s", the first string of each table row
// that starts a line is a title too.
func titles(src string) []title {
	var out []title
	skipUntil := -1
	each := false
	for _, m := range declared.FindAllStringSubmatchIndex(src, -1) {
		start := m[2]
		text, ok := literal(src[m[1]:], src[m[6]])
		if !ok {
			continue
		}
		skipped := start < skipUntil || skips.MatchString(src[m[4]:m[5]])
		if skipped && start >= skipUntil && src[m[2]:m[3]] == "describe" {
			line := src[strings.LastIndexByte(src[:start], '\n')+1:]
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			skipUntil = len(src)
			if end := strings.Index(src[start:], "\n"+indent+"}"); end != -1 {
				skipUntil = start + end
			}
		}
		each = each || strings.Contains(src[m[4]:m[5]], ".each") && strings.Contains(text, "%")
		out = append(out, title{text, skipped})
	}
	if each {
		for _, m := range eachRow.FindAllStringSubmatchIndex(src, -1) {
			if text, ok := literal(src[m[1]:], src[m[2]]); ok {
				out = append(out, title{text, false})
			}
		}
	}
	return out
}

// literal reads the rest of a string literal up to its closing quote.
func literal(s string, quote byte) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
			if i < len(s) {
				b.WriteByte(s[i])
			}
		case quote:
			return b.String(), true
		default:
			b.WriteByte(s[i])
		}
	}
	return "", false
}

type checker struct {
	file          string
	ref           *reference
	failures      []string
	scopes, kinds map[string]int
}

func (c *checker) fail(n int, format string, args ...any) {
	where := c.file
	if n > 0 {
		where = fmt.Sprintf("%s:%d", c.file, n)
	}
	c.failures = append(c.failures, where+": "+fmt.Sprintf(format, args...))
}

func check(file, doc string, ref *reference, layout []section) *checker {
	c := &checker{file: file, ref: ref, scopes: map[string]int{}, kinds: map[string]int{}}
	rows := map[string]int{}
	seen := map[string]bool{}
	current := ""
	var header []string
	fenced := false
	lines := strings.Split(doc, "\n")
	for i := 0; i < len(lines); i++ {
		n, line := i+1, strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "```") {
			fenced, header = !fenced, nil
			continue
		}
		if fenced {
			continue
		}
		if name, ok := strings.CutPrefix(line, "## "); ok {
			current, header = name, nil
			seen[name] = true
			continue
		}
		if strings.Contains(strings.ToLower(line), "<table") {
			c.fail(n, "an HTML table; write a Markdown table")
		}
		switch {
		case header != nil && strings.HasPrefix(line, "|"):
			rows[current]++
			c.row(n, header, cells(line))
		case strings.HasPrefix(line, "|") && i+1 < len(lines) && separator.MatchString(strings.TrimSpace(lines[i+1])):
			header = cells(line)
			i++
			k := slices.IndexFunc(layout, func(s section) bool { return s.name == current })
			if k == -1 {
				c.fail(n, "a table under %q, which is not a section of the inventory", current)
			} else if got := strings.Join(header, " | "); got != layout[k].header {
				c.fail(n, "the header is %q, want %q", got, layout[k].header)
			}
		default:
			header = nil
			if strings.Contains(line, "|") {
				c.fail(n, "a line that contains | outside a table")
			}
		}
	}
	for _, s := range layout {
		if !seen[s.name] {
			c.fail(0, "the section %q is missing", s.name)
		} else if rows[s.name] < s.minRows {
			c.fail(0, "the section %q has %d rows, fewer than %d", s.name, rows[s.name], s.minRows)
		}
	}
	return c
}

// cells splits a table row on the pipes that are not escaped.
func cells(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	var out []string
	start := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' {
			i++
		} else if line[i] == '|' {
			out = append(out, strings.TrimSpace(line[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(line[start:]))
}

func (c *checker) row(n int, header, row []string) {
	if len(row) != len(header) {
		c.fail(n, "the row has %d cells and the header %d", len(row), len(header))
		return
	}
	cell := map[string]string{}
	for i, h := range header {
		cell[h] = row[i]
	}
	fail := func(format string, args ...any) { c.fail(n, "%q: %s", row[0], fmt.Sprintf(format, args...)) }
	if scope := cell["Scope"]; slices.Contains(scopes, scope) {
		c.scopes[scope]++
	} else {
		fail("scope %q is not in, out, or undecided", scope)
	}
	if kind, ok := cell["Kind"]; ok {
		if slices.Contains(kinds, kind) {
			c.kinds[kind]++
		} else {
			fail("kind %q is not limitation, accident, or possible bug", kind)
		}
	}
	ev, ok := cell["Evidence"]
	if !ok {
		return
	}
	if ev == "" {
		fail("the row has no evidence")
		return
	}
	path := c.cite(fail, ev, "", true)
	c.cite(fail, cell["Notes"], path, false)
}

// cite checks the paths, symbols, and test titles of one cell, starting from
// the file of path, and returns the last path.
func (c *checker) cite(fail func(string, ...any), text, path string, evidence bool) string {
	untitled := ""
	for _, m := range citation.FindAllStringSubmatch(text, -1) {
		code, name := m[1], escaped.ReplaceAllString(m[2], "$1")
		switch {
		case c.ref.isPath(code):
			if untitled != "" {
				fail("%s needs a test title", untitled)
			}
			path, untitled = code, ""
			if c.ref.file(path) == nil {
				fail("%s is not a file at %s", path, c.ref.commit)
			} else if evidence && strings.Contains(path, ".test.") {
				untitled = path
			}
		case name != "" && private.MatchString(name):
			untitled = ""
			fail("%q names a tracker id, an environment variable, or the product; quote another part of the title", name)
		case !evidence && name == "":
		case path == "":
			fail("%q comes before any path", code+name)
		case name != "":
			untitled = ""
			c.title(fail, path, name)
		case len([]rune(code)) < 3:
			fail("the symbol %q is shorter than three characters", code)
		default:
			c.symbol(fail, path, code)
		}
	}
	if untitled != "" {
		fail("%s needs a test title", untitled)
	}
	if evidence && path == "" {
		fail("the evidence names no path")
	}
	return path
}

func (c *checker) title(fail func(string, ...any), path, name string) {
	src := c.ref.file(path)
	if src == nil {
		return
	}
	found, skipped := 0, 0
	for _, t := range src.titles {
		if !strings.Contains(t.text, name) {
			continue
		}
		if t.skipped {
			skipped++
		} else {
			found++
		}
	}
	switch {
	case found > 1:
		fail("%q is part of %d test titles in %s; quote more of one", name, found, path)
	case found == 0 && skipped > 0:
		fail("%q is the title of a skipped test in %s", name, path)
	case found == 0:
		fail("%q is not part of a test title in %s", name, path)
	}
}

func (c *checker) symbol(fail func(string, ...any), path, sym string) {
	src := c.ref.file(path)
	word := regexp.MustCompile(`(^|[^\w$])` + regexp.QuoteMeta(sym) + `($|[^\w$])`)
	if src != nil && !word.MatchString(src.text) {
		fail("%q does not occur as a word in %s", sym, path)
	}
}
