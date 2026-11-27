package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const membersURL = "/api/v1/admin/members"
const syncPolicy = "/api/v1/projects/shop/policy.bundle"
const syncStream = "/api/v1/rooms/shop/stream"

func syncStore(t *testing.T) (*Store, string, string, *atomic.Int64) {
	t.Helper()
	s, token := roomStore(t, "shop", "mem-ana")
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	lead, err := s.AddMember("lead", Lead)
	if err != nil {
		t.Fatal(err)
	}
	return s, token, lead, &clock
}

func syncPublish(t *testing.T, s *Store, project string, version int) {
	t.Helper()
	if _, err := s.PublishPolicy(project, []byte(fmt.Sprintf(
		`{"areas":[],"members":{},"project_id":%q,"repos":["api"],"roles":{},"version":%d}`, project, version,
	))); err != nil {
		t.Fatal(err)
	}
}

func syncPage(t *testing.T, h http.Handler, lead, query string) MembersPage {
	t.Helper()
	r := call(h, http.MethodGet, membersURL+query, lead, nil, "")
	wantStatus(t, r, http.StatusOK)
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("expected uncached member list")
	}
	var page MembersPage
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
		t.Fatal("invalid member page")
	}
	return page
}

func syncRow(t *testing.T, h http.Handler, lead string) MemberSync {
	t.Helper()
	for _, row := range syncPage(t, h, lead, "?project_id=shop").Members {
		if row.MemberID == "mem-ana" {
			return row
		}
	}
	t.Fatal("member missing from list")
	return MemberSync{}
}

func assertSync(t *testing.T, row MemberSync, policyAt, roomAt string, version int64) {
	t.Helper()
	assertTime := func(observed *string, want string) {
		t.Helper()
		if want == "" && observed != nil || want != "" && (observed == nil || *observed != want) {
			t.Errorf("unexpected observation time, want %q", want)
		}
	}
	assertTime(row.LastPolicyFetchAt, policyAt)
	assertTime(row.LastRoomConnectedAt, roomAt)
	if policyAt == "" && row.PolicyVersion != nil || policyAt != "" && (row.PolicyVersion == nil || *row.PolicyVersion != version) {
		t.Errorf("unexpected observed version, want %d when fetch is retained", version)
	}
}

func TestMemberSyncFetchExpiryAndRevocation(t *testing.T) {
	s, token, lead, clock := syncStore(t)
	h := s.Handler()
	now := func() string { return s.now().UTC().Format(timestampFmt) }
	expiry := observationRetention.Milliseconds()
	initial := clock.Load()
	if err := s.AddProject("unpublished", "api"); err != nil {
		t.Fatal(err)
	}
	syncPublish(t, s, "shop", 1)
	before, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, call(h, http.MethodGet, syncPolicy, token, nil, ""), http.StatusOK)
	fetchAt := now()
	assertSync(t, syncRow(t, h, lead), fetchAt, "", 1)
	after, err := os.ReadFile(s.path())
	if err != nil || string(after) != string(before) {
		t.Fatal("policy fetch rewrote Room or audit state")
	}
	clock.Add(1000)
	syncPublish(t, s, "shop", 2)
	head := call(h, http.MethodHead, syncPolicy, token, nil, "")
	wantStatus(t, head, http.StatusOK)
	if head.Header().Get(PolicyVersionHeader) != "2" || head.Body.Len() != 0 {
		t.Fatal("policy HEAD did not return just the current version header")
	}
	for _, tc := range []struct {
		method, path, credential string
		status                   int
	}{
		{http.MethodGet, "/api/v1/projects/unpublished/policy.bundle", token, 404},
		{http.MethodGet, syncPolicy, "", 401},
		{http.MethodGet, syncStream + "?last_seq=bad", token, 400},
		{http.MethodGet, "/api/v1/rooms/missing/stream", token, 404},
		{http.MethodGet, syncStream, "", 401},
		{http.MethodHead, syncStream, token, 200},
		{http.MethodGet, "/api/v1/rooms/shop/events", token, 200},
		{http.MethodGet, "/api/v1/projects/shop", token, 200},
	} {
		wantStatus(t, call(h, tc.method, tc.path, tc.credential, nil, ""), tc.status)
	}
	assertSync(t, syncRow(t, h, lead), fetchAt, "", 1)
	wantStatus(t, call(h, http.MethodGet, syncPolicy, token, nil, ""), http.StatusOK)
	latestFetch := now()
	assertSync(t, syncRow(t, h, lead), latestFetch, "", 2)

	clock.Add(1000)
	conn := startSyncStream(t, h, http.MethodGet, token, nil)
	roomAt := now()
	conn.nextFlush(t)
	conn.stop(t)
	assertSync(t, syncRow(t, h, lead), latestFetch, roomAt, 2)

	clock.Store(initial + 1000 + expiry - 1)
	assertSync(t, syncRow(t, h, lead), latestFetch, roomAt, 2)
	clock.Add(1)
	assertSync(t, syncRow(t, h, lead), "", roomAt, 0)
	if err := s.PruneMemberObservations(); err != nil {
		t.Fatal(err)
	}
	clock.Add(-1)
	assertSync(t, syncRow(t, h, lead), "", roomAt, 0)
	clock.Store(initial + 2000 + expiry)
	assertSync(t, syncRow(t, h, lead), "", "", 0)
	if err := s.PruneMemberObservations(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.observationsPath("mem-ana")); !os.IsNotExist(err) {
		t.Fatal("maintenance left an empty member file")
	}
	wantStatus(t, call(h, http.MethodGet, syncPolicy, token, nil, ""), http.StatusOK)
	if err := Open(s.dir).RemoveMember("mem-ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.observationsPath("mem-ana")); !os.IsNotExist(err) {
		t.Fatal("member removal retained observations")
	}
	if got := syncPage(t, h, lead, "").Members; len(got) != 1 || got[0].MemberID != "lead" || got[0].ProjectID != nil {
		t.Fatal("expected only the unobserved lead")
	}
	newToken, err := s.AddMember("mem-ana", Member)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, call(h, http.MethodGet, syncPolicy, token, nil, ""), 401)
	wantStatus(t, call(h, http.MethodGet, syncStream, token, nil, ""), 401)
	assertSync(t, syncRow(t, h, lead), "", "", 0)
	wantStatus(t, call(h, http.MethodGet, syncPolicy, newToken, nil, ""), 200)
	assertSync(t, syncRow(t, h, lead), now(), "", 2)
}

func TestMemberSyncStreamAdmissionAndPolls(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			s, token, lead, clock := syncStore(t)
			h := s.Handler()
			conn := startSyncStream(t, h, method, token, nil)
			defer conn.cancel()
			conn.nextFlush(t)
			at := s.now().UTC().Format(timestampFmt)
			assertSync(t, syncRow(t, h, lead), "", at, 0)
			clock.Add(1000)
			conn.nextFlush(t)
			assertSync(t, syncRow(t, h, lead), "", at, 0)
			conn.stop(t)
		})
	}
}

func TestMemberSyncPostCannotRestoreRevokedState(t *testing.T) {
	s, token, lead, _ := syncStore(t)
	h := s.Handler()
	entered, resume := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(resume) })
	defer unblock()
	conn := startSyncStream(t, h, http.MethodPost, token, func() {
		close(entered)
		<-resume
	})
	defer conn.cancel()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not reach admission")
	}
	if err := s.RemoveMember("mem-ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMember("mem-ana", Member); err != nil {
		t.Fatal(err)
	}
	unblock()
	conn.stop(t)
	wantStatus(t, conn.ResponseRecorder, 401)
	assertSync(t, syncRow(t, h, lead), "", "", 0)
}

type syncStreamWriter struct {
	*httptest.ResponseRecorder
	enable func()
	cancel context.CancelFunc
	done   chan struct{}
	flush  chan struct{}
}

func startSyncStream(t *testing.T, h http.Handler, method, token string, enable func()) *syncStreamWriter {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, method, syncStream, nil)
	if method == http.MethodPost {
		// Keep the upload open until the test closes the stream.
		req.Body = pendingStreamBody{ctx}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	w := &syncStreamWriter{
		ResponseRecorder: httptest.NewRecorder(), enable: enable, cancel: cancel,
		done: make(chan struct{}), flush: make(chan struct{}, 1),
	}
	t.Cleanup(cancel)
	go func() {
		defer close(w.done)
		h.ServeHTTP(w, req)
	}()
	return w
}

type pendingStreamBody struct{ ctx context.Context }

func (b pendingStreamBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (pendingStreamBody) Close() error { return nil }

func (w *syncStreamWriter) EnableFullDuplex() error {
	if w.enable != nil {
		w.enable()
	}
	return nil
}
func (*syncStreamWriter) SetReadDeadline(time.Time) error  { return nil }
func (*syncStreamWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *syncStreamWriter) Flush() {
	select {
	case w.flush <- struct{}{}:
	default:
	}
}
func (w *syncStreamWriter) nextFlush(t *testing.T) {
	t.Helper()
	select {
	case <-w.flush:
	case <-w.done:
		t.Fatal("stream ended before flush")
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not flush")
	}
}
func (w *syncStreamWriter) stop(t *testing.T) {
	t.Helper()
	// A rejected admission can end by itself. For a live stream, cancel its request.
	if w.enable == nil {
		w.cancel()
	}
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close")
	}
}

func TestMemberSyncListQueries(t *testing.T) {
	s, token, lead, _ := syncStore(t)
	h := s.Handler()
	upper, err := s.AddMember("mem-Ana", Member)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject("Shop", "api"); err != nil {
		t.Fatal(err)
	}
	syncPublish(t, s, "shop", 1)
	syncPublish(t, s, "Shop", 3)
	wantStatus(t, call(h, http.MethodGet, syncPolicy, token, nil, ""), 200)
	wantStatus(t, call(h, http.MethodGet, "/api/v1/projects/Shop/policy.bundle", token, nil, ""), 200)
	wantStatus(t, call(h, http.MethodGet, syncPolicy, upper, nil, ""), 200)
	var got []string
	q := "?limit=1"
	for range 4 {
		p := syncPage(t, h, lead, q)
		if len(p.Members) != 1 {
			t.Fatal("expected a one-row page")
		}
		row := p.Members[0]
		key := row.MemberID
		if row.ProjectID != nil {
			key += "/" + *row.ProjectID
		}
		got = append(got, key)
		if p.NextCursor == nil {
			break
		}
		pair, _ := json.Marshal([]*string{&row.MemberID, row.ProjectID})
		if *p.NextCursor != base64URL(pair) {
			t.Fatal("incorrect cursor")
		}
		q = "?limit=1&after=" + url.QueryEscape(*p.NextCursor)
	}
	if !slices.Equal(got, []string{"lead", "mem-Ana/shop", "mem-ana/Shop", "mem-ana/shop"}) {
		t.Fatalf("unexpected member/project order: %v", got)
	}
	filtered := syncPage(t, h, lead, "?project_id=Shop")
	if len(filtered.Members) != 3 || filtered.Members[0].ProjectID == nil || *filtered.Members[0].ProjectID != "Shop" ||
		filtered.Members[0].PolicyVersion != nil || filtered.Members[1].PolicyVersion != nil ||
		filtered.Members[2].PolicyVersion == nil || *filtered.Members[2].PolicyVersion != 3 {
		t.Fatal("project filter lost members or mixed observations")
	}
	deleted := base64URL([]byte(`["mem-bob",null]`))
	if p := syncPage(t, h, lead, "?after="+deleted); len(p.Members) != 0 || p.NextCursor != nil {
		t.Fatal("expected an empty final page after an absent member ID")
	}

	for _, query := range []string{
		"?after", "?after=bad!", "?after=" + base64URL([]byte(`["lead"]`)),
		"?after=" + base64URL([]byte(`[ "lead",null]`)),
		"?after=" + base64URL([]byte(`["lead",123]`)),
		"?limit=0", "?limit=101", "?limit=1.0", "?limit=1&%6cimit=1", "?unknown=x",
		"?project_id=", "?project_id=bad/name", "?project_id=shop;limit=1", "?project_id=%zz",
	} {
		wantStatus(t, call(h, http.MethodGet, membersURL+query, lead, nil, ""), 400)
	}
	wantStatus(t, call(h, http.MethodGet, membersURL+"?project_id=missing", lead, nil, ""), 404)
	wantStatus(t, call(h, http.MethodGet, membersURL, lead, []byte("{}"), ""), 400)
	wantStatus(t, call(h, http.MethodGet, membersURL, lead, make([]byte, (64<<10)+1), ""), 413)
	wantStatus(t, call(h, http.MethodGet, membersURL, token, nil, ""), 403)
	unauth := call(h, http.MethodGet, membersURL, "", nil, "")
	wantStatus(t, unauth, 401)
	if unauth.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing bearer challenge")
	}
	head := call(h, http.MethodHead, membersURL, lead, nil, "")
	wantStatus(t, head, 200)
	if head.Body.Len() != 0 || head.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("HEAD returned a body or omitted cache control")
	}
	wrong := call(h, http.MethodPut, membersURL, lead, nil, "")
	wantStatus(t, wrong, 405)
	if wrong.Header().Get("Allow") != "GET, HEAD, POST" || wrong.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect method or cache header")
	}
}
