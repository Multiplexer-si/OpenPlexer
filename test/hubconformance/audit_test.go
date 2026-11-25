package hubconformance

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/room"
)

const denialPost = `{"type":"policy.denied","agent":{"id":"session","vendor":"codex"},"body":{"action":"write","path":"src/main.go","rule":"locked"}}`

type auditEntry struct {
	audit.Record
	Result string `json:"result"`
}

type auditSnapshot struct {
	entries    []auditEntry
	checkpoint string
	body       []byte
}

func (f *fixture) readAudit(t *testing.T, previous ...string) auditSnapshot {
	t.Helper()
	res := f.call(t, get, auditPath, f.lead, "", http.StatusOK)
	snap := auditSnapshot{
		body: res.body, checkpoint: res.header.Get(checkpoint),
	}
	jwks := f.call(t, get, auditKeysPath, "", "", http.StatusOK).body
	result := audit.VerifyExport(snap.body, jwks, snap.checkpoint)
	if !result.OK || result.Completeness != "through_checkpoint" ||
		result.CheckpointEntries == nil || *result.CheckpointEntries != result.Entries {
		t.Fatalf("export or its tip checkpoint failed verification (rule %s, record %d)", result.Rule, result.RecordNumber)
	}
	if len(previous) != 0 {
		result := audit.VerifyExport(snap.body, jwks, previous...)
		if !result.OK {
			t.Fatalf("export does not extend the earlier checkpoint (rule %s)", result.Rule)
		}
	}
	var wire struct {
		Entries []auditEntry `json:"entries"`
	}
	decodeWire(t, res.body, &wire)
	for i, entry := range wire.Entries {
		if entry.ID != fmt.Sprint(len(wire.Entries)-i) || entry.Result != audit.ActionResult(entry.Action) {
			t.Fatalf("export entry %d has the wrong newest-first ID or result", i)
		}
	}
	snap.entries = wire.Entries
	return snap
}

func (f *fixture) auditExport(t *testing.T) {
	snap := f.readAudit(t)
	if len(snap.entries) == 0 {
		t.Fatal("expected audit history from seeding")
	}
	keys := f.call(t, get, policyKeysPath, "", "", http.StatusOK).body
	if result := audit.VerifyExport(snap.body, keys, snap.checkpoint); result.Rule != "K3" {
		t.Fatal("audit checkpoint must require its own JWKS")
	}
}

func (f *fixture) concurrentWrites(t *testing.T) {
	id, second := *project, *secondProject
	repo := f.repo(t, second)
	before := f.readAudit(t)
	var last int64
	for _, raw := range f.replay(t, id, "") {
		last = eventFrom(t, raw, id).Seq()
	}

	const count = 32
	type result struct {
		res response
		err error
	}
	var posts [count]result
	var published result
	start := make(chan struct{})
	var writers sync.WaitGroup
	for i := range posts {
		writers.Go(func() {
			<-start
			body := statusPost
			if i%2 == 0 {
				body = denialPost
			}
			posts[i].res, posts[i].err = f.send(t.Context(), post, eventsPath(id), f.member, body, fmt.Sprintf("race-%d", i))
		})
	}
	policyPath := projectPath(second) + "/policy"
	writers.Go(func() {
		<-start
		published.res, published.err = f.send(t.Context(), put, policyPath, f.lead, bundleJSON(second, repo, 1, ""), "")
	})
	close(start)
	writers.Wait()
	if published.err != nil {
		t.Fatal("parallel policy publication failed")
	}
	published.res.expect(t, put, policyPath, http.StatusOK)
	keys, err := policy.ParseKeySet(f.call(t, get, policyKeysPath, "", "", http.StatusOK).body)
	if err != nil {
		t.Fatal(err)
	}
	acknowledged := verifyBundle(t, published.res, keys, second, repo, 1)
	stored := verifyBundle(t, f.call(t, get, projectPath(second)+"/policy.bundle", f.member, "", http.StatusOK), keys, second, repo, 1)
	if stored.Signature != acknowledged.Signature || !sameJSON(t, stored.Bundle, acknowledged.Bundle) {
		t.Fatal("the stored policy differs from the publication acknowledged during the race")
	}

	bySeq := make(map[int64][]byte, count)
	byID := make(map[string]bool, count)
	denials := make(map[string]room.Event, count/2)
	for i, posted := range posts {
		if posted.err != nil {
			t.Fatalf("parallel event post %d failed", i)
		}
		posted.res.expect(t, post, eventsPath(id), http.StatusOK)
		event := eventFrom(t, posted.res.body, id)
		if bySeq[event.Seq()] != nil || byID[event.ID()] {
			t.Fatalf("duplicate Room sequence or event ID in parallel post %d", i)
		}
		bySeq[event.Seq()], byID[event.ID()] = posted.res.body, true
		kind := "status"
		if i%2 == 0 {
			kind = "policy.denied"
			denials[event.ID()] = event
		}
		if event.Type() != kind {
			t.Fatalf("parallel post %d returned the wrong event type", i)
		}
	}
	retry := f.call(t, post, eventsPath(id), f.member, denialPost, http.StatusOK, "race-0")
	eventFrom(t, retry.body, id)
	if !sameJSON(t, retry.body, posts[0].res.body) {
		t.Fatal("denial retry returned a different envelope")
	}
	page := f.replay(t, id, cursor(last))
	if len(page) != count {
		t.Fatalf("replay returned %d events after the race, want %d", len(page), count)
	}
	for i, raw := range page {
		seq := last + int64(i) + 1
		if eventFrom(t, raw, id).Seq() != seq || bySeq[seq] == nil || !sameJSON(t, raw, bySeq[seq]) {
			t.Fatalf("missing or changed parallel event at sequence %d", seq)
		}
	}

	after := f.readAudit(t, before.checkpoint)
	added := len(after.entries) - len(before.entries)
	if added != count/2+1 {
		t.Fatalf("race appended %d audit entries, want %d", added, count/2+1)
	}
	policyUpdates := 0
	for _, entry := range after.entries[:added] {
		if entry.Action == "policy.updated" && entry.Target == "project:"+second {
			policyUpdates++
			continue
		}
		id, _ := entry.Meta["event_id"].(string)
		event, found := denials[id]
		if !found || entry.Action != "event.policy-denied" || entry.Target != "event:"+id ||
			entry.ActorMemberID == nil || *entry.ActorMemberID != event.OnBehalfOf() ||
			entry.AgentSessionID == nil || *entry.AgentSessionID != event.Agent().ID ||
			len(entry.Meta) != 3 || entry.Meta["room_id"] != event.Mission() || entry.Meta["seq"] != float64(event.Seq()) {
			t.Fatal("new audit entry does not match a posted denial and its link metadata")
		}
		delete(denials, id)
	}
	if policyUpdates != 1 || len(denials) != 0 {
		t.Fatal("audit history did not record each audited action once")
	}
}
