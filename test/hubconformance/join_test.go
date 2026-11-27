package hubconformance

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/projects"
	"github.com/multiplexer-si/openplexer/internal/testbin"
)

func joinBinary(t *testing.T) string {
	t.Helper()
	dir, err := testbin.Dir("openplexer")
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		return filepath.Join(dir, "openplexer")
	}
	bin := filepath.Join(t.TempDir(), "openplexer")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./apps/openplexer")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build openplexer: %v\n%s", err, out)
	}
	return bin
}

type joinCheckout struct {
	bin, user, root string
}

func newJoinCheckout(t *testing.T, bin, repo string) joinCheckout {
	t.Helper()
	dir := t.TempDir()
	j := joinCheckout{bin, filepath.Join(dir, "user"), filepath.Join(dir, "checkout")}
	for _, path := range []string{j.user, j.root} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://git.example.test/" + repo + ".git"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = j.root
		cmd.Env = append(cmd.Environ(), "HOME="+j.user, "GIT_CONFIG_NOSYSTEM=1")
		if err := cmd.Run(); err != nil {
			t.Fatal("could not prepare a clean checkout")
		}
	}
	return j
}

func (j joinCheckout) run(t *testing.T, expected int, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), j.bin, args...)
	cmd.Dir = j.root
	cmd.Env = append(cmd.Environ(), "HOME="+j.user, "OPENPLEXER_HOME=", "XDG_CONFIG_HOME="+filepath.Join(j.user, "config"))
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	status := 0
	if e, ok := err.(*exec.ExitError); ok {
		status = e.ExitCode()
	} else if err != nil {
		t.Fatal("could not run the join CLI")
	}
	if status != expected {
		t.Fatalf("openplexer %s exited %d, want %d", args[0], status, expected)
	}
	return string(out)
}

func (j joinCheckout) join(t *testing.T, expected int, origin, code, pin string) string {
	t.Helper()
	out := j.run(t, expected, origin+"/join/"+code+"#fp="+pin+"\n", "join", "--link-stdin")
	if strings.Contains(out, code) {
		t.Fatal("join printed an invite secret")
	}
	return out
}

func mustReport(t *testing.T, output string, expected ...string) {
	t.Helper()
	for _, line := range expected {
		if !strings.Contains(output, line) {
			t.Errorf("openplexer omitted %q", line)
		}
	}
}

func (f *fixture) joinFingerprint(t *testing.T) string {
	t.Helper()
	fp, err := policy.FingerprintKeySet(f.call(t, get, policyKeysPath, "", "", 200).body)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func (f *fixture) join(t *testing.T) {
	const repo = "team/demo"
	id, member := "join-"+rand.Text(), "developer-"+rand.Text()
	f.call(t, post, "/api/v1/admin/projects", f.lead, fmt.Sprintf(`{"id":%q,"repo":%q}`, id, repo), 200)
	policyPath := projectPath(id) + "/policy"
	f.call(t, put, policyPath, f.lead, bundleJSON(id, repo, 1, ""), 200)

	fp := f.joinFingerprint(t)
	bin := joinBinary(t)
	checkout := newJoinCheckout(t, bin, repo)
	first := f.issueInvite(t, createInviteJSON(id, member, 60, true))
	wrongPin := strings.Repeat("0", 64)
	if wrongPin == fp {
		wrongPin = strings.Repeat("1", 64)
	}
	mustReport(t, checkout.join(t, 1, f.origin, first.Code, wrongPin), "fingerprint")
	mustReport(t, checkout.run(t, 0, "", "join", "--discard-pending"), "no pending join")
	if _, err := os.Stat(filepath.Join(checkout.user, ".openplexer", "projects.json")); !os.IsNotExist(err) {
		t.Fatal("wrong pin published a registry")
	}

	mustReport(t, checkout.join(t, 1, f.origin, first.Code, fp), "pending_approval: "+first.Invite.ID)
	f.call(t, post, invitePath+"/"+first.Invite.ID+"/approve", f.lead, "", 200)
	mustReport(t, checkout.join(t, 0, f.origin, first.Code, fp), "project: "+id, "member: "+member,
		"hub role: member", "policy: verified", "policy version: 1", "no policy role yet")
	mustReport(t, checkout.run(t, 5, "", "policy", "check", "write", "src/main.go"), "deny:")
	mustReport(t, checkout.run(t, 0, "", "join", "--discard-pending"), "no pending join")

	repoBundle := func(version int, assigned bool) string {
		membership := "{}"
		if assigned {
			membership = fmt.Sprintf(`{%q:"developer"}`, member)
		}
		return fmt.Sprintf(`{"project_id":%q,"repos":[%q],"version":%d,
			"areas":[{"name":"source","patterns":["src/**"]}],
			"roles":{"developer":{"read":[],"write":["source"],"exec":[]}},"members":%s}`, id, repo, version, membership)
	}
	f.call(t, put, policyPath, f.lead, repoBundle(2, true), 200)
	mustReport(t, checkout.run(t, 0, "", "policy", "sync"), "policy: verified", "version 2")
	mustReport(t, checkout.run(t, 0, "", "policy", "check", "write", "src/main.go"), "allow:")
	f.call(t, put, policyPath, f.lead, repoBundle(3, false), 200)
	mustReport(t, checkout.run(t, 0, "", "policy", "sync"), "policy: verified", "version 3")
	mustReport(t, checkout.run(t, 5, "", "policy", "check", "write", "src/main.go"), "deny:")

	other := newJoinCheckout(t, bin, repo)
	mustReport(t, other.join(t, 1, f.origin, newInviteBearer(), fp), "HTTP 404")
	mustReport(t, other.run(t, 0, "", "join", "--discard-pending"), "discarded")
	mustReport(t, other.join(t, 1, f.origin, first.Code, fp), "HTTP 409")
	mustReport(t, other.run(t, 0, "", "join", "--discard-pending"), "discarded")
	replacement := f.issueInvite(t, createInviteJSON(id, "replacement-"+rand.Text(), 60, false))
	mustReport(t, other.join(t, 0, f.origin, replacement.Code, fp), "policy: verified", "policy version: 3")

	r, err := projects.Read(checkout.user)
	if err != nil || len(r.Projects()) != 1 {
		t.Fatal("join did not publish the checkout registry")
	}
}

func (f *fixture) joinRotation(t *testing.T) {
	if f.target != localWrangler {
		t.Skip("staging and activation use the Worker key rotation API")
	}
	const repo = "team/rotation"
	id, member := "rotation-"+rand.Text(), "developer-"+rand.Text()
	f.call(t, post, "/api/v1/admin/projects", f.lead, fmt.Sprintf(`{"id":%q,"repo":%q}`, id, repo), 200)
	policyPath := projectPath(id) + "/policy"
	f.call(t, put, policyPath, f.lead, bundleJSON(id, repo, 1, ""), 200)
	invite := f.issueInvite(t, createInviteJSON(id, member, 300, true))
	oldPin := f.joinFingerprint(t)
	checkout := newJoinCheckout(t, joinBinary(t), repo)
	mustReport(t, checkout.join(t, 1, f.origin, invite.Code, oldPin), "pending_approval: "+invite.Invite.ID)
	const keys = "/api/v1/admin/keys/policy"
	var staged struct {
		KID string `json:"kid"`
	}
	decodeWire(t, f.call(t, post, keys, f.lead, "", 200).body, &staged)
	stagedPin := f.joinFingerprint(t)
	if staged.KID == "" || oldPin == stagedPin {
		t.Fatal("staging did not change the policy pin")
	}
	mustReport(t, checkout.join(t, 1, f.origin, invite.Code, oldPin), "fingerprint")
	f.call(t, post, invitePath+"/"+invite.Invite.ID+"/approve", f.lead, "", 200)
	f.call(t, put, keys+"/active", f.lead, fmt.Sprintf(`{"kid":%q}`, staged.KID), 200)
	if f.joinFingerprint(t) != stagedPin {
		t.Fatal("activation changed the staged policy pin")
	}
	f.call(t, put, policyPath, f.lead, bundleJSON(id, repo, 2, ""), 200)
	mustReport(t, checkout.join(t, 0, f.origin, invite.Code, stagedPin), "policy: verified", "policy version: 2", "no policy role yet")
}
