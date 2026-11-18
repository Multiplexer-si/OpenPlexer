package agentcli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTOMLRendersAHookFileValue(t *testing.T) {
	var file struct {
		Hooks map[string]any `json:"hooks"`
	}
	text := `{"hooks":{"PreToolUse":[{"matcher":"Bash|apply_patch","hooks":[{"type":"command","command":"/a b/openplexer hook codex pre-tool --say \"hi\"","timeout":5,"async":false}]}]}}`
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	got, err := toml(file.Hooks["PreToolUse"])
	if err != nil {
		t.Fatal(err)
	}
	want := `[{hooks=[{async=false,command="/a b/openplexer hook codex pre-tool --say \"hi\"",timeout=5,type="command"}],matcher="Bash|apply_patch"}]`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := toml(nil); err == nil {
		t.Fatal("null has no TOML form")
	}
}
