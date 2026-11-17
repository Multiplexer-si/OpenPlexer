package hook

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Guard is a file or directory that no agent may write through a gated tool.
type Guard struct {
	// Path is absolute, or relative to the project root. A write to the path,
	// or to a path under it, is denied.
	Path string
	// Why completes the sentence "OpenPlexer blocked a write to X, because".
	Why string
}

// guard denies the first write that a guard covers. It reads each write as
// the shell would place it, so a relative path is taken from cwd, a leading ~
// is the home directory, and a path that climbs out of the project counts.
func (e Env) guard(cwd, root string, writes []string) (Verdict, bool) {
	for _, write := range writes {
		target := e.absolute(write, cwd)
		for _, g := range e.Guards {
			path := g.Path
			if !filepath.IsAbs(path) {
				if root == "" {
					continue
				}
				path = filepath.Join(root, path)
			}
			if within(target, path) {
				return Verdict{Deny, fmt.Sprintf("OpenPlexer blocked a write to %s, because %s. An agent may not change it. Change it yourself if you need to.", write, g.Why)}, true
			}
		}
	}
	return Verdict{}, false
}

func (e Env) absolute(path, cwd string) string {
	if e.Home != "" && (path == "~" || strings.HasPrefix(path, "~/")) {
		path = filepath.Join(e.Home, path[1:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// within reports whether path is dir or under it. The default file systems
// of macOS and Windows ignore case, so a path that differs only in case
// names the same file there.
func within(path, dir string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		path, dir = strings.ToLower(path), strings.ToLower(dir)
	}
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
