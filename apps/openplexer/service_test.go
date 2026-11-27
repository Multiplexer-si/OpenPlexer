package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/projects"
)

func TestServiceHomeResolution(t *testing.T) {
	user, err := projects.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("PATH", user)
	var manager, missing string
	switch runtime.GOOS {
	case "darwin":
		manager, missing = "launchctl", "Could not find service"
	case "linux":
		manager, missing = "systemctl", "LoadState=not-found"
	default:
		t.Skip("daemon services require macOS or Linux")
	}
	if err := os.WriteFile(filepath.Join(user, manager), []byte("#!/bin/sh\necho '"+missing+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkout, registered := filepath.Join(user, "checkout"), filepath.Join(user, "registered")
	for _, dir := range []string{checkout, registered} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := projects.Register(user, checkout, registered, nil); err != nil {
		t.Fatal(err)
	}
	c := cli{t: t, bin: prebuiltCLI}
	checkHome := func(home string, args ...string) {
		t.Helper()
		if out := c.must(exitOK, args...); !strings.Contains(out, "home: "+home+"\n") {
			t.Fatalf("service status did not select %s", home)
		}
	}
	t.Chdir(checkout)
	env, explicit := filepath.Join(user, "from-env"), filepath.Join(user, "explicit")
	t.Setenv("OPENPLEXER_HOME", env)
	checkHome(explicit, "service", "status", "--home", explicit)
	checkHome(explicit, "--home", explicit, "service", "status")
	checkHome(env, "service", "status")
	t.Setenv("OPENPLEXER_HOME", "")
	checkHome(registered, "service", "status")
	t.Chdir(user)
	checkHome(filepath.Join(user, ".openplexer"), "service", "status")
}

func TestServiceUsage(t *testing.T) {
	c := newCLI(t)
	c.must(exitUsage, "service")
	c.must(exitUsage, "service", "unknown")
	c.must(exitUsage, "service", "status", "--home")
	c.must(exitUsage, "service", "uninstall", "extra")
}
