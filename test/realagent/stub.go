package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type stubConfig struct {
	LogPath    string
	DenyTokens []string
	Reason     string
	Style      string
}

const (
	styleJSON  = "json"
	styleExit2 = "exit2"
)

type record struct {
	Event    string          `json:"event"`
	Input    json.RawMessage `json:"input"`
	Decision string          `json:"decision"`
	Stdout   string          `json:"stdout,omitempty"`
	Stderr   string          `json:"stderr,omitempty"`
	Exit     int             `json:"exit"`
}

func decide(cfg stubConfig, input []byte) record {
	rec := record{Input: json.RawMessage(input), Decision: "none"}
	var in struct {
		Event     string          `json:"hook_event_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		rec.Event = "unparseable"
		rec.Input, _ = json.Marshal(string(input))
		return rec
	}
	rec.Event = in.Event
	if in.Event != "PreToolUse" {
		return rec
	}
	rec.Decision = "allow"
	for _, tok := range cfg.DenyTokens {
		if strings.Contains(string(in.ToolInput), tok) {
			rec.Decision = "deny"
			break
		}
	}
	if rec.Decision != "deny" {
		return rec
	}
	if cfg.Style == styleExit2 {
		rec.Stderr = cfg.Reason
		rec.Exit = 2
		return rec
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": cfg.Reason,
	}})
	rec.Stdout = string(out)
	return rec
}

func runStub(cfg stubConfig, stdin io.Reader, stdout, stderr io.Writer) int {
	input, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "stub: read stdin:", err)
		return 1
	}
	rec := decide(cfg, input)
	if line, err := json.Marshal(rec); err == nil {
		if f, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.Write(append(line, '\n'))
			f.Close()
		}
	}
	fmt.Fprint(stdout, rec.Stdout)
	fmt.Fprint(stderr, rec.Stderr)
	return rec.Exit
}

func readLog(path string) ([]record, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		recs = append(recs, r)
	}
	return recs, nil
}
