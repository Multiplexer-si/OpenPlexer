package hook

import (
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

func inProject(path string) bool {
	return !(strings.HasPrefix(path, "/") || driveAbsolute.MatchString(path) || path == ".." || strings.HasPrefix(path, "../"))
}
