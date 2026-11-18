package agentcli

import "testing"

func TestClaudeSlugMatchesTheProjectsDirectoryName(t *testing.T) {
	got := claudeSlug("/private/var/folders/xy/T/realagent-123/project")
	if want := "-private-var-folders-xy-T-realagent-123-project"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
