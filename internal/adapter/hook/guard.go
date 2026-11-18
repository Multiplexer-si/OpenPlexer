package hook

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

type Guard struct {
	// Path is absolute, or relative to the root of the project of the working
	// directory and of the project that holds the written file. A write to
	// the path, or to a path under it, is denied.
	Path string
	// Why completes the sentence "OpenPlexer blocked a write to X, because".
	Why string
}

func (e Env) guard(cwdRoot string, targets []target) (Verdict, bool) {
	for _, t := range targets {
		for _, g := range e.Guards {
			guarded := []string{g.Path}
			if !filepath.IsAbs(g.Path) {
				guarded = nil
				for _, root := range []string{cwdRoot, t.root} {
					if root != "" {
						guarded = append(guarded, filepath.Join(root, g.Path))
					}
				}
			}
			for _, path := range guarded {
				if within(t.absolute, path) {
					return Verdict{Deny, fmt.Sprintf("OpenPlexer blocked a write to %s, because %s. An agent may not change it. Change it yourself if you need to.", t.named, g.Why)}, true
				}
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

// The default file systems of macOS and Windows ignore case, so a path that
// differs only in case names the same file there.
func within(path, dir string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		path, dir = strings.ToLower(path), strings.ToLower(dir)
	}
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
