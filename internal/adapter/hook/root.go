package hook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var agentMarkers = []string{".claude", ".codex", ".mcp.json"}

// ErrNoRoot says that no project holds a path.
var ErrNoRoot = errors.New("no project root")

// Roots places a path in a project.
type Roots struct {
	// Home is never a project root, and the search stops below it.
	Home     string
	Recorded []string
}

// Find returns the project root of cwd, spelled as the ancestor of cwd.
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
	outermostRepo := -1
	var marked []int
	for i, dir := range dirs {
		if info, err := os.Stat(dir); err == nil {
			for _, root := range recorded {
				if os.SameFile(info, root) {
					return dir, nil
				}
			}
		}
		if holds(dir, ".git") {
			outermostRepo = i
		} else if holds(dir, agentMarkers...) {
			marked = append(marked, i)
		}
	}

	noRepo := outermostRepo < 0
	markerAboveRepo := !noRepo && len(marked) > 0 && marked[len(marked)-1] > outermostRepo
	var candidates []string
	switch {
	case noRepo && len(marked) == 0:
		return "", fmt.Errorf("%w above %s: no directory there is a recorded project, and none holds .git, .claude, .codex, or .mcp.json", ErrNoRoot, cwd)
	case noRepo && len(marked) == 1:
		return dirs[marked[0]], nil
	case noRepo:
		for _, i := range marked {
			candidates = append(candidates, dirs[i])
		}
	case markerAboveRepo:
		candidates = []string{dirs[outermostRepo], dirs[marked[len(marked)-1]]}
	}
	if candidates != nil {
		return "", fmt.Errorf("more than one directory could be the project root of %s: %s. None of them is a recorded project, and no repository holds them all", cwd, strings.Join(candidates, " and "))
	}

	root := dirs[outermostRepo]
	for i := outermostRepo - 1; i >= 0; i-- {
		if registersWorktree(root, dirs[i]) {
			root = dirs[i]
		}
	}
	return root, nil
}

func holds(dir string, names ...string) bool {
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func registersWorktree(root, dir string) bool {
	admin, commonDir := gitDirs(dir)
	_, rootCommon := gitDirs(root)
	if admin == "" || rootCommon == "" {
		return false
	}
	back, ok := pathIn(filepath.Join(admin, "gitdir"), "", admin)
	return ok && sameFile(commonDir, rootCommon) &&
		sameFile(filepath.Dir(admin), filepath.Join(rootCommon, "worktrees")) &&
		sameFile(back, filepath.Join(dir, ".git"))
}

func gitDirs(dir string) (gitDir, commonDir string) {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", ""
	}
	if info.IsDir() {
		return dotGit, dotGit
	}
	if !info.Mode().IsRegular() {
		return "", ""
	}
	gitDir, ok := pathIn(dotGit, "gitdir: ", dir)
	if !ok {
		return "", ""
	}
	if commonDir, ok := pathIn(filepath.Join(gitDir, "commondir"), "", gitDir); ok {
		return gitDir, commonDir
	}
	return gitDir, gitDir
}

func pathIn(file, prefix, base string) (string, bool) {
	f, err := os.Open(file)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<16))
	if err != nil {
		return "", false
	}
	path, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), prefix)
	if !ok || path == "" {
		return "", false
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return path, true
}

func sameFile(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}
