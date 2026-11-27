// Package projects stores the local mapping from checkout roots to OpenPlexer homes.
package projects

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/multiplexer-si/openplexer/internal/jsonedit"
)

// ErrUnregistered means that no registered root contains the path.
var ErrUnregistered = errors.New("no registered checkout contains the path")

type Project struct {
	Root string
	Home string
}

type claim struct {
	Project
	info fs.FileInfo
}

// Registry is a snapshot of the claims, including conflicting claims.
type Registry struct {
	Path   string
	claims []claim
}

// Directory resolves a directory and verifies that the original spelling and
// the resolved spelling identify the same directory.
func Directory(path string) (string, fs.FileInfo, error) {
	if path == "" {
		return "", nil, errors.New("a directory path is required")
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		path = cwd + string(filepath.Separator) + path
	}
	before, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	after, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !after.IsDir() || !os.SameFile(before, after) {
		return "", nil, fmt.Errorf("%s does not identify a stable directory", path)
	}
	return resolved, after, nil
}

// Read loads one private registry snapshot. Readers rely on atomic replacement
// and can proceed while another process updates the registry.
func Read(userHome string) (Registry, error) {
	dir, err := registryDir(userHome)
	if err != nil {
		return Registry{}, err
	}
	return read(dir)
}

func registryDir(userHome string) (string, error) {
	home, _, err := Directory(userHome)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".openplexer"), nil
}

func read(dir string) (Registry, error) {
	r := Registry{Path: filepath.Join(dir, "projects.json")}
	file, err := os.Lstat(r.Path)
	if err != nil {
		return r, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return r, err
	}
	if !info.IsDir() || !ownedWithMode(info, 0o022) {
		return r, errors.New("the project registry directory must be writable only by its owner")
	}
	if !file.Mode().IsRegular() || !ownedWithMode(file, 0o077) {
		return r, errors.New("the project registry must be a private regular file")
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return r, err
	}
	doc, err := jsonedit.Parse(data)
	if err != nil || doc.Root().Kind != jsonedit.Object {
		return r, errors.New("the project registry must be a JSON object mapping checkout roots to homes")
	}
	for _, member := range doc.Root().Members {
		var home string
		root := member.Key
		if doc.Decode(member.Value, &home) != nil || !absoluteClean(root) || !absoluteClean(home) {
			return r, errors.New("the project registry must contain absolute checkout and home paths")
		}
		_, info, _ := Directory(root)
		r.claims = append(r.claims, claim{Project{root, home}, info})
	}
	return r, nil
}

func absoluteClean(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

// Projects returns every recorded path pair, including claims that conflict.
func (r Registry) Projects() []Project {
	projects := make([]Project, 0, len(r.claims))
	for _, c := range r.claims {
		projects = append(projects, c.Project)
	}
	return projects
}

// Lookup selects the deepest registered ancestor of a resolved absolute path.
// If that root has conflicting claims, Lookup returns the conflict.
func (r Registry) Lookup(path string) (Project, error) {
	for dir := path; ; dir = filepath.Dir(dir) {
		var match Project
		info, _ := os.Lstat(dir)
		for _, c := range r.claims {
			if c.Root != dir && (info == nil || !info.IsDir() || c.info == nil || !os.SameFile(info, c.info)) {
				continue
			}
			if match.Root != "" {
				return match, fmt.Errorf("conflicting homes claim the checkout %s. Repair projects.json", dir)
			}
			match = Project{dir, c.Home}
		}
		if match.Root != "" {
			return match, nil
		}
		if dir == filepath.Dir(dir) {
			return Project{}, ErrUnregistered
		}
	}
}

// CheckHome verifies that the registered home still exists as recorded.
func (p Project) CheckHome() error {
	home, _, err := Directory(p.Home)
	if err != nil {
		return fmt.Errorf("cannot use the registered home: %w", err)
	}
	if home != p.Home {
		return fmt.Errorf("the registered home %s no longer names its canonical directory", p.Home)
	}
	return nil
}

// Register atomically adds a claim, or keeps the same existing claim. The
// caller can validate the contents of the canonical home before publication.
func Register(userHome, root, home string, validate func(string) error) error {
	canonicalRoot, rootInfo, err := Directory(root)
	if err != nil {
		return fmt.Errorf("cannot register the checkout: %w", err)
	}
	canonicalHome, homeInfo, err := Directory(home)
	if err != nil {
		return fmt.Errorf("cannot register the OpenPlexer home: %w", err)
	}
	dir, err := registryDir(userHome)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedWithMode(info, 0o022) {
		return errors.New("the project registry directory must be writable only by its owner")
	}
	lock, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockRegistry(lock); err != nil {
		return err
	}
	locked, err := lock.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, locked) || !sameDirectory(dir, locked) {
		return errors.New("the registry directory changed while acquiring its lock")
	}
	r, err := read(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	homes := make(map[string]string, len(r.claims)+1)
	for _, c := range r.claims {
		if _, found := homes[c.Root]; found {
			return fmt.Errorf("conflicting homes claim the checkout %s. Repair projects.json", c.Root)
		}
		homes[c.Root] = c.Home
	}
	claimed := false
	for _, c := range r.claims {
		if c.Root != canonicalRoot && (c.info == nil || !os.SameFile(rootInfo, c.info)) {
			continue
		}
		if claimed {
			return fmt.Errorf("conflicting homes claim the checkout %s. Repair projects.json", canonicalRoot)
		}
		claimed = true
		_, registeredHome, err := Directory(c.Home)
		if err != nil || !os.SameFile(homeInfo, registeredHome) {
			return fmt.Errorf("checkout %s is already registered with another home", canonicalRoot)
		}
	}
	if validate != nil {
		if err := validate(canonicalHome); err != nil {
			return err
		}
	}
	if claimed {
		return nil
	}
	homes[canonicalRoot] = canonicalHome
	data, err := json.MarshalIndent(homes, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".projects-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	closeErr := temp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if !sameDirectory(root, rootInfo) || !sameDirectory(canonicalRoot, rootInfo) ||
		!sameDirectory(home, homeInfo) || !sameDirectory(canonicalHome, homeInfo) ||
		!sameDirectory(dir, info) {
		return errors.New("a directory changed while registering the checkout")
	}
	return os.Rename(temp.Name(), r.Path)
}

func sameDirectory(path string, before fs.FileInfo) bool {
	after, err := os.Stat(path)
	return err == nil && os.SameFile(before, after)
}
