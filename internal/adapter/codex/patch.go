package codex

import (
	"strings"

	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
)

const (
	beginMarker = "*** Begin Patch"
	endMarker   = "*** End Patch"
	environment = "*** Environment ID: "
	addFile     = "*** Add File: "
	deleteFile  = "*** Delete File: "
	updateFile  = "*** Update File: "
	moveTo      = "*** Move to: "
	endOfFile   = "*** End of File"
)

func isTrimmed(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trim(s string) string { return strings.TrimFunc(s, isTrimmed) }

func linesOf(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func framed(lines []string) bool {
	return len(lines) >= 2 && trim(lines[0]) == beginMarker && trim(lines[len(lines)-1]) == endMarker
}

func body(lines []string) ([]string, bool) {
	if framed(lines) {
		return lines[1 : len(lines)-1], true
	}
	if len(lines) < 4 {
		return nil, false
	}
	inner := lines[1 : len(lines)-1]
	switch lines[0] {
	case "<<EOF", "<<'EOF'", `<<"EOF"`:
		if strings.HasSuffix(lines[len(lines)-1], "EOF") && framed(inner) {
			return inner[1 : len(inner)-1], true
		}
	}
	return nil, false
}

func chunkLength(lines []string, at int, first bool) int {
	head := lines[at]
	start := -1
	switch {
	case head == "@@" || strings.HasPrefix(head, "@@ "):
		start = at + 1
	case first:
		start = at
	}
	if start < 0 || start >= len(lines) {
		return -1
	}
	n := 0
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if line == endOfFile {
			if n == 0 {
				return -1
			}
			return start - at + n + 1
		}
		if line == "" || strings.ContainsRune(" +-", rune(line[0])) {
			n++
			continue
		}
		if n == 0 {
			return -1
		}
		break
	}
	return start - at + n
}

func updateEnd(lines []string, at int, add func(string) bool) int {
	i := at
	if i < len(lines) && strings.HasPrefix(lines[i], moveTo) {
		if !add(lines[i][len(moveTo):]) {
			return -1
		}
		i++
	}
	chunks := 0
	for i < len(lines) {
		if trim(lines[i]) == "" {
			i++
			continue
		}
		if strings.HasPrefix(lines[i], "***") {
			break
		}
		n := chunkLength(lines, i, chunks == 0)
		if n < 0 {
			return -1
		}
		i += n
		chunks++
	}
	if chunks == 0 {
		return -1
	}
	return i
}

// ParsePatch lists every path that the patch adds, updates, deletes, or moves
// to, in patch order, with no repeat. It returns false if Codex would not
// accept the text as a patch, or if a path is empty.
func ParsePatch(text string) ([]string, bool) {
	writes, ok := patchTargets(text)
	if !ok {
		return nil, false
	}
	var paths []string
	seen := map[string]bool{}
	for _, write := range writes {
		if !seen[write.Path] {
			seen[write.Path] = true
			paths = append(paths, write.Path)
		}
	}
	return paths, true
}

func patchTargets(text string) ([]hook.WriteTarget, bool) {
	lines, ok := body(linesOf(trim(text)))
	if !ok {
		return nil, false
	}
	var targets []hook.WriteTarget
	add := func(raw string, kind hook.WriteKind) bool {
		path := trim(raw)
		if path != "" {
			targets = append(targets, hook.WriteTarget{Path: path, Kind: kind})
		}
		return path != ""
	}
	i := 0
	if len(lines) > 0 && strings.HasPrefix(trim(lines[0]), environment) {
		i = 1
	}
	if i >= len(lines) {
		return nil, false
	}
	for i < len(lines) {
		header := trim(lines[i])
		switch {
		case strings.HasPrefix(header, addFile):
			if !add(header[len(addFile):], hook.WriteThrough) {
				return nil, false
			}
			for i++; i < len(lines) && strings.HasPrefix(lines[i], "+"); i++ {
			}
		case strings.HasPrefix(header, deleteFile):
			if !add(header[len(deleteFile):], hook.WriteEntry) {
				return nil, false
			}
			i++
		case strings.HasPrefix(header, updateFile):
			kind := hook.WriteThrough
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], moveTo) {
				kind = hook.WriteEntry
			}
			if !add(header[len(updateFile):], kind) {
				return nil, false
			}
			next := updateEnd(lines, i+1, func(path string) bool { return add(path, hook.WriteEntry) })
			if next < 0 {
				return nil, false
			}
			i = next
		default:
			return nil, false
		}
	}
	return targets, true
}
