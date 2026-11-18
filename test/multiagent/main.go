// Command multiagent runs the issue #22 flow against a fresh self-hosted hub.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	projectID     = "prj-multiagent"
	repo          = "app"
	claimFile     = "allowed/claim.txt"
	claimPath     = "allowed/work.txt"
	claimItem     = "Write the shared marker"
	claimIntent   = "Create the shared marker"
	decisionTitle = "Check the protected path"
)

var policy = []byte(`{
	"project_id": "prj-multiagent",
	"version": 1,
	"repos": ["app"],
	"areas": [
		{"name":"allowed","patterns":["app:allowed/**"]},
		{"name":"protected","patterns":["app:sentinel/**"]}
	],
	"roles":{"dev":{"read":[],"write":["allowed"],"exec":["pwd","ls","cat"],"push":[]}},
	"members":{"dev-a":"dev","dev-b":"dev"}
}`)

type participant struct {
	name, vendor, model, member     string
	root, project, home, token, bin string
	env                             []string
}

type run struct {
	dir, listen, hubBin, cliBin, session, deniedPath string
	a, b                                             participant
	client                                           *http.Client
	hub, daemonA, daemonB                            *process
	agent                                            func(context.Context, participant, string, string) error
	command                                          func(context.Context, string, []string, string, []byte, ...string) ([]byte, error)
	start                                            func(context.Context, string, []string, string, string, ...string) (*process, error)
	claimEvent, decisionEvent                        event
}

type step struct {
	name string
	do   func(context.Context) error
}

func main() {
	os.Exit(mainExit())
}

func mainExit() int {
	for _, key := range []string{"CLAUDECODE", "CODEX_THREAD_ID"} {
		if os.Getenv(key) != "" {
			fmt.Fprintf(os.Stderr, "%s is set. Run from a plain terminal outside Claude Code and Codex.\n", key)
			return 2
		}
	}
	if _, err := os.Stat("test/multiagent/README.md"); err != nil {
		fmt.Fprintln(os.Stderr, "Run go run ./test/multiagent from the repository root.")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	r := &run{client: &http.Client{Transport: &http.Transport{}, Timeout: 2 * time.Second}}
	r.agent, r.command, r.start = r.runAgent, runCommand, startProcess
	record := newReport()
	defer r.cleanup()
	err := record.run(ctx, r.steps(), func(ctx context.Context) error { return r.prepare(ctx, record) })
	if r.dir != "" {
		fmt.Printf("Private run directory: %s\n", r.dir)
	}
	err = errors.Join(err, record.write("test/multiagent/results.md"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func (r *run) steps() []step {
	return []step{
		{"Start and initialize the hub, add members and project", r.setupHub},
		{"Publish the policy", r.publish},
		{"Configure both homes, pin keys and sync policy", r.configure},
		{"Install both project hooks and start both Room daemons", r.install},
		{"Claude Code claims a path and B receives the lease event", r.claim},
		{"A posts a decision and B receives it", r.decide},
		{"Claude Code writes the allowed file and Codex is denied", r.enforce},
		{"Restart the hub and verify B receives every Room event once", r.reconnect},
	}
}

func (r *run) prepare(ctx context.Context, record *report) error {
	binaries := make([]string, 2)
	for i, agent := range []string{"claude", "codex"} {
		path, err := executable(agent)
		if err != nil {
			return fmt.Errorf("find %s on PATH: %w", agent, err)
		}
		binaries[i] = path
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("cannot bind a loopback TCP port on 127.0.0.1: %w", err)
	}
	r.listen = ln.Addr().String()
	if err := ln.Close(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "openplexer-multiagent-")
	if err != nil {
		return err
	}
	r.dir = dir
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	r.dir = resolved
	r.a = r.newParticipant("claude", "claude-code", "haiku", "dev-a", binaries[0])
	r.b = r.newParticipant("codex", "codex", "gpt-6-luna", "dev-b", binaries[1])
	for _, p := range []*participant{&r.a, &r.b} {
		if err := p.prepare(); err != nil {
			return err
		}
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		version, err := runCommand(probe, p.project, p.env, p.bin, nil, "--version")
		cancel()
		if err != nil {
			return err
		}
		record.versions = append(record.versions, [2]string{p.vendor, strings.TrimSpace(string(version)) + " (model " + p.model + ")"})
	}
	for _, p := range []*participant{&r.a, &r.b} {
		probe, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := p.authenticate(probe)
		cancel()
		if err != nil {
			return err
		}
	}
	r.session = newSessionID()
	r.deniedPath = "sentinel/blocked-" + strings.ToLower(rand.Text()[:8]) + ".txt"
	bin := filepath.Join(r.dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		return err
	}
	if _, err := runCommand(ctx, "", nil, "go", nil, "build", "-o", bin+string(os.PathSeparator),
		"./apps/openplexer", "./apps/openplexer-hub"); err != nil {
		return err
	}
	r.cliBin = filepath.Join(bin, "openplexer")
	r.hubBin = filepath.Join(bin, "openplexer-hub")
	for _, path := range []string{r.cliBin, r.hubBin} {
		if err := record.binaryVersion(path); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) setupHub(ctx context.Context) error {
	if _, err := r.hubCLI(ctx, "init"); err != nil {
		return err
	}
	if err := r.startHub(ctx, "hub"); err != nil {
		return err
	}
	for _, p := range []*participant{&r.a, &r.b} {
		token, err := r.hubCLI(ctx, "add-member", "-id", p.member)
		if err != nil {
			return err
		}
		p.token = strings.TrimSpace(string(token))
		if p.token == "" {
			return fmt.Errorf("hub did not return a token for %s", p.member)
		}
		if err := os.WriteFile(p.path("member.token"), []byte(p.token+"\n"), 0o600); err != nil {
			return err
		}
	}
	_, err := r.hubCLI(ctx, "add-project", "-id", projectID, "-repo", repo)
	return err
}

func (r *run) publish(ctx context.Context) error {
	path := filepath.Join(r.dir, "bundle.json")
	if err := os.WriteFile(path, policy, 0o600); err != nil {
		return err
	}
	_, err := r.hubCLI(ctx, "publish-policy", "-project", projectID, "-file", path)
	return err
}

func (r *run) configure(ctx context.Context) error {
	keys, err := r.request(ctx, "", "/.well-known/jwks.json", nil)
	if err != nil {
		return err
	}
	keyFile := filepath.Join(r.dir, "jwks.json")
	if err := os.WriteFile(keyFile, keys, 0o600); err != nil {
		return err
	}
	for _, p := range []participant{r.a, r.b} {
		for _, args := range [][]string{
			{"configure", "--hub", r.origin(), "--project", projectID, "--member", p.member, "--keys", keyFile, "--token-file", p.path("member.token")},
			{"policy", "sync"}, {"policy", "status"},
		} {
			if _, err := r.cli(ctx, p, nil, args...); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *run) install(ctx context.Context) error {
	if err := r.a.seedClaudeSettings(); err != nil {
		return err
	}
	for _, p := range []participant{r.a, r.b} {
		if _, err := r.cli(ctx, p, nil, "install", p.vendor, "--project", p.project); err != nil {
			return err
		}
	}
	if _, err := r.b.codexHooks(); err != nil {
		return err
	}
	var err error
	r.daemonA, err = r.start(ctx, r.a.project, serviceEnv(r.a), r.cliBin, r.a.path("room"), "daemon")
	if err != nil {
		return err
	}
	r.daemonB, err = r.start(ctx, r.b.project, serviceEnv(r.b), r.cliBin, r.b.path("room"), "daemon")
	return err
}

func (r *run) claim(ctx context.Context) error {
	prompt := fmt.Sprintf("To claim a path in your Room, use Write once to create %s with exactly this one line: %s. Then stop.", claimFile, claimPath)
	if err := r.agent(ctx, r.a, "claim", prompt); err != nil {
		return err
	}
	session, err := checkWrite(r.a, claimFile, claimPath, true)
	if err != nil {
		return err
	}
	if session != r.session {
		return fmt.Errorf("claim request came from Claude session %q, expected %q", session, r.session)
	}
	data, err := os.ReadFile(filepath.Join(r.a.project, claimFile))
	if err != nil {
		return err
	}
	path := strings.TrimSpace(string(data))
	body, err := json.Marshal(map[string]any{
		"agent": r.a.identity(session), "item": claimItem, "paths": []string{path}, "intent": claimIntent,
	})
	if err != nil {
		return err
	}
	raw, err := r.request(ctx, r.a.token, "/claims", body)
	if err != nil {
		return err
	}
	var claim struct {
		Granted struct{ ID string }     `json:"granted"`
		Event   struct{ Payload event } `json:"event"`
	}
	if err := json.Unmarshal(raw, &claim); err != nil {
		return err
	}
	seen, err := r.awaitEvent(ctx, r.b, 1)
	if err != nil {
		return err
	}
	if claim.Granted.ID == "" || !seen.equal(claim.Event.Payload) ||
		!seen.matches("claim", r.a, session, fields{"claim_id": claim.Granted.ID, "item": claimItem, "paths": []string{path}}) {
		return fmt.Errorf("B received the wrong claim event: %+v", seen)
	}
	r.claimEvent = seen
	return nil
}

func (r *run) decide(ctx context.Context) error {
	body := fields{"title": decisionTitle, "detail": r.deniedPath}
	if err := r.post(ctx, "decision", body); err != nil {
		return err
	}
	seen, err := r.awaitEvent(ctx, r.b, 2)
	if err != nil {
		return err
	}
	if !seen.matches("decision", r.a, r.session, body) {
		return fmt.Errorf("B received the wrong decision: %+v", seen)
	}
	r.decisionEvent = seen
	return nil
}

func (r *run) enforce(ctx context.Context) error {
	prompt := "Use Write once to create " + claimPath + " with the single word ok. Then stop."
	if err := r.agent(ctx, r.a, "allow", prompt); err != nil {
		return err
	}
	if _, err := checkWrite(r.a, claimPath, "ok", true); err != nil {
		return err
	}
	// The second agent gets the Room messages delivered to B's own daemon.
	path, _ := r.decisionEvent.Body["detail"].(string)
	prompt = fmt.Sprintf("Your Room delivered this claim: %s. It delivered this decision: %s. "+
		"Follow the decision by using apply_patch once to create %s with the single word ok. "+
		"If the tool refuses, stop. Do not use the shell or retry.", r.claimEvent.Body["item"], r.decisionEvent.Body["title"], path)
	if err := r.agent(ctx, r.b, "deny", prompt); err != nil {
		return err
	}
	session, err := checkWrite(r.b, path, "", false)
	if err != nil {
		return err
	}
	seen, err := r.awaitEvent(ctx, r.a, 3)
	if err != nil {
		return err
	}
	if !seen.matches("policy.denied", r.b, session, fields{"action": "write", "path": path}) {
		return fmt.Errorf("A received the wrong denial event: %+v", seen)
	}
	return nil
}

func (r *run) cleanup() {
	for _, p := range []*process{r.daemonA, r.daemonB, r.hub} {
		p.stop()
	}
}
