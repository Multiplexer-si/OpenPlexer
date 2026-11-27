package hook

import (
	"path/filepath"
	"regexp"
	"strings"
)

type tokenKind int

const (
	wordToken tokenKind = iota
	sepToken
	redirToken
	dupToken
)

type token struct {
	kind  tokenKind
	text  string
	reads bool
}

var dynamic = regexp.MustCompile("[$`()]")

// isSpace is the white space of ECMA-262, the set that the reference splits on.
func isSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func isWordEnd(r rune) bool {
	return isSpace(r) || strings.ContainsRune(";&|<>", r)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func startsWith(s []rune, i int, text string) bool {
	return i+len([]rune(text)) <= len(s) && string(s[i:i+len([]rune(text))]) == text
}

func matchDup(s []rune, i int) int {
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	if j+1 >= len(s) || (s[j] != '<' && s[j] != '>') || s[j+1] != '&' {
		return 0
	}
	j += 2
	switch {
	case j < len(s) && isDigit(s[j]):
		for j < len(s) && isDigit(s[j]) {
			j++
		}
	case j < len(s) && s[j] == '-':
		j++
	default:
		return 0
	}
	if j < len(s) && !isWordEnd(s[j]) {
		return 0
	}
	return j - i
}

var redirOperators = []string{">>", ">&", ">|", "<<<", "<<-", "<<", "<>", ">", "<"}

func matchRedir(s []rune, i int) int {
	for _, op := range []string{"&>>", "&>"} {
		if startsWith(s, i, op) {
			return len(op)
		}
	}
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	for _, op := range redirOperators {
		if startsWith(s, j, op) {
			return j - i + len(op)
		}
	}
	return 0
}

func matchSep(s []rune, i int) int {
	for _, op := range []string{"&&", "||", "|&"} {
		if startsWith(s, i, op) {
			return len(op)
		}
	}
	if strings.ContainsRune(";|&\n", s[i]) {
		return 1
	}
	return 0
}

func readWord(s []rune, i int) (string, int) {
	var text strings.Builder
	for i < len(s) && !isWordEnd(s[i]) {
		switch c := s[i]; {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) && s[j] != c {
				if c == '"' && s[j] == '\\' && j+1 < len(s) {
					j++
				}
				text.WriteRune(s[j])
				j++
			}
			i = j + 1
		case c == '\\' && i+1 < len(s):
			if s[i+1] != '\n' {
				text.WriteRune(s[i+1])
			}
			i += 2
		default:
			text.WriteRune(c)
			i++
		}
	}
	return text.String(), i
}

type heredoc struct {
	delimiter string
	tabs      bool
}

func skipBodies(s []rune, i int, docs []heredoc) int {
	for _, d := range docs {
		for i < len(s) {
			end := i
			for end < len(s) && s[end] != '\n' {
				end++
			}
			line := strings.TrimSuffix(string(s[i:end]), "\r")
			i = end + 1
			if d.tabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == d.delimiter {
				break
			}
		}
	}
	return min(i, len(s))
}

var (
	heredocOperator = regexp.MustCompile(`^[0-9]*<<-?$`)
	readOperator    = regexp.MustCompile(`^[0-9]*<`)
	readWriteOp     = regexp.MustCompile(`^[0-9]*<>`)
)

func tokens(command string) []token {
	s := []rune(command)
	var out []token
	var pending []heredoc
	heredocOp := ""
	for i := 0; i < len(s); {
		if isSpace(s[i]) && s[i] != '\n' {
			i++
		} else if n := matchDup(s, i); n > 0 {
			out = append(out, token{kind: dupToken, text: string(s[i : i+n])})
			i += n
		} else if n := matchRedir(s, i); n > 0 {
			text := string(s[i : i+n])
			out = append(out, token{kind: redirToken, text: text, reads: readOperator.MatchString(text) && !readWriteOp.MatchString(text)})
			heredocOp = ""
			if heredocOperator.MatchString(text) {
				heredocOp = text
			}
			i += n
		} else if n := matchSep(s, i); n > 0 {
			out = append(out, token{kind: sepToken, text: string(s[i : i+n])})
			heredocOp = ""
			i += n
			if s[i-1] == '\n' && len(pending) > 0 {
				i = skipBodies(s, i, pending)
				pending = nil
			}
		} else {
			word, end := readWord(s, i)
			out = append(out, token{kind: wordToken, text: word})
			if heredocOp != "" {
				pending = append(pending, heredoc{delimiter: word, tabs: strings.HasSuffix(heredocOp, "-")})
			}
			heredocOp = ""
			i = end
		}
	}
	return out
}

// Segments splits a shell line into its commands, at && || ; | |& & and line
// feeds outside quotes, and lists the words of each. A redirection operator and
// its one target word are not words of the command, and an fd duplication is
// dropped.
func Segments(command string) [][]string {
	var out [][]string
	var words []string
	skipTarget := false
	flush := func() {
		if len(words) > 0 {
			out = append(out, words)
		}
		words, skipTarget = nil, false
	}
	for _, t := range tokens(command) {
		switch t.kind {
		case sepToken:
			flush()
		case redirToken:
			skipTarget = true
		case wordToken:
			if skipTarget {
				skipTarget = false
			} else {
				words = append(words, t.text)
			}
		}
	}
	flush()
	return out
}

var (
	mutatingVerbs = map[string]bool{"tee": true, "touch": true, "rm": true, "mkdir": true, "rmdir": true, "truncate": true}
	destLastVerbs = map[string]bool{"mv": true, "cp": true, "ln": true, "install": true}
	valueOptions  = map[string]*regexp.Regexp{
		"touch":    regexp.MustCompile(`^(-[dtr]|--date|--reference)$`),
		"truncate": regexp.MustCompile(`^(-[sr]|--size|--reference)$`),
		"mkdir":    regexp.MustCompile(`^(-m|--mode|--context)$`),
		"install":  regexp.MustCompile(`^(-[mogS]|--mode|--owner|--group|--suffix)$`),
		"cp":       regexp.MustCompile(`^(-S|--suffix)$`),
		"mv":       regexp.MustCompile(`^(-S|--suffix)$`),
		"ln":       regexp.MustCompile(`^(-S|--suffix)$`),
	}
	sedInPlace   = regexp.MustCompile(`^(--in-place|-[a-zA-Z]*i)`)
	sedScriptOpt = regexp.MustCompile(`^(-[a-zA-Z]*[ef]|--expression|--file)$`)
	sedScriptEq  = regexp.MustCompile(`^--(expression|file)=`)
	targetDirEq  = regexp.MustCompile(`^--target-directory=(.+)$`)
	targetDirOpt = regexp.MustCompile(`^(-t|--target-directory)$`)
	installDirs  = regexp.MustCompile(`^-[a-zA-Z]*d`)
)

type shellOperands struct {
	paths       []string
	targetDir   *string
	dirsOnly    bool
	inPlace     bool
	hasScript   bool
	noTargetDir bool
}

func parseOperands(verb string, args []string) shellOperands {
	valueOpt := valueOptions[verb]
	var out shellOperands
	ended := false
	for k := 0; k < len(args); k++ {
		a := args[k]
		if !ended && a == "--" {
			ended = true
			continue
		}
		if ended || !strings.HasPrefix(a, "-") || len(a) == 1 {
			out.paths = append(out.paths, a)
			continue
		}
		switch {
		case verb == "sed":
			out.inPlace = out.inPlace || sedInPlace.MatchString(a)
			if sedScriptOpt.MatchString(a) {
				out.hasScript = true
				k++
			} else if sedScriptEq.MatchString(a) {
				out.hasScript = true
			}
		case destLastVerbs[verb]:
			if m := targetDirEq.FindStringSubmatch(a); m != nil {
				out.targetDir = &m[1]
			} else if targetDirOpt.MatchString(a) {
				k++
				out.targetDir = nil
				if k < len(args) {
					out.targetDir = &args[k]
				}
			} else if a == "--no-target-directory" || !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'T') {
				out.noTargetDir = true
			} else if verb == "install" && installDirs.MatchString(a) {
				out.dirsOnly = true
			} else if valueOpt != nil && valueOpt.MatchString(a) {
				k++
			}
		default:
			if valueOpt != nil && valueOpt.MatchString(a) {
				k++
			}
		}
	}
	return out
}

func argTargets(verb string, args []string) []string {
	parsed := parseOperands(verb, args)
	operands := parsed.paths
	var written []string
	switch {
	case verb == "sed" && parsed.inPlace && parsed.hasScript:
		written = operands
	case verb == "sed" && parsed.inPlace && len(operands) > 0:
		written = operands[1:]
	case verb == "sed":
	case destLastVerbs[verb] && parsed.targetDir != nil:
		written = []string{*parsed.targetDir}
	case destLastVerbs[verb] && parsed.dirsOnly:
		written = operands
	case destLastVerbs[verb] && len(operands) >= 2:
		written = []string{operands[len(operands)-1]}
	case destLastVerbs[verb]:
	default:
		written = operands
	}
	var kept []string
	for _, path := range written {
		if path != "" && !dynamic.MatchString(path) {
			kept = append(kept, path)
		}
	}
	return kept
}

// WriteTargets lists the paths that a shell command is clear to write, in the
// order that the command names them, with no repeat. They are the targets of
// redirections, the arguments of the commands that create or remove files, the
// destination of mv, cp, ln, and install, and the file operands of sed -i.
func WriteTargets(command string) []string {
	var targets []string
	seen := map[string]bool{}
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			targets = append(targets, path)
		}
	}
	walkShellWrites(command, add, func(verb string, args []string) {
		if mutatingVerbs[verb] || destLastVerbs[verb] || verb == "sed" {
			for _, path := range argTargets(verb, args) {
				add(path)
			}
		}
	})
	return targets
}

func walkShellWrites(command string, redirectTarget func(string), operands func(string, []string)) {
	verb, hasVerb := "", false
	var args []string
	var redirect *token
	flush := func() {
		if hasVerb {
			operands(verb, args)
		}
		verb, hasVerb, args, redirect = "", false, nil, nil
	}
	for _, t := range tokens(command) {
		switch {
		case t.kind == sepToken:
			flush()
		case t.kind == redirToken:
			redirect = &token{kind: t.kind, text: t.text, reads: t.reads}
		case t.kind == wordToken && redirect != nil:
			if !redirect.reads && !dynamic.MatchString(t.text) {
				redirectTarget(t.text)
			}
			redirect = nil
		case t.kind == wordToken && !hasVerb:
			verb, hasVerb = t.text[strings.LastIndexAny(t.text, `\/`)+1:], true
		case t.kind == wordToken:
			args = append(args, t.text)
		}
	}
	flush()
}

var gitValueOptions = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--config-env": true, "--attr-source": true}

func MovedPaths(command string) []string {
	var moved []string
	for _, words := range Segments(command) {
		verb, args := words[0][strings.LastIndexAny(words[0], `\/`)+1:], words[1:]
		dir := ""
		if verb == "git" {
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				if gitValueOptions[args[0]] && len(args) > 1 {
					if args[0] == "-C" {
						dir = under(dir, args[1])
					}
					args = args[1:]
				}
				args = args[1:]
			}
			if len(args) == 0 || args[0] != "mv" {
				continue
			}
			args = args[1:]
		} else if verb != "mv" {
			continue
		}
		ended := false
		for k := 0; k < len(args); k++ {
			a := args[k]
			switch {
			case !ended && a == "--":
				ended = true
			case ended || !strings.HasPrefix(a, "-") || len(a) == 1:
				if a == "" || dynamic.MatchString(a) {
					continue
				}
				if a = under(dir, a); !dynamic.MatchString(a) {
					moved = append(moved, a)
				}
			case verb == "mv" && (targetDirOpt.MatchString(a) || valueOptions["mv"].MatchString(a)):
				k++
			}
		}
	}
	return moved
}

func under(dir, path string) string {
	if dir == "" || filepath.IsAbs(path) || path == "~" || strings.HasPrefix(path, "~/") {
		return path
	}
	return filepath.Join(dir, path)
}

var substitution = regexp.MustCompile("\\$\\(|`|[<>]\\(")

func bareTee(words []string) bool {
	if words[0] != "tee" {
		return false
	}
	for _, w := range words {
		if dynamic.MatchString(w) {
			return false
		}
	}
	return true
}
