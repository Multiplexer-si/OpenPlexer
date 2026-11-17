package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type preToolCall struct {
	Tool     string   `json:"tool"`
	Fields   []string `json:"toolInputFields"`
	Decision string   `json:"decision"`
	Exit     int      `json:"exit"`
}

type result struct {
	Agent              string         `json:"agent"`
	Version            string         `json:"version"`
	Model              string         `json:"model"`
	Scenario           string         `json:"scenario"`
	Style              string         `json:"style"`
	AgentExit          int            `json:"agentExit"`
	Seconds            float64        `json:"seconds"`
	HookEvents         map[string]int `json:"hookEvents"`
	PreToolUse         []preToolCall  `json:"preToolUse"`
	TargetOnDisk       bool           `json:"targetOnDisk"`
	WantOnDisk         bool           `json:"wantOnDisk"`
	ReasonToolResult   bool           `json:"reasonReachedAgentTools"`
	ReasonInAgentReply bool           `json:"reasonInAgentReply"`
	Pass               bool           `json:"pass"`
	Problems           []string       `json:"problems,omitempty"`
}

func runMain(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	agentFlag := fs.String("agent", "all", "claude, codex or all")
	scenarioFlag := fs.String("scenario", "all", "scenario name or all")
	style := fs.String("style", styleJSON, "deny protocol: json or exit2")
	out := fs.String("out", filepath.Join("test", "realagent", "out", time.Now().UTC().Format("20060102T150405Z")), "directory for raw output")
	samples := fs.String("samples", "", "directory that receives redacted hook input samples")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var runs int
	written := map[string]bool{}
	failed := false
	for _, a := range agents {
		if *agentFlag != "all" && *agentFlag != a.name {
			continue
		}
		for _, s := range scenarios {
			if *scenarioFlag != "all" && *scenarioFlag != s.name {
				continue
			}
			runs++
			res, err := runOne(a, s, exe, *style, filepath.Join(*out, a.name, s.name), *samples, written)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s/%s: %v\n", a.name, s.name, err)
				failed = true
				continue
			}
			verdict := "PASS"
			if !res.Pass {
				verdict, failed = "FAIL", true
			}
			fmt.Printf("%s %s/%s hooks=%v onDisk=%v reasonInReply=%v %s\n",
				verdict, a.name, s.name, res.HookEvents, res.TargetOnDisk, res.ReasonInAgentReply, strings.Join(res.Problems, "; "))
		}
	}
	fmt.Printf("real agent runs: %d, raw output in %s\n", runs, *out)
	if failed || runs == 0 {
		return 1
	}
	return 0
}

func runOne(a agent, s scenario, exe, style, outDir, samplesDir string, written map[string]bool) (*result, error) {
	version, err := a.version()
	if err != nil {
		return nil, fmt.Errorf("%s is not usable: %w", a.binary, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	project, err := os.MkdirTemp("", "realagent-project-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(project)
	if project, err = filepath.EvalSymlinks(project); err != nil {
		return nil, err
	}
	logPath := filepath.Join(outDir, "hooks.jsonl")
	if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	hookCmd := strings.Join([]string{shellQuote(exe), "stub", "-log", shellQuote(logPath),
		"-deny", shellQuote(strings.Join(s.denyTokens, ",")),
		"-reason", shellQuote(s.reason), "-style", style}, " ")
	cmd, err := a.prepare(project, hookCmd, s.prompt)
	if err != nil {
		return nil, err
	}
	timer := time.AfterFunc(5*time.Minute, func() { cmd.Process.Kill() })
	defer timer.Stop()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()
	res := &result{Agent: a.name, Version: version, Scenario: s.name, Style: style,
		Seconds: time.Since(start).Seconds(), HookEvents: map[string]int{}, WantOnDisk: s.wantOnDisk}
	res.Model = map[string]string{"claude": claudeModel, "codex": codexModel}[a.name]
	if ee, ok := runErr.(*exec.ExitError); ok {
		res.AgentExit = ee.ExitCode()
	} else if runErr != nil {
		return nil, runErr
	}
	if err := os.WriteFile(filepath.Join(outDir, "agent.stdout"), stdout.Bytes(), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "agent.stderr"), stderr.Bytes(), 0o644); err != nil {
		return nil, err
	}
	recs, err := readLog(logPath)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		res.HookEvents[r.Event]++
		if r.Event == "PreToolUse" {
			res.PreToolUse = append(res.PreToolUse, summarize(r))
		}
	}
	if res.TargetOnDisk, err = s.exists(project); err != nil {
		return nil, err
	}
	said, tools := a.feedback(stdout.Bytes(), stderr.Bytes())
	if tok := s.reasonToken(); tok != "" {
		res.ReasonToolResult = strings.Contains(tools, tok)
		res.ReasonInAgentReply = strings.Contains(said, tok)
	}
	judge(res, s)
	if samplesDir != "" {
		if err := writeSamples(samplesDir, a.name, recs, project, written); err != nil {
			return nil, err
		}
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	return res, os.WriteFile(filepath.Join(outDir, "result.json"), data, 0o644)
}

func summarize(r record) preToolCall {
	var in struct {
		Tool  string         `json:"tool_name"`
		Input map[string]any `json:"tool_input"`
	}
	json.Unmarshal(r.Input, &in)
	var fields []string
	for k := range in.Input {
		fields = append(fields, k)
	}
	slices.Sort(fields)
	return preToolCall{Tool: in.Tool, Fields: fields, Decision: r.Decision, Exit: r.Exit}
}

func judge(res *result, s scenario) {
	var denies, allows int
	for _, c := range res.PreToolUse {
		if c.Decision == "deny" {
			denies++
		} else {
			allows++
		}
	}
	if len(res.PreToolUse) == 0 {
		res.Problems = append(res.Problems, "no PreToolUse hook fired")
	}
	if s.wantOnDisk && denies > 0 {
		res.Problems = append(res.Problems, "stub denied an allowed scenario")
	}
	if !s.wantOnDisk && denies == 0 {
		res.Problems = append(res.Problems, "stub never denied")
	}
	if res.TargetOnDisk != s.wantOnDisk {
		res.Problems = append(res.Problems, fmt.Sprintf("target on disk is %v, want %v", res.TargetOnDisk, s.wantOnDisk))
	}
	res.Pass = len(res.Problems) == 0
}
