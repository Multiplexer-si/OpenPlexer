package hook

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/install/*/*/installed.json from the current output")

const exe = "/usr/local/bin/openplexer"

var testJournal = func() Journal {
	dir, err := os.MkdirTemp("", "openplexer-journal")
	if err != nil {
		panic(err)
	}
	return Journal{Dir: dir}
}()

func handlerFor(agent string) Handler {
	if agent == "codex" {
		return Handler{Agent: "codex", Event: "PreToolUse", Matcher: "Bash|apply_patch", Command: exe + " hook codex pre-tool", Timeout: 5}
	}
	return Handler{Agent: "claude-code", Event: "PreToolUse", Matcher: "Bash|Edit|NotebookEdit|Write", Command: exe + " hook claude-code pre-tool", Timeout: 5, OnFailure: "block"}
}

type userEdit struct {
	name string
	fn   func(t *testing.T, s string) string
}

func none(_ *testing.T, s string) string { return s }

func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if strings.Count(s, old) != 1 {
		t.Fatalf("anchor %q found %d times in the file", old, strings.Count(s, old))
	}
	return strings.Replace(s, old, new, 1)
}

func insertAfter(t *testing.T, s, anchor, text string) string {
	t.Helper()
	return replaceOnce(t, s, anchor, anchor+text)
}

func insertBefore(t *testing.T, s, anchor, text string) string {
	t.Helper()
	return replaceOnce(t, s, anchor, text+anchor)
}

func timeoutTo30(agent string) userEdit {
	re := regexp.MustCompile(`(hook ` + agent + ` pre-tool"\s*,\s*"timeout":\s*)5\b`)
	return userEdit{"raise our timeout to 30", func(_ *testing.T, s string) string { return re.ReplaceAllString(s, "${1}30") }}
}

var matcherToBash = userEdit{"change our matcher to Bash", func(_ *testing.T, s string) string {
	re := regexp.MustCompile(`("matcher":\s*")Bash\|Edit\|NotebookEdit\|Write(")`)
	return re.ReplaceAllString(s, "${1}Bash${2}")
}}

func replaceEdit(name, old, new string) userEdit {
	return userEdit{name, func(t *testing.T, s string) string { return replaceOnce(t, s, old, new) }}
}

const (
	nl            = "\r\n"
	ourGroupStart = "," + nl + "\t\t\t{" + nl + "\t\t\t\t\"matcher\": \"Bash|Edit|NotebookEdit|Write\""
	ourGroupEnd   = nl + "\t\t],"
	userGroup     = "{" + nl + "\t\t\t\t\"matcher\": \"Read\"," + nl + "\t\t\t\t\"hooks\": [" + nl + "\t\t\t\t\t{" + nl +
		"\t\t\t\t\t\t\"type\": \"command\"," + nl + "\t\t\t\t\t\t\"command\": \"echo read\"" + nl + "\t\t\t\t\t}" + nl + "\t\t\t\t]" + nl + "\t\t\t}"
	preToolUseOpen = "\"PreToolUse\": [" + nl + "\t\t\t"
	userHandlerEnd = "\"timeout\": 10" + nl + "\t\t\t\t\t}"
)

func cutOurGroup(s string) (rest, group string) {
	i := strings.Index(s, ourGroupStart)
	if i < 0 {
		return s, ""
	}
	j := i + strings.Index(s[i:], ourGroupEnd)
	return s[:i] + s[j:], s[i+len(","+nl+"\t\t\t") : j]
}

var claudeEdits = []userEdit{
	{"no edit", none},
	timeoutTo30("claude-code"),
	matcherToBash,
	{"move our group to the front of PreToolUse", func(t *testing.T, s string) string {
		rest, group := cutOurGroup(s)
		if group == "" {
			return s
		}
		return insertAfter(t, rest, preToolUseOpen, group+","+nl+"\t\t\t")
	}},
	{"move our handler into the user's group", func(t *testing.T, s string) string {
		rest, group := cutOurGroup(s)
		if group == "" {
			return s
		}
		open := "\"hooks\": [" + nl + "\t\t\t\t\t"
		from := strings.Index(group, open) + len(open)
		to := strings.LastIndex(group, nl+"\t\t\t\t]")
		return insertAfter(t, rest, userHandlerEnd, ","+nl+"\t\t\t\t\t"+group[from:to])
	}},
	{"add a user group before ours", func(t *testing.T, s string) string {
		return insertAfter(t, s, preToolUseOpen, userGroup+","+nl+"\t\t\t")
	}},
	{"add a user group after ours", func(t *testing.T, s string) string {
		return insertBefore(t, s, ourGroupEnd+nl+"\t\t\"PostToolUse\"", ","+nl+"\t\t\t"+userGroup)
	}},
	{"add a Stop hook", func(t *testing.T, s string) string {
		stop := "," + nl + "\t\t\"Stop\": [" + nl + "\t\t\t{" + nl + "\t\t\t\t\"hooks\": [" + nl + "\t\t\t\t\t{" + nl +
			"\t\t\t\t\t\t\"type\": \"command\"," + nl + "\t\t\t\t\t\t\"command\": \"say done\"" + nl + "\t\t\t\t\t}" + nl + "\t\t\t\t]" + nl + "\t\t\t}" + nl + "\t\t]"
		return insertBefore(t, s, nl+"\t}"+","+nl+"\t\"cleanupPeriodDays\"", stop)
	}},
	replaceEdit("change an unrelated key", `"cleanupPeriodDays": 20`, `"cleanupPeriodDays": 7`),
	{"the user deletes our group by hand", func(t *testing.T, s string) string {
		rest, _ := cutOurGroup(s)
		return rest
	}},
}

type goldenCase struct {
	agent, dir string
	edits      []userEdit
}

var goldenCases = []goldenCase{
	{agent: "claude-code", dir: "realistic-tabs-crlf", edits: claudeEdits},
	{agent: "claude-code", dir: "four-space-lf", edits: []userEdit{
		{"no edit", none},
		timeoutTo30("claude-code"),
		replaceEdit("change an unrelated key", `"includeCoAuthoredBy": false`, `"includeCoAuthoredBy": true`),
		replaceEdit("add a user hook next to the Stop hook", `"command": "say done"`+"\n                    }",
			`"command": "say done"`+"\n                    },\n                    {\n                        \"type\": \"command\",\n                        \"command\": \"say bye\"\n                    }"),
	}},
	{agent: "claude-code", dir: "compact-one-line", edits: []userEdit{
		{"no edit", none},
		timeoutTo30("claude-code"),
		replaceEdit("change an unrelated key", `"Bash(ls)"`, `"Bash(pwd)"`),
	}},
	{agent: "claude-code", dir: "bom-two-space"},
	{agent: "claude-code", dir: "user-hook-named-like-ours"},
	{agent: "claude-code", dir: "missing", edits: []userEdit{{"no edit", none}, timeoutTo30("claude-code")}},
	{agent: "claude-code", dir: "empty-object"},
	{agent: "claude-code", dir: "empty-object-newline"},
	{agent: "claude-code", dir: "empty-pretooluse-compact"},
	{agent: "claude-code", dir: "blank-file"},
	{agent: "claude-code", dir: "bom-only"},
	{agent: "codex", dir: "crlf-two-space", edits: []userEdit{
		{"no edit", none},
		timeoutTo30("codex"),
		{"add a user group after ours", func(t *testing.T, s string) string {
			group := "{" + nl + "        \"matcher\": \"Read\"," + nl + "        \"hooks\": [" + nl + "          {" + nl + "            \"type\": \"command\"," + nl +
				"            \"command\": \"echo read\"" + nl + "          }" + nl + "        ]" + nl + "      }"
			return insertBefore(t, s, nl+"    ],"+nl+"    \"Stop\"", ","+nl+"      "+group)
		}},
	}},
	{agent: "codex", dir: "compact-one-line", edits: []userEdit{{"no edit", none}, timeoutTo30("codex")}},
	{agent: "codex", dir: "missing"},
}

func TestInstallUninstallGolden(t *testing.T) {
	for _, c := range goldenCases {
		t.Run(c.agent+"/"+c.dir, func(t *testing.T) {
			dir := filepath.Join("testdata", "install", c.agent, c.dir)
			before, err := os.ReadFile(filepath.Join(dir, "before.json"))
			exists := err == nil
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			h := handlerFor(c.agent)

			installed, change, added, err := Install(before, exists, h)
			if err != nil || change != Added {
				t.Fatalf("Install = %v, %v; want Added", change, err)
			}
			goldenPath := filepath.Join(dir, "installed.json")
			if *update {
				if err := os.WriteFile(goldenPath, installed, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(installed, golden) {
				t.Fatalf("Install output differs from installed.json\ngot  %q\nwant %q", installed, golden)
			}

			again, change, _, err := Install(installed, true, h)
			if err != nil || change != Unchanged || !bytes.Equal(again, installed) {
				t.Fatalf("second Install = %v, %v, changed bytes %v; want Unchanged and the same bytes", change, err, !bytes.Equal(again, installed))
			}

			edits := c.edits
			if edits == nil {
				edits = []userEdit{{"no edit", none}}
			}
			for _, e := range edits {
				t.Run(e.name, func(t *testing.T) {
					edited := e.fn(t, string(installed))
					out, remove, change, err := Uninstall([]byte(edited), c.agent, added)
					if err != nil {
						t.Fatal(err)
					}
					if !exists {
						if !remove || change != Removed {
							t.Fatalf("Uninstall = remove %v, %v; want the file that install created deleted", remove, change)
						}
						return
					}
					want := e.fn(t, string(before))
					if remove || string(out) != want {
						t.Fatalf("Uninstall of the edited file (remove=%v) differs from the edit of the original\ngot  %q\nwant %q", remove, out, want)
					}
					wantChange := Removed
					if e.name == "the user deletes our group by hand" {
						wantChange = Unchanged
					}
					if change != wantChange {
						t.Fatalf("Uninstall change = %v, want %v", change, wantChange)
					}
					out2, remove2, change2, err := Uninstall(out, c.agent, added)
					if err != nil || remove2 || change2 != Unchanged || !bytes.Equal(out2, out) {
						t.Fatalf("second Uninstall = %v, %v, %v; want Unchanged and the same bytes", remove2, change2, err)
					}
				})
			}
		})
	}
}

func TestUninstallRemovesOnlyTheContainersThatInstallAdded(t *testing.T) {
	ours := `{"type":"command","command":"/b/openplexer hook claude-code pre-tool"}`
	user := `{"type":"command","command":"mine"}`
	alone := `{"hooks":{"PreToolUse":[{"hooks":[` + ours + `]}]}}`
	cases := []struct {
		name, before string
		added        Level
		want         string
	}{
		{"a group with other handlers keeps them", `{"hooks":{"PreToolUse":[{"matcher":"x","hooks":[` + user + `,` + ours + `]}]}}`, AddedFile, `{"hooks":{"PreToolUse":[{"matcher":"x","hooks":[` + user + `]}]}}`},
		{"a group that emptied goes", `{"hooks":{"PreToolUse":[{"hooks":[` + user + `]},{"hooks":[` + ours + `]}]}}`, AddedUnknown, `{"hooks":{"PreToolUse":[{"hooks":[` + user + `]}]}}`},
		{"an event list that install added loses its key", `{"hooks":{"PreToolUse":[{"hooks":[` + ours + `]}],"Stop":[{"hooks":[` + user + `]}]}}`, AddedEventList, `{"hooks":{"Stop":[{"hooks":[` + user + `]}]}}`},
		{"an event list that install did not add stays empty", `{"hooks":{"PreToolUse":[{"hooks":[` + ours + `]}],"Stop":[{"hooks":[` + user + `]}]}}`, AddedGroup, `{"hooks":{"PreToolUse":[],"Stop":[{"hooks":[` + user + `]}]}}`},
		{"a hooks object that install added loses its key", `{"a":1,"hooks":{"PreToolUse":[{"hooks":[` + ours + `]}]}}`, AddedHooksObject, `{"a":1}`},
		{"a hooks object that install did not add stays empty", `{"a":1,"hooks":{"PreToolUse":[{"hooks":[` + ours + `]}]}}`, AddedEventList, `{"a":1,"hooks":{}}`},
		{"a top level that install did not add stays empty", alone, AddedHooksObject, `{}`},
		{"a hook with no record takes its containers and leaves the top level", alone, AddedUnknown, `{}`},
		{"a hook that install put into a list that was there leaves the list", alone, AddedGroup, `{"hooks":{"PreToolUse":[]}}`},
		{"a document that install wrote into a blank file goes, and the file stays", "  \n" + alone + "\n", AddedDocument, "  \n"},
		{"a list that was already empty stays", `{"hooks":{"Stop":[],"PreToolUse":[{"hooks":[` + ours + `]}]}}`, AddedFile, `{"hooks":{"Stop":[]}}`},
		{"ours under another event is removed too", `{"hooks":{"Stop":[{"hooks":[` + ours + `,` + user + `]}]}}`, AddedFile, `{"hooks":{"Stop":[{"hooks":[` + user + `]}]}}`},
		{"a handler for the other agent stays", `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/b/openplexer hook codex pre-tool"},` + ours + `]}]}}`, AddedFile, `{"hooks":{"PreToolUse":[{"hooks":[{"command":"/b/openplexer hook codex pre-tool"}]}]}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, remove, _, err := Uninstall([]byte(c.before), "claude-code", c.added)
			if err != nil || remove || string(out) != c.want {
				t.Fatalf("got %q remove=%v err=%v\nwant %q", out, remove, err, c.want)
			}
		})
	}
	t.Run("a file that install created and that holds nothing else goes", func(t *testing.T) {
		before := `{"hooks":{"PreToolUse":[{"hooks":[` + ours + `]}],"Stop":[{"hooks":[` + ours + `]}]}}`
		_, remove, change, err := Uninstall([]byte(before), "claude-code", AddedFile)
		if err != nil || !remove || change != Removed {
			t.Fatalf("got remove=%v change=%v err=%v", remove, change, err)
		}
	})
}

func TestWhatCountsAsOurs(t *testing.T) {
	h := handlerFor("claude-code")
	wrap := func(handler string) string {
		return `{"hooks":{"Stop":[{"hooks":[` + handler + `]}]}}`
	}
	cases := []struct {
		name, text string
		want       Change
		wantText   string
	}{
		{"wrapped in env, under another event", wrap(`{"command":"env A=1 /opt/openplexer hook claude-code pre-tool"}`), Unchanged, ""},
		{"split into command and args", wrap(`{"command":"/opt/openplexer","args":["hook","claude-code","pre-tool"]}`), Unchanged, ""},
		{"a Windows executable name", wrap(`{"command":"C:/tools/openplexer.exe hook claude-code pre-tool --quiet"}`), Unchanged, ""},
		{"another timeout and matcher still ours", `{"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"command":"/x/openplexer hook claude-code pre-tool --verbose","timeout":99}]}]}}`, Unchanged, ""},
		{"the other agent's handler is not ours", wrap(`{"command":"/opt/openplexer hook codex pre-tool"}`), Added, ""},
		{"a different program is not ours", wrap(`{"command":"/opt/other hook claude-code pre-tool"}`), Added, ""},
		{"a binary whose name only starts with openplexer is not ours", wrap(`{"command":"/opt/openplexer-lint hook claude-code --strict"}`), Added, ""},
		{"a binary with another extension is not ours", wrap(`{"command":"/opt/openplexer.test hook claude-code pre-tool"}`), Added, ""},
		{"words in two commands are not one handler", wrap(`{"command":"openplexer && hook claude-code"}`), Added, ""},
		{"a moved executable is repointed", wrap(`{"command":"/old/bin/openplexer hook claude-code pre-tool"}`), Repointed, wrap(`{"command":"` + h.Command + `"}`)},
		{"a moved executable in a wrapper is left alone", wrap(`{"command":"env A=1 /old/openplexer hook claude-code pre-tool"}`), Unchanged, ""},
		{"a group that is not an object is the user's", `{"hooks":{"PreToolUse":[1,{"hooks":"x"},{"matcher":"m"}]}}`, Added, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, change, added, err := Install([]byte(c.text), true, h)
			if err != nil || change != c.want {
				t.Fatalf("Install = %v, %v; want %v", change, err, c.want)
			}
			switch {
			case change == Unchanged && string(out) != c.text:
				t.Fatalf("Unchanged but the bytes moved: %q", out)
			case change == Repointed && string(out) != c.wantText:
				t.Fatalf("got %q, want %q", out, c.wantText)
			case change == Added:
				back, remove, _, err := Uninstall(out, "claude-code", added)
				if err != nil || remove || string(back) != c.text {
					t.Fatalf("Uninstall after Install gave %q (remove=%v, err=%v), want the original", back, remove, err)
				}
			}
		})
	}
}

func TestRefusals(t *testing.T) {
	ours := `{"type":"command","command":"/b/openplexer hook claude-code pre-tool"`
	cases := []struct{ name, text, want string }{
		{"trailing comma", "{\n  \"a\": 1,\n}", "invalid JSON at line 3, column 1"},
		{"a // comment", "{\n  // note\n  \"a\": 1\n}", "invalid JSON at line 2, column 3"},
		{"truncated", `{"hooks": {"PreToolUse": [`, "invalid JSON at line 1, column 26"},
		{"root is an array", `[]`, "the top level is array, not an object at line 1, column 1"},
		{"hooks is an array", "{\n  \"hooks\": []\n}", `"hooks" is array, not an object at line 2, column 12`},
		{"hooks is null", "{\n  \"hooks\": null\n}", `"hooks" is null, not an object at line 2, column 12`},
		{"PreToolUse is an object", `{"hooks":{"PreToolUse":{}}}`, "hooks.PreToolUse is object, not an array at line 1, column 24"},
		{"duplicate hooks key", "{\n  \"hooks\": {},\n  \"hooks\": {}\n}", `duplicate key "hooks" at the top level at line 3, column 3`},
		{"duplicate top-level key", `{"a":1,"a":2}`, `duplicate key "a" at the top level at line 1, column 8`},
		{"duplicate event key", `{"hooks":{"Stop":[],"Stop":[]}}`, `duplicate key "Stop" in "hooks" at line 1, column 21`},
		{"duplicate command key in our handler", `{"hooks":{"PreToolUse":[{"hooks":[` + ours + `,"command":"x"}]}]}}`, `duplicate key "command" in hooks.PreToolUse[0].hooks[0] at line 1, column 105`},
		{"duplicate key in a group", `{"hooks":{"Stop":[{"matcher":"a","matcher":"b"}]}}`, `duplicate key "matcher" in hooks.Stop[0] at line 1, column 34`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(c.text), 0o600); err != nil {
				t.Fatal(err)
			}
			for name, run := range map[string]func() (Change, error){
				"install":   func() (Change, error) { return testJournal.Install(path, handlerFor("claude-code"), "") },
				"uninstall": func() (Change, error) { return testJournal.Uninstall(path, "claude-code") },
			} {
				change, err := run()
				if err == nil || !strings.Contains(err.Error(), c.want) || change != Unchanged {
					t.Errorf("%s: change %v, error %v; want a refusal naming %q", name, change, err, c.want)
				}
				if got, _ := os.ReadFile(path); string(got) != c.text {
					t.Errorf("%s: the file changed to %q", name, got)
				}
			}
		})
	}
}

func TestJournalInstall(t *testing.T) {
	h := handlerFor("claude-code")

	t.Run("a symlink stays and its target is edited", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "dotfiles", "settings.json")
		link := filepath.Join(dir, "settings.json")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("{\n  \"a\": 1\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("dotfiles", "settings.json"), link); err != nil {
			t.Fatal(err)
		}
		if change, err := testJournal.Install(link, h, ""); err != nil || change != Added {
			t.Fatalf("Install = %v, %v", change, err)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the symlink was replaced: %v %v", info, err)
		}
		got, _ := os.ReadFile(target)
		if !strings.Contains(string(got), "openplexer hook claude-code pre-tool") {
			t.Fatalf("target not edited: %q", got)
		}
		if change, err := testJournal.Uninstall(link, "claude-code"); err != nil || change != Removed {
			t.Fatalf("Uninstall = %v, %v", change, err)
		}
		if got, _ := os.ReadFile(target); string(got) != "{\n  \"a\": 1\n}\n" {
			t.Fatalf("target after uninstall: %q", got)
		}
	})

	t.Run("a linked file that held only our entry is emptied, not deleted", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "dotfiles", "settings.json")
		link := filepath.Join(dir, "settings.json")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := testJournal.Install(link, h, ""); err != nil {
			t.Fatal(err)
		}
		if change, err := testJournal.Uninstall(link, "claude-code"); err != nil || change != Removed {
			t.Fatalf("Uninstall = %v, %v", change, err)
		}
		if got, err := os.ReadFile(link); err != nil || string(got) != "{}\n" {
			t.Fatalf("the linked file holds %q (%v), want an empty object", got, err)
		}
	})

	t.Run("a dangling symlink gets its target created", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "settings.json")
		if err := os.Symlink("real.json", link); err != nil {
			t.Fatal(err)
		}
		if _, err := testJournal.Install(link, h, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "real.json")); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("the symlink was replaced")
		}
	})

	t.Run("the mode of an existing file is kept", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := testJournal.Install(path, h, ""); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, want 0600", info.Mode().Perm())
		}
	})

	t.Run("a new file gets 0644 and its parent directories", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a", "b", "settings.json")
		if change, err := testJournal.Install(path, h, ""); err != nil || change != Added {
			t.Fatalf("Install = %v, %v", change, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("stat = %v, %v", info, err)
		}
	})

	t.Run("nothing is written when nothing changes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if _, err := testJournal.Install(path, h, ""); err != nil {
			t.Fatal(err)
		}
		first, _ := os.Stat(path)
		if change, err := testJournal.Install(path, h, ""); err != nil || change != Unchanged {
			t.Fatalf("Install = %v, %v", change, err)
		}
		second, _ := os.Stat(path)
		if !os.SameFile(first, second) || !first.ModTime().Equal(second.ModTime()) {
			t.Fatal("the file was rewritten")
		}
	})

	t.Run("uninstall deletes a file that held only our entry and ignores a missing one", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "settings.json")
		if change, err := testJournal.Uninstall(path, "claude-code"); err != nil || change != Unchanged {
			t.Fatalf("on a missing file: %v, %v", change, err)
		}
		if _, err := testJournal.Install(path, h, ""); err != nil {
			t.Fatal(err)
		}
		if change, err := testJournal.Uninstall(path, "claude-code"); err != nil || change != Removed {
			t.Fatalf("Uninstall = %v, %v", change, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("file still there: %v", err)
		}
	})
}

func TestALaterRunRemovesTheTemporaryFileOfAKilledRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	stale := filepath.Join(dir, ".settings.json.openplexer-123.tmp")
	fresh := filepath.Join(dir, ".settings.json.openplexer-456.tmp")
	other := filepath.Join(dir, ".other.json.openplexer-789.tmp")
	for _, name := range []string{stale, fresh, other} {
		if err := os.WriteFile(name, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleAfter)
	for _, name := range []string{stale, other} {
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testJournal.Install(path, handlerFor("claude-code"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old temporary file of this hook file is still there: %v", err)
	}
	for _, name := range []string{fresh, other} {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s is gone, want a file that a live run may hold, and a file of another hook file, kept: %v", filepath.Base(name), err)
		}
	}
}

func TestFileChangedWhileEditing(t *testing.T) {
	const concurrent = "{\"claude wrote this\": true}\n"
	for _, c := range []struct {
		name   string
		before string
		run    func(path string, steal func()) error
	}{
		{"install", "{}\n", func(path string, steal func()) error {
			_, err := editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
				out, change, _, err := Install(text, exists, handlerFor("claude-code"))
				steal()
				return out, false, change, err
			})
			return err
		}},
		{"uninstall", `{"a":1,"hooks":{"PreToolUse":[{"hooks":[{"command":"/b/openplexer hook claude-code pre-tool"}]}]}}`, func(path string, steal func()) error {
			_, err := editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
				out, remove, change, err := Uninstall(text, "claude-code", AddedFile)
				steal()
				return out, remove, change, err
			})
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(path, []byte(c.before), 0o644); err != nil {
				t.Fatal(err)
			}
			err := c.run(path, func() {
				if err := os.WriteFile(path, []byte(concurrent), 0o644); err != nil {
					t.Fatal(err)
				}
			})
			if err == nil || !strings.Contains(err.Error(), "changed while openplexer edited it") {
				t.Fatalf("error = %v, want one saying the file changed", err)
			}
			if got, _ := os.ReadFile(path); string(got) != concurrent {
				t.Fatalf("the concurrent content was overwritten: %q", got)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Fatalf("a temp file was left behind: %v", entries)
			}
		})
	}
}
