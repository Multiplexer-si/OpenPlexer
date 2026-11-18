package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

type auditEntry struct {
	Action string `json:"action"`
	Target string `json:"target"`
	Reason string `json:"reason"`
	Agent  string `json:"agent"`
	Query  string `json:"query"`
	State  string `json:"state"`
}

type result struct {
	Agent              string       `json:"agent"`
	Version            string       `json:"version"`
	OpenPlexer         string       `json:"openplexer"`
	Model              string       `json:"model"`
	Scenario           string       `json:"scenario"`
	AgentExit          int          `json:"agentExit"`
	Seconds            float64      `json:"seconds"`
	PolicyStatus       string       `json:"policyStatus"`
	Install            string       `json:"install"`
	Audit              []auditEntry `json:"audit"`
	CodexTrust         *trustReport `json:"codexTrust,omitempty"`
	NetworkBlocked     *bool        `json:"networkBlocked,omitempty"`
	NetworkExits       []int        `json:"networkExits,omitempty"`
	TargetOnDisk       bool         `json:"targetOnDisk"`
	WantOnDisk         bool         `json:"wantOnDisk"`
	ReasonReachedAgent bool         `json:"reasonReachedAgent"`
	ReasonInAgentReply bool         `json:"reasonInAgentReply"`
	ReasonSeen         string       `json:"reasonSeen"`
	Removed            []string     `json:"removedOutsideRunDir,omitempty"`
	Pass               bool         `json:"pass"`
	Problems           []string     `json:"problems,omitempty"`
}

func runMain(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	agentFlag := fs.String("agent", "all", "claude, codex or all")
	scenarioFlag := fs.String("scenario", "all", "scenario name or all")
	out := fs.String("out", "", "directory for raw output (default: a new temporary directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := refuseInsideAgent(); code != 0 {
		return code
	}
	if *out == "" {
		dir, err := os.MkdirTemp("", "realagent-out-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		*out = dir
	}
	bin, version, cleanup, err := buildInTemp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cleanup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var runs int
	failed := false
	for _, a := range agents {
		if *agentFlag != "all" && *agentFlag != a.name {
			continue
		}
		if err := a.signedIn(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v, so no scenario of %s starts\n", a.name, err, a.name)
			failed = true
			continue
		}
		for _, s := range scenarios {
			if *scenarioFlag != "all" && *scenarioFlag != s.name {
				continue
			}
			runs++
			res, err := runOne(ctx, a, s, bin, version, filepath.Join(*out, a.name, s.name))
			if ctx.Err() != nil {
				fmt.Fprintf(os.Stderr, "interrupted during %s/%s; raw output so far is in %s\n", a.name, s.name, *out)
				return 1
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s/%s: %v\n", a.name, s.name, err)
				failed = true
				continue
			}
			verdict := "PASS"
			if !res.Pass {
				verdict, failed = "FAIL", true
			}
			fmt.Printf("%s %s/%s onDisk=%v audit=%s reasonReached=%v reasonInReply=%v %s\n",
				verdict, a.name, s.name, res.TargetOnDisk, auditSummary(res.Audit, s), res.ReasonReachedAgent, res.ReasonInAgentReply, strings.Join(res.Problems, "; "))
		}
	}
	fmt.Printf("real agent runs: %d, raw output in %s\n", runs, *out)
	if failed || runs == 0 {
		return 1
	}
	return 0
}

func refuseInsideAgent() int {
	for _, v := range []string{"CLAUDECODE", "CODEX_THREAD_ID"} {
		if os.Getenv(v) != "" {
			fmt.Fprintf(os.Stderr, "run: %s is set, so this shell belongs to an agent session, and the agents would inherit its variables. Run from a plain terminal, or with env -i as README.md shows.\n", v)
			return 2
		}
	}
	return 0
}

func buildInTemp() (bin, version string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "realagent-bin-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if bin, version, err = buildOpenPlexer(dir); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return bin, version, cleanup, nil
}

func auditSummary(entries []auditEntry, s scenario) string {
	for _, e := range entries {
		if s.want.matches(e) {
			return strings.TrimPrefix(e.Action, "policy.") + "/" + e.State
		}
	}
	return "none"
}

func runOne(ctx context.Context, a agent, s scenario, bin, version, outDir string) (*result, error) {
	agentVersion, err := versionOf(a.name)
	if err != nil {
		return nil, fmt.Errorf("%s is not usable: %w", a.name, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "realagent-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	d := runSetup{project: filepath.Join(root, "project"), state: filepath.Join(root, "state"),
		home: filepath.Join(root, "home"), sessionID: newSessionID()}
	policyDir := filepath.Join(root, "policy")
	for _, dir := range []string{d.project, d.state, policyDir} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, err
		}
	}
	left, err := leftoverPaths(a, d)
	if err != nil {
		return nil, err
	}
	for _, p := range left {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			return nil, fmt.Errorf("%s exists before the run, so the harness cannot tell what the run adds", p)
		}
	}
	guard, err := agentConfig()
	if err != nil {
		return nil, err
	}
	before, err := fingerprint(guard)
	if err != nil {
		return nil, err
	}

	res := &result{Agent: a.name, Version: agentVersion, OpenPlexer: version, Model: a.model, Scenario: s.name, WantOnDisk: s.wantOnDisk}
	op := openplexer{bin: bin, home: d.home}
	if a.seed != nil {
		if err := a.seed(d); err != nil {
			return nil, err
		}
	}
	if res.PolicyStatus, err = setUpPolicy(op, s, policyDir); err != nil {
		return nil, err
	}
	if res.Install, err = op.install(a.installName, d.project); err != nil {
		return nil, err
	}
	if err := op.checkRoot(d.project); err != nil {
		return nil, err
	}
	if s.offline {
		plain, sandboxed, blocked, err := networkControl()
		if err != nil {
			return nil, err
		}
		res.NetworkBlocked, res.NetworkExits = &blocked, []int{plain, sandboxed}
	}
	if err := a.afterInstall(&d, s.offline); err != nil {
		return nil, err
	}
	var trustProblem error
	if a.name == codex.name {
		report, err := codexTrust(d.project, filepath.Join(root, "codexhome"), d.codexHooks)
		res.CodexTrust, trustProblem = &report, err
	}

	if trustProblem == nil {
		if err := runAgent(ctx, a, s, d, res, outDir); err != nil {
			removeLeftovers(left, res)
			return nil, err
		}
	} else {
		res.Problems = append(res.Problems, "codex trust check: "+trustProblem.Error()+", so the agent was not started")
	}
	if err := removeLeftovers(left, res); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	after, err := fingerprint(guard)
	if err != nil {
		return nil, err
	}
	for _, path := range changed(before, after) {
		res.Problems = append(res.Problems, path+" changed during the run")
	}
	if res.Audit, err = readAudit(filepath.Join(d.home, "audit.jsonl")); err != nil {
		return nil, err
	}
	if res.TargetOnDisk, err = s.exists(root); err != nil {
		return nil, err
	}
	if trustProblem == nil {
		judge(res, s)
	}
	res.Pass = len(res.Problems) == 0
	if err := copyIfExists(filepath.Join(d.home, "audit.jsonl"), filepath.Join(outDir, "audit.jsonl")); err != nil {
		return nil, err
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	return res, os.WriteFile(filepath.Join(outDir, "result.json"), data, 0o644)
}

func leftoverPaths(a agent, d runSetup) ([]string, error) {
	left, err := a.leftovers(d)
	if err != nil {
		return nil, err
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return append(left, filepath.Join(user, ".openplexer")), nil
}

func removeLeftovers(left []string, res *result) error {
	var errs []error
	for _, p := range left {
		if _, err := os.Lstat(p); err == nil {
			res.Removed = append(res.Removed, p)
		}
		errs = append(errs, os.RemoveAll(p))
	}
	return errors.Join(errs...)
}

func runAgent(ctx context.Context, a agent, s scenario, d runSetup, res *result, outDir string) error {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd, err := a.prepare(tctx, d, s)
	if err != nil {
		return err
	}
	cmd.WaitDelay = 10 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()
	res.Seconds = time.Since(start).Seconds()
	var ee *exec.ExitError
	switch {
	case errors.As(runErr, &ee):
		res.AgentExit = ee.ExitCode()
	case runErr != nil && ctx.Err() == nil:
		return runErr
	}
	if tctx.Err() == context.DeadlineExceeded {
		res.Problems = append(res.Problems, "the agent ran for 5 minutes and was killed")
	}
	if err := os.WriteFile(filepath.Join(outDir, "agent.stdout"), stdout.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "agent.stderr"), stderr.Bytes(), 0o644); err != nil {
		return err
	}
	said, tools := a.feedback(stdout.Bytes(), stderr.Bytes())
	if s.reason != "" {
		res.ReasonReachedAgent = strings.Contains(tools, s.reason)
		res.ReasonInAgentReply = strings.Contains(said, s.reason)
		res.ReasonSeen = reasonLine(tools, s.reason)
	}
	return nil
}

func reasonLine(text, reason string) string {
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, reason); i >= 0 {
			line = strings.TrimSpace(line[max(0, i-80):])
			if len(line) > 400 {
				line = line[:400]
			}
			return line
		}
	}
	return ""
}

func readAudit(path string) ([]auditEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []auditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Action string  `json:"action"`
			Target string  `json:"target"`
			Reason *string `json:"reason"`
			Meta   struct {
				Agent  string `json:"agent"`
				Action string `json:"action"`
				State  string `json:"policy_state"`
			} `json:"meta"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		switch rec.Action {
		case "policy.loaded", "policy.confirmed", "policy.rejected":
			continue
		}
		e := auditEntry{Action: rec.Action, Target: rec.Target, Agent: rec.Meta.Agent, Query: rec.Meta.Action, State: rec.Meta.State}
		if rec.Reason != nil {
			e.Reason = *rec.Reason
		}
		out = append(out, e)
	}
	return out, nil
}

func copyIfExists(from, to string) error {
	data, err := os.ReadFile(from)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}

func (w auditWant) matches(e auditEntry) bool {
	return e.Action == w.action && e.Query == w.query && e.State == w.state && e.Target == w.target
}

func (w auditWant) contradicts(e auditEntry) bool {
	return e.Target == w.target && e.Action != w.action
}

func judge(res *result, s scenario) {
	if !slices.ContainsFunc(res.Audit, s.want.matches) {
		res.Problems = append(res.Problems, fmt.Sprintf("no audit record is %s for a %s of %q in state %s", s.want.action, s.want.query, s.want.target, s.want.state))
	}
	for _, e := range res.Audit {
		if s.want.contradicts(e) {
			res.Problems = append(res.Problems, fmt.Sprintf("the audit holds %s for %q, which contradicts %s", e.Action, e.Target, s.want.action))
		}
	}
	if res.TargetOnDisk != s.wantOnDisk {
		res.Problems = append(res.Problems, fmt.Sprintf("target on disk is %v, want %v", res.TargetOnDisk, s.wantOnDisk))
	}
	if s.reason != "" && !res.ReasonReachedAgent {
		res.Problems = append(res.Problems, "the reason did not reach the agent's tool results: "+s.reason)
	}
	if s.offline && (res.NetworkBlocked == nil || !*res.NetworkBlocked) {
		res.Problems = append(res.Problems, "the offline profile did not block the network control")
	}
}
