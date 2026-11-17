package codex

import "strings"

// The paths that an apply_patch call touches are read the way Codex reads the
// patch. A header counts only where the parser of Codex looks for one.

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

// isTrimmed is the white space that Rust trims: the Unicode White_Space
// property. It is the white space of ECMA-262 with U+0085 added.
func isTrimmed(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func trim(s string) string { return strings.TrimFunc(s, isTrimmed) }

// linesOf splits on a line feed, drops one carriage return at the end of each
// line, and has no empty line at the end.
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

// body returns the lines between the markers, also inside a `<<EOF` wrapper,
// and false if the patch has no markers.
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

// chunkLength returns the number of lines that the update chunk at index at
// spans, or -1 if the chunk is not valid. Only the first chunk of a hunk may
// leave out its `@@` line.
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

// updateEnd returns the index after the move line and the chunks of an Update
// File hunk, starting just after its header, or -1 if the hunk is not valid.
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
	lines, ok := body(linesOf(trim(text)))
	if !ok {
		return nil, false
	}
	var targets []string
	seen := map[string]bool{}
	add := func(raw string) bool {
		path := trim(raw)
		if path != "" && !seen[path] {
			seen[path] = true
			targets = append(targets, path)
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
			if !add(header[len(addFile):]) {
				return nil, false
			}
			for i++; i < len(lines) && strings.HasPrefix(lines[i], "+"); i++ {
			}
		case strings.HasPrefix(header, deleteFile):
			if !add(header[len(deleteFile):]) {
				return nil, false
			}
			i++
		case strings.HasPrefix(header, updateFile):
			if !add(header[len(updateFile):]) {
				return nil, false
			}
			next := updateEnd(lines, i+1, add)
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
