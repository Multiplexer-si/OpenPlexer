package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var fixture = map[string]string{
	"packages/protocol/src/glob.ts": "export function toRegExp(glob: string): RegExp {\n  const e = new RegExp(glob);\n  return e;\n}\n",
	"packages/protocol/test/policy.test.ts": `// Design doc sec 12.2 example policy
import { describe, it } from "vitest";

describe("policy evaluate", () => {
  it("unmatched read allow", () => {});
  it(
    "unmatched write deny",
    () => {},
  );
  it.skip("a skipped case", () => {});
  it("#141: a revoked override is dropped", () => {});
  it("MP_HOME is protected", () => {});
});

describe.skipIf(true)("needs a binary", () => {
  it("runs the real binary", () => {});
});
`,
}

// The inventory writes a backtick as ^, because a Go raw string cannot hold one.
var inventory = strings.ReplaceAll(`# Inventory

## Policy

| Behavior | Evidence | Scope | Notes |
|---|---|---|---|
| A read of a path in no area is allowed. | ^packages/protocol/test/policy.test.ts^ "unmatched read allow" | in | |
| A write of a path in no area is denied. | ^packages/protocol/test/policy.test.ts^ "unmatched write deny" | in | Only writes are denied by default. |
| Patterns compile to regular expressions. | ^packages/protocol/src/glob.ts^ ^toRegExp^ | in | |

## Undocumented

| Behavior | Evidence | Scope | Kind |
|---|---|---|---|
| Reads are allowed by default. See [Policy](#policy). | ^packages/protocol/test/policy.test.ts^ "unmatched read allow" | in | limitation |

## Out of scope

| Component | Scope |
|---|---|
| Dashboard | out |
`, "^", "`")

var testLayout = []section{
	{"Policy", component, 3},
	{"Undocumented", undocumented, 1},
	{"Out of scope", "Component | Scope", 1},
}

func fakeReference(t *testing.T) *reference {
	t.Helper()
	dir := t.TempDir()
	for name, text := range fixture {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ref, err := open(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestInventoryPasses(t *testing.T) {
	c := check("inventory.md", inventory, fakeReference(t), testLayout)
	if len(c.failures) > 0 {
		t.Fatalf("want no failures, got:\n%s", strings.Join(c.failures, "\n"))
	}
	if c.scopes["in"] != 4 || c.scopes["out"] != 1 || c.kinds["limitation"] != 1 {
		t.Errorf("want in 4, out 1, limitation 1, got scopes %v and kinds %v", c.scopes, c.kinds)
	}
}

func TestMutationsFail(t *testing.T) {
	replace := func(old, new string) func(string) string {
		return func(doc string) string {
			if !strings.Contains(doc, old) {
				t.Fatalf("the inventory has no %q", old)
			}
			return strings.Replace(doc, old, new, 1)
		}
	}
	title := func(name string) func(string) string { return replace(`"unmatched write deny"`, name) }
	appendText := func(text string) func(string) string {
		return func(doc string) string { return doc + strings.ReplaceAll(text, "^", "`") }
	}
	inToOut := func(doc string) string {
		lines := strings.Split(doc, "\n")
		for i, line := range lines {
			if row := cells(line); len(row) == 4 && row[2] == "in" {
				row[1], row[2] = "", "out"
				lines[i] = "| " + strings.Join(row, " | ") + " |"
			}
		}
		return strings.Join(lines, "\n")
	}
	bogusRow := "| Bogus. | ^apps/nope.ts^ | banana | |\n"
	tests := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{"a title that occurs only in a comment", title(`"Design doc sec 12.2 example policy"`), "is not part of a test title"},
		{"a common string", title(`"import"`), "is not part of a test title"},
		{"a part of two titles", title(`"unmatched"`), "is part of 2 test titles"},
		{"a skipped test", title(`"a skipped case"`), "is the title of a skipped test"},
		{"a test in a skipped describe block", title(`"runs the real binary"`), "is the title of a skipped test"},
		{"a one-letter symbol", replace("`toRegExp`", "`e`"), "shorter than three characters"},
		{"a symbol inside a longer word", replace("`toRegExp`", "`RegEx`"), "does not occur as a word"},
		{"a directory as evidence", replace("`packages/protocol/src/glob.ts`", "`packages/protocol/`"), "is not a file"},
		{"a path that does not exist", replace("`packages/protocol/src/glob.ts`", "`packages/protocol/src/nope.ts`"), "is not a file"},
		{"a bogus path in Notes", replace("Only writes are denied by default.", "See `packages/protocol/src/nope.ts`."), "is not a file"},
		{"a bogus title in Notes", replace("Only writes are denied by default.", `See "no such test".`), "is not part of a test title"},
		{"a test file without a title", replace(`policy.test.ts`+"`"+` "unmatched read allow"`, `policy.test.ts`+"`"), "needs a test title"},
		{"evidence without a path", replace("`packages/protocol/src/glob.ts` `toRegExp`", "`toRegExp`"), "comes before any path"},
		{"an unknown scope", replace("| in | |", "| banana | |"), "is not in, out, or undecided"},
		{"an unknown kind", replace("| limitation |", "| quirk |"), "is not limitation, accident, or possible bug"},
		{"a deleted section", replace("## Undocumented", "## Something else"), `the section "Undocumented" is missing`},
		{"a file reduced to a heading", func(string) string { return "# nothing\n" }, `the section "Policy" is missing`},
		{"a deleted row", replace("| Patterns compile to regular expressions. | `packages/protocol/src/glob.ts` `toRegExp` | in | |\n", ""), "fewer than 3"},
		{"every in row flipped to out without evidence", inToOut, "the row has no evidence"},
		{"a changed header", replace("| Behavior | Evidence | Scope | Kind |", "| Behavior | Evidence | Scope |"), "the header is"},
		{"a table under another heading", appendText("\n## Extra\n\n| Behavior | Evidence | Scope | Notes |\n|---|---|---|---|\n" + bogusRow), "not a section of the inventory"},
		{"a table inside a blockquote", appendText("\n> | Behavior | Evidence | Scope | Notes |\n> |---|---|---|---|\n> " + bogusRow), "outside a table"},
		{"a table without leading pipes", appendText("\nBehavior | Evidence | Scope | Notes\n---|---|---|---\nBogus. | ^apps/nope.ts^ | banana |\n"), "outside a table"},
		{"an HTML table", appendText("\n<table><tr><td>^apps/nope.ts^</td><td>banana</td></tr></table>\n"), "an HTML table"},
		{"a single row after a blank line", replace("\n\n## Undocumented", "\n\n"+strings.ReplaceAll(bogusRow, "^", "`")+"\n## Undocumented"), "outside a table"},
		{"a tracker id with a hash", title(`"#141: a revoked override"`), "names a tracker id"},
		{"a requirement id", title(`"R24: a pending spend"`), "names a tracker id"},
		{"a requirement id with a hyphen", title(`"NFR-4: hub unreachable"`), "names a tracker id"},
		{"an environment variable", title(`"MP_HOME is protected"`), "names a tracker id"},
		{"the product name", title(`"the Multiplexer home"`), "names a tracker id"},
		{"the command name", title(`"mpx install"`), "names a tracker id"},
	}
	ref := fakeReference(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := check("inventory.md", tt.mutate(inventory), ref, testLayout)
			for _, f := range c.failures {
				t.Log(f)
				if strings.Contains(f, tt.want) {
					return
				}
			}
			t.Errorf("want a failure that contains %q", tt.want)
		})
	}
}
