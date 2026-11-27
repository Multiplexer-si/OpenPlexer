package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/jsonedit"
)

// Handler is the hook handler that OpenPlexer owns in one agent's hook file.
type Handler struct {
	Agent     string // "claude-code" or "codex": the word after `hook` in Command. It is the identity.
	Event     string // "PreToolUse"
	Matcher   string
	Command   string
	Timeout   int
	OnFailure string // "" leaves the field out
}

// Change says what Install or Uninstall did to the file text.
type Change int

const (
	Unchanged Change = iota
	Added
	Repointed
	Removed
)

func (c Change) String() string {
	return [...]string{"Unchanged", "Added", "Repointed", "Removed"}[c]
}

const preToolUse = "PreToolUse"

type handlerJSON struct {
	Type      string `json:"type"`
	Command   string `json:"command"`
	Timeout   int    `json:"timeout"`
	OnFailure string `json:"onFailure,omitempty"`
}

type groupJSON struct {
	Matcher string        `json:"matcher"`
	Hooks   []handlerJSON `json:"hooks"`
}

type eventHooks struct {
	event  string
	groups []groupJSON
}

func (e eventHooks) MarshalJSON() ([]byte, error) {
	key, err := json.Marshal(e.event)
	if err != nil {
		return nil, err
	}
	groups, err := json.Marshal(e.groups)
	if err != nil {
		return nil, err
	}
	return append(append(append([]byte("{"), key...), ':'), append(groups, '}')...), nil
}

func (h Handler) group() groupJSON {
	return groupJSON{Matcher: h.Matcher, Hooks: []handlerJSON{{Type: "command", Command: h.Command, Timeout: h.Timeout, OnFailure: h.OnFailure}}}
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func isBlank(text []byte) bool {
	return len(bytes.TrimSpace(bytes.TrimPrefix(text, utf8BOM))) == 0
}

type ours struct {
	command            jsonedit.Node
	commandText        string
	list               jsonedit.Node
	event              jsonedit.Node
	eventIdx, groupIdx int
	handlerIdx         int
}

type layout struct {
	doc      *jsonedit.Doc
	root     jsonedit.Node
	hooksIdx int
	hooks    jsonedit.Node
	eventIdx int
	ours     []ours
}

func inspect(text []byte, agent, event string) (*layout, error) {
	doc, err := jsonedit.Parse(text)
	if err != nil {
		return nil, err
	}
	l := &layout{doc: doc, root: doc.Root(), hooksIdx: -1, eventIdx: -1}
	if l.root.Kind != jsonedit.Object {
		return nil, l.refuse(l.root.Start, "the top level is %s, not an object", l.root.Kind)
	}
	if m, dup := l.root.DuplicateKey(); dup {
		return nil, l.refuse(m.KeyStart, "duplicate key %q at the top level", m.Key)
	}
	if l.hooksIdx, err = l.root.MemberIndex("hooks"); err != nil || l.hooksIdx < 0 {
		return l, err
	}
	l.hooks = l.root.Members[l.hooksIdx].Value
	if l.hooks.Kind != jsonedit.Object {
		return nil, l.refuse(l.hooks.Start, `"hooks" is %s, not an object`, l.hooks.Kind)
	}
	if m, dup := l.hooks.DuplicateKey(); dup {
		return nil, l.refuse(m.KeyStart, `duplicate key %q in "hooks"`, m.Key)
	}
	for ei, em := range l.hooks.Members {
		if em.Key == event {
			l.eventIdx = ei
		}
		if em.Value.Kind != jsonedit.Array {
			if em.Key == event {
				return nil, l.refuse(em.Value.Start, "hooks.%s is %s, not an array", event, em.Value.Kind)
			}
			continue
		}
		for gi, group := range em.Value.Elems {
			if group.Kind != jsonedit.Object {
				continue
			}
			where := fmt.Sprintf("hooks.%s[%d]", em.Key, gi)
			if m, dup := group.DuplicateKey(); dup {
				return nil, l.refuse(m.KeyStart, "duplicate key %q in %s", m.Key, where)
			}
			list, ok, _ := group.Get("hooks")
			if !ok || list.Kind != jsonedit.Array {
				continue
			}
			for hi, handler := range list.Elems {
				if handler.Kind != jsonedit.Object {
					continue
				}
				if m, dup := handler.DuplicateKey(); dup {
					return nil, l.refuse(m.KeyStart, "duplicate key %q in %s.hooks[%d]", m.Key, where, hi)
				}
				if cmd, text, ok := l.ownedBy(handler, agent); ok {
					l.ours = append(l.ours, ours{
						command: cmd, commandText: text, list: list, event: em.Value,
						eventIdx: ei, groupIdx: gi, handlerIdx: hi,
					})
				}
			}
		}
	}
	return l, nil
}

func (l *layout) refuse(offset int, format string, args ...any) error {
	line, col := l.doc.Position(offset)
	return fmt.Errorf("%s at line %d, column %d", fmt.Sprintf(format, args...), line, col)
}

func (l *layout) ownedBy(handler jsonedit.Node, agent string) (jsonedit.Node, string, bool) {
	node, ok, _ := handler.Get("command")
	var command string
	if !ok || node.Kind != jsonedit.String || l.doc.Decode(node, &command) != nil {
		return node, "", false
	}
	var args []string
	if argsNode, ok, _ := handler.Get("args"); ok && argsNode.Kind == jsonedit.Array {
		for _, a := range argsNode.Elems {
			var s string
			if a.Kind == jsonedit.String && l.doc.Decode(a, &s) == nil {
				args = append(args, s)
			}
		}
	}
	segments := Segments(command)
	if len(segments) == 0 {
		segments = [][]string{nil}
	}
	last := len(segments) - 1
	segments[last] = append(segments[last], args...)
	for _, words := range segments {
		for i := 0; i+2 < len(words); i++ {
			if isOpenplexer(words[i]) && words[i+1] == "hook" && words[i+2] == agent {
				return node, command, true
			}
		}
	}
	return node, "", false
}

func isOpenplexer(word string) bool {
	name := word[strings.LastIndexAny(word, `/\`)+1:]
	return name == "openplexer" || strings.EqualFold(name, "openplexer.exe")
}

// Install returns the text of a hook file that has h in it. It adds h as the
// last group of its event when no handler of ours for h.Agent is there, points
// a canonical handler of ours at h.Command when only the executable differs,
// and otherwise changes nothing. When it adds h, it also says how much it
// added, and Uninstall takes that level back.
func Install(text []byte, exists bool, h Handler) ([]byte, Change, Level, error) {
	if h.Agent == "" || h.Event == "" || h.Command == "" {
		return nil, Unchanged, AddedUnknown, errors.New("handler needs an agent, an event, and a command")
	}
	if !canonical(h.Command, h.Agent) {
		return nil, Unchanged, AddedUnknown, fmt.Errorf("the command %q does not run a binary named openplexer, so uninstall could not find the entry. Rename the binary to openplexer", h.Command)
	}
	if !exists {
		return newFile(h), Added, AddedFile, nil
	}
	if isBlank(text) {
		return append(slices.Clone(text), newFile(h)...), Added, AddedDocument, nil
	}
	l, err := inspect(text, h.Agent, h.Event)
	if err != nil {
		return nil, Unchanged, AddedUnknown, err
	}
	if len(l.ours) > 0 {
		out, change, err := l.repoint(text, h)
		return out, change, AddedUnknown, err
	}
	var edit jsonedit.Edit
	level := AddedGroup
	switch {
	case l.hooksIdx < 0:
		level = AddedHooksObject
		edit, err = l.doc.AppendMember(l.root, "hooks", eventHooks{h.Event, []groupJSON{h.group()}})
	case l.eventIdx < 0:
		level = AddedEventList
		edit, err = l.doc.AppendMember(l.hooks, h.Event, []groupJSON{h.group()})
	default:
		edit, err = l.doc.AppendElem(l.hooks.Members[l.eventIdx].Value, h.group())
	}
	if err != nil {
		return nil, Unchanged, AddedUnknown, err
	}
	out, err := l.doc.Apply(edit)
	return out, Added, level, err
}

func newFile(h Handler) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Hooks eventHooks `json:"hooks"`
	}{eventHooks{h.Event, []groupJSON{h.group()}}})
	return b.Bytes()
}

func canonical(command, agent string) bool {
	segments := Segments(command)
	if len(segments) != 1 {
		return false
	}
	w := segments[0]
	if len(w) != 4 && !(len(w) == 5 && w[0] == "openplexer" && w[4] == "--project-home") {
		return false
	}
	return isOpenplexer(w[0]) && w[1] == "hook" && w[2] == agent && w[3] == "pre-tool"
}

func (l *layout) repoint(text []byte, h Handler) ([]byte, Change, error) {
	var edits []jsonedit.Edit
	for _, o := range l.ours {
		if o.commandText != h.Command && canonical(o.commandText, h.Agent) {
			e, err := l.doc.ReplaceValue(o.command, h.Command)
			if err != nil {
				return nil, Unchanged, err
			}
			edits = append(edits, e)
		}
	}
	if len(edits) == 0 {
		return text, Unchanged, nil
	}
	out, err := l.doc.Apply(edits...)
	return out, Repointed, err
}

// Uninstall removes every handler of ours for agent, and each container that
// this empties and that install added. With AddedUnknown, it cannot tell, so
// it removes each emptied container and keeps the top level. remove tells the
// caller to delete the file.
func Uninstall(text []byte, agent string, added Level) (out []byte, remove bool, change Change, err error) {
	if added == AddedUnknown {
		added = AddedHooksObject
	}
	cur := text
	for {
		if isBlank(cur) {
			return cur, false, change, nil
		}
		l, err := inspect(cur, agent, preToolUse)
		if err != nil {
			return nil, false, Unchanged, err
		}
		if len(l.ours) == 0 {
			return cur, false, change, nil
		}
		edit, wholeDocument, err := l.removal(l.ours[0], added)
		if err != nil {
			return nil, false, Unchanged, err
		}
		if wholeDocument && added == AddedFile {
			return nil, true, Removed, nil
		}
		if cur, err = l.doc.Apply(edit); err != nil {
			return nil, false, Unchanged, err
		}
		if wholeDocument {
			// Install wrote the document and one line break after it.
			cur = bytes.TrimSuffix(cur, []byte("\n"))
		}
		change = Removed
	}
}

// removal returns the edit that takes o out, with the largest container that
// o alone filled and that install added.
func (l *layout) removal(o ours, added Level) (edit jsonedit.Edit, wholeDocument bool, err error) {
	parents := []struct {
		container jsonedit.Node
		child     int
		// An emptied container goes only when install added this much.
		removableFrom Level
	}{
		{o.list, o.handlerIdx, AddedUnknown},
		{o.event, o.groupIdx, AddedEventList},
		{l.hooks, o.eventIdx, AddedHooksObject},
		{l.root, l.hooksIdx, AddedDocument},
	}
	for _, p := range parents {
		if len(p.container.Elems)+len(p.container.Members) > 1 || added < p.removableFrom {
			edit, err = l.doc.RemoveChild(p.container, p.child)
			return edit, false, err
		}
	}
	return jsonedit.Edit{Start: l.root.Start, End: l.root.End}, true, nil
}

type transform func(text []byte, exists bool) (out []byte, remove bool, change Change, err error)

func editFile(path string, edit transform) (Change, error) {
	target, err := resolveLink(path, 0)
	if err != nil {
		return Unchanged, err
	}
	removeStaleTemps(target)
	text, err := os.ReadFile(target)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Unchanged, err
	}
	mode := fs.FileMode(0o644)
	if exists {
		info, err := os.Stat(target)
		if err != nil {
			return Unchanged, err
		}
		mode = info.Mode().Perm()
	}
	out, remove, change, err := edit(text, exists)
	if err != nil {
		return Unchanged, fmt.Errorf("%s: %w", path, err)
	}
	if info, err := os.Lstat(path); remove && err == nil && info.Mode()&fs.ModeSymlink != 0 {
		out, remove = []byte("{}\n"), false
	}
	if change == Unchanged || (exists && !remove && bytes.Equal(out, text)) {
		return Unchanged, nil
	}
	if err := replace(target, text, exists, out, remove, mode); err != nil {
		return Unchanged, fmt.Errorf("%s: %w", path, err)
	}
	return change, nil
}

func resolveLink(path string, depth int) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return path, nil
	}
	if depth > 32 {
		return "", fmt.Errorf("%s: too many levels of symbolic links", path)
	}
	link, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	return resolveLink(link, depth+1)
}

var errChanged = errors.New("the file changed while openplexer edited it; run the command again")

// A run that was killed leaves its temporary file. A later run removes it
// once it is older than any run that could still write it.
const staleAfter = time.Minute

func tempPattern(target string) string {
	return "." + filepath.Base(target) + ".openplexer-*.tmp"
}

func removeStaleTemps(target string) {
	dir := filepath.Dir(target)
	prefix, suffix, _ := strings.Cut(tempPattern(target), "*")
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > staleAfter {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

func replace(target string, parsed []byte, existed bool, out []byte, remove bool, mode fs.FileMode) error {
	unchanged := func() error {
		now, err := os.ReadFile(target)
		switch {
		case existed && (err != nil || !bytes.Equal(now, parsed)):
			return errChanged
		case !existed && err == nil:
			return errChanged
		case !existed && !errors.Is(err, fs.ErrNotExist):
			return err
		}
		return nil
	}
	if remove {
		if err := unchanged(); err != nil {
			return err
		}
		return os.Remove(target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return writeAtomic(target, out, mode, unchanged)
}

// writeAtomic puts out at target through a temporary file and a rename, so a
// reader sees the old file or the new one.
func writeAtomic(target string, out []byte, mode fs.FileMode, beforeRename func() error) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), tempPattern(target))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := beforeRename(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
