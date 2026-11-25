package hubconformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

func eventFrom(t *testing.T, raw []byte, id string) room.Event {
	t.Helper()
	msg, err := room.ParseMessage(raw)
	if err != nil {
		t.Fatal("response is not a valid Room envelope")
	}
	event, ok := msg.Payload().(room.Event)
	seq, hasSeq := msg.Seq()
	if !ok || msg.V() != 1 || msg.Type() != "event" || msg.Room() != id || event.Mission() != id ||
		!hasSeq || seq < 1 || seq != event.Seq() || msg.Sender().ID != event.Agent().ID {
		t.Fatalf("event envelope does not match its payload or Room %s", id)
	}
	kind := room.Daemon
	if event.Agent().Vendor == "human" {
		kind = room.Dashboard
	}
	if msg.Sender().Kind != kind {
		t.Fatal("event envelope has the wrong sender kind")
	}
	var wire struct {
		EventID string `json:"event_id"`
		TS      string `json:"ts"`
	}
	decode(t, raw, &wire)
	if _, err := time.Parse(time.RFC3339Nano, wire.TS); err != nil || wire.TS != event.At() || wire.EventID != event.ID() {
		t.Fatal("event envelope must carry the payload timestamp and event ID")
	}
	return event
}

func (f *fixture) replay(t *testing.T, id, query string) []json.RawMessage {
	t.Helper()
	res := f.call(t, get, eventsPath(id)+query, f.member, "", http.StatusOK)
	var page []json.RawMessage
	decode(t, res.body, &page)
	if page == nil {
		t.Fatal("replay must return a JSON array, including when empty")
	}
	return page
}

func cursor(seq int64) string { return fmt.Sprintf("?last_seq=%d", seq) }

func (f *fixture) roomEvents(t *testing.T) {
	id := *project
	path := eventsPath(id)
	t.Run("post-and-replay", func(t *testing.T) {
		if page := f.replay(t, id, ""); len(page) != 0 {
			t.Fatal("fixture Room must start empty")
		}
		res := f.call(t, post, path, f.member, statusPost, http.StatusOK)
		event := eventFrom(t, res.body, id)
		body, ok := event.Body().(*room.Status)
		if !ok || body.Summary != "working" || event.Seq() != 1 || event.Agent() != (room.Agent{
			ID: event.OnBehalfOf() + "/session", Vendor: "codex", Parent: event.OnBehalfOf() + "/parent",
		}) {
			t.Fatal("post did not preserve the content or assign the expected sequence and agent")
		}
		page := f.replay(t, id, cursor(0))
		if len(page) != 1 || !sameJSON(t, res.body, page[0]) {
			t.Fatal("replay did not return the posted envelope")
		}
		if page := f.replay(t, id, cursor(event.Seq())); len(page) != 0 {
			t.Fatal("replay included the cursor event")
		}
	})
	t.Run("100-event-page", func(t *testing.T) {
		id := *secondProject
		const count = 101
		for i := range count {
			body := strings.Replace(statusPost, "working", fmt.Sprintf("page-%d", i), 1)
			res := f.call(t, post, eventsPath(id), f.member, body, http.StatusOK)
			if eventFrom(t, res.body, id).Seq() != int64(i+1) {
				t.Fatalf("post %d did not return sequence %d", i+1, i+1)
			}
		}
		page := f.replay(t, id, "")
		if len(page) != 100 {
			t.Fatalf("first page has %d events, want 100", len(page))
		}
		for i, raw := range page {
			event := eventFrom(t, raw, id)
			if event.Seq() != int64(i+1) || event.Type() != "status" || event.Body().(*room.Status).Summary != fmt.Sprintf("page-%d", i) {
				t.Fatalf("first page event %d has the wrong sequence or content", i+1)
			}
		}
		next := f.replay(t, id, cursor(100))
		if len(next) != 1 || eventFrom(t, next[0], id).Seq() != 101 {
			t.Fatal("second page must contain only event 101")
		}
	})
	t.Run("invalid-cursors", func(t *testing.T) {
		for _, query := range []string{"?last_seq=", "?last_seq=x", "?last_seq=1.5", "?last_seq=-1",
			"?last_seq=0&last_seq=1", "?last_seq=0&last_seq=0", "?last_seq=0&bad=%ZZ", "?last_seq=9007199254740992"} {
			f.call(t, get, path+query, f.member, "", http.StatusBadRequest)
		}
	})
	t.Run("ahead-cursor", func(t *testing.T) {
		if page := f.replay(t, id, cursor(1<<53-1)); len(page) != 0 {
			t.Fatal("ahead cursor did not return an empty page")
		}
	})
	t.Run("body-limit", func(t *testing.T) {
		f.call(t, post, path, f.member, pad(statusPost, roomLimit+1), http.StatusRequestEntityTooLarge)
		res := f.call(t, post, path, f.member, pad(statusPost, roomLimit), http.StatusOK)
		eventFrom(t, res.body, id)
	})
	t.Run("idempotency-limit-and-retry", func(t *testing.T) {
		key := strings.Repeat("k", 128)
		f.call(t, post, path, f.member, statusPost, http.StatusBadRequest, key+"x")
		res := f.call(t, post, path, f.member, statusPost, http.StatusOK, key)
		event := eventFrom(t, res.body, id)
		retryBody := strings.Replace(statusPost, `"body"`, `"body":false,"body"`, 1)
		retry := f.call(t, post, path, f.member, strings.Replace(retryBody, "working", "repeated", 1), http.StatusOK, key)
		eventFrom(t, retry.body, id)
		if !sameJSON(t, res.body, retry.body) {
			t.Fatal("retry did not return the original event envelope")
		}
		for _, body := range []string{strings.Replace(statusPost, `"summary"`, `"Summary"`, 1),
			strings.Replace(statusPost, `"agent"`, `"Agent"`, 1), statusPost + " {}"} {
			f.call(t, post, path, f.member, body, http.StatusBadRequest, key)
		}
		if page := f.replay(t, id, cursor(event.Seq())); len(page) != 0 {
			t.Fatal("retry or invalid post consumed a sequence number")
		}
		otherMember := f.call(t, post, path, f.lead, statusPost, http.StatusOK, key)
		otherRoom := f.call(t, post, eventsPath(*secondProject), f.member, statusPost, http.StatusOK, key)
		leadEvent, otherEvent := eventFrom(t, otherMember.body, id), eventFrom(t, otherRoom.body, *secondProject)
		if leadEvent.Seq() != event.Seq()+1 || leadEvent.OnBehalfOf() == event.OnBehalfOf() ||
			leadEvent.ID() == event.ID() || otherEvent.ID() == event.ID() {
			t.Fatal("deduplication crossed a member or Room boundary")
		}
	})
	t.Run("reserved-types", func(t *testing.T) {
		for _, body := range []string{
			`{"type":"claim","body":{"item":"work","paths":[],"intent":"edit","ttl_seconds":10}}`,
			`{"type":"release","body":{"claim_id":"claim","outcome":"done"}}`,
			`{"type":"decision.confirm","body":{"decision_id":"decision","result":"confirmed"}}`,
			`{"type":"policy.override","body":{"lead":"lead","agent":"session","scope":"mission","areas":[],"actions":[],"reason":"test"}}`,
			`{"type":"policy.override.revoked","body":{"override_id":"override","agent":"session","area":"area","revoked_by":"lead"}}`,
			`{"type":"human","agent":{"id":"session","vendor":"codex"},"body":{"text":"hello"}}`,
		} {
			f.call(t, post, path, f.member, body, http.StatusForbidden)
		}
		res := f.call(t, post, path, f.member, `{"type":"human","body":{"text":"hello"}}`, http.StatusOK)
		event := eventFrom(t, res.body, id)
		if event.Type() != "human" || event.Agent() != (room.Agent{ID: event.OnBehalfOf(), Vendor: "human"}) {
			t.Fatal("a human post must carry the member identity")
		}
	})
	t.Run("unknown-room", func(t *testing.T) {
		f.call(t, get, eventsPath(f.missing), f.member, "", http.StatusNotFound)
		f.call(t, post, eventsPath(f.missing), f.member, statusPost, http.StatusNotFound)
	})
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	return bytes.Equal(mustCanonical(t, a), mustCanonical(t, b))
}
