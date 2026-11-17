package hook

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	driveAbsolute  = regexp.MustCompile(`^[A-Za-z]:/`)
	driveLetter    = regexp.MustCompile(`^[A-Za-z]:`)
	leadingDotDirs = regexp.MustCompile(`^(\./)+`)
)

func normalizePath(path, cwd string) string {
	p := strings.ReplaceAll(path, `\`, "/")
	base := strings.TrimRight(strings.ReplaceAll(cwd, `\`, "/"), "/")
	if !strings.HasPrefix(p, "/") && !driveAbsolute.MatchString(p) {
		return leadingDotDirs.ReplaceAllString(p, "")
	}
	prefix := base + "/"
	inside := false
	if driveLetter.MatchString(base) {
		inside = len(p) >= len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
	} else {
		inside = strings.HasPrefix(p, prefix)
	}
	if inside {
		return p[len(prefix):]
	}
	return p
}

func normalizeTarget(path, cwd, root string) string {
	if root == "" {
		return normalizePath(path, cwd)
	}
	abs := filepath.Clean(path)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, path)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, `..\`) || filepath.IsAbs(rel) {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

func inProject(path string) bool {
	return !(strings.HasPrefix(path, "/") || driveAbsolute.MatchString(path) || path == ".." || strings.HasPrefix(path, "../"))
}
