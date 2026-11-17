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

// normalizePath reads path as a path under cwd, with no look at the file
// system. A relative path loses its leading `./`. An absolute path inside cwd
// becomes the path under it, and any other absolute path stays as it is. A
// Windows path is compared without case. The result uses forward slashes.
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

// normalizeTarget reads path as a path in the project. With a project root it
// is the path under the root, and a path that leaves the root stays absolute.
// With no root it is normalizePath.
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

// inProject reports a path that is relative and stays inside the project. The
// policy governs the files of the project, and not the files under the home
// directory of the user or under /tmp.
func inProject(path string) bool {
	return !(strings.HasPrefix(path, "/") || driveAbsolute.MatchString(path) || path == ".." || strings.HasPrefix(path, "../"))
}
