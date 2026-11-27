//go:build unix

package projects

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRegisterCanonicalizesPathsAndRejectsAnotherHome(t *testing.T) {
	user, root, home := t.TempDir(), t.TempDir(), t.TempDir()
	alias := filepath.Join(user, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	homeAlias := filepath.Join(user, "home")
	if err := os.Symlink(home, homeAlias); err != nil {
		t.Fatal(err)
	}
	if err := Register(user, alias, homeAlias, nil); err != nil {
		t.Fatal(err)
	}
	if err := Register(user, root, home, nil); err != nil {
		t.Fatalf("registering the same directory pair: %v", err)
	}
	if err := Register(user, alias, t.TempDir(), nil); err == nil {
		t.Fatal("another home was allowed to claim the same directory")
	}
	registry, err := Read(user)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _, err := Directory(root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalHome, _, err := Directory(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Projects(); len(got) != 1 || got[0] != (Project{canonicalRoot, canonicalHome}) {
		t.Fatalf("paths were not stored canonically: %v", got)
	}
	info, err := os.Stat(registry.Path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("registry mode = %o, want 0600", mode)
	}
	if err := os.Chmod(registry.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(user); err == nil {
		t.Fatal("read an exposed registry")
	}
}

func TestRegisterChecksDirectoryIdentityBeforePublishing(t *testing.T) {
	user, root, home := t.TempDir(), t.TempDir(), t.TempDir()
	err := Register(user, root, home, func(path string) error {
		if err := os.Rename(path, path+"-old"); err != nil {
			return err
		}
		t.Cleanup(func() { os.RemoveAll(path + "-old") })
		return os.Mkdir(path, 0o700)
	})
	if err == nil || !strings.Contains(err.Error(), "directory changed") {
		t.Fatalf("register with replaced home: %v", err)
	}
	if _, err := Read(user); !os.IsNotExist(err) {
		t.Fatalf("registry after failed registration: %v", err)
	}
}

func TestConcurrentRegistrationKeepsEveryClaim(t *testing.T) {
	user, home := t.TempDir(), t.TempDir()
	var writes sync.WaitGroup
	for range 12 {
		root := t.TempDir()
		writes.Go(func() {
			if err := Register(user, root, home, nil); err != nil {
				t.Error(err)
			}
		})
	}
	writes.Wait()
	registry, err := Read(user)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(registry.Projects()); got != 12 {
		t.Fatalf("registry holds %d claims, want 12", got)
	}
}

func TestResolveHomeWithNoRegistryKeepsTheLegacyHome(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("OPENPLEXER_HOME", "")
	legacy := filepath.Join(user, ".openplexer")
	if err := os.Symlink(t.TempDir(), legacy); err != nil {
		t.Fatal(err)
	}
	if home, err := ResolveHome(""); err != nil || home != legacy {
		t.Fatalf("home = %q, %v, want %q", home, err, legacy)
	}
}
