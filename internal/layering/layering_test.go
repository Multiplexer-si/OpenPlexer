package layering

import (
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Patterns are package paths relative to the module root. A trailing "/..."
// matches the package and everything under it.
type rule struct {
	subjects []string
	deny     []string
	except   []string
}

var rules = []rule{
	{
		subjects: []string{"internal/policy", "internal/room", "internal/audit"},
		deny:     []string{"internal/adapter/...", "internal/daemon", "internal/hub"},
	},
	{
		subjects: []string{"internal/adapter/..."},
		deny:     []string{"internal/daemon", "internal/hub"},
	},
	{
		subjects: []string{"internal/daemon"},
		deny:     []string{"internal/hub"},
	},
	{
		subjects: []string{"internal/hub"},
		deny:     []string{"internal/daemon", "internal/adapter/..."},
	},
	{
		subjects: []string{"apps/openplexer"},
		deny:     []string{"internal/hub"},
	},
	{
		subjects: []string{"apps/openplexer-hub"},
		deny:     []string{"internal/daemon", "internal/adapter/..."},
	},
	{
		// Allowlist: every internal package is denied except the ones listed.
		subjects: []string{"apps/openplexer-verify"},
		deny:     []string{"internal/..."},
		except:   []string{"internal/audit"},
	},
}

const moduleRoot = "../.."

func modulePath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(path)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

// directImports maps each package in the module to the module packages it
// imports. It reads the source in this process, not through "go list", so that
// the test cache sees every file the result depends on. A subprocess is
// invisible to the cache, which then reports a stale pass after a violation.
//
// The second result maps each package to its imports from other modules. Test
// files are not read, so both results describe the code that a binary can
// contain.
func directImports(t *testing.T) (graph, external map[string][]string) {
	t.Helper()
	module := modulePath(t)
	graph = map[string][]string{}
	external = map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != moduleRoot && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dir, err := filepath.Rel(moduleRoot, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(dir)
		graph[pkg] = graph[pkg]
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if dep, ok := strings.CutPrefix(imported, module+"/"); ok {
				graph[pkg] = append(graph[pkg], dep)
			} else if first, _, _ := strings.Cut(imported, "/"); strings.Contains(first, ".") {
				// Only an import path outside the standard library has a dot in its first element.
				external[pkg] = append(external[pkg], imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return graph, external
}

func transitiveDeps(graph map[string][]string, pkg string) []string {
	seen := map[string]bool{}
	var visit func(string)
	visit = func(p string) {
		for _, dep := range graph[p] {
			if !seen[dep] {
				seen[dep] = true
				visit(dep)
			}
		}
	}
	visit(pkg)
	return slices.Sorted(maps.Keys(seen))
}

func matches(pattern, pkg string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/..."); ok {
		return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
	}
	return pkg == pattern
}

func matchesAny(patterns []string, pkg string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool { return matches(pattern, pkg) })
}

func TestLayering(t *testing.T) {
	graph, _ := directImports(t)
	packages := slices.Sorted(maps.Keys(graph))
	for _, r := range rules {
		for _, pattern := range r.subjects {
			matched := false
			for _, pkg := range packages {
				if !matches(pattern, pkg) {
					continue
				}
				matched = true
				for _, dep := range transitiveDeps(graph, pkg) {
					if matchesAny(r.deny, dep) && !matchesAny(r.except, dep) {
						t.Errorf("%s must not depend on %s", pkg, dep)
					}
				}
			}
			if !matched {
				t.Errorf("subject pattern %q matches no packages", pattern)
			}
		}
	}
}

// The module depends on other modules for its tests only. Code that a binary
// can contain imports the standard library and this module, and nothing else.
func TestBinariesUseOnlyTheStandardLibrary(t *testing.T) {
	_, external := directImports(t)
	for _, pkg := range slices.Sorted(maps.Keys(external)) {
		for _, dep := range external[pkg] {
			t.Errorf("%s must not import %s: only a test may import another module", pkg, dep)
		}
	}
}
