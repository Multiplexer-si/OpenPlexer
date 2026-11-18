// Command realagent runs real Claude Code and Codex sessions against the
// openplexer binary that it builds, with the hook that `openplexer install`
// writes. It spends tokens on the developer's own agent accounts, so nothing
// in CI runs it. See README.md.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/multiplexer-si/openplexer/test/internal/agentcli"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runMain(os.Args[2:]))
	case "trust":
		os.Exit(trustMain(os.Args[2:]))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: realagent run [-agent claude|codex|all] [-scenario name|all] [-out dir]")
	fmt.Fprintln(os.Stderr, "       realagent trust")
	os.Exit(2)
}

func trustMain(args []string) int {
	if len(args) != 0 {
		usage()
	}
	if code := refuseInsideAgent(); code != 0 {
		return code
	}
	bin, _, cleanup, err := buildInTemp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cleanup()
	root, err := os.MkdirTemp("", "realagent-trust-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(root)
	if root, err = filepath.EvalSymlinks(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	op := openplexer{bin: bin, home: filepath.Join(root, "home")}
	if _, err := op.install(codex.installName, project); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := op.checkRoot(project); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	flags, err := agentcli.HooksOverrides(filepath.Join(project, ".codex", "hooks.json"), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	report, err := codexTrust(project, filepath.Join(root, "codexhome"), flags)
	out, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, "trust:", err)
		return 1
	}
	return 0
}
