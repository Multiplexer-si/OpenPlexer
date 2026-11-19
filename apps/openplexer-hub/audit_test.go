package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/testbin"
)

func TestHubAuditRequiresTheSelectedVerifier(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENPLEXER_BIN_DIR", dir)
	hub := filepath.Join(dir, "openplexer-hub")
	if err := os.WriteFile(hub, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestHubExportVerifiesWithStandaloneBinary$")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "OPENPLEXER_BIN_DIR") ||
		!strings.Contains(string(output), filepath.Join(dir, "openplexer-verify")) {
		t.Fatalf("hub audit without the selected verifier: %v\n%s", err, output)
	}
}

func TestHubExportVerifiesWithStandaloneBinary(t *testing.T) {
	bin, err := testbin.Dir("openplexer-hub", "openplexer-verify")
	if err != nil {
		t.Fatal(err)
	}
	hubDir := filepath.Join(t.TempDir(), "hub")
	admin := commands(t, hubDir)
	admin(0, "init")
	admin(0, "add-member", "--id", "lead", "--role", "lead")

	var bundle map[string]json.RawMessage
	if err := json.Unmarshal(readAuditFile(t, "../../spec/policy/v1/examples/bundle/minimal.json"), &bundle); err != nil {
		t.Fatal(err)
	}
	var project string
	if err := json.Unmarshal(bundle["project_id"], &project); err != nil {
		t.Fatal(err)
	}
	const repo = "example-repo"
	bundle["repos"] = marshal(t, []string{repo})
	bundlePath := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(bundlePath, marshal(t, bundle), 0600); err != nil {
		t.Fatal(err)
	}
	admin(0, "add-project", "--id", project, "--repo", repo)
	admin(0, "publish-policy", "--project", project, "--file", bundlePath)
	admin(0, "publish-policy", "--project", project, "--file", bundlePath)
	admin(1, "export-audit")

	offline := t.TempDir()
	admin(0, "export-audit", "--out", offline)
	files, err := os.ReadDir(offline)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("offline files = %v, want only the three audit files", files)
	}
	exportPath := filepath.Join(offline, "export.json")
	raw := readAuditFile(t, exportPath)
	admin(1, "export-audit", "--out", offline)
	if !bytes.Equal(raw, readAuditFile(t, exportPath)) {
		t.Fatal("a second export overwrote a populated directory")
	}

	binary := filepath.Join(bin, "openplexer-verify")
	if bin == "" {
		binary = filepath.Join(t.TempDir(), "openplexer-verify")
		build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../openplexer-verify")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build verifier: %v\n%s", err, output)
		}
	}
	verify := func(export string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, "--export", export, "--jwks", "jwks.json", "--checkpoint", "checkpoint.jws")
		cmd.Dir = offline
		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	output, err := verify("export.json")
	if err != nil || !strings.Contains(output, "Completeness is proven through checkpoint count 3.") {
		t.Fatalf("verify original export: %v\n%s", err, output)
	}
	var wire struct {
		Entries    []map[string]json.RawMessage `json:"entries"`
		NextCursor json.RawMessage              `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	actions := []string{"policy.updated", "project.added", "member.added"}
	if len(wire.Entries) != len(actions) {
		t.Fatalf("unexpected audit record count: %s", raw)
	}
	for i, action := range actions {
		if string(wire.Entries[i]["action"]) != string(marshal(t, action)) {
			t.Fatalf("unexpected audit actions: %s", raw)
		}
	}
	id := strings.Trim(string(wire.Entries[0]["id"]), `"`)
	wire.Entries[0]["target"] = marshal(t, "project:tampered")
	if err := os.WriteFile(filepath.Join(offline, "tampered.json"), marshal(t, wire), 0600); err != nil {
		t.Fatal(err)
	}
	output, err = verify("tampered.json")
	var exit *exec.ExitError
	want := fmt.Sprintf("record %s (position %d): rule A3:", id, len(wire.Entries))
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.HasPrefix(output, want) {
		t.Fatalf("verify altered export: %v\n%s\nwant exit 1 and %s", err, output, want)
	}
}

func TestExportAuditPreservesExistingFiles(t *testing.T) {
	admin := commands(t, t.TempDir())
	admin(0, "init")
	out := t.TempDir()
	checkpoint := filepath.Join(out, "checkpoint.jws")
	previous := []byte("kept checkpoint")
	if err := os.WriteFile(checkpoint, previous, 0600); err != nil {
		t.Fatal(err)
	}
	admin(1, "export-audit", "--out", out)
	files, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !bytes.Equal(previous, readAuditFile(t, checkpoint)) {
		t.Fatal("failed export changed the output directory")
	}
}

func readAuditFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
