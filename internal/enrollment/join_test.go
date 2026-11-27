package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/hub"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

const testOrigin = "https://hub.example.test"

type joinTransport func(*http.Request) (*http.Response, error)

func (f joinTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type joinFixture struct {
	t           *testing.T
	store       *hub.Store
	req         Request
	handler     http.Handler
	keys        []byte
	redeems     int
	hash        string
	redeemReply []byte
	policyReads int
	intercept   func(*http.Request, *httptest.ResponseRecorder) (*http.Response, error)
	before      func(*http.Request)
}

func newJoinFixture(t *testing.T) *joinFixture {
	t.Helper()
	store := hub.Open(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProject("demo", "team/demo"); err != nil {
		t.Fatal(err)
	}
	f := &joinFixture{t: t, store: store, handler: store.Handler()}
	f.req = Request{UserHome: t.TempDir(), Root: t.TempDir(), Now: time.Now}
	f.publish("team/demo", 1, false)
	keys, err := store.PolicyKeys()
	if err != nil {
		t.Fatal(err)
	}
	f.keys, err = json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = joinTransport(f.request)
	return f
}

func (f *joinFixture) publish(repo string, version int, grant bool) {
	f.t.Helper()
	members := "{}"
	if grant {
		members = `{"new-member":"dev"}`
	}
	policy := fmt.Sprintf(`{"version":%d,"project_id":"demo","repos":[%q],"areas":[{"name":"source","patterns":["src/**"]}],"roles":{"dev":{"read":[],"write":["source"],"exec":[]}},"members":%s}`, version, repo, members)
	if _, err := f.store.PublishPolicy("demo", []byte(policy)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *joinFixture) invite(member string, approval bool) hub.Invite {
	f.t.Helper()
	invite, err := f.store.CreateInvite(hub.InviteInput{ProjectID: "demo", MemberID: member, Role: hub.Member, ExpiresIn: 60, RequireApproval: approval})
	if err != nil {
		f.t.Fatal(err)
	}
	fp, err := policy.FingerprintKeySet(f.keys)
	if err != nil {
		f.t.Fatal(err)
	}
	f.req.Link, err = ParseLink(testOrigin + "/join/" + invite.Code + "#fp=" + fp)
	if err != nil {
		f.t.Fatal("could not construct link")
	}
	return invite.Invite
}

func (f *joinFixture) request(req *http.Request) (*http.Response, error) {
	f.t.Helper()
	if f.before != nil {
		f.before(req)
	}
	if req.URL.Host != "hub.example.test" || req.URL.Fragment != "" || strings.HasPrefix(req.URL.Path, "/join/") {
		f.t.Fatal("join sent an unexpected request")
	}
	switch req.URL.Path {
	case redeemPath:
		if req.Header.Get("Authorization") != "" {
			f.t.Fatal("redeem sent bearer")
		}
		f.redeems++
		var data struct {
			TokenHash string `json:"token_hash"`
		}
		body, err := req.GetBody()
		if err != nil || json.NewDecoder(body).Decode(&data) != nil {
			f.t.Fatal("could not decode redeem request")
		}
		body.Close()
		if f.hash != "" && f.hash != data.TokenHash {
			f.t.Fatal("retry generated a new bearer")
		}
		f.hash = data.TokenHash
	case "/.well-known/jwks.json":
		if req.Header.Get("Authorization") != "" {
			f.t.Fatal("JWKS request sent bearer")
		}
	default:
		f.policyReads++
		token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if !validCode(token) || digest(token) != f.hash {
			f.t.Fatal("policy request omitted enrollment bearer")
		}
	}
	reply := httptest.NewRecorder()
	f.handler.ServeHTTP(reply, req)
	if req.URL.Path == redeemPath && reply.Code == http.StatusOK {
		if f.redeemReply != nil && !bytes.Equal(f.redeemReply, reply.Body.Bytes()) {
			f.t.Fatal("receipt changed on retry")
		}
		f.redeemReply = bytes.Clone(reply.Body.Bytes())
	}
	if f.intercept != nil {
		return f.intercept(req, reply)
	}
	return reply.Result(), nil
}

func (f *joinFixture) join(checkpoint func(string) error) (Result, error) {
	return runJoin(f.t.Context(), f.req, checkpoint)
}

func (f *joinFixture) checkUnpublished() {
	f.t.Helper()
	reg, err := projects.Read(f.req.UserHome)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
	if len(reg.Projects()) != 0 {
		f.t.Fatal("join registered an unfinished home")
	}
}

func TestJoinLostResponsesAndApproval(t *testing.T) {
	f := newJoinFixture(t)
	invite := f.invite("new-member", true)
	f.before = func(req *http.Request) {
		if req.URL.Path != redeemPath {
			return
		}
		files, err := filepath.Glob(filepath.Join(f.req.UserHome, ".openplexer", "joins", "*.json"))
		if err != nil || len(files) != 1 {
			t.Fatal("redeem happened before the pending write")
		}
		info, err := os.Stat(files[0])
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("pending state is missing or exposed")
		}
		data, err := os.ReadFile(files[0])
		var record struct{ Enrollment pending }
		if err != nil || json.Unmarshal(data, &record) != nil || !validCode(record.Enrollment.Bearer) ||
			bytes.Contains(data, []byte(f.req.Link.code)) || record.Enrollment.CodeHash != digest(f.req.Link.code) {
			t.Fatal("pending state leaked the invite or omitted the saved bearer")
		}
	}
	lost := true
	f.intercept = func(req *http.Request, res *httptest.ResponseRecorder) (*http.Response, error) {
		if lost && req.URL.Path == redeemPath {
			lost = false
			return nil, io.ErrUnexpectedEOF
		}
		return res.Result(), nil
	}
	if _, err := f.join(nil); err == nil {
		t.Fatal("expected lost approval response")
	}
	f.checkUnpublished()
	r, err := f.join(nil)
	if err != nil || r.PendingID != invite.ID {
		t.Fatal("retry did not recover the public invite ID")
	}
	if _, err := f.store.ApproveInvite(invite.ID); err != nil {
		t.Fatal(err)
	}
	lost = true
	if _, err := f.join(nil); err == nil {
		t.Fatal("expected lost receipt")
	}
	f.checkUnpublished()
	r, err = f.join(nil)
	if err != nil || r.Status.State != daemon.Verified || r.Status.Version != 1 || r.Status.Role != "" {
		t.Fatalf("initial policy was not verified without a role (%v)", err)
	}
	f.publish("team/demo", 2, true)
	r, err = f.join(nil)
	if err != nil || r.Status.Version != 2 || r.Status.Role != "dev" || len(r.Grants.Write) != 1 || f.redeems != 4 {
		t.Fatalf("completed join did not sync the new grant (%v)", err)
	}
}

func TestJoinPinMismatchAndStagedRotation(t *testing.T) {
	f := newJoinFixture(t)
	f.invite("new-member", false)
	old := f.req.Link
	f.req.Link.Fingerprint = strings.Repeat("f", 64)
	if _, err := f.join(nil); err == nil || f.redeems != 0 {
		t.Fatal("join spent the invite with a wrong fingerprint")
	}
	f.checkUnpublished()
	f.req.Link = old
	var doc struct{ Keys []map[string]any }
	if err := json.Unmarshal(f.keys, &doc); err != nil {
		t.Fatal(err)
	}
	var staged map[string]any
	data, _ := json.Marshal(doc.Keys[0])
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatal(err)
	}
	staged["kid"] = "staged"
	changed, _ := json.Marshal(map[string]any{"keys": append(doc.Keys, staged)})
	f.intercept = func(req *http.Request, res *httptest.ResponseRecorder) (*http.Response, error) {
		r := res.Result()
		if req.URL.Path == "/.well-known/jwks.json" && f.redeems > 0 {
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(changed))
		}
		return r, nil
	}
	if _, err := f.join(nil); err == nil || !strings.Contains(err.Error(), "fingerprint") || f.redeems != 1 || f.policyReads != 0 {
		t.Fatal("join admitted a key set that changed during redemption")
	}
	f.checkUnpublished()
	fp, err := policy.FingerprintKeySet(changed)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Link.Fingerprint = fp
	if r, err := f.join(nil); err != nil || r.Status.State != daemon.Verified {
		t.Fatalf("trusted staged pin could not resume join (%v)", err)
	}
	if _, err := Discard(f.req.UserHome, f.req.Root, ""); err != nil {
		t.Fatal(err)
	}
}

func TestJoinResumesLocalWrites(t *testing.T) {
	for _, step := range []string{"reserved", "configured", "synced", "marked", "renamed", "registered"} {
		t.Run(step, func(t *testing.T) {
			f := newJoinFixture(t)
			f.invite("new-member", false)
			f.req.Home = filepath.Join(f.req.UserHome, "custom", "home")
			interrupted := errors.New("test interruption")
			if _, err := f.join(func(got string) error {
				if got == step {
					return interrupted
				}
				return nil
			}); !errors.Is(err, interrupted) {
				t.Fatalf("did not stop at %s (%v)", step, err)
			}
			if step != "registered" {
				f.checkUnpublished()
			}
			var floor []byte
			if step == "renamed" || step == "registered" {
				var err error
				floor, err = os.ReadFile(filepath.Join(f.req.Home, "policy-floor.json"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = Discard(f.req.UserHome, f.req.Root, "")
				if (step == "renamed") != (err != nil) {
					t.Fatal("discard handled the published home incorrectly")
				}
				after, err := os.ReadFile(filepath.Join(f.req.Home, "policy-floor.json"))
				if err != nil || !bytes.Equal(floor, after) {
					t.Fatal("discard changed the floor")
				}
			}
			f.req.Home = ""
			if r, err := f.join(nil); err != nil || r.Status.State != daemon.Verified {
				t.Fatalf("could not resume after %s (%v)", step, err)
			}
		})
	}
}

func TestJoinFailuresReleaseOnlyOnGone(t *testing.T) {
	for _, status := range []int{404, 409, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newJoinFixture(t)
			invite := f.invite("new-member", false)
			switch status {
			case 404:
				f.req.Link.code = strings.Repeat("A", 43)
			case 409:
				req := httptest.NewRequest("POST", redeemPath, strings.NewReader(fmt.Sprintf(`{"code":%q,"token_hash":%q}`, f.req.Link.code, strings.Repeat("a", 64))))
				res := httptest.NewRecorder()
				f.handler.ServeHTTP(res, req)
				if res.Code != 200 {
					t.Fatal("could not preclaim invite")
				}
			case 410:
				if _, err := f.store.RevokeInvite(invite.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.join(nil); err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Fatal("join missed redeem failure")
			}
			f.checkUnpublished()
			f.invite("replacement", false)
			f.hash = ""
			if status != 410 {
				before := f.redeems
				if _, err := f.join(nil); err == nil || before != f.redeems {
					t.Fatal("pending reservation did not stop a replacement invite")
				}
				if _, err := Discard(f.req.UserHome, f.req.Root, ""); err != nil {
					t.Fatal(err)
				}
			}
			if r, err := f.join(nil); err != nil || r.Status.State != daemon.Verified {
				t.Fatalf("new invite could not join same root (%v)", err)
			}
		})
	}
}

func TestJoinRejectsRedirectAndInvalidReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"redirect", "", 307},
		{"duplicate", `{"state":"redeemed","state":"redeemed","project_id":"demo","repo":"team/demo","member_id":"new-member","hub_role":"member"}`, 200},
		{"unknown", `{"state":"redeemed","project_id":"demo","repo":"team/demo","member_id":"new-member","hub_role":"member","unknown":1}`, 200},
		{"case", `{"state":"redeemed","Project_id":"demo","repo":"team/demo","member_id":"new-member","hub_role":"member"}`, 200},
		{"wrong repo", `{"state":"redeemed","project_id":"demo","repo":"team/another","member_id":"new-member","hub_role":"member"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJoinFixture(t)
			f.invite("new-member", false)
			f.intercept = func(req *http.Request, res *httptest.ResponseRecorder) (*http.Response, error) {
				if req.URL.Path != redeemPath {
					return res.Result(), nil
				}
				res = httptest.NewRecorder()
				res.Header().Set("Location", "/elsewhere")
				res.WriteHeader(tc.status)
				res.WriteString(tc.body)
				return res.Result(), nil
			}
			if _, err := f.join(nil); err == nil || f.redeems != 1 {
				t.Fatal("join accepted an unusable redeem response")
			}
			f.checkUnpublished()
		})
	}
}

func TestJoinDiscardRecoversInterruptedReservationWrite(t *testing.T) {
	f := newJoinFixture(t)
	root, err := projects.CanonicalPath(f.req.Root)
	if err != nil {
		t.Fatal(err)
	}
	joins := filepath.Join(f.req.UserHome, ".openplexer", "joins")
	if err := os.MkdirAll(joins, 0o700); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(joins, digest(root)+".json.tmp")
	if err := os.WriteFile(temp, []byte("interrupted owner-only reservation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discard(f.req.UserHome, f.req.Root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("discard retained an interrupted reservation write")
	}
	if f.redeems != 0 {
		t.Fatal("discard contacted the hub")
	}
}

func TestJoinSerializesCanonicalRoot(t *testing.T) {
	f := newJoinFixture(t)
	f.invite("new-member", false)
	alias := filepath.Join(f.req.UserHome, "checkout-link")
	if err := os.Symlink(f.req.Root, alias); err != nil {
		t.Fatal(err)
	}
	paused, resume := make(chan struct{}), make(chan struct{})
	results := make(chan error, 2)
	go func() {
		_, err := f.join(func(step string) error {
			if step == "reserved" {
				close(paused)
				<-resume
			}
			return nil
		})
		results <- err
	}()
	select {
	case <-paused:
	case err := <-results:
		t.Fatalf("could not reserve first join: %v", err)
	}
	go func() {
		req := f.req
		req.Root = alias
		_, err := Join(t.Context(), req)
		results <- err
	}()
	close(resume)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if f.redeems != 1 {
		t.Fatalf("the same root redeemed %d times", f.redeems)
	}
}

func TestJoinResumesAfterPolicyUnavailable(t *testing.T) {
	f := newJoinFixture(t)
	f.invite("new-member", false)
	f.intercept = func(req *http.Request, res *httptest.ResponseRecorder) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/policy.bundle") {
			res = httptest.NewRecorder()
			res.WriteHeader(http.StatusNotFound)
		}
		return res.Result(), nil
	}
	if _, err := f.join(nil); err == nil || !strings.Contains(err.Error(), "initial policy sync") {
		t.Fatal("join did not report unavailable policy")
	}
	f.checkUnpublished()
	f.intercept = nil
	if r, err := f.join(nil); err != nil || r.Status.Version != 1 || f.redeems != 2 {
		t.Fatalf("join did not recover its receipt and sync (%v)", err)
	}
}
