// Command realagent runs real Claude Code and Codex sessions against a stub
// hook. It spends tokens on the developer's own agent accounts, so nothing in
// CI runs it. See README.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "stub":
		os.Exit(stubMain(os.Args[2:]))
	case "samples":
		os.Exit(samplesMain(os.Args[2:]))
	case "run":
		os.Exit(runMain(os.Args[2:]))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: realagent run [-agent claude|codex|all] [-scenario name|all] [-style json|exit2] [-out dir] [-samples dir]")
	fmt.Fprintln(os.Stderr, "       realagent samples -agent name -log hooks.jsonl -dir dir")
	fmt.Fprintln(os.Stderr, "       realagent stub -log file [-deny tokens] [-reason text] [-style json|exit2]")
	os.Exit(2)
}

func stubMain(args []string) int {
	fs := flag.NewFlagSet("stub", flag.ContinueOnError)
	cfg := stubConfig{}
	var deny string
	fs.StringVar(&cfg.LogPath, "log", "", "JSONL file that receives one record per hook call")
	fs.StringVar(&deny, "deny", "", "comma-separated tokens; a PreToolUse input that holds one is denied")
	fs.StringVar(&cfg.Reason, "reason", "", "reason that the stub gives with a denial")
	fs.StringVar(&cfg.Style, "style", styleJSON, "deny protocol: json or exit2")
	if err := fs.Parse(args); err != nil || cfg.LogPath == "" {
		return 2
	}
	if deny != "" {
		cfg.DenyTokens = strings.Split(deny, ",")
	}
	return runStub(cfg, os.Stdin, os.Stdout, os.Stderr)
}

func samplesMain(args []string) int {
	fs := flag.NewFlagSet("samples", flag.ContinueOnError)
	agentName := fs.String("agent", "", "claude or codex")
	logPath := fs.String("log", "", "hooks.jsonl of an earlier run")
	dir := fs.String("dir", "", "directory that receives the samples")
	if err := fs.Parse(args); err != nil || *agentName == "" || *logPath == "" || *dir == "" {
		return 2
	}
	recs, err := readLog(*logPath)
	if err != nil || len(recs) == 0 {
		fmt.Fprintln(os.Stderr, "samples: no records in", *logPath, err)
		return 1
	}
	var first struct {
		Cwd string `json:"cwd"`
	}
	json.Unmarshal(recs[0].Input, &first)
	if err := writeSamples(*dir, *agentName, recs, first.Cwd, map[string]bool{}); err != nil {
		fmt.Fprintln(os.Stderr, "samples:", err)
		return 1
	}
	return 0
}
