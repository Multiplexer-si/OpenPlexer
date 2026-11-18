package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/hub"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestAdminWorkflow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	cmd := commands(t, dir)
	cmd(1, "serve")
	cmd(0, "init")
	cmd(1, "init")
	cmd(1, "add-member", "-id", "wrong-role", "-role", "unknown")

	source, err := os.ReadFile("../../spec/policy/v1/examples/bundle/full.json")
	if err != nil {
		t.Fatal(err)
	}
	example, err := policy.ParseBundle(source)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := os.ReadFile("../../spec/policy/v1/examples/bundle/minimal.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(publish, &document); err != nil {
		t.Fatal(err)
	}
	// The full example supplies the identities and repo name. Scope the
	// minimal example's empty repository list to that repository.
	repo := example.Repos[0]
	document["repos"] = []string{repo}
	document["members"] = map[string]string{"mem-ana": example.Members["mem-ana"]}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(source, &full); err != nil {
		t.Fatal(err)
	}
	document["roles"] = full["roles"]
	bundle := marshal(t, document)
	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(path, bundle, 0600); err != nil {
		t.Fatal(err)
	}

	cmd(1, "publish-policy", "-project", example.ProjectID, "-file", path)
	member := cmd(0, "add-member", "-id", "mem-ana")
	lead := cmd(0, "add-member", "-id", "mem-lee", "-role", "lead")
	cmd(1, "add-member", "-id", "mem-ana")
	cmd(0, "add-project", "-id", example.ProjectID, "-repo", repo)
	cmd(1, "add-project", "-id", example.ProjectID, "-repo", example.Repos[1])
	cmd(1, "serve", "-listen", "invalid-listen-address")
	assertInvalidPolicyFixture(t, cmd, example.ProjectID, repo)
	handler := hub.Open(dir).Handler()

	projectURL := "/api/v1/projects/" + example.ProjectID
	bundleURL := projectURL + "/policy.bundle"
	request(t, handler, "GET", bundleURL, "", nil, http.StatusUnauthorized)
	request(t, handler, "GET", bundleURL, member, nil, http.StatusNotFound)
	project := request(t, handler, "GET", projectURL, member, nil, http.StatusOK)
	var scope hub.Project
	if err := json.Unmarshal(project, &scope); err != nil || scope.Repo != repo || scope.ID != example.ProjectID {
		t.Fatalf("project scope = %s, error = %v", project, err)
	}

	cmd(0, "publish-policy", "-project", example.ProjectID, "-file", path)
	request(t, handler, "PUT", projectURL+"/policy", member, bundle, http.StatusForbidden)
	request(t, handler, "PUT", projectURL+"/policy", lead, source, http.StatusBadRequest)
	// Repeating the same publication returns the existing signature.
	original := request(t, handler, "GET", bundleURL, member, nil, http.StatusOK)
	retry := request(t, handler, "PUT", projectURL+"/policy", lead, bundle, http.StatusOK)
	if !bytes.Equal(original, retry) {
		t.Fatal("repeating publication changed the signed bundle")
	}

	keys := request(t, handler, "GET", "/.well-known/jwks.json", "", nil, http.StatusOK)
	verify(t, original, keys, bundle)
	document["version"] = example.Version
	newer := marshal(t, document)
	request(t, handler, "PUT", projectURL+"/policy", lead, newer, http.StatusOK)
	request(t, handler, "PUT", projectURL+"/policy", lead, bundle, http.StatusConflict)
	// A freshly opened store serves the new policy with the same public key.
	handler = hub.Open(dir).Handler()
	verify(t, request(t, handler, "GET", bundleURL, member, nil, http.StatusOK), keys, newer)

	stored, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0600 {
		t.Fatalf("state permissions = %v", fileInfo.Mode().Perm())
	}
	for _, token := range []string{member, lead} {
		if token == "" || bytes.Contains(stored, []byte(token)) {
			t.Fatal("token is empty or was stored in plaintext")
		}
	}
	cmd(0, "remove-member", "-id", "mem-ana")
	cmd(1, "remove-member", "-id", "mem-ana")
	request(t, handler, "GET", bundleURL, member, nil, http.StatusUnauthorized)
	request(t, handler, "GET", bundleURL, lead, nil, http.StatusOK)
}

func assertInvalidPolicyFixture(t *testing.T, cmd func(int, ...string) string, project, repo string) {
	t.Helper()
	source, err := os.ReadFile("../../spec/policy/v1/fixtures/validate-bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			Name  string
			Input map[string]any
		}
	}
	if err := json.Unmarshal(source, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixtures.Cases {
		if c.Name == "a missing members is invalid" {
			c.Input["repos"] = []string{repo}
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, marshal(t, c.Input), 0600); err != nil {
				t.Fatal(err)
			}
			cmd(1, "publish-policy", "-project", project, "-file", path)
			return
		}
	}
	t.Fatal("missing invalid bundle fixture")
}

func TestUsage(t *testing.T) {
	cmd := commands(t, t.TempDir())
	cmd(0, "-h")
	cmd(0, "serve", "-h")
	cmd(2, "unknown")
	cmd(2, "init", "extra-argument")
	cmd(2, "add-project", "-unknown")
}

func commands(t *testing.T, dir string) func(int, ...string) string {
	t.Helper()
	return func(want int, args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if args[0] != "-h" {
			args = append([]string{args[0], "-data-dir", dir}, args[1:]...)
		}
		if got := run(args, &stdout, &stderr); got != want {
			t.Fatalf("%v returned %d, want %d (%s)", args, got, want, &stderr)
		}
		return strings.TrimSpace(stdout.String())
	}
}

func request(t *testing.T, h http.Handler, method, path, token string, body []byte, want int) []byte {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != want {
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, res.Code, want, res.Body)
	}
	return res.Body.Bytes()
}

func verify(t *testing.T, signed, keys, source []byte) {
	t.Helper()
	var message hub.SignedBundle
	if err := json.Unmarshal(signed, &message); err != nil {
		t.Fatal(err)
	}
	set, err := policy.ParseKeySet(keys)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := policy.VerifySignedBundle(message.Signature, set)
	if err != nil {
		t.Fatal(err)
	}
	want, err := audit.Canonical(source)
	if err != nil {
		t.Fatal(err)
	}
	display, err := audit.Canonical(message.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(display, want) || !bytes.Equal(verified.Payload, want) || message.Version != verified.Bundle.Version {
		t.Fatalf("signed bundle display or canonical payload differs from the source: %s", signed)
	}
}

func marshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
