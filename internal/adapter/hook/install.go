package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

// eventHooks is the `hooks` object that Install creates, with one event.
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

// ours is one handler of ours and where it sits.
type ours struct {
	command            jsonedit.Node
	commandText        string
	list               jsonedit.Node // the group's hooks array
	event              jsonedit.Node // the event's array of groups
	eventIdx, groupIdx int
	handlerIdx         int
}

// layout is what Install and Uninstall learn about a hook file.
type layout struct {
	doc      *jsonedit.Doc
	root     jsonedit.Node
	hooksIdx int // member of root, -1 when absent
	hooks    jsonedit.Node
	eventIdx int // member of hooks for the install event, -1 when absent
	ours     []ours
}

func inspect(text []byte, agent, event string) (*layout, error) {
	doc, err := jsonedit.Parse(text)
	if err != nil {
		return nil, err
	}
	l := &layout{doc: doc, root: doc.Root(), hooksIdx: -1, eventIdx: -1}
	if l.root.Kind != jsonedit.Object {
		return nil, fmt.Errorf("the top level is %s, not an object", l.root.Kind)
	}
	if key, dup := l.root.DuplicateKey(); dup {
		return nil, fmt.Errorf("duplicate key %q at the top level", key)
	}
	if l.hooksIdx, err = l.root.MemberIndex("hooks"); err != nil || l.hooksIdx < 0 {
		return l, err
	}
	l.hooks = l.root.Members[l.hooksIdx].Value
	if l.hooks.Kind != jsonedit.Object {
		return nil, fmt.Errorf(`"hooks" is %s, not an object`, l.hooks.Kind)
	}
	if key, dup := l.hooks.DuplicateKey(); dup {
		return nil, fmt.Errorf(`duplicate key %q in "hooks"`, key)
	}
	for ei, em := range l.hooks.Members {
		if em.Key == event {
			l.eventIdx = ei
		}
		if em.Value.Kind != jsonedit.Array {
			if em.Key == event {
				return nil, fmt.Errorf("hooks.%s is %s, not an array", event, em.Value.Kind)
			}
			continue
		}
		for gi, group := range em.Value.Elems {
			if group.Kind != jsonedit.Object {
				continue
			}
			where := fmt.Sprintf("hooks.%s[%d]", em.Key, gi)
			if key, dup := group.DuplicateKey(); dup {
				return nil, fmt.Errorf("duplicate key %q in %s", key, where)
			}
			list, ok, _ := group.Get("hooks")
			if !ok || list.Kind != jsonedit.Array {
				continue
			}
			for hi, handler := range list.Elems {
				if handler.Kind != jsonedit.Object {
					continue
				}
				if key, dup := handler.DuplicateKey(); dup {
					return nil, fmt.Errorf("duplicate key %q in %s.hooks[%d]", key, where, hi)
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

// ownedBy reports whether handler runs `openplexer hook <agent>`. It returns
// the command value and its decoded text.
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
	base := word[strings.LastIndexAny(word, `/\`)+1:]
	return base == "openplexer" || base == "openplexer.exe"
}

// Install returns the text of a hook file that has h in it. It adds h as the
// last group of its event when no handler of ours for h.Agent is there, points
// a canonical handler of ours at h.Command when only the executable differs,
// and otherwise changes nothing.
func Install(text []byte, exists bool, h Handler) ([]byte, Change, error) {
	if h.Agent == "" || h.Event == "" || h.Command == "" {
		return nil, Unchanged, errors.New("handler needs an agent, an event, and a command")
	}
	if !exists || isBlank(text) {
		return newFile(h), Added, nil
	}
	l, err := inspect(text, h.Agent, h.Event)
	if err != nil {
		return nil, Unchanged, err
	}
	if len(l.ours) > 0 {
		return l.repoint(text, h)
	}
	var edit jsonedit.Edit
	switch {
	case l.hooksIdx < 0:
		edit, err = l.doc.AppendMember(l.root, "hooks", eventHooks{h.Event, []groupJSON{h.group()}})
	case l.eventIdx < 0:
		edit, err = l.doc.AppendMember(l.hooks, h.Event, []groupJSON{h.group()})
	default:
		edit, err = l.doc.AppendElem(l.hooks.Members[l.eventIdx].Value, h.group())
	}
	if err != nil {
		return nil, Unchanged, err
	}
	out, err := l.doc.Apply(edit)
	return out, Added, err
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

// canonical reports whether command is exactly `<openplexer> hook <agent> pre-tool`.
func canonical(command, agent string) bool {
	segments := Segments(command)
	if len(segments) != 1 || len(segments[0]) != 4 {
		return false
	}
	w := segments[0]
	return isOpenplexer(w[0]) && w[1] == "hook" && w[2] == agent && w[3] == "pre-tool"
}

func (l *layout) repoint(text []byte, h Handler) ([]byte, Change, error) {
	if !canonical(h.Command, h.Agent) {
		return text, Unchanged, nil
	}
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

// Uninstall removes every handler of ours for agent, and each group, event
// list, and hooks object that this empties. When that empties the whole file,
// remove is true and the caller deletes it.
func Uninstall(text []byte, agent string) (out []byte, remove bool, change Change, err error) {
	cur := text
	for {
		l, err := inspect(cur, agent, preToolUse)
		if err != nil {
			return nil, false, Unchanged, err
		}
		if len(l.ours) == 0 {
			return cur, false, change, nil
		}
		edit, whole, err := l.removal(l.ours[0])
		if err != nil {
			return nil, false, Unchanged, err
		}
		if whole {
			return nil, true, Removed, nil
		}
		if cur, err = l.doc.Apply(edit); err != nil {
			return nil, false, Unchanged, err
		}
		change = Removed
	}
}

// removal removes the handler, and climbs while that leaves its container
// empty: the group, then the event, then the hooks object, then the file.
func (l *layout) removal(o ours) (edit jsonedit.Edit, whole bool, err error) {
	levels := []struct {
		container jsonedit.Node
		child     int
	}{
		{o.list, o.handlerIdx},
		{o.event, o.groupIdx},
		{l.hooks, o.eventIdx},
		{l.root, l.hooksIdx},
	}
	for _, lv := range levels {
		if len(lv.container.Elems)+len(lv.container.Members) > 1 {
			edit, err = l.doc.RemoveChild(lv.container, lv.child)
			return edit, false, err
		}
	}
	return jsonedit.Edit{}, true, nil
}

// InstallFile installs h into the file at path, creating it when missing.
func InstallFile(path string, h Handler) (Change, error) {
	return editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
		out, change, err := Install(text, exists, h)
		return out, false, change, err
	})
}

// UninstallFile removes the handlers of ours for agent from the file at path.
func UninstallFile(path string, agent string) (Change, error) {
	return editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
		if !exists {
			return nil, false, Unchanged, nil
		}
		return Uninstall(text, agent)
	})
}

type transform func(text []byte, exists bool) (out []byte, remove bool, change Change, err error)

func editFile(path string, edit transform) (Change, error) {
	target, err := resolveLink(path, 0)
	if err != nil {
		return Unchanged, err
	}
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
		// Deleting the target would leave the link dangling, for example in
		// a dotfiles repository.
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

// resolveLink follows symlinks, including a dangling one, so that an edit
// lands on the file that the link names and the link stays.
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
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".openplexer-*.tmp")
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
	if err := unchanged(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
