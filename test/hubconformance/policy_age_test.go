package hubconformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/testbin"
)

func (f *fixture) hookPolicyAge(t *testing.T) {
	projectID := *project
	path, repo := projectPath(projectID), f.repo(t, projectID)
	var current struct{ Version int }
	res := f.call(t, get, path+"/policy.bundle", f.member, "", http.StatusOK)
	if err := json.Unmarshal(res.body, &current); err != nil || current.Version <= 0 {
		t.Fatal("could not read the current policy version")
	}
	publish := func(version int, grants string) {
		body := fmt.Sprintf(`{"version":%d,"project_id":%q,"repos":[%q],"areas":[{"name":"source","patterns":[%q]}],"roles":{"dev":{"read":[],"write":[%s],"exec":[]}},"members":{"member":"dev"}}`,
			version, projectID, repo, repo+":src/**", grants)
		f.call(t, put, path+"/policy", f.lead, body, http.StatusOK)
	}
	home, checkout, user := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	keys := f.call(t, get, policyKeysPath, "", "", http.StatusOK).body
	if err := daemon.Configure(home, daemon.Settings{
		HubURL: f.origin, ProjectID: projectID, MemberID: "member", Token: f.member, KeySet: keys, MaxPolicyAgeSeconds: 60,
	}); err != nil {
		t.Fatalf("configure hook home: %s", f.redact(err.Error()))
	}
	publish(current.Version+1, `"source"`)
	old := time.Now().Add(-2 * time.Minute)
	gate, err := daemon.Open(home, func() time.Time { return old })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Sync(t.Context()); err != nil {
		t.Fatalf("initial sync: %s", f.redact(err.Error()))
	}
	query := policy.Query{Action: policy.Write, Path: "src/app.ts"}
	verdict, err := gate.Decide("claude-code", query)
	result, ok := verdict.Result()
	if err != nil || !ok || result.Decision != policy.Allow {
		t.Fatal("initial policy did not allow the write")
	}
	publish(current.Version+2, "")

	dir, err := testbin.Dir("openplexer")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "openplexer")
	if dir == "" {
		binary = filepath.Join(t.TempDir(), "openplexer")
		build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./apps/openplexer")
		build.Dir = filepath.Join("..", "..")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build openplexer: %v\n%s", err, output)
		}
	}
	cmd := exec.CommandContext(t.Context(), binary, "--home", home, "hook", "claude-code", "pre-tool")
	cmd.Dir = checkout
	cmd.Env = append(os.Environ(), "HOME="+user, "CLAUDE_CONFIG_DIR="+filepath.Join(user, ".claude"), "CODEX_HOME="+filepath.Join(user, ".codex"))
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"hook_event_name":"PreToolUse","cwd":%q,"tool_name":"Write","tool_input":{"file_path":"src/app.ts"}}`, checkout))
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte(`"permissionDecision":"deny"`)) || bytes.Contains(output, []byte("policy is stale")) {
		t.Fatalf("real hook should deny under the refreshed policy: %v, %s", err, f.redact(string(output)))
	}
	gate, err = daemon.Open(home, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if status := gate.Status(); status.State != daemon.Verified || status.Version != int64(current.Version+2) {
		t.Fatalf("hook did not cache the new fresh policy: %+v", status)
	}
}
