package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/room"
)

func TestAuditAppendCommitsWithItsAction(t *testing.T) {
	s, token := roomStore(t, "project", "member")
	before := snapshot(t, s)
	for _, action := range []func() error{
		func() error { _, err := s.AddMember("member", Member); return err },
		func() error { return s.RemoveMember("unknown") },
		func() error { return s.AddProject("project", "other-repo") },
		func() error { _, err := s.PublishPolicy("project", []byte("{}")); return err },
	} {
		if err := action(); err == nil {
			t.Fatal("action should have failed")
		}
	}
	h := s.Handler()
	path := eventsURL("project")
	wantStatus(t, call(h, "POST", path, token, []byte(`{"type":"policy.denied"}`), ""), http.StatusBadRequest)
	override := []byte(`{"type":"policy.override","body":{"lead":"lead","agent":"agent","scope":"mission","areas":[],"actions":[],"reason":"test"}}`)
	wantStatus(t, call(h, "POST", path, token, override, ""), http.StatusForbidden)
	if after := snapshot(t, s); !bytes.Equal(before.Export, after.Export) {
		t.Fatal("a failed action changed the audit log")
	}

	hash := sha256.Sum256([]byte(token))
	denial := encode(t, map[string]any{
		"type":  "policy.denied",
		"agent": map[string]string{"id": "session", "vendor": "codex"},
		"body":  map[string]string{"action": "write", "path": token, "rule": hex.EncodeToString(hash[:])},
	})
	event := eventResponse(t, call(h, "POST", path, token, denial, "same"))
	retry := eventResponse(t, call(h, "POST", path, token, denial, "same"))
	if event.ID() != retry.ID() {
		t.Fatal("idempotent post created a second event")
	}
	if err := s.RemoveMember("member"); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, s)
	var wire struct{ Entries []auditEntry }
	decode(t, after.Export, &wire)
	var actions []string
	for _, entry := range wire.Entries {
		actions = append(actions, entry.Action)
	}
	want := []string{"member.removed", "event.policy-denied", "member.added", "project.added"}
	if !slices.Equal(actions, want) {
		t.Fatalf("actions = %v, want %v", actions, want)
	}
	entry := wire.Entries[1]
	if entry.Target != "event:"+event.ID() || entry.Result != "denied" ||
		entry.ActorMemberID == nil || *entry.ActorMemberID != "member" ||
		entry.AgentMemberID == nil || *entry.AgentMemberID != "member" ||
		entry.AgentSessionID == nil || *entry.AgentSessionID != "member/session" || len(entry.Meta) != 3 {
		t.Errorf("denial record = %+v", entry)
	}
	for _, secret := range []string{token, hex.EncodeToString(hash[:]), base64.RawURLEncoding.EncodeToString(hash[:]), string(encode(t, hash))} {
		if bytes.Contains(after.Export, []byte(secret)) {
			t.Fatal("audit export contains authentication material")
		}
	}
}

func TestRoomDenialRejectsInvalidDecisionTime(t *testing.T) {
	s, token := roomStore(t, "project", "member")
	s.now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 999999, time.UTC) }
	h := s.Handler()
	path := eventsURL("project")
	post := func(decidedAt string) []byte {
		return fmt.Appendf(nil, `{"type":"policy.denied","agent":{"id":"session","vendor":"codex"},"body":{"action":"write","path":"src/main.go","rule":"locked","decided_at":%q},"at":"spoofed"}`, decidedAt)
	}
	before := snapshot(t, s)
	for _, invalid := range []string{"yesterday", "2026-01-15T12:00:30.001Z"} {
		wantStatus(t, call(h, http.MethodPost, path, token, post(invalid), "denial"), http.StatusBadRequest)
	}
	if after := snapshot(t, s); !bytes.Equal(before.Export, after.Export) {
		t.Fatal("invalid decision time changed the audit log")
	}
	const atLimit = "2026-01-15T12:00:30.000Z"
	event := eventResponse(t, call(h, http.MethodPost, path, token, post(atLimit), "denial"))
	if event.Seq() != 1 || event.At() != "2026-01-15T12:00:00.000Z" || event.Body().(*room.PolicyDenied).DecidedAt != atLimit {
		t.Fatalf("unexpected event %s at %s with body %+v", event.ID(), event.At(), event.Body())
	}
}

func TestAuditAppendFailureLeavesStateUnchanged(t *testing.T) {
	s, token := roomStore(t, "project", "member")
	// Force the audit append to fail after the action has updated pending state.
	if err := s.update(func(st *state) error {
		st.WorkspaceID = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	readState := func() []byte {
		t.Helper()
		data, err := os.ReadFile(s.path())
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	if _, err := s.AddMember("other", Member); err == nil {
		t.Fatal("member addition should have failed")
	}
	body := []byte(`{"type":"policy.denied","agent":{"id":"session","vendor":"codex"},"body":{"action":"write","path":"src/main.go","rule":"locked"}}`)
	wantStatus(t, call(s.Handler(), "POST", eventsURL("project"), token, body, ""), http.StatusInternalServerError)
	if !bytes.Equal(before, readState()) {
		t.Fatal("a failed audit append left an action or record on disk")
	}
}

func TestConcurrentAuditAppendsFormOneChain(t *testing.T) {
	s := Open(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	const count = 32
	start := make(chan struct{})
	results := make(chan error, count)
	for i := range count {
		go func() {
			<-start
			results <- Open(s.dir).AddProject(fmt.Sprintf("project-%d", i), "repo")
		}()
	}
	close(start)
	for range count {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	got := snapshot(t, s)
	var wire struct{ Entries []auditEntry }
	decode(t, got.Export, &wire)
	if len(wire.Entries) != count {
		t.Fatalf("export has %d entries, want %d", len(wire.Entries), count)
	}
	for i, entry := range wire.Entries {
		if entry.ID != fmt.Sprint(count-i) || entry.WorkspaceID != wire.Entries[0].WorkspaceID || entry.Result != "added" {
			t.Errorf("entry %d has unexpected ID, workspace or result: %+v", i, entry)
		}
	}
}

func TestAuditExportRequiresLeadAndPublishesSeparateKeys(t *testing.T) {
	s, member := roomStore(t, "project", "member")
	lead, err := s.AddMember("lead", Lead)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	const path = "/api/v1/audit/export"
	for _, denied := range []struct {
		token  string
		status int
	}{{member, http.StatusForbidden}, {"", http.StatusUnauthorized}} {
		res := call(h, "GET", path, denied.token, nil, "")
		wantStatus(t, res, denied.status)
		if res.Header().Get(auditCheckpointHeader) != "" {
			t.Error("unauthorized response included a checkpoint")
		}
	}
	res := call(h, "GET", path, lead, nil, "")
	wantStatus(t, res, http.StatusOK)
	keys := call(h, "GET", "/.well-known/audit-jwks.json", "", nil, "")
	wantStatus(t, keys, http.StatusOK)
	policy := call(h, "GET", "/.well-known/jwks.json", "", nil, "")
	wantStatus(t, policy, http.StatusOK)
	token := res.Header().Get(auditCheckpointHeader)
	if result := audit.VerifyExport(res.Body.Bytes(), keys.Body.Bytes(), token); !result.OK || result.CheckpointEntries == nil || *result.CheckpointEntries != 3 {
		t.Fatalf("HTTP snapshot failed verification: %+v", result)
	}
	if result := audit.VerifyExport(res.Body.Bytes(), policy.Body.Bytes(), token); result.Rule != "K3" {
		t.Fatalf("policy keys verified audit checkpoint: %+v", result)
	}
	decodeKeys := func(raw []byte) []map[string]string {
		t.Helper()
		var jwks checkpointJWKS
		decode(t, raw, &jwks)
		return jwks.Keys
	}
	checkpointKey := decodeKeys(keys.Body.Bytes())[0]
	policyKey := decodeKeys(policy.Body.Bytes())[0]
	if checkpointKey["kid"] == policyKey["kid"] || checkpointKey["x"] == policyKey["x"] || checkpointKey["d"] != "" {
		t.Fatal("checkpoint JWKS did not publish a separate public key")
	}

	if err := s.update((*state).addCheckpointKey); err != nil {
		t.Fatal(err)
	}
	newKeys := call(Open(s.dir).Handler(), "GET", "/.well-known/audit-jwks.json", "", nil, "")
	wantStatus(t, newKeys, http.StatusOK)
	if keys := decodeKeys(newKeys.Body.Bytes()); len(keys) != 2 || keys[0]["kid"] != checkpointKey["kid"] {
		t.Fatalf("historic checkpoint key missing: %v", keys)
	}
	after := snapshot(t, Open(s.dir))
	if result := audit.VerifyExport(after.Export, newKeys.Body.Bytes(), token, after.Checkpoint); !result.OK {
		t.Fatalf("checkpoints signed before and after key change did not verify: %+v", result)
	}
}

func TestLegacyStoreStartsAuditFromGenesis(t *testing.T) {
	s := Open(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	keysBefore := call(h, "GET", "/.well-known/jwks.json", "", nil, "")
	wantStatus(t, keysBefore, http.StatusOK)
	if err := s.update(func(st *state) error {
		st.WorkspaceID, st.CheckpointKey, st.CheckpointKeys = "", nil, nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	empty := snapshot(t, Open(s.dir))
	if result := audit.VerifyExport(empty.Export, empty.JWKS, empty.Checkpoint); !result.OK || result.CheckpointEntries == nil || *result.CheckpointEntries != 0 {
		t.Fatalf("empty legacy snapshot failed verification: %+v", result)
	}
	if err := Open(s.dir).AddProject("first", "repo"); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, Open(s.dir))
	if !bytes.Equal(empty.JWKS, after.JWKS) {
		t.Fatal("checkpoint keys changed after migration")
	}
	keysAfter := call(h, "GET", "/.well-known/jwks.json", "", nil, "")
	if !bytes.Equal(keysBefore.Body.Bytes(), keysAfter.Body.Bytes()) {
		t.Fatal("policy keys changed after migration")
	}
}

func snapshot(t *testing.T, s *Store) AuditExport {
	t.Helper()
	got, err := s.ExportAudit()
	if err != nil {
		t.Fatal(err)
	}
	result := audit.VerifyExport(got.Export, got.JWKS, got.Checkpoint)
	if !result.OK {
		t.Fatalf("invalid snapshot: %s", result.Diagnostic())
	}
	if result.CheckpointEntries == nil || *result.CheckpointEntries != result.Entries {
		t.Fatalf("snapshot does not checkpoint its tip: %+v", result)
	}
	return got
}
