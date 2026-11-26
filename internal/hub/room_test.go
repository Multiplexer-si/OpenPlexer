package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

func TestDownloadRoom(t *testing.T) {
	const id = "demo"
	store, token := roomStore(t, id, "member")
	handler := store.Handler()
	path := "/api/v1/rooms/" + id + "/stream"
	for _, query := range []string{"?last_seq=", "?last_seq=-1", "?last_seq=0&last_seq=1", "?last_seq=9007199254740992", "?bad=%ZZ"} {
		wantStatus(t, call(handler, http.MethodGet, path+query, token, nil, ""), http.StatusBadRequest)
	}
	wantStatus(t, call(handler, http.MethodGet, path, "", nil, ""), http.StatusUnauthorized)
	wantStatus(t, call(handler, http.MethodGet, "/api/v1/rooms/missing/stream", token, nil, ""), http.StatusNotFound)

	post := func() {
		t.Helper()
		body := []byte(`{"type":"human","body":{"text":"hello"}}`)
		wantStatus(t, call(handler, http.MethodPost, eventsURL(id), token, body, ""), http.StatusOK)
	}
	post()
	post()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, path+"?last_seq=1", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	var frames []string
	w := &streamRecorder{ResponseRecorder: httptest.NewRecorder()}
	w.flush = func() {
		frames = append(frames, w.Body.String())
		w.Body.Reset()
		if len(frames) == 2 {
			post()
		}
		if len(frames) == 3 {
			cancel()
		}
	}
	handler.ServeHTTP(w, request)
	wantStatus(t, w.ResponseRecorder, http.StatusOK)
	if w.Header().Get("Content-Type") != roomStreamType || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("stream headers: %v", w.Header())
	}
	if len(frames) != 3 || frames[1] != "\n" {
		t.Fatalf("expected a replay event, a heartbeat, and a live event, got %q", frames)
	}
	for i, frame := range []string{frames[0], frames[2]} {
		message, err := room.ParseMessage([]byte(frame))
		seq, ok := message.Seq()
		if err != nil || !ok || seq != int64(i+2) || message.Room() != id {
			t.Fatalf("unexpected frame: %s (%v)", frame, err)
		}
	}
}

func TestRoomReplaysLegacyDenialTimeAfterUpgrade(t *testing.T) {
	const legacyTime = "2026-01-15T11:00:00Z"
	const key = "legacy-denial"
	store, token := roomStore(t, "demo", "member")
	// The base hub treated decided_at as a body extension and stored it as sent.
	legacyEvent := json.RawMessage(`{
		"id":"legacy","seq":1,"mission":"demo","type":"policy.denied",
		"agent":{"id":"member/session","vendor":"codex"},"on_behalf_of":"member",
		"at":"2026-01-15T12:00:00.000Z",
		"body":{"action":"write","path":"src/main.go","rule":"locked","decided_at":"` + legacyTime + `"}
	}`)
	if err := store.update(func(st *state) error {
		st.Rooms["demo"] = []eventRecord{{Event: legacyEvent, Key: key, Member: "member"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	upgraded := Open(store.dir)
	upgraded.now = func() time.Time { return time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) }
	h, path := upgraded.Handler(), eventsURL("demo")
	post := func(decidedAt string) []byte {
		return fmt.Appendf(nil, `{"type":"policy.denied","agent":{"id":"session","vendor":"codex"},"body":{"action":"write","path":"src/main.go","rule":"locked","decided_at":%q}}`, decidedAt)
	}
	wantStatus(t, call(h, http.MethodPost, path, token, post(legacyTime), "fresh"), http.StatusBadRequest)
	current := eventResponse(t, call(h, http.MethodPost, path, token, post("2026-01-15T11:00:00.000Z"), "fresh"))
	if current.Seq() != 2 {
		t.Fatalf("new event sequence = %d, want 2", current.Seq())
	}

	res := call(h, http.MethodGet, path, token, nil, "")
	wantStatus(t, res, http.StatusOK)
	var page []json.RawMessage
	decode(t, res.Body.Bytes(), &page)
	if len(page) != 2 {
		t.Fatalf("replay returned %d events, want 2", len(page))
	}
	legacy := parseEventMessage(t, page[0])
	if legacy.ID() != "legacy" || legacy.Seq() != 1 || parseEventMessage(t, page[1]).ID() != current.ID() {
		t.Fatalf("unexpected replay: %s", res.Body.Bytes())
	}
	if legacy.Body().(*room.PolicyDenied).DecidedAt != "" {
		t.Fatal("typed body exposed a decision time it could not parse")
	}
	var got, want map[string]any
	decode(t, encode(t, legacy), &got)
	decode(t, legacyEvent, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed payload = %v, want %v", got, want)
	}

	future := post("2026-01-16T12:00:00.000Z")
	wantStatus(t, call(h, http.MethodPost, path, token, future, "unknown"), http.StatusBadRequest)
	retry := call(h, http.MethodPost, path, token, future, key)
	wantStatus(t, retry, http.StatusOK)
	if !bytes.Equal(bytes.TrimSpace(retry.Body.Bytes()), page[0]) {
		t.Fatalf("retry = %s, want %s", retry.Body.Bytes(), page[0])
	}
}

type streamRecorder struct {
	*httptest.ResponseRecorder
	flush func()
}

func (*streamRecorder) SetWriteDeadline(time.Time) error { return nil }

func (w *streamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.flush()
}

func TestReadResumesTrailingWhitespace(t *testing.T) {
	const roomID = "mission"
	want := room.Resume{LastSeq: 7}
	message := encode(t, room.ResumeMessage(room.Sender{ID: "daemon", Kind: room.Daemon}, roomID, want.LastSeq))
	for _, tc := range []struct {
		name   string
		suffix string
		accept bool
	}{
		{"form feed", "\f", false},
		{"no-break space", "\u00a0", false},
		{"space and tab", " \t", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(message)+tc.suffix+"\n"))
			request.SetPathValue("room", roomID)
			resumes := make(chan room.Resume, 1)
			readResumes(request.Context(), request, resumes)
			got, ok := <-resumes
			if ok != tc.accept || (ok && got != want) {
				t.Fatalf("resume = %+v, received = %t, want received = %t with %+v", got, ok, tc.accept, want)
			}
		})
	}
}

func TestRoomSequenceAndResume(t *testing.T) {
	var delivery struct {
		Cases []struct {
			Name     string
			Input    struct{ Since int64 }
			Expected struct{ Sent []int64 }
		}
	}
	fixture(t, "fixtures/deliver.json", &delivery)
	first := delivery.Cases[0]
	var status map[string]json.RawMessage
	fixture(t, "examples/room-event/status.json", &status)
	example := readEvent(t, encode(t, status))
	otherRoom := example.Mission() + "-other"
	s, token := roomStore(t, example.Mission(), example.OnBehalfOf())
	if err := s.AddProject(otherRoom, "api"); err != nil {
		t.Fatal(err)
	}
	when := parseTime(t, example.At()).Add(time.Second)
	s.now = func() time.Time { return when }
	h := s.Handler()
	path := eventsURL(example.Mission())

	// A rejected event consumes no sequence number.
	invalid := encode(t, map[string]any{"type": example.Type(), "agent": status["agent"], "body": map[string]any{}})
	res := call(h, "POST", path, token, invalid, "")
	wantStatus(t, res, http.StatusBadRequest)

	responses := make(chan *httptest.ResponseRecorder, len(first.Expected.Sent))
	payload := encode(t, status)
	for idx := range first.Expected.Sent {
		go func() { responses <- call(h, "POST", path, token, payload, strconv.Itoa(idx)) }()
	}
	for range first.Expected.Sent {
		wantStatus(t, <-responses, http.StatusOK)
	}
	restarted := Open(s.dir).Handler()
	got := events(t, restarted, path+"?last_seq="+strconv.FormatInt(first.Input.Since, 10), token)
	assertSequences(t, got, first.Expected.Sent)
	for _, ev := range got {
		if ev.Mission() != example.Mission() || ev.OnBehalfOf() != example.OnBehalfOf() ||
			ev.Agent().ID != example.OnBehalfOf()+"/"+example.Agent().ID || !reflect.DeepEqual(ev.Body(), example.Body()) {
			t.Fatalf("stored event identity or body differs from the submitted event: %v", ev)
		}
		if ev.ID() == example.ID() || ev.At() != when.Format(timestampFmt) {
			t.Fatal("client-assigned id or timestamp survived the post")
		}
	}

	// A replay request uses its own cursor, including after another request
	// for this Room supplied a higher cursor.
	tail := first.Expected.Sent[1:]
	assertSequences(t, events(t, restarted, fmt.Sprintf("%s?last_seq=%d", path, tail[0]), token), tail[1:])
	last := first.Expected.Sent[len(first.Expected.Sent)-1]
	assertSequences(t, events(t, restarted, fmt.Sprintf("%s?last_seq=%d", path, last+1), token), nil)
	assertSequences(t, events(t, restarted, fmt.Sprintf("%s?last_seq=%d", path, first.Input.Since), token), first.Expected.Sent)
	wantStatus(t, call(h, "GET", path+"?last_seq=-1", token, nil, ""), http.StatusBadRequest)
	wantStatus(t, call(h, "GET", path+"?last_seq=0&last_seq=1", token, nil, ""), http.StatusBadRequest)
	wantStatus(t, call(h, "GET", eventsURL("missing"), token, nil, ""), http.StatusNotFound)
	wantStatus(t, call(h, "GET", path, "", nil, ""), http.StatusUnauthorized)

	repeat := call(restarted, "POST", path, token, encode(t, status), "0")
	repeatedEvent := eventResponse(t, repeat)
	ids := make([]string, 0, len(got))
	for _, ev := range got {
		ids = append(ids, ev.ID())
	}
	if !slices.Contains(ids, repeatedEvent.ID()) {
		t.Fatal("retry did not return an existing event")
	}
	assertSequences(t, events(t, restarted, path, token), first.Expected.Sent)

	other := eventsURL(otherRoom)
	otherEvent := eventResponse(t, call(restarted, "POST", other, token, encode(t, status), "0"))
	assertSequences(t, []room.Event{otherEvent}, first.Expected.Sent[:1])
	if otherEvent.Mission() != otherRoom || slices.Contains(ids, otherEvent.ID()) {
		t.Fatal("events or idempotency keys leaked across Rooms")
	}
}

func TestClaimRenewalAndExpiry(t *testing.T) {
	source, expiry, expired := claimFixtures(t)
	owner := source.OnBehalfOf()
	agent := source.Agent().ID
	otherRoom := source.Mission() + "-other"
	s, token := roomStore(t, source.Mission(), owner)
	if err := s.AddProject(otherRoom, "other-repo"); err != nil {
		t.Fatal(err)
	}
	other, err := s.AddMember("other-member", Member)
	if err != nil {
		t.Fatal(err)
	}
	now := parseTime(t, source.At())
	s.now = func() time.Time { return now }
	h := s.Handler()
	path := eventsURL(source.Mission())
	claims := claimsURL(source.Mission())
	claim, _ := createFrom(t, h, claims, token, source, nil)
	if !claim.ExpiresAt.Equal(expiry) {
		t.Fatalf("expires_at = %v, fixture expiry = %v", claim.ExpiresAt, expiry)
	}
	initial := events(t, h, path, token)
	assertSequences(t, initial, []int64{source.Seq()})
	leaseBody := initial[0].Body().(*room.Claim)
	sourceBody := source.Body().(*room.Claim)
	if leaseBody.ClaimID != claim.ID || leaseBody.Item != sourceBody.Item ||
		leaseBody.Intent != sourceBody.Intent || !slices.Equal(leaseBody.Paths, sourceBody.Paths) ||
		leaseBody.TTLSeconds != sourceBody.TTLSeconds {
		t.Fatalf("created claim did not preserve fixture fields: %+v", leaseBody)
	}

	now = expiry.Add(-time.Millisecond)
	prefix := claims + "/" + claim.ID
	validRenew := encode(t, map[string]any{"session_id": agent, "ttl_seconds": sourceBody.TTLSeconds})
	wrongSession := encode(t, map[string]any{"session_id": agent + "-other", "ttl_seconds": sourceBody.TTLSeconds})
	wantStatus(t, call(h, "POST", prefix+"/renew", other, validRenew, ""), http.StatusNotFound)
	wantStatus(t, call(h, "POST", prefix+"/renew", token, wrongSession, ""), http.StatusNotFound)
	wantStatus(t, call(h, "POST", prefix+"/release", other, validRenew, ""), http.StatusNotFound)

	renew := call(h, "POST", prefix+"/renew", token, validRenew, "")
	wantStatus(t, renew, http.StatusOK)
	var renewed lease
	decode(t, renew.Body.Bytes(), &renewed)
	wantExpiry := now.Add(time.Duration(sourceBody.TTLSeconds) * time.Second)
	if !renewed.ExpiresAt.Equal(wantExpiry) || !renewed.ExpiresAt.After(claim.ExpiresAt) {
		t.Fatalf("renewed expiry = %v, want %v", renewed.ExpiresAt, wantExpiry)
	}
	// At the original expiry the receiver stops listing the claim, while the
	// hub still holds the renewed lease and has posted no additional event.
	now = expiry
	still := events(t, h, path, token)
	assertSequences(t, still, []int64{source.Seq()})
	if still[0].ID() != initial[0].ID() {
		t.Fatal("renew changed the initial Room event")
	}
	receiver := room.NewReceiver()
	receiver.Receive(encode(t, room.EventMessage(still[0])), now)
	if !slices.ContainsFunc(receiver.Claims(source.Mission(), now.Add(-time.Millisecond)), func(c room.ActiveClaim) bool {
		return c.ID == claim.ID
	}) || len(receiver.Claims(source.Mission(), now)) != 0 {
		t.Fatal("receiver did not stop listing the claim at the original expiry")
	}
	wantStatus(t, call(h, "POST", prefix+"/renew", token, wrongSession, ""), http.StatusNotFound)
	assertSequences(t, events(t, h, path, token), []int64{source.Seq()})

	// A restart preserves the extended lease. At its exact expiry a claim
	// operation in another Room persists the release even if that call fails.
	restart := Open(s.dir)
	restart.now = s.now
	h = restart.Handler()
	now = wantExpiry
	assertSequences(t, events(t, h, path, token), []int64{source.Seq()})
	otherPrefix := claimsURL(otherRoom) + "/" + claim.ID
	wantStatus(t, call(h, "POST", otherPrefix+"/renew", token, validRenew, ""), http.StatusNotFound)
	all := events(t, h, path, token)
	assertSequences(t, all, []int64{source.Seq(), expired.Seq()})
	release := all[1]
	if release.Type() != expired.Type() || release.Agent() != initial[0].Agent() ||
		release.OnBehalfOf() != expired.OnBehalfOf() || release.At() != now.Format(timestampFmt) ||
		release.Body().(*room.Release).Outcome != expired.Body().(*room.Release).Outcome ||
		release.Body().(*room.Release).ClaimID != claim.ID {
		t.Fatalf("expiry release does not match the spec: %+v", release.Body())
	}
	wantStatus(t, call(h, "POST", prefix+"/renew", token, validRenew, ""), http.StatusNotFound)
	assertSequences(t, events(t, h, path, token), []int64{source.Seq(), expired.Seq()})
}

func TestClaimRenewalCanShortenLease(t *testing.T) {
	source, _, _ := claimFixtures(t)
	s, token := roomStore(t, source.Mission(), source.OnBehalfOf())
	now := parseTime(t, source.At())
	s.now = func() time.Time { return now }
	h := s.Handler()
	path := claimsURL(source.Mission())
	claim, _ := createFrom(t, h, path, token, source, map[string]any{"ttl_seconds": 30})

	now = now.Add(5 * time.Second)
	body := encode(t, map[string]any{"session_id": source.Agent().ID, "ttl_seconds": 10})
	res := call(h, "POST", path+"/"+claim.ID+"/renew", token, body, "")
	wantStatus(t, res, http.StatusOK)
	var renewed lease
	decode(t, res.Body.Bytes(), &renewed)
	if want := now.Add(10 * time.Second); !renewed.ExpiresAt.Equal(want) || !renewed.ExpiresAt.Before(claim.ExpiresAt) {
		t.Fatalf("renewed expiry = %v, want %v before original %v", renewed.ExpiresAt, want, claim.ExpiresAt)
	}
	assertSequences(t, events(t, h, eventsURL(source.Mission()), token), []int64{source.Seq()})
}

func TestClaimConflictsAndRelease(t *testing.T) {
	source, _, _ := claimFixtures(t)
	s, token := roomStore(t, source.Mission(), source.OnBehalfOf())
	sameRepo := source.Mission() + "-same"
	otherRepo := source.Mission() + "-different"
	for id, repo := range map[string]string{sameRepo: "api", otherRepo: "web"} {
		if err := s.AddProject(id, repo); err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.AddMember("other-member", Member)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	path := claimsURL(source.Mission())
	exclusive := map[string]any{"mode": "exclusive"}
	first, _ := createFrom(t, h, path, token, source, exclusive)
	second, conflicts := createFrom(t, h, path, token, source, exclusive)
	if len(conflicts) != 0 {
		t.Fatalf("claims from the same session conflicted: %+v", conflicts)
	}
	_, conflicts = createFrom(t, h, claimsURL(otherRepo), other, source, nil)
	if len(conflicts) != 0 {
		t.Fatalf("claims on different repositories conflicted: %+v", conflicts)
	}
	_, conflicts = createFrom(t, h, claimsURL(sameRepo), other, source, nil)
	var ids []string
	for _, claim := range conflicts {
		ids = append(ids, claim.ID)
	}
	wantIDs := []string{first.ID, second.ID}
	slices.Sort(ids)
	slices.Sort(wantIDs)
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("other Room on same repository returned the wrong conflicts: %+v", conflicts)
	}

	var storedRelease json.RawMessage
	fixture(t, "examples/room-event/release.json", &storedRelease)
	want := readEvent(t, storedRelease).Body().(*room.Release)
	body := encode(t, map[string]any{"session_id": source.Agent().ID, "outcome": want.Outcome})
	wantStatus(t, call(h, "POST", path+"/"+first.ID+"/release", token, body, ""), http.StatusOK)
	got := events(t, h, eventsURL(source.Mission()), token)
	released, ok := got[len(got)-1].Body().(*room.Release)
	if !ok || released.ClaimID != first.ID || released.Outcome != want.Outcome {
		t.Fatalf("release body = %+v", released)
	}
	wantStatus(t, call(h, "POST", eventsURL(source.Mission()), token, storedRelease, ""), http.StatusForbidden)
	wantStatus(t, call(h, "POST", path+"/"+first.ID+"/release", token, body, ""), http.StatusNotFound)
}

func claimFixtures(t *testing.T) (room.Event, time.Time, room.Event) {
	t.Helper()
	var claims struct {
		Cases []struct {
			Name  string
			Input struct {
				Events []json.RawMessage
				Now    string
			}
		}
	}
	fixture(t, "fixtures/active-claims.json", &claims)
	var initial, expired room.Event
	var expiry time.Time
	for _, c := range claims.Cases {
		switch c.Name {
		case "a claim is not active at the time that it expires":
			initial = readEvent(t, c.Input.Events[0])
			expiry = parseTime(t, c.Input.Now)
		case "a release with the outcome expired ends the claim":
			expired = readEvent(t, c.Input.Events[1])
		}
	}
	if initial.ID() == "" || expired.ID() == "" || expiry.IsZero() {
		t.Fatal("required active-claims fixture is missing")
	}
	return initial, expiry, expired
}

func roomStore(t *testing.T, id, member string) (*Store, string) {
	t.Helper()
	s := Open(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject(id, "api"); err != nil {
		t.Fatal(err)
	}
	token, err := s.AddMember(member, Member)
	if err != nil {
		t.Fatal(err)
	}
	return s, token
}

func createFrom(t *testing.T, h http.Handler, path, token string, source room.Event, extra map[string]any) (lease, []lease) {
	t.Helper()
	body := source.Body().(*room.Claim)
	agent := source.Agent()
	data := map[string]any{
		"agent": agentInput{ID: agent.ID, Vendor: agent.Vendor, Parent: agent.Parent},
		"item":  body.Item, "paths": body.Paths, "intent": body.Intent, "ttl_seconds": body.TTLSeconds,
	}
	for key, value := range extra {
		data[key] = value
	}
	res := call(h, "POST", path, token, encode(t, data), "")
	wantStatus(t, res, http.StatusOK)
	var result struct {
		Granted   lease
		Conflicts []lease
		Event     json.RawMessage
	}
	decode(t, res.Body.Bytes(), &result)
	ev := parseEventMessage(t, result.Event)
	if ev.Body().(*room.Claim).ClaimID != result.Granted.ID {
		t.Fatal("created claim and its event have different IDs")
	}
	return result.Granted, result.Conflicts
}

func eventsURL(roomID string) string { return "/api/v1/rooms/" + roomID + "/events" }
func claimsURL(roomID string) string { return "/api/v1/rooms/" + roomID + "/claims" }

func call(h http.Handler, method, url, token string, data []byte, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, bytes.NewReader(data))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func wantStatus(t *testing.T, res *httptest.ResponseRecorder, want int) {
	t.Helper()
	if res.Code != want {
		t.Fatalf("HTTP status %d, want %d: %s", res.Code, want, strings.TrimSpace(res.Body.String()))
	}
}

func events(t *testing.T, h http.Handler, path, token string) []room.Event {
	t.Helper()
	res := call(h, "GET", path, token, nil, "")
	wantStatus(t, res, http.StatusOK)
	var messages []json.RawMessage
	decode(t, res.Body.Bytes(), &messages)
	out := make([]room.Event, 0, len(messages))
	for _, msg := range messages {
		out = append(out, parseEventMessage(t, msg))
	}
	return out
}

func eventResponse(t *testing.T, res *httptest.ResponseRecorder) room.Event {
	t.Helper()
	wantStatus(t, res, http.StatusOK)
	return parseEventMessage(t, res.Body.Bytes())
}

func parseEventMessage(t *testing.T, text []byte) room.Event {
	t.Helper()
	message, err := room.ParseMessage(text)
	if err != nil {
		t.Fatal(err)
	}
	event, ok := message.Payload().(room.Event)
	seq, hasSeq := message.Seq()
	if !ok || !hasSeq || seq != event.Seq() || message.Room() != event.Mission() || message.Sender().ID != event.Agent().ID {
		t.Fatalf("invalid event envelope: %s", text)
	}
	return event
}

func assertSequences(t *testing.T, got []room.Event, want []int64) {
	t.Helper()
	var seqs []int64
	for _, ev := range got {
		seqs = append(seqs, ev.Seq())
	}
	if !slices.Equal(seqs, want) {
		t.Fatalf("sequences = %v, want %v", seqs, want)
	}
}

func fixture(t *testing.T, path string, out any) {
	t.Helper()
	text, err := os.ReadFile("../../spec/room/v1/" + path)
	if err != nil {
		t.Fatal(err)
	}
	decode(t, text, out)
}

func readEvent(t *testing.T, text []byte) room.Event {
	t.Helper()
	event, err := room.ParseEvent(text)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func parseTime(t *testing.T, text string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	text, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func decode(t *testing.T, text []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(text, out); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
}
