// Command openplexer is the local CLI. It configures the local gate, loads
// a signed policy bundle, reports the state of the policy, and checks one
// action against it. It installs the hook of a coding agent, and it is the
// command that the hook runs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

const usage = `usage:
  openplexer --version
  openplexer configure [--keys <jwks.json>] [--member <member-id>] [--stale-after-days <days>] [--max-policy-age <duration>]
  openplexer configure --project-root <dir> [--home <dir>]
  openplexer configure --hub <url> --project <id> --member <id> --keys <jwks.json> --token-file <file>
  openplexer daemon [--quiet]
  openplexer service install|uninstall|status [--home <dir>]
  openplexer room post < event.json
  openplexer policy sync
  openplexer policy load <signed-bundle.json>
  openplexer policy status
  openplexer policy check-session
  openplexer policy check [--agent <name>] [--session <id>] [--repo <repo>] <action> <target>
  openplexer install <agent> [--scope user|project] [--project <dir>] [--file <path>] [--portable]
  openplexer uninstall <agent> [--scope user|project] [--project <dir>] [--file <path>]
  openplexer hook <agent> pre-tool|session-start [--project-home]

An agent is claude-code or codex.

An action is read, write, exec, or git.push. The target is a path for read
and write, a command for exec, and a repo for git.push.

Use --home <dir> before the command to select a home. The other choices, in
order, are $OPENPLEXER_HOME, the registry for the current directory, and
~/.openplexer. Legacy hooks use $OPENPLEXER_HOME or ~/.openplexer.

install adds the OpenPlexer hook to the hook file of the agent, and uninstall
removes it. The user scope is the file in $CLAUDE_CONFIG_DIR or ~/.claude for
claude-code, and in $CODEX_HOME or ~/.codex for codex. The project scope is
the file in the .claude or .codex directory of the project, the current
directory by default. --project means the project scope, and install records
the project as a project root. --file names the hook file itself.
--portable registers the checkout and writes a command that uses PATH.
`

const (
	exitOK         = 0
	exitFailure    = 1
	exitUsage      = 2
	exitStale      = 3
	exitUnenforced = 4
	exitDenied     = 5
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, time.Now))
}

type command struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	now            func() time.Time
	home           string
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return exitOK
	}
	c := command{stdin: stdin, stdout: stdout, stderr: stderr, now: now}
	switch {
	case len(args) >= 2 && args[0] == "--home":
		c.home, args = args[1], args[2:]
	case len(args) > 0 && strings.HasPrefix(args[0], "--home="):
		c.home, args = strings.TrimPrefix(args[0], "--home="), args[1:]
	}
	if len(args) > 0 {
		switch args[0] {
		case "configure":
			return c.configure(args[1:])
		case "install":
			return c.install(args[1:])
		case "uninstall":
			return c.uninstall(args[1:])
		case "service":
			return c.service(args[1:])
		case "hook":
			return c.runHook(args[1:])
		}
	}
	var err error
	if c.home, err = projects.ResolveHome(c.home); err != nil {
		return c.fail(err)
	}
	switch {
	case len(args) == 2 && args[0] == "room" && args[1] == "post":
		return c.post()
	case len(args) >= 1 && args[0] == "daemon":
		return c.daemon(args[1:])
	case len(args) == 3 && args[0] == "policy" && args[1] == "load":
		return c.load(args[2])
	case len(args) == 2 && args[0] == "policy" && args[1] == "sync":
		return c.load("")
	case len(args) == 2 && args[0] == "policy" && args[1] == "status":
		return c.status()
	case len(args) == 2 && args[0] == "policy" && args[1] == "check-session":
		return c.checkSession()
	case len(args) >= 2 && args[0] == "policy" && args[1] == "check":
		return c.check(args[2:])
	}
	fmt.Fprint(stderr, usage)
	return exitUsage
}

func (c command) fail(err error) int {
	fmt.Fprintf(c.stderr, "openplexer: %v\n", err)
	return exitFailure
}

func (c command) flags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(c.stderr)
	flags.Usage = func() { fmt.Fprint(c.stderr, usage) }
	return flags
}

func (c command) configure(args []string) int {
	flags := c.flags("configure")
	keys := flags.String("keys", "", "")
	member := flags.String("member", "", "")
	hub := flags.String("hub", "", "")
	project := flags.String("project", "", "")
	tokenFile := flags.String("token-file", "", "")
	projectRoot := flags.String("project-root", "", "")
	home := flags.String("home", c.home, "")
	staleAfterDays := flags.Int("stale-after-days", 0, "")
	maxPolicyAge := flags.String("max-policy-age", "", "")
	if flags.Parse(args) != nil || flags.NArg() != 0 || flags.NFlag() == 0 {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	var configuring, registering, settingAge bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "project-root":
			registering = true
		case "home":
		case "max-policy-age":
			settingAge, configuring = true, true
		default:
			configuring = true
		}
	})
	if !configuring && !registering {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	var err error
	if c.home, err = projects.ResolveHome(*home); err != nil {
		return c.fail(err)
	}
	change := daemon.Settings{MemberID: *member, StaleAfterDays: *staleAfterDays, HubURL: *hub, ProjectID: *project}
	if settingAge {
		age, err := time.ParseDuration(*maxPolicyAge)
		if err != nil || age < time.Minute || age > time.Hour || age%time.Second != 0 {
			return c.fail(errors.New("maximum policy age must be between 1 minute and 1 hour in whole seconds"))
		}
		change.MaxPolicyAgeSeconds = int(age / time.Second)
	}
	if *tokenFile != "" {
		token, err := os.ReadFile(*tokenFile)
		if err != nil {
			return c.fail(err)
		}
		change.Token = strings.TrimSpace(string(token))
		if change.Token == "" {
			return c.fail(errors.New("token file is empty"))
		}
	}
	if *keys != "" {
		var err error
		if change.KeySet, err = os.ReadFile(*keys); err != nil {
			return c.fail(err)
		}
	}
	if registering {
		err = c.configureProject(*projectRoot, change, configuring)
	} else {
		err = daemon.Configure(c.home, change)
	}
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}

func (c command) load(path string) int {
	gate, err := daemon.Open(c.home, c.now)
	if err != nil {
		return c.fail(err)
	}
	var status daemon.Status
	if path == "" {
		status, err = gate.Sync(context.Background())
	} else {
		status, err = gate.Load(context.Background(), daemon.FileSource(path))
	}
	if err != nil && !errors.Is(err, daemon.ErrNotRecorded) {
		return c.fail(err)
	}
	c.report(status)
	if err != nil {
		fmt.Fprintf(c.stderr, "openplexer: the policy is loaded: %v\n", err)
	}
	return exitOK
}

func (c command) status() int {
	gate, err := daemon.Open(c.home, c.now)
	if err != nil {
		return c.fail(err)
	}
	status := gate.Status()
	c.report(status)
	activity, _ := daemon.ReadDaemonActivity(c.home)
	c.reportDaemonHeartbeat(activity)
	if status.MaxPolicyAge != 0 && activity.HeartbeatAt.IsZero() {
		fmt.Fprintln(c.stdout, "daemon heartbeat: unavailable")
	}
	return policyStatusExit(status)
}

func policyStatusExit(status daemon.Status) int {
	switch status.State {
	case daemon.Verified:
		return exitOK
	case daemon.Stale:
		return exitStale
	}
	return exitUnenforced
}

func (c command) report(status daemon.Status) {
	fmt.Fprintf(c.stdout, "policy: %s\n", status.State)
	fmt.Fprintf(c.stdout, "detail: %s\n", status.Detail)
	if status.MaxPolicyAge != 0 {
		confirmed := "never"
		if !status.HubConfirmedAt.IsZero() {
			confirmed = status.HubConfirmedAt.UTC().Format(time.RFC3339Nano)
		}
		fmt.Fprintf(c.stdout, "hub confirmed at: %s\nmaximum policy age: %s\n", confirmed, status.MaxPolicyAge)
	}
	switch {
	case status.Member == "":
		fmt.Fprintln(c.stdout, "member: not configured")
	case !status.Enforced():
		fmt.Fprintf(c.stdout, "member: %s\n", status.Member)
	case status.Role == "":
		fmt.Fprintf(c.stdout, "member: %s, who has no role in the policy\n", status.Member)
	default:
		fmt.Fprintf(c.stdout, "member: %s, with the role %s\n", status.Member, status.Role)
	}
	fmt.Fprintf(c.stdout, "home: %s\n", c.home)
}

func (c command) check(args []string) int {
	flags := c.flags("policy check")
	agent := flags.String("agent", "openplexer", "")
	session := flags.String("session", "", "")
	repo := flags.String("repo", "", "")
	if flags.Parse(args) != nil || flags.NArg() != 2 {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	query := policy.Query{Action: policy.Action(flags.Arg(0)), Repo: *repo}
	if *session != "" {
		query.AgentSessionID = session
	}
	switch target := flags.Arg(1); query.Action {
	case policy.Read, policy.Write:
		query.Path = target
	case policy.Exec:
		query.Command = target
	case policy.GitPush:
		query.Repo = target
	default:
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	gate, err := daemon.Open(c.home, c.now)
	if err != nil {
		return c.fail(err)
	}
	verdict, err := gate.Decide(*agent, query)
	if err != nil {
		fmt.Fprintf(c.stderr, "openplexer: %v\n", err)
	}
	result, decided := verdict.Result()
	if !decided {
		fmt.Fprintf(c.stdout, "no policy: %s\n", verdict.Policy.Detail)
		return exitUnenforced
	}
	if verdict.Policy.State == daemon.Stale && verdict.Policy.MaxPolicyAge == 0 {
		fmt.Fprintf(c.stderr, "openplexer: the policy is stale: %s\n", verdict.Policy.Detail)
	}
	fmt.Fprintf(c.stdout, "%s: %s\n", result.Decision, result.Rule)
	if result.Decision == policy.Deny {
		return exitDenied
	}
	return exitOK
}
