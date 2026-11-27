package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

const inviteAdminPath = "/api/v1/admin/invites"
const redeemPath = "/api/v1/invites/redeem"

func inviteFixture(t *testing.T) (*Store, *time.Time, string) {
	t.Helper()
	s := Open(t.TempDir())
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject("demo", "team/demo"); err != nil {
		t.Fatal(err)
	}
	lead, err := s.AddMember("lead", Lead)
	if err != nil {
		t.Fatal(err)
	}
	return s, &now, lead
}

func inviteCall(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	return call(h, method, path, token, []byte(body), "")
}

func wantInviteStatus(t *testing.T, res *httptest.ResponseRecorder, want int) {
	t.Helper()
	if res.Code != want {
		t.Fatalf("HTTP status %d, want %d", res.Code, want)
	}
}

func inviteCreate(t *testing.T, h http.Handler, lead, body string) CreatedInvite {
	t.Helper()
	res := inviteCall(h, "POST", inviteAdminPath, lead, body)
	wantInviteStatus(t, res, 201)
	var created CreatedInvite
	if json.Unmarshal(res.Body.Bytes(), &created) != nil || len(created.Code) != 43 {
		t.Fatal("invalid create response")
	}
	return created
}

func redeemBody(code, hash string) string {
	body, _ := json.Marshal(redeemInput{Code: code, TokenHash: hash})
	return string(body)
}

func TestInviteExpiryAndRetention(t *testing.T) {
	s, now, lead := inviteFixture(t)
	h := s.Handler()
	start := *now
	const create = `{"project_id":"demo","member_id":"ana","expires_in_seconds":60,"require_approval":true}`
	invite := inviteCreate(t, h, lead, create)
	if !invite.Invite.CreatedAt.Equal(start) || !invite.Invite.ExpiresAt.Equal(start.Add(time.Minute)) || invite.Invite.Role != Member {
		t.Fatal("incorrect invite defaults or times")
	}
	if _, err := s.AddMember("ana", Member); err == nil {
		t.Fatal("direct add accepted a reserved ID")
	}
	hash := hex.EncodeToString(randomBytes(32))
	body := redeemBody(invite.Code, hash)
	*now = invite.Invite.ExpiresAt.Add(-time.Millisecond)
	req := inviteCall(h, "POST", redeemPath, "", body)
	wantInviteStatus(t, req, 202)
	var claim redeemResult
	if req.Header().Get("Retry-After") != "5" || json.Unmarshal(req.Body.Bytes(), &claim) != nil ||
		claim.State != pendingApproval || claim.InviteID != invite.Invite.ID {
		t.Fatal("missing approval interval or public ID")
	}
	*now = invite.Invite.ExpiresAt
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", body), 410)
	path := inviteAdminPath + "/" + invite.Invite.ID
	wantInviteStatus(t, inviteCall(h, "POST", path+"/approve", lead, ""), 409)
	wantInviteStatus(t, inviteCall(h, "DELETE", path, lead, ""), 409)
	page, err := s.ListInvites("", 1)
	if err != nil || len(page.Invites) != 1 || page.Invites[0].State != expired {
		t.Fatal("list did not show expiry")
	}
	*now = now.Add(inviteRetention)
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", body), 404)
	page, err = s.ListInvites("", 1)
	if err != nil || len(page.Invites) != 0 || page.NextCursor != nil {
		t.Fatal("maintenance retained an old row")
	}
	if err := s.view(func(st *state) error {
		if len(st.Invites) != 0 {
			t.Error("maintenance did not purge the stored row")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	inviteCreate(t, h, lead, create)
}

func TestInviteReceiptAndAudit(t *testing.T) {
	s, now, lead := inviteFixture(t)
	h := s.Handler()
	create := `{"project_id":"demo","member_id":"ana","hub_role":"lead","expires_in_seconds":60,"require_approval":true}`
	invite := inviteCreate(t, h, lead, create)
	path := inviteAdminPath + "/" + invite.Invite.ID
	hash := hex.EncodeToString(randomBytes(32))
	body := redeemBody(invite.Code, hash)
	wantInviteStatus(t, inviteCall(h, "POST", path+"/approve", lead, ""), 409)
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", body), 202)
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", body), 202)
	other := redeemBody(invite.Code, hex.EncodeToString(randomBytes(32)))
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", other), 409)
	wantInviteStatus(t, inviteCall(h, "POST", path+"/approve", lead, ""), 200)
	wantInviteStatus(t, inviteCall(h, "POST", path+"/approve", lead, ""), 200)
	first := inviteCall(h, "POST", redeemPath, "", body)
	wantInviteStatus(t, first, 200)
	committed := *now
	*now = committed.Add(inviteReceiptWindow - time.Millisecond)
	retry := inviteCall(h, "POST", redeemPath, "", body)
	wantInviteStatus(t, retry, 200)
	if !bytes.Equal(first.Body.Bytes(), retry.Body.Bytes()) {
		t.Fatal("retry changed the receipt")
	}
	*now = committed.Add(inviteReceiptWindow)
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", body), 409)
	*now = committed.Add(10 * time.Minute)
	wantInviteStatus(t, inviteCall(h, "POST", path+"/approve", lead, ""), 200)
	wantInviteStatus(t, inviteCall(h, "DELETE", path, lead, ""), 409)
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", other), 409)

	export, err := s.ExportAudit()
	if err != nil {
		t.Fatal(err)
	}
	if !audit.VerifyExport(export.Export, export.JWKS, export.Checkpoint).OK {
		t.Fatal("audit verification failed")
	}
	if bytes.Contains(export.Export, []byte(invite.Code)) || bytes.Contains(export.Export, []byte(hash)) ||
		bytes.Contains(export.Export, []byte(lead)) {
		t.Fatal("audit contains secret material")
	}
	var chain struct{ Entries []audit.Record }
	if err := json.Unmarshal(export.Export, &chain); err != nil || len(chain.Entries) != 7 {
		t.Fatal("expected one audit record per transition and roster write")
	}
	if added, consumed := chain.Entries[0], chain.Entries[1]; added.Action != "member.added" ||
		added.ActorMemberID != nil || added.Meta["invite_id"] != invite.Invite.ID ||
		consumed.Action != "invite.redeemed" || consumed.ActorMemberID == nil || *consumed.ActorMemberID != "ana" ||
		consumed.Meta["hub_role"] != "lead" {
		t.Fatal("audit records lack the enrollment details")
	}
	if err := s.RemoveMember("ana"); err != nil {
		t.Fatal(err)
	}
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", body), 409)
	replacement := inviteCreate(t, h, lead, strings.Replace(create, `,"require_approval":true`, "", 1))
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", redeemBody(replacement.Code, hash)), 200)
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", body), 409)
}

func TestInviteRevocationAndValidation(t *testing.T) {
	s, _, lead := inviteFixture(t)
	h := s.Handler()
	create := `{"project_id":"demo","member_id":"ana"}`
	invite := inviteCreate(t, h, lead, create)
	path := inviteAdminPath + "/" + invite.Invite.ID
	wantInviteStatus(t, inviteCall(h, "POST", inviteAdminPath, lead, create), 409)
	wantInviteStatus(t, inviteCall(h, "POST", path+"/approve", lead, ""), 409)
	for range 2 {
		wantInviteStatus(t, inviteCall(h, "DELETE", path, lead, ""), 200)
	}
	wantInviteStatus(t, inviteCall(h, "POST", redeemPath, "", redeemBody(invite.Code, hex.EncodeToString(randomBytes(32)))), 410)
	create = strings.Replace(create, "ana", "bob", 1)
	for _, bad := range []string{
		strings.TrimSuffix(create, "}") + `,"project_id":"demo"}`,
		strings.TrimSuffix(create, "}") + `,"Role":"lead"}`,
		strings.TrimSuffix(create, "}") + `,"require_approval":null}`,
		strings.TrimSuffix(create, "}") + `,"expires_in_seconds":59}`,
		strings.TrimSuffix(create, "}") + `,"expires_in_seconds":60.5}`,
		strings.TrimSuffix(create, "}") + `,"expires_in_seconds":604801}`,
		"[]", "null", create + "{}",
	} {
		wantInviteStatus(t, inviteCall(h, "POST", inviteAdminPath, lead, bad), 400)
	}
	limit := inviteBodyLimit
	wantInviteStatus(t, inviteCall(h, "POST", inviteAdminPath, lead, create+strings.Repeat(" ", limit-len(create)+1)), 413)
	inviteCreate(t, h, lead, create+strings.Repeat(" ", limit-len(create)))
	for _, bad := range []string{"?limit=0", "?limit=101", "?limit=1&%6cimit=1", "?limit=%zz", "?after=", "?extra=1"} {
		wantInviteStatus(t, inviteCall(h, "GET", inviteAdminPath+bad, lead, ""), 400)
	}
	wantInviteStatus(t, inviteCall(h, "GET", inviteAdminPath, lead, " "), 400)
	head := inviteCall(h, "HEAD", inviteAdminPath, lead, "")
	wantInviteStatus(t, head, 200)
	if head.Body.Len() != 0 {
		t.Fatal("HEAD returned a body")
	}
}

func TestRedeemFailureLimit(t *testing.T) {
	s, now, lead := inviteFixture(t)
	h := s.Handler()
	start := *now
	redeem := func(body string) *httptest.ResponseRecorder {
		return inviteCall(h, "POST", redeemPath, "", body)
	}
	pending := inviteCreate(t, h, lead, `{"project_id":"demo","member_id":"ana","require_approval":true}`)
	revoked := inviteCreate(t, h, lead, `{"project_id":"demo","member_id":"bo"}`)
	wantInviteStatus(t, inviteCall(h, "DELETE", inviteAdminPath+"/"+revoked.Invite.ID, lead, ""), 200)
	hash := hex.EncodeToString(randomBytes(32))
	valid := redeemBody(pending.Code, hash)
	unknown := redeemBody(base64.RawURLEncoding.EncodeToString(randomBytes(32)), hash)
	for range 21 {
		res := redeem(valid)
		wantInviteStatus(t, res, 202)
		if res.Header().Get("Retry-After") != "5" {
			t.Fatal("pending approval must keep its retry interval")
		}
	}
	wantInviteStatus(t, redeem("{}"), 400)
	wantInviteStatus(t, redeem(redeemBody(pending.Code, hex.EncodeToString(randomBytes(32)))), 409)
	wantInviteStatus(t, redeem(redeemBody(revoked.Code, hash)), 410)
	for range 18 {
		wantInviteStatus(t, redeem(unknown), 404)
	}

	reopened := Open(s.dir)
	reopened.now = s.now
	h = reopened.Handler()
	assertThrottled := func(retry string) {
		t.Helper()
		res := redeem(unknown)
		wantInviteStatus(t, res, 429)
		if res.Header().Get("Retry-After") != retry || res.Header().Get("Cache-Control") != "no-store" ||
			strings.TrimSpace(res.Body.String()) != `{"error":"too many failed redeem attempts"}` {
			t.Fatal("wrong throttle response")
		}
	}
	assertThrottled("60")
	wantInviteStatus(t, redeem(valid), 202)
	wantInviteStatus(t, inviteCall(h, "POST", inviteAdminPath+"/"+pending.Invite.ID+"/approve", lead, ""), 200)
	wantInviteStatus(t, redeem(valid), 200)
	wantInviteStatus(t, redeem(valid), 200)
	*now = start.Add(time.Minute - time.Millisecond)
	assertThrottled("1")
	*now = start.Add(time.Minute)
	wantInviteStatus(t, redeem(unknown), 404)
}

func TestRosterRequestBodies(t *testing.T) {
	s, _, lead := inviteFixture(t)
	h := s.Handler()
	const members = "/api/v1/admin/members"
	for _, tc := range []struct{ name, body string }{
		{"duplicate", `{"id":"ana","role":"member","role":"member"}`},
		{"unknown", `{"id":"ana","role":"member","unknown":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := inviteCall(h, "POST", members, lead, tc.body)
			wantInviteStatus(t, res, 400)
			if strings.TrimSpace(res.Body.String()) != `{"error":"invalid input: member needs a valid id and role lead or member"}` {
				t.Fatal("wrong roster-add error")
			}
		})
	}
	wantInviteStatus(t, inviteCall(h, "POST", members, lead, `{"id":"ana","role":"member"}`), 200)

	path := members + "/ana"
	t.Run("delete-body", func(t *testing.T) {
		res := inviteCall(h, "DELETE", path, lead, strings.Repeat(" ", inviteBodyLimit))
		wantInviteStatus(t, res, 400)
		if strings.TrimSpace(res.Body.String()) != `{"error":"invalid input: member deletion does not accept a body"}` {
			t.Fatal("wrong roster-delete error")
		}
	})
	wantInviteStatus(t, inviteCall(h, "DELETE", path, lead, strings.Repeat(" ", inviteBodyLimit+1)), 413)
	wantInviteStatus(t, inviteCall(h, "DELETE", path, lead, ""), 200)
}

func TestLegacyMemberGetsStableEnrollment(t *testing.T) {
	s, _, _ := inviteFixture(t)
	raw, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	var old state
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	m := old.Members["lead"]
	m.EnrollmentID = ""
	old.Members["lead"] = m
	if err := s.save(&old); err != nil {
		t.Fatal(err)
	}

	want := m
	want.EnrollmentID = "unset"
	for i := range 2 {
		if err := s.view(func(st *state) error {
			got := st.Members["lead"]
			if i == 0 {
				want.EnrollmentID = got.EnrollmentID
			}
			if got != want || got.EnrollmentID == "" {
				t.Error("member upgrade did not preserve a stable enrollment")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInviteLiveLimit(t *testing.T) {
	s, now, lead := inviteFixture(t)
	if err := s.update(func(st *state) error {
		st.Invites = make(map[string]inviteRecord)
		for range inviteLiveLimit {
			id := hex.EncodeToString(randomBytes(16))
			st.Invites[id] = inviteRecord{Invite: Invite{
				ID: id, State: issued, CreatedAt: *now, ExpiresAt: now.Add(time.Minute),
				MemberID: id, ProjectID: "demo", Role: Member,
			}, CodeHash: sha256.Sum256([]byte(id))}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	const body = `{"project_id":"demo","member_id":"next"}`
	wantInviteStatus(t, inviteCall(h, "POST", inviteAdminPath, lead, body), 409)
	*now = now.Add(time.Minute)
	inviteCreate(t, h, lead, body)
	page, err := s.ListInvites("", DefaultInvitePage)
	if err != nil || len(page.Invites) != DefaultInvitePage || page.NextCursor == nil ||
		*page.NextCursor != page.Invites[len(page.Invites)-1].ID {
		t.Fatal("default page did not stop at the page limit")
	}
}
