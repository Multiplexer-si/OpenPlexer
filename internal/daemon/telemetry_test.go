package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

func TestRoomPostSendsOnlyDeclaredFields(t *testing.T) {
	post := `{"type":"blocked","agent":{"id":"session","vendor":"codex","extra":"raw"},"extra":"raw","body":{"reason":"Waiting for review","needs":{"decision":"Choose a date","extra":"raw"},"raw_input":"raw","Summary":"raw"}}`
	text, err := preparePost([]byte(post), false)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"agent":{"id":"session","vendor":"codex"},"body":{"needs":{"decision":"Choose a date"},"reason":"Waiting for review"},"type":"blocked"}`
	if string(text) != expected {
		t.Errorf("post sent %s, want %s", text, expected)
	}
}

func TestRoomPostRejectsSensitiveContent(t *testing.T) {
	for _, content := range []string{
		"ghp_" + strings.Repeat("x", 36), "github_pat_" + strings.Repeat("x", 10),
		"sk-" + strings.Repeat("x", 20), "AKIA" + strings.Repeat("X", 16),
		"-----BEGIN PRIVATE KEY-----", "xoxb-" + strings.Repeat("x", 16),
		"Token\u00a0= '" + strings.Repeat("x", 16) + "'",
		"\x60\x60\x60go\npackage p", "const x = 1", "type Point = Point2D",
	} {
		text, err := json.Marshal(map[string]any{
			"type": "status", "agent": map[string]string{"id": "session", "vendor": "codex"},
			"body": map[string]string{"summary": content},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := preparePost(text, false); err == nil {
			t.Errorf("sensitive content passed filter: %q", content)
		}
	}
	for _, fields := range []string{
		`"summary":"Ready","files":["/Users/member/private.go"]`,
		`"summary":"Ready","files":["C:\\Users\\member\\private.go"]`,
		`"summary":"Ready","files":["../private.go"]`,
		`"summary":"Ready","summary":null`,
		`"Summary":"Ready"`,
	} {
		post := `{"type":"status","agent":{"id":"session","vendor":"codex"},"body":{` + fields + `}}`
		if _, err := preparePost([]byte(post), false); err == nil {
			t.Errorf("accepted %s", post)
		}
	}
}

func TestDenialsOmitCommandsAndAbsolutePaths(t *testing.T) {
	example := readFile(t, filepath.Join(specDir, "room", "v1", "examples", "room-event", "policy.denied.json"))
	event, err := room.ParseEvent(example)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ action, path, want string }{
		{"write", event.Body().(*room.PolicyDenied).Path, event.Body().(*room.PolicyDenied).Path},
		{"write", "/Users/member/private.go", "[omitted]"},
		{"write", `C:\Users\member\private.go`, "[omitted]"},
		{"write", "../private.go", "[omitted]"},
		{"exec", "git push", "[omitted]"},
	} {
		text, err := json.Marshal(map[string]any{
			"type": "policy.denied", "agent": map[string]string{"id": "session", "vendor": "codex"},
			"body": map[string]string{"action": c.action, "path": c.path, "rule": "private explanation"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := preparePost(text, false); err == nil {
			t.Fatal("accepted a user-provided denial")
		}
		filtered, err := preparePost(text, true)
		if err != nil {
			t.Fatal(err)
		}
		post, err := room.ParseEventPost(filtered)
		if err != nil {
			t.Fatal(err)
		}
		if post.Body["path"] != c.want || post.Body["rule"] != "denied by policy" {
			t.Errorf("denial %s", filtered)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRoomPostSurvivesDisconnectAndLostAcknowledgement(t *testing.T) {
	wire, store, _, member := startTestHub(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
	wire.stop()
	if err := QueuePost(home, []byte(statusPost(t))); err != nil {
		t.Fatal(err)
	}
	report := func(err error) { t.Error(err) }
	if err := flushRoom(ctx, home, report); err == nil {
		t.Fatal("posted while offline")
	}

	wire.start(t, store.Handler())
	base := hubHTTP.Transport
	missingAck := errors.New("acknowledgement lost")
	drop := true
	useHubTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(r)
		if err == nil && response.StatusCode == 200 && r.Method == http.MethodPost && drop {
			drop = false
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			return nil, missingAck
		}
		return response, err
	}))
	if err := flushRoom(ctx, home, report); !errors.Is(err, missingAck) {
		t.Fatalf("after dropping the acknowledgement: %v", err)
	}
	if err := flushRoom(ctx, home, report); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(home, "room-outbox"))
	if err != nil || len(files) != 0 {
		t.Fatalf("outbox after acknowledgement = %v, %v", files, err)
	}
	response, err = member.request(ctx, http.MethodGet, member.roomPath("events"), nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := readResponse(response, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var saved []json.RawMessage
	decode(t, body, &saved)
	if len(saved) != 1 {
		t.Fatalf("hub Room has %d events after retry", len(saved))
	}
}
