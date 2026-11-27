package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/multiplexer-si/openplexer/internal/adapter"
	"github.com/multiplexer-si/openplexer/internal/enrollment"
)

const maxInviteLink = 4096

func (c command) join(args []string) int {
	flags := c.flags("join")
	// Flag parsing can quote an unknown option or a value containing a link.
	flags.SetOutput(io.Discard)
	stdin := flags.Bool("link-stdin", false, "")
	discard := flags.Bool("discard-pending", false, "")
	root := flags.String("project-root", "", "")
	home := flags.String("home", c.home, "")
	hook := flags.Bool("install-hook", false, "")
	service := flags.Bool("install-service", false, "")
	if flags.Parse(args) != nil || flags.NArg() != 0 || strings.Contains(*root, "#fp=") || strings.Contains(*home, "#fp=") || *discard && (*stdin || *hook || *service) {
		fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	if *root == "" {
		path, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return c.fail(errors.New("no Git checkout found; pass --project-root <dir>"))
		}
		*root = strings.TrimSuffix(string(path), "\n")
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return c.fail(err)
	}
	if *discard {
		removed, err := enrollment.Discard(user, *root, *home)
		if err != nil {
			return c.fail(err)
		}
		if removed {
			fmt.Fprintln(c.stdout, "discarded the pending join")
		} else {
			fmt.Fprintln(c.stdout, "no pending join")
		}
		return exitOK
	}
	var input *bufio.Reader
	var linkText string
	if *stdin {
		data, err := io.ReadAll(io.LimitReader(c.stdin, maxInviteLink+1))
		if err != nil || len(data) > maxInviteLink {
			return c.fail(errors.New("could not read one invite link from standard input"))
		}
		linkText = string(data)
	} else {
		file, _ := c.stdin.(*os.File)
		input = bufio.NewReaderSize(c.stdin, maxInviteLink+1)
		linkText, err = readInviteFromTerminal(file, input, c.stderr)
		if err != nil {
			return c.fail(err)
		}
	}
	// Accept a single line ending, or a file with no line ending.
	link, err := enrollment.ParseLink(strings.TrimSuffix(strings.TrimSuffix(linkText, "\n"), "\r"))
	if err != nil {
		return c.fail(err)
	}
	result, err := enrollment.Join(context.Background(), enrollment.Request{
		UserHome: user, Root: *root, Home: *home, Link: link, Now: c.now,
	})
	if err != nil {
		return c.fail(err)
	}
	if result.PendingID != "" {
		fmt.Fprintf(c.stdout, "pending_approval: %s\nConfirm this invite ID with the lead. After approval, rerun openplexer join with the same link.\n", result.PendingID)
		return exitFailure
	}
	c.home = result.Home
	c.reportJoin(result)
	c.offerJoinInstalls(result, input, *hook, *service)
	return exitOK
}

func (c command) reportJoin(result enrollment.Result) {
	r := result.Receipt
	fmt.Fprintf(c.stdout, "project: %s\nmember: %s\nhub role: %s\npolicy: %s\npolicy version: %d\nhome: %s\n",
		r.ProjectID, r.MemberID, r.HubRole, result.Status.State, result.Status.Version, result.Home)
	if result.Status.Role == "" {
		fmt.Fprintln(c.stdout, "no policy role yet")
	} else {
		fmt.Fprintf(c.stdout, "policy role: %s\n", result.Status.Role)
		for _, grant := range []struct {
			action string
			values []string
		}{
			{"read", result.Grants.Read}, {"write", result.Grants.Write},
			{"exec", result.Grants.Exec}, {"git.push", result.Grants.Push},
		} {
			if len(grant.values) != 0 {
				fmt.Fprintf(c.stdout, "%s grants: %q\n", grant.action, grant.values)
			}
		}
	}
	for _, grant := range result.Temporary {
		if grant.MissionID != "" {
			fmt.Fprintf(c.stdout, "temporary grants for mission %q on %q: %v\n", grant.MissionID, grant.Area, grant.Actions)
		} else {
			fmt.Fprintf(c.stdout, "active temporary grants on %q: %v\n", grant.Area, grant.Actions)
		}
	}
	fmt.Fprintln(c.stdout, "Room status skipped because a status event requires an agent session.")
}

func (c command) offerJoinInstalls(result enrollment.Result, input *bufio.Reader, installHook, installService bool) {
	remote, _ := exec.Command("git", "-C", result.Root, "config", "--local", "--get", "remote.origin.url").Output()
	if sameRepository(result.Receipt.Repo, strings.TrimSuffix(string(remote), "\n")) {
		for _, agent := range adapter.Agents {
			if (installHook || c.joinConsent(input, "Install the portable "+agent.Name+" hook?")) &&
				c.install([]string{agent.Name, "--project", result.Root, "--portable"}) == exitOK {
				continue
			}
			fmt.Fprintf(c.stdout, "To install the %s hook, run openplexer install %s --project %s --portable --home %s\n",
				agent.Name, agent.Name, quoteJoinPath(result.Root), quoteJoinPath(result.Home))
		}
	} else {
		fmt.Fprintln(c.stdout, "Portable hook setup requires a checkout with a matching Git origin.")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		fmt.Fprintln(c.stdout, "The login service requires macOS or Linux.")
		return
	}
	if (installService || c.joinConsent(input, "Install and start the OpenPlexer login service?")) &&
		c.service([]string{"install"}) == exitOK {
		return
	}
	fmt.Fprintf(c.stdout, "To start the login service, run openplexer service install --home %s\n", quoteJoinPath(result.Home))
}

func (c command) joinConsent(input *bufio.Reader, prompt string) bool {
	if input == nil {
		return false
	}
	fmt.Fprintf(c.stderr, "%s [y/N] ", prompt)
	line, err := input.ReadSlice('\n')
	answer := strings.ToLower(strings.TrimSpace(string(line)))
	return err == nil && (answer == "y" || answer == "yes")
}

func quoteJoinPath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
}

func sameRepository(repo, remote string) bool {
	if remote == "" {
		return false
	}
	if repo == remote {
		return true
	}
	remoteHost, remotePath := repositoryParts(remote)
	repoHost, repoPath := repositoryParts(repo)
	if remotePath == "" {
		return false
	}
	if remotePath == repoPath {
		return repoHost == "" || strings.EqualFold(repoHost, remoteHost)
	}
	prefix, path, qualified := strings.Cut(repoPath, "/")
	return repoHost == "" && qualified && path == remotePath && strings.EqualFold(prefix, remoteHost)
}

func repositoryParts(name string) (host, path string) {
	switch {
	case strings.Contains(name, "://"):
		u, err := url.Parse(name)
		if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return "", ""
		}
		host, path = u.Hostname(), u.EscapedPath()
	case strings.Contains(name, ":"):
		host, path, _ = strings.Cut(name, ":")
		if _, afterUser, found := strings.Cut(host, "@"); found {
			host = afterUser
		}
	default:
		path = name
	}
	return host, strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".git")
}
