package hook

import (
	"os"
	"path/filepath"
)

// rootMarkers are tried in order. A .git wins over the others, so that an
// agent that creates a .claude in a subdirectory does not move the root.
var rootMarkers = [][]string{{".git"}, {".claude", ".codex", ".mcp.json"}}

// FindRoot returns the nearest directory at or above cwd that holds a .git,
// or else the nearest that holds .claude, .codex, or .mcp.json. The home
// directory and the file system root are never a project root, and the
// search stops at either, as the reference does.
func FindRoot(cwd, home string) (string, bool) {
	home = filepath.Clean(home)
	for _, markers := range rootMarkers {
		for dir := filepath.Clean(cwd); dir != home; dir = filepath.Dir(dir) {
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			for _, marker := range markers {
				if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
					return dir, true
				}
			}
		}
	}
	return "", false
}
