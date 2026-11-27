package hubconformance

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
)

const invitePath = "/api/v1/admin/invites"
const redeemPath = "/api/v1/invites/redeem"
const deleteMethod = http.MethodDelete

func newInviteBearer() string {
	var raw [32]byte
	rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func memberDigest(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func redeemJSON(code, hash string) string {
	body, _ := json.Marshal(map[string]string{"code": code, "token_hash": hash})
	return string(body)
}

func createInviteJSON(projectID, memberID string, expiry int, approval bool) string {
	body, _ := json.Marshal(hub.InviteInput{ProjectID: projectID, MemberID: memberID, Role: hub.Member,
		ExpiresIn: float64(expiry), RequireApproval: approval})
	return string(body)
}

func (f *fixture) issueInvite(t *testing.T, body string) hub.CreatedInvite {
	t.Helper()
	var result hub.CreatedInvite
	res := f.call(t, post, invitePath, f.lead, body, http.StatusCreated)
	decodeWire(t, res.body, &result)
	code, err := base64.RawURLEncoding.Strict().DecodeString(result.Code)
	if err != nil || len(code) != 32 || len(result.Invite.ID) != 32 ||
		string(result.Invite.State) != "issued" {
		t.Fatal("invalid invite creation response")
	}
	return result
}

func (f *fixture) invites(t *testing.T) {
	expiring := f.issueInvite(t, createInviteJSON(*project, "expires-"+rand.Text(), 60, true))
	checkAfter := time.Now().Add(time.Minute)
	expiringBody := redeemJSON(expiring.Code, memberDigest(newInviteBearer()))
	if expiring.Invite.ExpiresAt.Sub(expiring.Invite.CreatedAt) != time.Minute {
		t.Fatal("invite has the wrong deadline")
	}
	f.call(t, post, redeemPath, "", expiringBody, 202)

	t.Run("roster-request-bodies", f.rosterRequestBodies)
	t.Run("validation-and-pagination", f.inviteValidation)
	t.Run("races-and-receipts", f.inviteRaces)
	t.Run("direct-race", f.directInviteRace)
	t.Run("revocation", f.inviteRevocation)
	t.Run("expiry", func(t *testing.T) {
		timer := time.NewTimer(time.Until(checkAfter))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal("cancelled while waiting for invite expiry")
		}
		f.call(t, post, redeemPath, "", expiringBody, 410)
		f.call(t, post, invitePath+"/"+expiring.Invite.ID+"/approve", f.lead, "", 409)
		f.call(t, deleteMethod, invitePath+"/"+expiring.Invite.ID, f.lead, "", 409)
	})
	t.Run("redeem-failure-limit", f.redeemFailureLimit)
}

func (f *fixture) redeemFailureLimit(t *testing.T) {
	invite := f.issueInvite(t, createInviteJSON(*project, "rate-limit-"+rand.Text(), 3600, true))
	hash := memberDigest(newInviteBearer())
	valid := redeemJSON(invite.Code, hash)
	for range 21 {
		res := f.call(t, post, redeemPath, "", valid, 202)
		if res.header.Get("Retry-After") != "5" {
			t.Fatal("pending approval must keep its retry interval")
		}
	}
	f.call(t, post, redeemPath, "", redeemJSON(newInviteBearer(), hash), 404)
	// Allow one window to end while the requests are in flight.
	for range 42 {
		body := redeemJSON(newInviteBearer(), hash)
		res, err := f.send(t.Context(), post, redeemPath, "", body, "")
		if err != nil {
			t.Fatal("redeem transport error")
		}
		if res.status == 404 {
			res.expect(t, post, redeemPath, 404)
			continue
		}
		res.expect(t, post, redeemPath, 429)
		retry, err := strconv.Atoi(res.header.Get("Retry-After"))
		if err != nil || retry < 1 || retry > 60 || strings.TrimSpace(string(res.body)) != `{"error":"too many failed redeem attempts"}` {
			t.Fatal("wrong throttle response")
		}
		f.call(t, post, redeemPath, "", valid, 202)
		f.call(t, post, invitePath+"/"+invite.Invite.ID+"/approve", f.lead, "", 200)
		f.call(t, post, redeemPath, "", valid, 200)
		f.call(t, post, redeemPath, "", valid, 200)
		return
	}
	t.Fatal("team did not limit failed redeems")
}

func (f *fixture) rosterRequestBodies(t *testing.T) {
	const members = "/api/v1/admin/members"
	id := "roster-validation-" + rand.Text()
	input := `{"id":` + jsonString(id) + `,"role":"member"}`
	for _, field := range []string{`"role":"member"`, `"unknown":true`} {
		body := strings.TrimSuffix(input, "}") + "," + field + "}"
		res := f.call(t, post, members, f.lead, body, 400)
		if strings.TrimSpace(string(res.body)) != `{"error":"invalid input: member needs a valid id and role lead or member"}` {
			t.Fatal("wrong roster-add error")
		}
	}
	f.call(t, post, members, f.lead, input, 200)
	path := members + "/" + id
	res := f.call(t, deleteMethod, path, f.lead, strings.Repeat(" ", roomLimit), 400)
	if strings.TrimSpace(string(res.body)) != `{"error":"invalid input: member deletion does not accept a body"}` {
		t.Fatal("wrong roster-delete error")
	}
	f.call(t, deleteMethod, path, f.lead, strings.Repeat(" ", roomLimit+1), 413)
	f.call(t, deleteMethod, path, f.lead, "", 200)
}

func (f *fixture) inviteValidation(t *testing.T) {
	request := createInviteJSON(*project, "validation-"+rand.Text(), 60, false)
	unknownID := strings.Repeat("0", 32)
	detail := invitePath + "/" + unknownID
	routes := []struct{ method, path, body, allow string }{
		{get, invitePath, "", "GET, HEAD, POST"},
		{post, invitePath, request, "GET, HEAD, POST"},
		{deleteMethod, detail, "", "DELETE"},
		{post, detail + "/approve", "", "POST"},
	}
	for _, r := range routes {
		for _, unauthorized := range []string{"", "short", f.unknown, f.revoked} {
			f.call(t, r.method, r.path, unauthorized, r.body, 401)
		}
		f.call(t, r.method, r.path, f.member, r.body, 403)
		res := f.call(t, put, r.path, f.lead, "", 405)
		if res.header.Get("Allow") != r.allow {
			t.Errorf("wrong Allow header for %s", r.path)
		}
	}
	if !f.cloudflareEdge {
		res, err := f.send(t.Context(), get, invitePath, "", "", "", "Bearer "+f.lead, "Bearer "+f.lead)
		if err != nil {
			t.Fatal("duplicate bearer request failed")
		}
		res.expect(t, get, invitePath, 401)
	}
	f.call(t, head, invitePath, f.lead, "", 200)
	f.call(t, get, redeemPath, "", "", 405)
	f.call(t, post, redeemPath, "", redeemJSON(newInviteBearer(), memberDigest(newInviteBearer())), 404)
	f.call(t, deleteMethod, detail, f.lead, "", 404)
	f.call(t, post, detail+"/approve", f.lead, "", 404)
	f.call(t, post, invitePath, f.lead, createInviteJSON(f.missing, "missing-"+rand.Text(), 60, false), 404)
	f.call(t, post, invitePath, f.lead, `{"project_id":1,"member_id":"ana"}`, 400)
	f.call(t, post, invitePath, f.lead, strings.TrimSuffix(request, "}")+`,"project_id":"other"}`, 400)
	f.call(t, post, invitePath, f.lead, strings.TrimSuffix(request, "}")+`,"unknown":true}`, 400)
	f.call(t, post, redeemPath, "", `{"code":"bad","token_hash":"bad"}`, 400)
	f.call(t, post, invitePath, f.lead, pad(request, roomLimit+1), 413)
	f.call(t, post, redeemPath, "", pad("{}", roomLimit+1), 413)
	f.call(t, deleteMethod, detail, f.lead, "{}", 400)
	f.call(t, post, detail+"/approve", f.lead, "{}", 400)
	f.call(t, get, invitePath, f.lead, "{}", 400)
	valid := f.issueInvite(t, pad(request, roomLimit))
	f.call(t, post, invitePath, f.lead, request, 409)
	f.call(t, deleteMethod, invitePath+"/"+valid.Invite.ID, f.lead, "", 200)
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=1&%6cimit=1", "?after=", "?after=%zz", "?extra=1"} {
		f.call(t, get, invitePath+q, f.lead, "", 400)
	}
	var page, next hub.InvitePage
	decodeWire(t, f.call(t, get, invitePath+"?limit=1", f.lead, "", 200).body, &page)
	if len(page.Invites) != 1 || page.NextCursor == nil || *page.NextCursor != page.Invites[0].ID {
		t.Fatal("first page did not end at its only invite")
	}
	decodeWire(t, f.call(t, get, invitePath+"?limit=100&after="+*page.NextCursor, f.lead, "", 200).body, &next)
	if len(next.Invites) == 0 || len(next.Invites) > 100 || next.Invites[0].ID <= *page.NextCursor {
		t.Fatal("next page did not advance")
	}
	const high = "ffffffffffffffffffffffffffffffff"
	var empty hub.InvitePage
	decodeWire(t, f.call(t, get, invitePath+"?after="+high, f.lead, "", 200).body, &empty)
	if empty.Invites == nil || len(empty.Invites) != 0 || empty.NextCursor != nil {
		t.Fatal("expected empty page after a cursor without a row")
	}
}

func (f *fixture) raceInvite(t *testing.T, bodies [2]string) [2]response {
	t.Helper()
	var replies [2]response
	var errors [2]error
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for i, body := range bodies {
		go func() {
			<-start
			replies[i], errors[i] = f.send(t.Context(), post, redeemPath, "", body, "")
			done <- struct{}{}
		}()
	}
	close(start)
	<-done
	<-done
	for _, err := range errors {
		if err != nil {
			t.Fatal("redeem transport failed")
		}
	}
	return replies
}

func (f *fixture) inviteRaces(t *testing.T) {
	previous := f.readAudit(t)
	memberID := "racing-" + rand.Text()
	create := createInviteJSON(*project, memberID, 3600, true)
	invite := f.issueInvite(t, create)
	approve := invitePath + "/" + invite.Invite.ID + "/approve"
	f.call(t, post, "/api/v1/admin/members", f.lead, `{"id":`+jsonString(memberID)+`,"role":"member"}`, 409)
	f.call(t, post, approve, f.lead, "", 409)
	tokens := [2]string{newInviteBearer(), newInviteBearer()}
	hashes := [2]string{memberDigest(tokens[0]), memberDigest(tokens[1])}
	bodies := [2]string{redeemJSON(invite.Code, hashes[0]), redeemJSON(invite.Code, hashes[1])}
	competing := f.raceInvite(t, bodies)
	win := 0
	if competing[0].status != 202 {
		win = 1
	}
	competing[win].expect(t, post, redeemPath, 202)
	competing[1-win].expect(t, post, redeemPath, 409)
	var pending map[string]string
	decodeWire(t, competing[win].body, &pending)
	if competing[win].header.Get("Retry-After") != "5" || len(pending) != 2 ||
		pending["state"] != "pending_approval" || pending["invite_id"] != invite.Invite.ID {
		t.Fatal("pending reply did not identify the bound invite")
	}
	f.call(t, post, redeemPath, "", bodies[win], 202)
	f.call(t, post, approve, f.lead, "", 200)
	f.call(t, post, approve, f.lead, "", 200)
	identical := f.raceInvite(t, [2]string{bodies[win], bodies[win]})
	for _, res := range identical {
		res.expect(t, post, redeemPath, 200)
		var receipt map[string]string
		decodeWire(t, res.body, &receipt)
		if len(receipt) != 5 || receipt["state"] != "redeemed" || receipt["project_id"] != *project ||
			receipt["repo"] != f.repo(t, *project) || receipt["member_id"] != memberID || receipt["hub_role"] != "member" {
			t.Fatal("wrong redemption receipt")
		}
	}
	if !bytes.Equal(identical[0].body, identical[1].body) {
		t.Fatal("identical redeems returned different receipts")
	}
	f.call(t, get, projectPath(*project), tokens[win], "", 200)
	f.call(t, get, projectPath(*project), tokens[1-win], "", 401)
	f.call(t, post, redeemPath, "", bodies[1-win], 409)
	f.call(t, deleteMethod, invitePath+"/"+invite.Invite.ID, f.lead, "", 409)
	var approved hub.InviteResult
	decodeWire(t, f.call(t, post, approve, f.lead, "", 200).body, &approved)
	if string(approved.Invite.State) != "redeemed" {
		t.Fatal("approval retry did not return the redeemed state")
	}

	audited := f.readAudit(t, previous.checkpoint)
	for _, secret := range []string{invite.Code, tokens[0], tokens[1], hashes[0], hashes[1], memberDigest(invite.Code)} {
		if bytes.Contains(audited.body, []byte(secret)) {
			t.Fatal("invite audit export contains secret material")
		}
	}
	var actions []string
	for _, entry := range audited.entries {
		if entry.Target != "invite:"+invite.Invite.ID && entry.Target != "member:"+memberID {
			continue
		}
		actions = append(actions, entry.Action)
		if entry.Action == "member.added" {
			if entry.ActorMemberID != nil || entry.Meta["invite_id"] != invite.Invite.ID || entry.Meta["role"] != "member" {
				t.Fatal("member audit record did not identify the invite")
			}
			continue
		}
		if entry.ActorMemberID == nil || entry.Meta["project_id"] != *project ||
			entry.Meta["member_id"] != memberID || entry.Meta["hub_role"] != "member" {
			t.Fatal("missing invite audit actor or metadata")
		}
	}
	if strings.Join(actions, ",") != "member.added,invite.redeemed,invite.approved,invite.requested,invite.created" {
		t.Fatal("expected exactly one audit entry for each enrollment transition")
	}

	f.call(t, deleteMethod, "/api/v1/admin/members/"+memberID, f.lead, "", 200)
	f.call(t, post, redeemPath, "", bodies[win], 409)
	replacement := f.issueInvite(t, createInviteJSON(*project, memberID, 3600, false))
	f.call(t, post, redeemPath, "", redeemJSON(replacement.Code, hashes[win]), 200)
	f.call(t, post, redeemPath, "", bodies[win], 409)

	live := f.issueInvite(t, createInviteJSON(*project, "unique-"+rand.Text(), 3600, false))
	f.call(t, post, redeemPath, "", redeemJSON(live.Code, hashes[win]), 409)
	f.call(t, deleteMethod, invitePath+"/"+live.Invite.ID, f.lead, "", 200)
}

func (f *fixture) inviteRevocation(t *testing.T) {
	const ttl = 3600
	invite := f.issueInvite(t, createInviteJSON(*project, "revoke-"+rand.Text(), ttl, true))
	path := invitePath + "/" + invite.Invite.ID
	body := redeemJSON(invite.Code, memberDigest(newInviteBearer()))
	f.call(t, post, redeemPath, "", body, 202)
	f.call(t, post, path+"/approve", f.lead, "", 200)
	for range 2 {
		var result hub.InviteResult
		res := f.call(t, deleteMethod, path, f.lead, "", 200)
		decodeWire(t, res.body, &result)
		if string(result.Invite.State) != "revoked" {
			t.Fatal("revoke did not return the revoked state")
		}
	}
	f.call(t, post, path+"/approve", f.lead, "", 409)
	f.call(t, post, redeemPath, "", body, 410)
}

func (f *fixture) directInviteRace(t *testing.T) {
	memberID := "direct-" + rand.Text()
	invite := f.issueInvite(t, createInviteJSON(*project, memberID, 3600, false))
	tokenA, tokenB := newInviteBearer(), newInviteBearer()
	replies := f.raceInvite(t, [2]string{redeemJSON(invite.Code, memberDigest(tokenA)), redeemJSON(invite.Code, memberDigest(tokenB))})
	if replies[0].status == 409 {
		replies[0], replies[1] = replies[1], replies[0]
		tokenA, tokenB = tokenB, tokenA
	}
	replies[0].expect(t, post, redeemPath, 200)
	replies[1].expect(t, post, redeemPath, 409)
	f.call(t, get, projectPath(*project), tokenA, "", 200)
	f.call(t, get, projectPath(*project), tokenB, "", 401)
	var additions, redeems int
	for _, record := range f.readAudit(t).entries {
		if record.Target == "member:"+memberID && record.Action == "member.added" {
			additions++
		}
		if record.Target == "invite:"+invite.Invite.ID && record.Action == "invite.redeemed" {
			redeems++
		}
	}
	if additions != 1 || redeems != 1 {
		t.Fatal("competing redeems must commit one enrollment")
	}
}
