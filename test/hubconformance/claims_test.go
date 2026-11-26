package hubconformance

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

type lease struct {
	ID      string    `json:"id"`
	Room    string    `json:"room"`
	Intent  string    `json:"intent"`
	Mode    string    `json:"mode"`
	Member  string    `json:"on_behalf_of"`
	Agent   wireAgent `json:"agent"`
	Paths   []string  `json:"paths"`
	Expires time.Time `json:"expires_at"`
}

type wireAgent struct {
	ID     string `json:"id"`
	Vendor string `json:"vendor"`
	Parent string `json:"parent"`
}

func claimReply(t *testing.T, res response, id string, ttl int64, mode string) (lease, room.Event, []lease) {
	t.Helper()
	var reply struct {
		Granted   lease           `json:"granted"`
		Conflicts []lease         `json:"conflicts"`
		Event     json.RawMessage `json:"event"`
	}
	decodeWire(t, res.body, &reply)
	event := eventFrom(t, reply.Event, id)
	claim := reply.Granted
	body, ok := event.Body().(*room.Claim)
	if !ok || claim.ID == "" || body.ClaimID != claim.ID || body.TTLSeconds != ttl || body.Item != "work" ||
		body.Intent != "edit" || !slices.Equal(body.Paths, []string{"src/main.go"}) || reply.Conflicts == nil ||
		claim.Room != id || claim.Mode != mode || !slices.Equal(claim.Paths, body.Paths) || claim.Intent != body.Intent ||
		claim.Member != event.OnBehalfOf() || room.Agent(claim.Agent) != event.Agent() ||
		claim.Agent != (wireAgent{ID: claim.Member + "/session", Vendor: "codex"}) {
		t.Fatal("claim response did not return the lease, conflicts, and matching claim event")
	}
	at, err := time.Parse(time.RFC3339Nano, event.At())
	if err != nil || !claim.Expires.Equal(at.Add(time.Duration(ttl)*time.Second)) {
		t.Fatal("lease expiry does not match the hub's event time and TTL")
	}
	return claim, event, reply.Conflicts
}

func (f *fixture) requireRelease(t *testing.T, claim lease, after int64, outcome string) {
	t.Helper()
	page := f.replay(t, claim.Room, cursor(after))
	if len(page) != 1 {
		t.Fatalf("expected one release event after sequence %d, got %d", after, len(page))
	}
	event := eventFrom(t, page[0], claim.Room)
	body, ok := event.Body().(*room.Release)
	if !ok || body.ClaimID != claim.ID || body.Outcome != outcome || event.Agent() != room.Agent(claim.Agent) ||
		event.OnBehalfOf() != claim.Member || event.Seq() != after+1 {
		t.Fatalf("release event did not match the claim, its owner, or outcome %s", outcome)
	}
	if outcome == "expired" {
		at, err := time.Parse(time.RFC3339Nano, event.At())
		if err != nil || at.Before(claim.Expires) {
			t.Fatal("hub released an expired claim before its deadline")
		}
	}
}

func (f *fixture) claims(t *testing.T) {
	id, second := *project, *secondProject
	path := claimsPath(id)
	t.Run("create-defaults-and-body-limit", func(t *testing.T) {
		f.call(t, post, path, f.member, pad(claimPost, roomLimit+1), http.StatusRequestEntityTooLarge)
		for _, tc := range []struct {
			body string
			ttl  int64
		}{
			{pad(claimPost, roomLimit), 3600},
			{strings.TrimSuffix(claimPost, "}") + `,"implicit":true}`, 600},
		} {
			res := f.call(t, post, path, f.member, tc.body, http.StatusOK)
			claim, event, conflicts := claimReply(t, res, id, tc.ttl, "shared")
			if len(conflicts) != 0 {
				t.Fatal("an uncontested claim reported conflicts")
			}
			f.call(t, post, path+"/"+claim.ID+"/release", f.member, releasePost, http.StatusOK)
			f.requireRelease(t, claim, event.Seq(), "done")
		}
	})
	t.Run("cross-project-conflicts", func(t *testing.T) {
		exclusive := strings.TrimSuffix(claimPost, "}") + `,"mode":"exclusive"}`
		res := f.call(t, post, path, f.member, exclusive, http.StatusOK)
		first, _, _ := claimReply(t, res, id, 3600, "exclusive")
		otherPath := claimsPath(second)
		res = f.call(t, post, otherPath, f.lead, claimPost, http.StatusOK)
		other, _, conflicts := claimReply(t, res, second, 3600, "shared")
		if len(conflicts) != 1 || !reflect.DeepEqual(conflicts[0], first) || other.Member == first.Member {
			t.Fatal("second project did not grant an advisory conflict with the first project's claim")
		}
		f.call(t, post, path+"/"+first.ID+"/release", f.member, releasePost, http.StatusOK)
		f.call(t, post, otherPath+"/"+other.ID+"/release", f.lead, releasePost, http.StatusOK)
	})
	t.Run("owner-renew-and-release", func(t *testing.T) {
		res := f.call(t, post, path, f.member, claimPost, http.StatusOK)
		claim, event, _ := claimReply(t, res, id, 3600, "shared")
		prefix, wrongRoom := path+"/"+claim.ID, claimsPath(second)+"/"+claim.ID
		const renewal = `{"session_id":"session","ttl_seconds":600}`
		for _, action := range []struct{ suffix, body string }{{"/renew", renewal}, {"/release", releasePost}} {
			endpoint := prefix + action.suffix
			f.call(t, post, endpoint, f.lead, action.body, http.StatusNotFound)
			f.call(t, post, endpoint, f.member, strings.Replace(action.body, `"session"`, `"other"`, 1), http.StatusNotFound)
			f.call(t, post, wrongRoom+action.suffix, f.member, action.body, http.StatusNotFound)
			f.call(t, post, endpoint, f.member, strings.Replace(action.body, `"session_id"`, `"Session_ID"`, 1), http.StatusBadRequest)
			f.call(t, post, endpoint, f.member, pad(action.body, roomLimit+1), http.StatusRequestEntityTooLarge)
		}
		f.call(t, post, prefix+"/release", f.member, strings.Replace(releasePost, "done", "expired", 1), http.StatusBadRequest)
		var renewed lease
		decodeWire(t, f.call(t, post, prefix+"/renew", f.member, pad(renewal, roomLimit), http.StatusOK).body, &renewed)
		initialExpiry := claim.Expires
		claim.Expires = renewed.Expires
		if !reflect.DeepEqual(renewed, claim) || !renewed.Expires.Before(initialExpiry) ||
			renewed.Expires.Before(initialExpiry.Add(-3000*time.Second)) {
			t.Fatal("renew should shorten the lease to 600 seconds from the renewal and preserve its other fields")
		}
		if page := f.replay(t, id, cursor(event.Seq())); len(page) != 0 {
			t.Fatal("renew or a refused action appended a Room event")
		}
		var released lease
		decodeWire(t, f.call(t, post, prefix+"/release", f.member, pad(releasePost, roomLimit), http.StatusOK).body, &released)
		if !reflect.DeepEqual(released, renewed) {
			t.Fatal("release did not return the renewed lease")
		}
		f.requireRelease(t, claim, event.Seq(), "done")
		f.call(t, post, prefix+"/renew", f.member, renewal, http.StatusNotFound)
		f.call(t, post, prefix+"/release", f.member, releasePost, http.StatusNotFound)
	})
	t.Run("unknown-room-and-unavailable-claim", func(t *testing.T) {
		missing := claimsPath(f.missing)
		f.call(t, post, missing, f.member, claimPost, http.StatusNotFound)
		for _, prefix := range []string{missing + "/" + f.missing, path + "/" + f.missing} {
			f.call(t, post, prefix+"/renew", f.member, renewPost, http.StatusNotFound)
			f.call(t, post, prefix+"/release", f.member, releasePost, http.StatusNotFound)
		}
	})
	t.Run("invalid-request-fields", func(t *testing.T) {
		prefix := path + "/" + f.missing
		for _, tc := range []struct{ path, body string }{
			{path, strings.Replace(claimPost, `"paths"`, `"Paths"`, 1)},
			{path, strings.TrimSuffix(claimPost, "}") + `,"ttl_seconds":10,"ttl_seconds":null}`},
			{path, strings.TrimSuffix(claimPost, "}") + `,"paths":[null]}`},
			{prefix + "/renew", `{"session_id":"session","ttl_seconds":10,"ttl_seconds":null}`},
			{prefix + "/release", `{"session_id":"session","outcome":"done","outcome":null}`},
		} {
			f.call(t, post, tc.path, f.member, tc.body, http.StatusBadRequest)
		}
	})
	t.Run("expiry-sweep-on-failed-action", func(t *testing.T) {
		for _, tc := range []struct {
			name, body string
			want       int
		}{
			{"unavailable-claim", renewPost, http.StatusNotFound},
			{"invalid-renewal", `{}`, http.StatusBadRequest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				text := strings.TrimSuffix(claimPost, "}") + `,"ttl_seconds":1}`
				res, err := f.send(t.Context(), post, path, f.member, text, "")
				if err != nil {
					t.Fatal("could not submit the one-second claim")
				}
				if f.target == externalHub && res.status == http.StatusBadRequest {
					res.expect(t, post, path, http.StatusBadRequest)
					t.Skip("external hub rejected the one-second claim TTL needed for a bounded expiry check")
				}
				res.expect(t, post, path, http.StatusOK)
				claim, event, _ := claimReply(t, res, id, 1, "shared")
				// Wait from receipt to avoid comparing the client's clock with the hub's.
				time.Sleep(1250 * time.Millisecond)
				if page := f.replay(t, id, cursor(event.Seq())); len(page) != 0 {
					t.Fatal("an expired lease created a release before the next claim operation")
				}
				wrongRoom := claimsPath(second) + "/" + claim.ID + "/renew"
				f.call(t, post, wrongRoom, f.lead, tc.body, tc.want)
				f.requireRelease(t, claim, event.Seq(), "expired")
				f.call(t, post, path+"/"+claim.ID+"/renew", f.member, renewPost, http.StatusNotFound)
				f.requireRelease(t, claim, event.Seq(), "expired")
			})
		}
	})
}
