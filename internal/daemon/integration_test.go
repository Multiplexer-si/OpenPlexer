package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/room"
)

// The HTTP server and client use real HTTP framing over net.Pipe, which also
// runs in a sandbox that disallows binding sockets.
type pipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

type pipeAddr struct{}

func (pipeAddr) String() string  { return "http-pipe" }
func (pipeAddr) Network() string { return "pipe" }

func (*pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeHub struct {
	mu       sync.Mutex
	listener *pipeListener
	server   *http.Server
}

func (h *pipeHub) start(t *testing.T, handler http.Handler) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listener = &pipeListener{connections: make(chan net.Conn), done: make(chan struct{})}
	h.server = &http.Server{Handler: handler, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	server, listener := h.server, h.listener
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
}

func (h *pipeHub) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.server != nil {
		h.server.Close()
		h.listener.Close()
		h.server = nil
	}
}

func (h *pipeHub) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	h.mu.Lock()
	listener := h.listener
	h.mu.Unlock()
	client, server := net.Pipe()
	select {
	case listener.connections <- server:
		return client, nil
	case <-listener.done:
	case <-ctx.Done():
	}
	client.Close()
	server.Close()
	return nil, fmt.Errorf("hub unreachable: %w", net.ErrClosed)
}

const testHubOrigin = "http://127.0.0.1"

func TestAuditRoomBoundary(t *testing.T) {
	const subscribed = "mis-checkout"
	const other = "fixture-project-b"
	useHubTransport(t, handlerTransport(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", roomStreamType)
		event := func(room, mission string, seq int, body string) {
			fmt.Fprintf(w, `{"v":1,"type":"event","sender":{"id":"hub","kind":"server"},"room":%q,"seq":%d,"payload":{"id":"e%d","seq":%d,"mission":%q,"type":"status","agent":{"id":"ses","vendor":"codex"},"on_behalf_of":"mem-ana","at":"t","body":%s}}`+"\n", room, seq, seq, seq, mission, body)
		}
		event(subscribed, other, 20, `{"summary":"other mission"}`)
		event(other, other, 21, `{"summary":"other Room"}`)
		event(subscribed, other, 22, `{}`)
		fmt.Fprintln(w, `{"v":1,"type":"error","sender":{"id":"hub","kind":"server"},"room":"fixture-project-b","payload":{"code":"unauthorized","message":"other Room"}}`)
		event(subscribed, subscribed, 2, `{"summary":"subscribed Room"}`)
		event(subscribed, subscribed, 3, `{}`)
	}))
	receiver := room.NewReceiver()
	receiver.Restore(subscribed, 1)
	client := hubClient{origin: testHubOrigin, project: subscribed, member: "mem-ana"}
	var delivered []string
	var saved []int64
	err := client.followRoom(context.Background(), time.Now, receiver, func(step room.Step, seq int64) error {
		if event, ok := step.(room.Deliver); ok {
			delivered = append(delivered, event.Event.Mission())
		}
		saved = append(saved, seq)
		return nil
	})
	if !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "discarded 4 Room frames outside the subscription") {
		t.Errorf("stream result = %v, want EOF and four discarded frames", err)
	}
	if !slices.Equal(delivered, []string{subscribed}) || !slices.Equal(saved, []int64{2, 3}) {
		t.Errorf("delivered %v and saved %v, want just the subscribed event and its unreadable successor counted", delivered, saved)
	}
	if got := receiver.LastSeq(subscribed); got != 3 {
		t.Errorf("subscribed cursor = %d, want 3", got)
	}
	if got := receiver.LastSeq(other); got != 0 {
		t.Errorf("foreign cursor = %d, want 0", got)
	}
}

func startTestHub(t *testing.T) (*pipeHub, *hub.Store, hubClient, hubClient) {
	t.Helper()
	store := hub.Open(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	lead, err := store.AddMember("mem-lee", hub.Lead)
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.AddMember("mem-ana", hub.Member)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddProject("prj-shop", "api"); err != nil {
		t.Fatal(err)
	}
	var wire pipeHub
	wire.start(t, store.Handler())
	t.Cleanup(wire.stop)
	transport := &http.Transport{DialContext: wire.dial}
	t.Cleanup(transport.CloseIdleConnections)
	useHubTransport(t, transport)
	return &wire, store, hubClient{origin: testHubOrigin, project: "prj-shop", member: "mem-lee", token: lead},
		hubClient{origin: testHubOrigin, project: "prj-shop", member: "mem-ana", token: member}
}

func publishTestBundle(t *testing.T, ctx context.Context, client hubClient, version int, allow bool) {
	t.Helper()
	fixture := readFixture(t, "evaluate-defaults-and-roles.json")
	var bundle map[string]any
	decode(t, fixture.Shared.Bundle, &bundle)
	bundle["project_id"] = client.project
	bundle["repos"] = []string{"api"}
	bundle["version"] = version
	bundle["members"] = map[string]string{"mem-ana": "backend"}
	if !allow {
		bundle["roles"].(map[string]any)["backend"].(map[string]any)["write"] = []string{}
	}
	text, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.request(ctx, http.MethodPut, "/api/v1/projects/"+client.project+"/policy", bytes.NewReader(text), "application/json", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readResponse(response, 4<<20); err != nil {
		t.Fatal(err)
	}
}

func statusPost(t *testing.T) string {
	t.Helper()
	text := readFile(t, filepath.Join(specDir, "room", "v1", "examples", "room-event", "status.json"))
	event, err := room.ParseEvent(text)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"type":"status","agent":{"id":"session","vendor":"codex"},"body":{"summary":%q}}`, event.Body().(*room.Status).Summary)
}

func postTestStatus(t *testing.T, ctx context.Context, client hubClient, key string) room.Event {
	t.Helper()
	post := statusPost(t)
	response, err := client.request(ctx, http.MethodPost, client.roomPath("events"), bytes.NewBufferString(post), "application/json", key)
	if err != nil {
		t.Fatal(err)
	}
	text, err := readResponse(response, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	message, err := room.ParseMessage(text)
	if err != nil {
		t.Fatal(err)
	}
	return message.Payload().(room.Event)
}

func waitForPolicy(t *testing.T, ctx context.Context, home string, version int64) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		gate, err := Open(home, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		if status := gate.Status(); status.Version == version && status.State == Verified {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("policy version %d never became verified: %v", version, gate.Status())
		}
	}
}

func receiveSeq(t *testing.T, ctx context.Context, events <-chan room.Event, want int64) {
	t.Helper()
	select {
	case event := <-events:
		if event.Seq() != want {
			t.Fatalf("received seq %d, want %d", event.Seq(), want)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for seq %d", want)
	}
}

func TestHubAgentPolicyAndRoomReconnect(t *testing.T) {
	wire, store, lead, member := startTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	home := t.TempDir()
	response, err := member.request(ctx, http.MethodGet, "/.well-known/jwks.json", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := readResponse(response, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	configure(t, home, Settings{HubURL: member.origin, ProjectID: member.project, MemberID: member.member, Token: member.token, KeySet: keys})
	publishTestBundle(t, ctx, lead, 1, true)
	bad := member
	bad.token = "invalid"
	if _, err := bad.request(ctx, http.MethodGet, "/api/v1/projects/prj-shop/policy.bundle", nil, "", ""); !errors.Is(err, hubStatus(401)) {
		t.Fatalf("policy authorization: %v", err)
	}
	if _, err := bad.request(ctx, http.MethodPost, bad.roomPath("stream"), nil, "", ""); !errors.Is(err, hubStatus(401)) {
		t.Fatalf("stream authorization: %v", err)
	}
	if _, err := bad.request(ctx, http.MethodPost, bad.roomPath("events"), bytes.NewBufferString(statusPost(t)), "application/json", ""); !errors.Is(err, hubStatus(401)) {
		t.Fatalf("post authorization: %v", err)
	}
	if _, err := member.request(ctx, http.MethodPut, "/api/v1/projects/prj-shop/policy", bytes.NewBufferString("{}"), "application/json", ""); !errors.Is(err, hubStatus(403)) {
		t.Fatalf("publish authorization: %v", err)
	}

	run := func() (<-chan room.Event, <-chan error, context.CancelFunc) {
		events := make(chan room.Event, 16)
		done := make(chan error, 1)
		runCtx, stop := context.WithCancel(ctx)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			done <- Run(runCtx, home, time.Now, func(event room.Event) error {
				select {
				case events <- event:
					return nil
				case <-runCtx.Done():
					return runCtx.Err()
				}
			}, func(err error) { t.Log(err) })
		}()
		t.Cleanup(func() {
			stop()
			<-finished
		})
		return events, done, stop
	}
	stopRun := func(done <-chan error, stop context.CancelFunc) {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("daemon did not stop")
		}
	}
	events, done, stop := run()
	stopOnExit := true
	defer func() {
		if stopOnExit {
			stopRun(done, stop)
		}
	}()
	check := func(version int64, state State, want policy.Decision, now time.Time) {
		t.Helper()
		gate := open(t, home, now)
		query := policy.Query{
			Action: policy.Write, Path: "src/orders/cart.ts", Repo: "caller-supplied",
			Overrides: []policy.Override{{ID: "unsigned", Area: "backend", Actions: []policy.Action{policy.Write}, Scope: policy.OverrideOnce}},
		}
		verdict, err := gate.Decide("codex", query)
		result, decided := verdict.Result()
		if err != nil || !decided || result.Decision != want || verdict.Policy.Version != version || verdict.Policy.State != state || verdict.Query.Repo != "api" {
			t.Fatalf("v%d %s = %+v, %v, %v", version, state, verdict, result, err)
		}
	}
	waitForPolicy(t, ctx, home, 1)
	check(1, Verified, policy.Allow, time.Now())
	if got := postTestStatus(t, ctx, member, "one"); got.OnBehalfOf() != member.member {
		t.Fatalf("hub did not assign authenticated member: %s", got.OnBehalfOf())
	}
	receiveSeq(t, ctx, events, 1)

	publishTestBundle(t, ctx, lead, 2, false)
	waitForPolicy(t, ctx, home, 2)
	check(2, Verified, policy.Deny, time.Now())
	wire.stop()
	if _, err := open(t, home, time.Now()).Sync(ctx); err == nil {
		t.Fatal("sync succeeded while hub was down")
	}
	check(2, Verified, policy.Deny, time.Now())
	check(2, Stale, policy.Deny, time.Now().Add(15*24*time.Hour))

	// Add events to the durable Room while the agent's connection is down.
	wire.start(t, store.Handler())
	for _, key := range []string{"two", "three", "three"} {
		postTestStatus(t, ctx, lead, key)
	}
	receiveSeq(t, ctx, events, 2)
	receiveSeq(t, ctx, events, 3)
	stopOnExit = false
	stopRun(done, stop)
	// The persisted cursor is also used by a new daemon process.
	events, done, stop = run()
	stopOnExit = true
	postTestStatus(t, ctx, lead, "four")
	receiveSeq(t, ctx, events, 4)
	stopOnExit = false
	stopRun(done, stop)
}

func TestRoomResumeReplacesSubscription(t *testing.T) {
	_, _, _, member := startTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	postTestStatus(t, ctx, member, "one")
	postTestStatus(t, ctx, member, "two")
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	const initialSeq = 1
	sender := room.Sender{ID: member.member, Kind: room.Daemon}
	encoder := json.NewEncoder(writer)
	go func() { _ = encoder.Encode(room.ResumeMessage(sender, member.project, initialSeq)) }()
	response, err := member.request(ctx, http.MethodPost, member.roomPath("stream"), reader, roomStreamType, "")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	readSeq := func() int64 {
		t.Helper()
		for scanner.Scan() {
			if len(scanner.Bytes()) == 0 {
				continue
			}
			message, err := room.ParseMessage(scanner.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			seq, _ := message.Seq()
			return seq
		}
		t.Fatalf("stream ended: %v", scanner.Err())
		return 0
	}
	got := []int64{readSeq()}
	if err := encoder.Encode(room.ResumeMessage(sender, member.project, 0)); err != nil {
		t.Fatal(err)
	}
	got = append(got, readSeq(), readSeq())
	postTestStatus(t, ctx, member, "three")
	got = append(got, readSeq())
	if !slices.Equal(got, []int64{2, 1, 2, 3}) {
		t.Errorf("replacement delivered %v", got)
	}
}

func TestHubRoomCursorIsOnDisk(t *testing.T) {
	home := t.TempDir()
	if err := writeJSON(home, roomCursorFile, roomCursor{LastSeq: 41}); err != nil {
		t.Fatal(err)
	}
	test := readFile(t, filepath.Join(specDir, "room", "v1", "examples", "resume", "after-41.json"))
	var fixture, stored map[string]int64
	decode(t, test, &fixture)
	decode(t, readFile(t, filepath.Join(home, roomCursorFile)), &stored)
	if stored["last_seq"] != fixture["last_seq"] {
		t.Fatalf("stored cursor %v, want %v", stored, fixture)
	}
}

func TestRoomReconnectsFromSavedCursor(t *testing.T) {
	const project = "prj-shop"
	token := strings.Repeat("A", 43)
	home := t.TempDir()
	configure(t, home, Settings{
		HubURL: testHubOrigin, ProjectID: project, MemberID: "mem-ana", Token: token, KeySet: newSigner(t).keySet(t),
	})
	var resume map[string]int64
	decode(t, readFile(t, filepath.Join(specDir, "room", "v1", "examples", "resume", "after-41.json")), &resume)
	if err := writeJSON(home, roomCursorFile, roomCursor{LastSeq: resume["last_seq"]}); err != nil {
		t.Fatal(err)
	}
	status := func(seq int, body string) string {
		return fmt.Sprintf(`{"v":1,"type":"event","sender":{"id":"mem-ana/session","kind":"daemon"},"room":%q,"seq":%d,"payload":{"id":"e%d","seq":%d,"mission":%q,"type":"status","agent":{"id":"mem-ana/session","vendor":"codex"},"on_behalf_of":"mem-ana","at":"t","body":%s}}`+"\n",
			project, seq, seq, seq, project, body)
	}
	event := func(seq int) string { return status(seq, `{"summary":"working"}`) }
	var behind bytes.Buffer
	if err := json.Compact(&behind, readFile(t, filepath.Join(specDir, "room", "v1", "examples", "error", "behind.json"))); err != nil {
		t.Fatal(err)
	}
	duplicateField := strings.Replace(event(100), `"seq":100`, `"seq":100,"seq":100`, 1)
	replies := []struct {
		cursor int64
		frames string
		end    error
	}{
		{41, event(42) + "\n" + status(43, "{}") + duplicateField, io.EOF},
		{43, event(42) + event(43) + event(44), os.ErrDeadlineExceeded},
		{44, fmt.Sprintf(`{"v":1,"type":"error","sender":{"id":"hub","kind":"server"},"room":%q,"payload":%s}`+"\n",
			project, behind.Bytes()) + status(45, `{"summary":"after behind on old response"}`), io.EOF},
		{44, event(44) + event(45), io.EOF},
	}
	var requests int
	useHubTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/rooms/"+project+"/stream" {
			return &http.Response{StatusCode: 404, Body: http.NoBody}, nil
		}
		if requests >= len(replies) {
			t.Fatalf("unexpected extra stream with cursor %s", r.URL.RawQuery)
		}
		reply := replies[requests]
		requests++
		var saved roomCursor
		decode(t, readFile(t, filepath.Join(home, roomCursorFile)), &saved)
		if r.Method != http.MethodGet || r.Body != nil || r.Header.Get("Authorization") != "Bearer "+token ||
			r.URL.RawQuery != fmt.Sprintf("last_seq=%d", reply.cursor) || saved.LastSeq != reply.cursor {
			t.Fatalf("request %d has method %s, cursor %s and saved cursor %d, want GET with saved cursor %d",
				requests, r.Method, r.URL.RawQuery, saved.LastSeq, reply.cursor)
		}
		stream := io.MultiReader(strings.NewReader(reply.frames), streamEnd{reply.end})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {roomStreamType}}, Body: io.NopCloser(stream)}, nil
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var delivered []int64
	var ended []error
	err := Run(ctx, home, time.Now, func(event room.Event) error {
		delivered = append(delivered, event.Seq())
		if event.Seq() == 45 {
			if event.Body().(*room.Status).Summary != "working" {
				return errors.New("delivered a frame after behind on the old response")
			}
			cancel()
		}
		return nil
	}, func(err error) {
		if strings.HasPrefix(err.Error(), "Room stream:") {
			ended = append(ended, err)
		}
	})
	if err != nil || requests != len(replies) || !slices.Equal(delivered, []int64{42, 44, 45}) {
		t.Fatalf("Run = %v, opened %d streams, delivered %v", err, requests, delivered)
	}
	if len(ended) != 3 || !errors.Is(ended[0], io.EOF) || !errors.Is(ended[1], os.ErrDeadlineExceeded) || !errors.Is(ended[2], errRoomBehind) {
		t.Fatalf("stream exits = %v", ended)
	}
	var saved roomCursor
	decode(t, readFile(t, filepath.Join(home, roomCursorFile)), &saved)
	if saved.LastSeq != 45 {
		t.Fatalf("final cursor = %d, want 45", saved.LastSeq)
	}
}

type streamEnd struct{ error }

func (e streamEnd) Read([]byte) (int, error) { return 0, e.error }
