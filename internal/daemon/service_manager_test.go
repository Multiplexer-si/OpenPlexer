package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdDefinitionQuotesPaths(t *testing.T) {
	s := Service{platform: "linux", Home: "/tmp/Lee's \"home\" %n\\\t"}
	got, err := s.definition("/opt/Lee app %n $bin/openplexer")
	if err != nil {
		t.Fatal(err)
	}
	want := `ExecStart=":/opt/Lee app %%n $bin/openplexer" daemon --quiet
Environment="OPENPLEXER_HOME=/tmp/Lee's \"home\" %%n\\\t"
`
	if !strings.Contains(string(got), want) {
		t.Fatalf("generated unit has incorrect path lines:\n%s", got)
	}
}

func TestSystemdDefinitionRejectsUnsupportedPaths(t *testing.T) {
	for _, tt := range []struct {
		name, binary, home, hint string
	}{
		{"backslash in executable", "/opt/a\\b/openplexer", "/tmp/home", "without quotes"},
		{"control in executable", "/opt/a\tb/openplexer", "/tmp/home", "without quotes"},
		{"invalid UTF-8 in executable", "/opt/a\xffb/openplexer", "/tmp/home", "without quotes"},
		{"NUL in home", "/opt/openplexer", "/tmp/a\x00b", "OPENPLEXER_HOME"},
		{"invalid UTF-8 in home", "/opt/openplexer", "/tmp/a\xffb", "OPENPLEXER_HOME"},
		{"Unicode noncharacter in home", "/opt/openplexer", "/tmp/a\U0001fffeb", "OPENPLEXER_HOME"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := Service{platform: "linux", Home: tt.home}
			if unit, err := s.definition(tt.binary); len(unit) != 0 || err == nil || !strings.Contains(err.Error(), tt.hint) {
				t.Fatalf("definition = %q, %v; want refusal mentioning %q", unit, err, tt.hint)
			}
		})
	}
}

func TestSystemdInstallRefusesQuotesBeforeWritingUnit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	home := filepath.Join(dir, "home")
	serviceTestHome(t, home)
	runner := &fakeServiceRunner{t: t}
	s, err := newService(home, "linux", runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{`/opt/Lee's app %n/openplexer`, `/opt/Lee's "app"/openplexer`, `/opt/Lee "app"/openplexer`} {
		runner.calls = []serviceCall{missingUnit(s.Name)}
		if err := s.Install(binary); err == nil || !strings.Contains(err.Error(), "Install the binary in a path without quotes") {
			t.Fatalf("install %q: %v; want a path without quotes", binary, err)
		}
		if _, err := os.Lstat(s.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected install left a unit file: %v", err)
		}
	}
}
