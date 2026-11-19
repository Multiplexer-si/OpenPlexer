package main

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type report struct {
	started   time.Time
	versions  [][2]string
	preflight string
	steps     []outcome
}

type outcome struct {
	name, status, detail string
}

func newReport() *report { return &report{started: time.Now().UTC()} }

func (r *report) binaryVersion(ctx context.Context, path string, prebuilt bool) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return err
	}
	revision, modified := info.Main.Version, ""
	if prebuilt {
		output, err := runCommand(ctx, "", nil, path, nil, "--version")
		if err != nil {
			return err
		}
		r.versions = append(r.versions, [2]string{filepath.Base(path), strings.TrimSpace(string(output)) + " · " + info.GoVersion})
		return nil
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = " (modified checkout)"
			}
		}
	}
	r.versions = append(r.versions, [2]string{filepath.Base(path), revision + modified + " · " + info.GoVersion})
	return nil
}

func (r *report) run(ctx context.Context, steps []step, prepare func(context.Context) error) error {
	for _, s := range steps {
		r.steps = append(r.steps, outcome{name: s.name, status: "SKIP"})
	}
	if err := prepare(ctx); err != nil {
		r.preflight = "FAIL · " + err.Error()
		return err
	}
	r.preflight = "PASS"
	for i, s := range steps {
		fmt.Printf("%d. %s\n", i+1, s.name)
		if err := s.do(ctx); err != nil {
			r.steps[i].status, r.steps[i].detail = "FAIL", err.Error()
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		r.steps[i].status = "PASS"
	}
	return nil
}

func (r *report) write(path string) error {
	var text strings.Builder
	fmt.Fprintf(&text, "# Multi-agent flow results\n\nRun started %s.\n\n", r.started.Format(time.RFC3339))
	for _, v := range r.versions {
		fmt.Fprintf(&text, "- %s · %s\n", v[0], tableCell(v[1]))
	}
	if len(r.versions) > 0 {
		fmt.Fprintln(&text)
	}
	fmt.Fprintf(&text, "Preflight · %s\n\n", tableCell(r.preflight))
	fmt.Fprintln(&text, "| Step | Result | Detail |")
	fmt.Fprintln(&text, "| --- | --- | --- |")
	for i, s := range r.steps {
		fmt.Fprintf(&text, "| %d. %s | %s | %s |\n", i+1, s.name, s.status, tableCell(s.detail))
	}
	fmt.Fprintln(&text, "| 9. Write this report | PASS | |")
	f, err := os.CreateTemp(filepath.Dir(path), ".results-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(text.String())
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func tableCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(s)
}
