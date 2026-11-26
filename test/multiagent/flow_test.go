package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestFlow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     int
		external bool
	}{
		{name: "pass"},
		{name: "external hub", external: true},
		{name: "claim without hook audit", fail: 5},
		{name: "denied file on disk", fail: 7},
		{name: "duplicate delivery after reconnect", fail: 8},
		{name: "missing delivery after reconnect", fail: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &run{dir: t.TempDir(), listen: "127.0.0.1:1", hubBin: "hub", cliBin: "cli",
				session: "claude-session", deniedPath: "sentinel/decision-target.txt"}
			if tc.external {
				r.listen = ""
				if err := r.parseFlags([]string{"-hub-url", "https://worker.example/", "-lead-token-file", filepath.Join(r.dir, "lead.token")}); err != nil {
					t.Fatal(err)
				}
			}
			r.a = r.newParticipant("claude", "claude-code", "haiku", "dev-a", "")
			r.b = r.newParticipant("codex", "codex", "gpt-6-luna", "dev-b", "")
			for _, p := range []*participant{&r.a, &r.b} {
				if err := p.prepare(); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p.home, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			f := &fakeFlow{t: t, run: r, fault: tc.name, up: tc.external}
			r.agent, r.command, r.start = f.agent, f.command, f.start
			r.client = &http.Client{Transport: f}
			defer r.cleanup()

			report := newReport()
			err := report.run(t.Context(), r.steps(), func(context.Context) error { return nil })
			if tc.fail == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(f.agents, []string{"claude/claim", "claude/allow", "codex/deny"}) {
					t.Fatalf("agent calls %v", f.agents)
				}
				if tc.external {
					if f.hubStarts != 0 || !f.reconnected || !slices.Equal(f.admin, []string{"add-member", "add-member", "add-project", "publish-policy"}) {
						t.Fatalf("external run started the hub %d times, restarted B: %t, admin calls: %v", f.hubStarts, f.reconnected, f.admin)
					}
				} else if f.hubStarts != 2 || f.reconnected {
					t.Fatalf("local run started the hub %d times, restarted B: %t", f.hubStarts, f.reconnected)
				}
			} else if err == nil || !strings.HasPrefix(err.Error(), fmt.Sprintf("step %d: ", tc.fail)) {
				t.Fatalf("expected failure at step %d, got %v", tc.fail, err)
			}
			for i, result := range report.steps {
				status := "PASS"
				if tc.fail > 0 && i+1 >= tc.fail {
					status = "SKIP"
					if i+1 == tc.fail {
						status = "FAIL"
					}
				}
				if result.status != status {
					t.Fatalf("step %d = %s, want %s", i+1, result.status, status)
				}
			}
			path := filepath.Join(r.dir, "results.md")
			if err := report.write(path); err != nil {
				t.Fatal(err)
			}
			text, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(text, []byte("| 9. Write this report | PASS |")) {
				t.Fatalf("incomplete report:\n%s", text)
			}
		})
	}
}

type fakeFlow struct {
	t           *testing.T
	run         *run
	fault       string
	hubStarts   int
	up          bool
	agents      []string
	admin       []string
	queued      []byte
	history     []event
	paused      bool
	reconnected bool
}

func (f *fakeFlow) start(_ context.Context, _ string, _ []string, bin, log string, _ ...string) (*process, error) {
	p := &process{done: make(chan struct{})}
	p.signal = func(signal os.Signal) error {
		if signal == syscall.SIGSTOP {
			f.paused = true
			return nil
		}
		f.paused = false
		ev := f.history[len(f.history)-1]
		b := f.run.b
		switch f.fault {
		case "missing delivery after reconnect":
			ev.Seq++
		case "duplicate delivery after reconnect":
			appendFake(f.t, b.path("room.jsonl"), ev)
		}
		f.deliver(b, ev)
		return nil
	}
	p.cancel = func() {
		if p.running() != nil {
			return
		}
		close(p.done)
		if bin == f.run.hubBin {
			f.up = false
			if f.hubStarts == 1 {
				writeFake(f.t, f.run.b.path("room.err"), []byte("Room stream: disconnected\n"))
			}
		}
		if f.run.hubURL != "" && log == f.run.b.path("room") {
			f.paused = true
		}
	}
	if bin == f.run.hubBin {
		f.hubStarts++
		f.up = true
		if f.hubStarts == 2 {
			f.deliverPost(f.queued)
		}
	} else {
		writeFake(f.t, log+".jsonl", nil)
		writeFake(f.t, log+".err", nil)
		if log == f.run.b.path("room-reconnected") {
			f.reconnected = true
			f.paused = false
			f.deliver(f.run.b, f.history[len(f.history)-1])
		}
	}
	return p, nil
}

func (f *fakeFlow) command(_ context.Context, dir string, _ []string, bin string, input []byte, args ...string) ([]byte, error) {
	if bin == f.run.hubBin && f.run.hubURL != "" {
		want := []string{"--hub", "https://worker.example", "--token-file", f.run.leadTokenFile}
		if len(args) < 5 || !slices.Equal(args[1:5], want) {
			return nil, fmt.Errorf("unexpected external admin arguments: %v", args)
		}
		f.admin = append(f.admin, args[0])
	}
	switch args[0] {
	case "add-member":
		return []byte("token-" + args[len(args)-1]), nil
	case "init", "add-project", "publish-policy", "configure", "policy":
		return nil, nil
	case "install":
		if bin == f.run.cliBin && dir == f.run.b.project {
			path := filepath.Join(dir, ".codex", "hooks.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return nil, err
			}
			writeFake(f.t, path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash|apply_patch","hooks":[{"type":"command","command":"/bin/openplexer hook codex pre-tool","timeout":5}]}]}}`))
		}
		return nil, nil
	case "room":
		if !slices.Equal(args, []string{"room", "post"}) || dir != f.run.a.project {
			return nil, fmt.Errorf("unexpected CLI call %v in %s", args, dir)
		}
		if f.up {
			f.deliverPost(input)
		} else {
			f.queued = input
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected CLI call %v", args)
	}
}

func (f *fakeFlow) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.run.hubURL != "" && req.URL.Scheme+"://"+req.URL.Host != "https://worker.example" {
		return nil, fmt.Errorf("unexpected external request origin: %s", req.URL.Host)
	}
	path := req.URL.Path
	var response any
	switch path {
	case "/.well-known/jwks.json":
		response = fields{"keys": []string{"test"}}
	case "/api/v1/rooms/" + projectID + "/claims":
		if req.Method != http.MethodPost || req.Header.Get("Authorization") != "Bearer "+f.run.a.token {
			return nil, fmt.Errorf("claim did not use A's membership")
		}
		var claim struct {
			Agent struct{ ID, Vendor string }
			Item  string
			Paths []string
		}
		if err := json.NewDecoder(req.Body).Decode(&claim); err != nil {
			return nil, err
		}
		if !slices.Equal(claim.Paths, []string{claimPath}) || claim.Agent.ID != f.run.session {
			return nil, fmt.Errorf("unexpected claim %+v", claim)
		}
		ev := f.emit("claim", f.run.a, claim.Agent.ID, fields{"claim_id": "lease-1", "paths": claim.Paths, "item": claim.Item})
		response = fields{"granted": fields{"id": "lease-1"}, "event": fields{"payload": ev}}
	case "/api/v1/rooms/" + projectID + "/events":
		if req.URL.Query().Get("last_seq") != "0" || req.Header.Get("Authorization") != "Bearer "+f.run.b.token {
			return nil, fmt.Errorf("history did not use B's membership and a zero cursor")
		}
		envelopes := make([]fields, 0, len(f.history))
		for _, ev := range f.history {
			envelopes = append(envelopes, fields{"payload": ev})
		}
		response = envelopes
	default:
		return nil, fmt.Errorf("unexpected HTTP request %s", path)
	}
	text := jsonFake(f.t, response)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(text))}, nil
}

func (f *fakeFlow) deliverPost(input []byte) {
	var post event
	if err := json.Unmarshal(input, &post); err != nil {
		f.t.Fatal(err)
	}
	f.emit(post.Type, f.run.a, post.Agent.ID, post.Body)
}

func (f *fakeFlow) emit(kind string, p participant, session string, body fields) event {
	seq := len(f.history) + 1
	ev := event{Seq: seq, ID: fmt.Sprintf("event-%d", seq), Mission: projectID, Type: kind, Member: p.member, Body: body}
	ev.Agent.ID, ev.Agent.Vendor = p.member+"/"+session, p.vendor
	f.history = append(f.history, ev)
	f.deliver(f.run.a, ev)
	if !f.paused {
		f.deliver(f.run.b, ev)
	}
	return ev
}

func (f *fakeFlow) deliver(p participant, ev event) {
	log := "room.jsonl"
	if p.member == f.run.b.member && f.reconnected {
		log = "room-reconnected.jsonl"
	}
	appendFake(f.t, p.path(log), ev)
	writeFake(f.t, filepath.Join(p.home, "room-cursor.json"), jsonFake(f.t, fields{"last_seq": ev.Seq}))
}

func (f *fakeFlow) agent(_ context.Context, p participant, name, prompt string) error {
	f.agents = append(f.agents, p.name+"/"+name)
	session, action := f.run.session, "policy.allowed"
	var target, content string
	switch name {
	case "claim":
		target, content = claimFile, claimPath
	case "allow":
		target, content = claimPath, "ok"
	case "deny":
		target, action, session = f.run.deniedPath, "policy.denied", "codex-session"
		if !strings.Contains(prompt, decisionTitle) || !strings.Contains(prompt, claimItem) {
			return fmt.Errorf("B did not receive A's claim and decision in the prompt")
		}
	default:
		return fmt.Errorf("unknown prompt %s", name)
	}
	if !strings.Contains(prompt, target) {
		return fmt.Errorf("prompt %s did not name %s", name, target)
	}
	if content != "" || name == "deny" && f.fault == "denied file on disk" {
		writeFake(f.t, filepath.Join(p.project, target), []byte(content+"\n"))
	}
	if f.fault == "claim without hook audit" {
		return nil
	}
	appendFake(f.t, filepath.Join(p.home, "audit.jsonl"), fields{
		"action": action, "target": target, "agent_session_id": session, "actor_member_id": p.member, "workspace_id": projectID,
		"meta": fields{"agent": p.vendor, "action": "write", "policy_state": "verified", "policy_version": 1},
	})
	if name == "deny" {
		f.emit(action, p, session, fields{"path": target, "action": "write"})
	}
	return nil
}

func writeFake(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFake(t *testing.T, path string, obj any) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(obj); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func jsonFake(t *testing.T, obj any) []byte {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
