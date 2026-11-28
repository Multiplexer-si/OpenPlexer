package hubconformance

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

func (f *fixture) signingContinuity(t *testing.T) {
	pin := func(name, path string) []byte {
		t.Helper()
		if *signingPins == "" {
			return f.call(t, get, path, "", "", http.StatusOK).body
		}
		data, err := os.ReadFile(filepath.Join(*signingPins, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	policyKeys, err := policy.ParseKeySet(pin("policy.jwks.json", policyKeysPath))
	if err != nil {
		t.Fatal("invalid pinned policy JWKS")
	}
	auditKeys := pin("audit.jwks.json", auditKeysPath)
	id := *project
	repo := f.repo(t, id)
	var current signedBundle
	decodeWire(t, f.call(t, get, projectPath(id)+"/policy.bundle", f.member, "", http.StatusOK).body, &current)
	version := current.Version + 1
	published := f.call(t, put, projectPath(id)+"/policy", f.lead, bundleJSON(id, repo, version, ""), http.StatusOK)
	verifyBundle(t, published, policyKeys, id, repo, version)

	res := f.call(t, get, auditPath, f.lead, "", http.StatusOK)
	result := audit.VerifyExport(res.body, auditKeys, res.header.Get(checkpoint))
	if !result.OK || result.Completeness != "through_checkpoint" ||
		result.CheckpointEntries == nil || *result.CheckpointEntries != result.Entries {
		t.Fatalf("export did not verify through its checkpoint with the pinned keys (rule %s)", result.Rule)
	}
}
