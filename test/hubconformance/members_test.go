package hubconformance

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
)

const membersPath = "/api/v1/admin/members"

func (f *fixture) readMembers(t *testing.T, query string) hub.MembersPage {
	t.Helper()
	res := f.call(t, get, membersPath+query, f.lead, "", 200)
	var page hub.MembersPage
	if jsonv2.Unmarshal(res.body, &page, jsonv2.RejectUnknownMembers(true)) != nil || page.Members == nil {
		t.Fatal("invalid member page")
	}
	return page
}

func (f *fixture) memberRow(t *testing.T, projectID, id string) (hub.MemberSync, bool) {
	t.Helper()
	params := url.Values{"project_id": {projectID}}
	for {
		page := f.readMembers(t, "?"+params.Encode())
		for _, row := range page.Members {
			if row.MemberID == id {
				return row, true
			}
		}
		if page.NextCursor == nil {
			return hub.MemberSync{}, false
		}
		if *page.NextCursor == params.Get("after") || len(page.Members) == 0 {
			t.Fatal("member page did not advance")
		}
		params.Set("after", *page.NextCursor)
	}
}

func memberTuple(row hub.MemberSync) string {
	payload, _ := json.Marshal([]*string{&row.MemberID, row.ProjectID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func observedTime(t *testing.T, value *string) time.Time {
	t.Helper()
	if value == nil {
		t.Fatal("missing observation")
	}
	at, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil || at.IsZero() {
		t.Fatal("invalid observation timestamp")
	}
	return at
}

func (f *fixture) memberSync(t *testing.T) {
	id := *secondProject
	query := "?project_id=" + url.QueryEscape(id)
	f.call(t, get, membersPath, "", "", 401)
	f.call(t, get, membersPath, f.unknown, "", 401)
	if f.revoked != "" {
		f.call(t, get, membersPath, f.revoked, "", 401)
	}
	f.call(t, get, membersPath, f.member, "", 403)
	f.call(t, head, membersPath+query, f.lead, "", 200)
	f.call(t, head, membersPath+"?limit=0", f.lead, "", 400)
	f.call(t, get, membersPath+"?project_id="+f.missing, f.lead, "", 404)
	wrong := f.call(t, put, membersPath, f.lead, "", 405)
	if wrong.header.Get("Allow") != "GET, HEAD, POST" {
		t.Fatal("incorrect Allow for the member list")
	}
	for _, bad := range []string{
		"?bad=1", "?limit=101", "?limit=1&limit=2", "?limit=", "?project_id=bad%2Fid",
		query + "&project_id=" + id, "?after=%25", "?after=" + base64.RawURLEncoding.EncodeToString([]byte(`["member"]`)),
	} {
		f.call(t, get, membersPath+bad, f.lead, "", 400)
	}
	first := f.readMembers(t, query+"&limit=1")
	if len(first.Members) != 1 || first.NextCursor == nil || *first.NextCursor != memberTuple(first.Members[0]) {
		t.Fatal("expected one row with a canonical cursor")
	}
	second := f.readMembers(t, query+"&limit=1&after="+url.QueryEscape(*first.NextCursor))
	if len(second.Members) != 1 || second.Members[0].MemberID <= first.Members[0].MemberID {
		t.Fatal("second member page did not advance in ID order")
	}

	policyURL := projectPath(id) + "/policy"
	bundleURL := policyURL + ".bundle"
	repo := f.repo(t, id)
	f.call(t, put, policyURL, f.lead, bundleJSON(id, repo, 1, ""), 200)
	f.call(t, get, bundleURL, f.member, "", 200)

	var memberID string
	params := url.Values{"project_id": {id}}
	for {
		page := f.readMembers(t, "?"+params.Encode())
		for _, row := range page.Members {
			if row.PolicyVersion != nil && *row.PolicyVersion == 1 {
				if row.HubRole != hub.Member || memberID != "" || row.ProjectID == nil || *row.ProjectID != id {
					t.Fatal("expected one member's policy observation for the second project")
				}
				memberID = row.MemberID
			}
		}
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == params.Get("after") || len(page.Members) == 0 {
			t.Fatal("member page did not advance")
		}
		params.Set("after", *page.NextCursor)
	}
	read := func() hub.MemberSync {
		t.Helper()
		row, ok := f.memberRow(t, id, memberID)
		if !ok {
			t.Fatal("observed member missing from list")
		}
		return row
	}
	prior := read()
	beforeFetch := observedTime(t, prior.LastPolicyFetchAt)
	f.call(t, put, policyURL, f.lead, bundleJSON(id, repo, 2, ""), 200)
	policyHead := f.call(t, head, bundleURL, f.member, "", 200)
	if policyHead.header.Get(hub.PolicyVersionHeader) != "2" {
		t.Fatal("HEAD did not report the current policy version")
	}
	f.call(t, get, bundleURL, f.unknown, "", 401)
	f.call(t, get, eventsPath(id), f.member, "", 200)
	f.call(t, get, streamPath(id)+"?last_seq=invalid", f.member, "", 400)
	headStream, err := f.send(t.Context(), head, streamPath(id), f.member, "", "")
	if err != nil {
		t.Fatal("could not check the Room stream with HEAD")
	}
	checkStreamHeaders(t, headStream.status, headStream.header)
	if len(headStream.body) != 0 {
		t.Fatal("Room HEAD returned a body")
	}

	behind := read()
	if behind.PolicyVersion == nil || *behind.PolicyVersion != 1 || !observedTime(t, behind.LastPolicyFetchAt).Equal(beforeFetch) ||
		!sameOptionalTime(behind.LastRoomConnectedAt, prior.LastRoomConnectedAt) {
		t.Fatal("publishing a newer version and non-observing reads changed the member's state")
	}
	scan, closeStream := f.openStream(t, streamPath(id)+cursor(1<<53-1))
	defer closeStream()
	if len(streamLine(t, scan)) != 0 {
		t.Fatal("expected a heartbeat for an empty replay")
	}
	connected := read()
	roomTime := observedTime(t, connected.LastRoomConnectedAt)
	if sameOptionalTime(connected.LastRoomConnectedAt, prior.LastRoomConnectedAt) ||
		!observedTime(t, connected.LastPolicyFetchAt).Equal(beforeFetch) ||
		connected.PolicyVersion == nil || *connected.PolicyVersion != 1 {
		t.Fatal("initial Room connection did not update only Room time")
	}
	if len(streamLine(t, scan)) != 0 {
		t.Fatal("expected a blank stream poll")
	}
	f.call(t, get, bundleURL, f.member, "", 200)
	current := read()
	if current.PolicyVersion == nil || *current.PolicyVersion != 2 ||
		!observedTime(t, current.LastPolicyFetchAt).After(beforeFetch) ||
		!observedTime(t, current.LastRoomConnectedAt).Equal(roomTime) {
		t.Fatal("later fetch or stream poll changed the wrong observation")
	}
	closeStream()

	t.Run("revoke-and-re-add", func(t *testing.T) {
		f.reAddObservedMember(t, id, bundleURL)
	})
}

func sameOptionalTime(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (f *fixture) reAddObservedMember(t *testing.T, projectID, bundleURL string) {
	t.Helper()
	id := "sync-" + rand.Text()
	active := false
	add := func() string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"id": id, "role": "member"})
		res := f.call(t, post, membersPath, f.lead, string(body), 200)
		active = true
		var reply struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(res.body, &reply) != nil {
			t.Fatal("invalid member creation response")
		}
		return parseToken(t, "member creation", []byte(reply.Token))
	}
	remove := func() {
		t.Helper()
		active = false
		f.call(t, http.MethodDelete, membersPath+"/"+id, f.lead, "", 200)
	}
	defer func() {
		if active {
			remove()
		}
	}()
	old := add()
	f.call(t, get, bundleURL, old, "", 200)
	row, ok := f.memberRow(t, projectID, id)
	if !ok || row.PolicyVersion == nil || *row.PolicyVersion != 2 {
		t.Fatal("missing temporary member's fetch")
	}
	remove()
	if _, ok := f.memberRow(t, projectID, id); ok {
		t.Fatal("revoked member remained on the roster")
	}
	replacement := add()
	f.call(t, get, bundleURL, old, "", 401)
	f.call(t, get, streamPath(projectID), old, "", 401)
	row, ok = f.memberRow(t, projectID, id)
	if !ok || row.LastPolicyFetchAt != nil || row.PolicyVersion != nil || row.LastRoomConnectedAt != nil {
		t.Fatal("a new credential inherited the previous member's state")
	}
	f.call(t, get, bundleURL, replacement, "", 200)
	row, ok = f.memberRow(t, projectID, id)
	if !ok || row.PolicyVersion == nil || *row.PolicyVersion != 2 {
		t.Fatal("replacement credential did not record its own fetch")
	}
}
