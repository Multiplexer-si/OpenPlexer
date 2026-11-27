//go:build unix

package projects

import (
	"fmt"
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

func TestRegistryUsesOneSpellingOnCaseInsensitiveVolumes(t *testing.T) {
	user := t.TempDir()
	root, home := filepath.Join(user, "Checkout"), filepath.Join(user, "PolicyHome")
	rootAlias, homeAlias := filepath.Join(user, "checkout"), filepath.Join(user, "policyhome")
	for _, dir := range []string{root, home} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(rootAlias); os.IsNotExist(err) {
		t.Skip("the test volume is case-sensitive")
	} else if err != nil {
		t.Fatal(err)
	}
	if err := Register(user, rootAlias, homeAlias, nil); err != nil {
		t.Fatal(err)
	}
	if err := Register(user, root, home, nil); err != nil {
		t.Fatal(err)
	}
	if err := Register(user, root, user, nil); err == nil {
		t.Fatal("another home was allowed to claim the same checkout by case")
	}
	registry, err := Read(user)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(filepath.Dir(registry.Path))
	want := Project{filepath.Join(base, "Checkout"), filepath.Join(base, "PolicyHome")}
	if path, err := CanonicalPath(rootAlias); path != want.Root || err != nil {
		t.Fatalf("canonical alias = %q, %v, want %q", path, err, want.Root)
	}
	if got := registry.Projects(); len(got) != 1 || got[0] != want {
		t.Fatalf("claims = %v, want [%v]", got, want)
	}
	project, err := registry.Lookup(filepath.Join(rootAlias, "future", "file"))
	if project.Home != want.Home || err != nil {
		t.Fatalf("lookup by alias = %v, %v", project, err)
	}
	oldRoot, oldHome := filepath.Join(base, "checkout"), filepath.Join(base, "policyhome")
	if err := os.WriteFile(registry.Path, []byte(fmt.Sprintf("{%q:%q}", oldRoot, oldHome)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err = Read(user)
	if err != nil {
		t.Fatal(err)
	}
	project, err = registry.Lookup(want.Root)
	if err != nil || project.Home != want.Home || project.CheckHome() != nil {
		t.Fatalf("lookup with older registry spelling = %v, %v", project, err)
	}
	if err := os.WriteFile(registry.Path, []byte(fmt.Sprintf("{%q:%q,%q:%q}", want.Root, want.Home, oldRoot, oldHome)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err = Read(user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(want.Root); err == nil || !strings.Contains(err.Error(), "conflicting homes") {
		t.Fatalf("lookup with hand-edited alias claim: %v", err)
	}
}

func TestReadRefusesSymlinkRegistry(t *testing.T) {
	user := t.TempDir()
	target := filepath.Join(user, "registry.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(user, ".openplexer")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "projects.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(user); err == nil {
		t.Fatal("read a symlink registry")
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
