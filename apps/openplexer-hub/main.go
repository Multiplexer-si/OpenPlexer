// Command openplexer-hub serves and administers a single-team hub.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
)

const usage = `Usage: openplexer-hub <command> [flags]
       openplexer-hub --version
Commands:
  init            Initialize a data directory or a Worker team
  add-member      Add a member and print a bearer token
  remove-member   Revoke a member's bearer token
  add-project     Scope a project to one repository
  publish-policy  Publish a JSON policy bundle
  export-audit    Write the audit export, checkpoint, and public keys
  serve           Start the HTTP server
Use <command> -h for command flags.
`

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithInput(args, os.Stdin, stdout, stderr, nil)
}

func runWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer, transport http.RoundTripper) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
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
	var remoteAction func(remoteAdmin) error
	switch command {
	case "init":
		action = (*hub.Store).Init
		id := flags.String("id", "", "First lead's member ID with --hub")
		leadFile := flags.String("lead-token-file", "", "Private file to create or reuse for the first lead with --hub")
		remoteAction = func(h remoteAdmin) error { return h.init(*id, *leadFile) }
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
		remoteAction = func(h remoteAdmin) error { return h.addMember(*id, *role, stdout) }
	case "remove-member":
		id := flags.String("id", "", "Member ID")
		action = func(s *hub.Store) error { return s.RemoveMember(*id) }
		remoteAction = func(h remoteAdmin) error { return h.removeMember(*id) }
	case "add-project":
		id := flags.String("id", "", "Project ID and Room ID")
		repo := flags.String("repo", "", "Repository identifier used in the bundle")
		action = func(s *hub.Store) error { return s.AddProject(*id, *repo) }
		remoteAction = func(h remoteAdmin) error { return h.addProject(*id, *repo) }
	case "publish-policy":
		project := flags.String("project", "", "Project ID")
		path := flags.String("file", "", "Path to the JSON policy bundle")
		action = func(s *hub.Store) error { return publish(s, *project, *path) }
		remoteAction = func(h remoteAdmin) error { return h.publishPolicy(*project, *path) }
	case "export-audit":
		out := flags.String("out", "", "Output directory for the three audit files")
		action = func(s *hub.Store) error { return exportAudit(s, *out) }
	case "serve":
		listen := flags.String("listen", "127.0.0.1:8787", "HTTP listen address")
		action = func(s *hub.Store) error { return serve(s, *listen) }
	default:
		fmt.Fprintf(stderr, "Unknown command %q\n%s", command, usage)
		return 2
	}
	var address, tokenFile string
	if remoteAction != nil {
		flags.StringVar(&address, "hub", "", "Worker hub origin")
		flags.StringVar(&tokenFile, "token-file", "-", "Private credential file or - for stdin with --hub")
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
	used := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { used[f.Name] = true })
	var err error
	if used["hub"] {
		if used["data-dir"] {
			fmt.Fprintln(stderr, "Use either --hub or --data-dir")
			return 2
		}
		err = runRemote(address, tokenFile, command, stdin, transport, remoteAction)
	} else {
		if used["token-file"] || (command == "init" && (used["id"] || used["lead-token-file"])) {
			fmt.Fprintln(stderr, "Worker credentials and first lead flags require --hub")
			return 2
		}
		err = action(hub.Open(*dir))
	}
	if err != nil {
		fmt.Fprintf(stderr, "openplexer-hub %s: %v\n", command, err)
		return 1
	}
	return 0
}

func exportAudit(s *hub.Store, out string) error {
	if out == "" {
		return errors.New("--out is required")
	}
	snapshot, err := s.ExportAudit()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(out, ".audit-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	files := []struct {
		name string
		data []byte
	}{
		{"jwks.json", snapshot.JWKS},
		{"checkpoint.jws", []byte(snapshot.Checkpoint)},
		{"export.json", snapshot.Export},
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(stage, file.name), file.data, 0600); err != nil {
			return err
		}
	}
	// Link without overwriting. Publish the log after its key set and checkpoint.
	for i, file := range files {
		if err := os.Link(filepath.Join(stage, file.name), filepath.Join(out, file.name)); err != nil {
			for _, published := range files[:i] {
				err = errors.Join(err, os.Remove(filepath.Join(out, published.name)))
			}
			return fmt.Errorf("publish audit files: %w", err)
		}
	}
	return nil
}

func policyText(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, hub.MaxPolicyBytes+1))
}

func publish(s *hub.Store, project, path string) error {
	text, err := policyText(path)
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
