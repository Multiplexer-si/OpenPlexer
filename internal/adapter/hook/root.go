package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var agentMarkers = []string{".claude", ".codex", ".mcp.json"}

// Roots places a working directory in a project.
type Roots struct {
	// Home is never a project root, and the search stops below it.
	Home     string
	Recorded []string
}

// Find returns the project root of cwd, spelled as the ancestor of cwd.
//
// The nearest recorded root at or above cwd wins, whatever markers lie below
// it, so a nested repository does not move the root. With no recorded root,
// the one directory that holds .git is the root, or else the one directory
// that holds an agent marker. When more than one directory could
// be the root, Find returns an error, because a root that is too small lets a
// write outside it pass unjudged.
func (r Roots) Find(cwd string) (string, error) {
	var dirs []string
	home := filepath.Clean(r.Home)
	for dir := filepath.Clean(cwd); dir != home && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
	}

	var recorded []os.FileInfo
	for _, root := range r.Recorded {
		if info, err := os.Stat(root); err == nil {
			recorded = append(recorded, info)
		}
	}
	var repos, marked []string
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil {
			for _, root := range recorded {
				if os.SameFile(info, root) {
					return dir, nil
				}
			}
		}
		if holds(dir, ".git") {
			repos = append(repos, dir)
		} else if holds(dir, agentMarkers...) {
			marked = append(marked, dir)
		}
	}

	candidates := marked
	if len(repos) > 0 {
		candidates = repos
		if len(marked) > 0 && len(marked[len(marked)-1]) < len(repos[0]) {
			candidates = append(candidates, marked[len(marked)-1])
		}
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no project root above %s: no directory there is a recorded project, and none holds .git, .claude, .codex, or .mcp.json", cwd)
	case 1:
		return candidates[0], nil
	}
	return "", fmt.Errorf("more than one directory could be the project root of %s: %s. None of them is a recorded project, and a nested repository must not move the root", cwd, strings.Join(candidates, " and "))
}

func holds(dir string, names ...string) bool {
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
