// Command openplexer-hub serves and administers a single-team hub.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
)

const usage = `Usage: openplexer-hub <command> [flags]
Commands:
  init            Initialize a data directory
  add-member      Add a member and print a bearer token
  remove-member   Revoke a member's bearer token
  add-project     Scope a project to one repository
  publish-policy  Publish a JSON policy bundle
  serve           Start the HTTP server
Use <command> -h for command flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("data-dir", "./hub-data", "Hub data directory")
	var action func(*hub.Store) error
	switch command {
	case "init":
		action = (*hub.Store).Init
	case "add-member":
		id := flags.String("id", "", "Member ID")
		role := flags.String("role", string(hub.Member), "Member role (lead or member)")
		action = func(s *hub.Store) error {
			token, err := s.AddMember(*id, hub.Role(*role))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(stdout, token)
			return err
		}
	case "remove-member":
		id := flags.String("id", "", "Member ID")
		action = func(s *hub.Store) error { return s.RemoveMember(*id) }
	case "add-project":
		id := flags.String("id", "", "Project ID and Room ID")
		repo := flags.String("repo", "", "Repository identifier used in the bundle")
		action = func(s *hub.Store) error { return s.AddProject(*id, *repo) }
	case "publish-policy":
		project := flags.String("project", "", "Project ID")
		path := flags.String("file", "", "Path to the JSON policy bundle")
		action = func(s *hub.Store) error { return publish(s, *project, *path) }
	case "serve":
		listen := flags.String("listen", "127.0.0.1:8787", "HTTP listen address")
		action = func(s *hub.Store) error { return serve(s, *listen) }
	default:
		fmt.Fprintf(stderr, "Unknown command %q\n%s", command, usage)
		return 2
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "Unexpected positional arguments")
		return 2
	}
	if err := action(hub.Open(*dir)); err != nil {
		fmt.Fprintf(stderr, "openplexer-hub %s: %v\n", command, err)
		return 1
	}
	return 0
}

func publish(s *hub.Store, project, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	text, err := io.ReadAll(io.LimitReader(file, hub.MaxPolicyBytes+1))
	if err != nil {
		return err
	}
	_, err = s.PublishPolicy(project, text)
	return err
}

func serve(s *hub.Store, addr string) error {
	if err := s.Check(); err != nil {
		return err
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}
	return server.ListenAndServe()
}
