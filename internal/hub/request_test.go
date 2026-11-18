package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/room"
)

const statusRequest = `{"type":"status","agent":{"id":"session","vendor":"codex"},"body":{"summary":"working"}}`
const claimRequest = `{"agent":{"id":"session","vendor":"codex"},"item":"work","paths":["src/main.go"],"intent":"edit"}`

func TestRoomRequestCaseSensitivity(t *testing.T) {
	for _, tc := range []struct{ name, route, body, field string }{
		{"event type", "events", strings.Replace(statusRequest, `"type"`, `"Type"`, 1), "type"},
		{"event agent", "events", strings.Replace(statusRequest, `"agent"`, `"Agent"`, 1), "agent"},
		{"event body", "events", strings.Replace(statusRequest, `"body"`, `"Body"`, 1), "body"},
		{"event agent id", "events", strings.Replace(statusRequest, `"id"`, `"ID"`, 1), "agent.id"},
		{"event summary", "events", strings.Replace(statusRequest, `"summary"`, `"Summary"`, 1), "body.summary"},
		{"claim paths", "claims", strings.Replace(claimRequest, `"paths"`, `"Paths"`, 1), "paths"},
		{"claim agent vendor", "claims", strings.Replace(claimRequest, `"vendor"`, `"Vendor"`, 1), "agent.vendor"},
		{"renew session", "renew", `{"Session_ID":"session","ttl_seconds":10}`, "session_id"},
		{"renew ttl", "renew", `{"session_id":"session","TTL_Seconds":10}`, "ttl_seconds"},
		{"release session", "release", `{"Session_ID":"session","outcome":"done"}`, "session_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantBadField(t, postRoomRequest(t, tc.route, tc.body), tc.field)
		})
	}

	t.Run("release outcome in another case is ignored", func(t *testing.T) {
		res := postRoomRequest(t, "release", `{"session_id":"session","outcome":"done","Outcome":"expired"}`)
		wantStatus(t, res, http.StatusOK)
	})
}

func TestRoomRequestFinalMember(t *testing.T) {
	for _, tc := range []struct{ name, route, body, field string }{
		{"event agent replaces earlier object", "events", `{"type":"status","agent":{"id":"session","vendor":"codex"},"agent":{"vendor":"codex"},"body":{"summary":"working"}}`, "agent.id"},
		{"event body replaces earlier object", "events", `{"type":"status","agent":{"id":"session","vendor":"codex"},"body":{"summary":"working"},"body":{}}`, "body.summary"},
		{"claim agent replaces earlier object", "claims", `{"agent":{"id":"session","vendor":"codex"},"agent":{"id":"session"},"item":"work","paths":[],"intent":"edit"}`, "agent.vendor"},
		{"create null ttl", "claims", strings.TrimSuffix(claimRequest, "}") + `,"ttl_seconds":10,"ttl_seconds":null}`, "ttl_seconds"},
		{"renew null ttl", "renew", `{"session_id":"session","ttl_seconds":10,"ttl_seconds":null}`, "ttl_seconds"},
		{"release null outcome", "release", `{"session_id":"session","outcome":"done","outcome":null}`, "outcome"},
		{"claim null path", "claims", strings.TrimSuffix(claimRequest, "}") + `,"paths":["src/main.go",null]}`, "paths[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantBadField(t, postRoomRequest(t, tc.route, tc.body), tc.field)
		})
	}

	t.Run("earlier objects and scalar types are discarded", func(t *testing.T) {
		res := postRoomRequest(t, "events", `{"type":false,"type":"status","agent":{"id":false,"parent":"old"},"agent":{"id":"session","vendor":"codex"},"body":{"plan_step":"old"},"body":{"summary":"new"}}`)
		ev := eventResponse(t, res)
		body := ev.Body().(*room.Status)
		if ev.Agent().Parent != "" || body.Summary != "new" || body.PlanStep != "" {
			t.Fatalf("stored fields from a superseded object: agent = %+v, body = %+v", ev.Agent(), body)
		}
	})
}

func TestRoomRequestValidatesIdempotentRetry(t *testing.T) {
	s, token := roomStore(t, "project", "member")
	h := s.Handler()
	path := eventsURL("project")
	const key = "retry"
	wantStatus(t, call(h, "POST", path, token, []byte(statusRequest), key), http.StatusOK)
	invalid := strings.Replace(statusRequest, `"summary"`, `"Summary"`, 1)
	wantBadField(t, call(h, "POST", path, token, []byte(invalid), key), "body.summary")
	assertSequences(t, events(t, h, path, token), []int64{1})
}

func TestRoomRequestRejectsTrailingJSON(t *testing.T) {
	for _, suffix := range []string{"}", "]", " {}"} {
		res := postRoomRequest(t, "events", statusRequest+suffix)
		wantStatus(t, res, http.StatusBadRequest)
	}
}

func postRoomRequest(t *testing.T, route, body string) *httptest.ResponseRecorder {
	t.Helper()
	s, token := roomStore(t, "project", "member")
	h := s.Handler()
	path := "/api/v1/rooms/project/" + route
	if route == "renew" || route == "release" {
		claims := claimsURL("project")
		res := call(h, "POST", claims, token, []byte(claimRequest), "")
		wantStatus(t, res, http.StatusOK)
		var result struct{ Granted lease }
		decode(t, res.Body.Bytes(), &result)
		path = claims + "/" + result.Granted.ID + "/" + route
	}
	return call(h, "POST", path, token, []byte(body), "")
}

func wantBadField(t *testing.T, res *httptest.ResponseRecorder, field string) {
	t.Helper()
	wantStatus(t, res, http.StatusBadRequest)
	var body struct{ Error string }
	decode(t, res.Body.Bytes(), &body)
	if !strings.Contains(body.Error, field) {
		t.Fatalf("HTTP error %q does not identify %s", body.Error, field)
	}
}
