// Command openplexer-verify verifies audit exports offline.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	flags := flag.NewFlagSet("openplexer-verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: openplexer-verify --export export.json --jwks jwks.json [--checkpoint file]... [--json]")
		fmt.Fprintln(stderr, "       openplexer-verify --version")
		flags.PrintDefaults()
	}
	exportPath := flags.String("export", "", "Path to the JSON audit export")
	jwksPath := flags.String("jwks", "", "Path to the trusted checkpoint JWKS")
	asJSON := flags.Bool("json", false, "Print the verification result as JSON")
	var paths []string
	flags.Func("checkpoint", "Path to a compact checkpoint JWS (repeatable)", func(path string) error {
		paths = append(paths, path)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *exportPath == "" || *jwksPath == "" || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	export, err := os.ReadFile(*exportPath)
	if err != nil {
		fmt.Fprintln(stderr, "openplexer-verify: read export:", err)
		return 2
	}
	jwks, err := os.ReadFile(*jwksPath)
	if err != nil {
		fmt.Fprintln(stderr, "openplexer-verify: read JWKS:", err)
		return 2
	}
	var tokens []string
	for _, path := range paths {
		token, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(stderr, "openplexer-verify: read checkpoint:", err)
			return 2
		}
		tokens = append(tokens, string(token))
	}

	result := audit.VerifyExport(export, jwks, tokens...)
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, "openplexer-verify: write result:", err)
			return 2
		}
	} else if !result.OK {
		fmt.Fprintln(stderr, result.Diagnostic())
	} else {
		noun := "records"
		if result.Entries == 1 {
			noun = "record"
		}
		fmt.Fprintf(stdout, "Chain is consistent from genesis (%d %s). ", result.Entries, noun)
		if result.CheckpointEntries == nil {
			fmt.Fprintln(stdout, "Completeness is not proven.")
		} else {
			fmt.Fprintf(stdout, "Completeness is proven through checkpoint count %d.\n", *result.CheckpointEntries)
			if *result.CheckpointEntries < result.Entries {
				fmt.Fprintf(stdout, "Records after position %d are unanchored.\n", *result.CheckpointEntries)
			}
		}
	}
	if !result.OK {
		return 1
	}
	return 0
}
