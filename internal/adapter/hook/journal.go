package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Journal is the directory that holds what install wrote, with one record
// file for each agent and hook file.
type Journal struct {
	Dir string
}

// Level is the outermost thing that install put into a hook file. Uninstall
// removes a container that it emptied only when install added it.
type Level int

const (
	// AddedUnknown is the level of a hook that has no record.
	AddedUnknown Level = iota
	AddedGroup
	AddedEventList
	AddedHooksObject
	// AddedDocument is the level of a file that existed and held no JSON value.
	AddedDocument
	AddedFile
)

var addedNames = [...]string{"unknown", "group", "event-list", "hooks-object", "document", "file"}

func (a Level) MarshalText() ([]byte, error) {
	return []byte(addedNames[a]), nil
}

func (a *Level) UnmarshalText(text []byte) error {
	for i, name := range addedNames {
		if name == string(text) {
			*a = Level(i)
			return nil
		}
	}
	return fmt.Errorf("unknown level %q", text)
}

type record struct {
	Agent        string `json:"agent"`
	File         string `json:"file"`
	Root         string `json:"root,omitempty"`
	Added        Level  `json:"added"`
	SessionAdded Level  `json:"session_added,omitempty"`
}

func (j Journal) recordPath(agent, file string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + filepath.Clean(file)))
	return filepath.Join(j.Dir, hex.EncodeToString(sum[:16])+".json")
}

func (j Journal) get(agent, file string) (record, bool) {
	var r record
	text, err := os.ReadFile(j.recordPath(agent, file))
	if err != nil || json.Unmarshal(text, &r) != nil {
		return record{}, false
	}
	return r, true
}

func (j Journal) put(r record) error {
	text, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(j.recordPath(r.Agent, r.File), append(text, '\n'), 0o600, func() error { return nil })
}

// Install installs h into the hook file at path and records what it added,
// with root as the project root when root is not empty.
//
// The record is written before the file, and Uninstall removes it after the
// file, so a run that is killed leaves a record too many and never a hook
// without one.
func (j Journal) Install(path string, h Handler, root string, startup ...Handler) (Change, error) {
	return j.install(path, h, root, nil, startup)
}

// InstallAndRegister publishes a portable registration after the hook write.
// If publication fails, it tries to restore the hook file and its install record.
func (j Journal) InstallAndRegister(path string, h Handler, publish func() error, startup ...Handler) (Change, error) {
	return j.install(path, h, "", publish, startup)
}

func (j Journal) install(path string, h Handler, root string, publish func() error, startup []Handler) (Change, error) {
	if err := os.MkdirAll(j.Dir, 0o700); err != nil {
		return Unchanged, fmt.Errorf("%w. The file is unchanged", err)
	}
	unlock, err := lockDir(j.Dir)
	if err != nil {
		return Unchanged, fmt.Errorf("%w. The file is unchanged", err)
	}
	defer unlock()
	was, known := j.get(h.Agent, path)
	recordWritten := false
	change, err := editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
		out, change := text, Unchanged
		next := was
		next.Agent, next.File, next.Root = h.Agent, filepath.Clean(path), root
		for _, handler := range slices.Concat([]Handler{h}, startup) {
			edited, editChange, added, err := Install(out, exists, handler)
			if err != nil {
				return nil, false, Unchanged, err
			}
			out, exists = edited, true
			if editChange == Added {
				if handler.Event == sessionStartEvent {
					next.SessionAdded = added
				} else {
					next.Added = added
				}
			}
			if editChange == Added || change == Unchanged {
				change = editChange
			}
		}
		var err error
		if !known || was != next {
			err = j.put(next)
			recordWritten = err == nil
		}
		return out, false, change, err
	}, publish)
	if err == nil || change != Unchanged {
		return change, err
	}
	err = fmt.Errorf("%w. The file is unchanged", err)
	if recordWritten {
		var rollbackErr error
		if known {
			rollbackErr = j.put(was)
		} else {
			rollbackErr = os.Remove(j.recordPath(h.Agent, path))
		}
		if rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("cannot restore the hook install record: %w", rollbackErr))
		}
	}
	return Unchanged, err
}

// Uninstall removes the handlers of ours for agent from the hook file at
// path, and the record of the file.
func (j Journal) Uninstall(path, agent string) (Change, error) {
	unlock, err := lockDir(j.Dir)
	switch {
	case err == nil:
		defer unlock()
	case !errors.Is(err, fs.ErrNotExist):
		return Unchanged, fmt.Errorf("%w. The file is unchanged", err)
	}
	recordPath := path
	was, known := j.get(agent, recordPath)
	if !known {
		// Portable installs use a canonical checkout root. Uninstall may
		// receive that root through a symlink.
		if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			candidate := filepath.Join(dir, filepath.Base(path))
			if saved, ok := j.get(agent, candidate); ok {
				recordPath, was = candidate, saved
			}
		}
	}
	change, err := editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
		if !exists {
			return nil, false, Unchanged, nil
		}
		return uninstall(text, agent, was.Added, was.SessionAdded)
	}, nil)
	if err != nil {
		return Unchanged, fmt.Errorf("%w. The file is unchanged", err)
	}
	if err := os.Remove(j.recordPath(agent, recordPath)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return change, fmt.Errorf("the hook is out of %s, but its record was not removed: %w", path, err)
	}
	return change, nil
}

// Roots returns the project root of every project-scope install.
func (j Journal) Roots() ([]string, error) {
	entries, err := os.ReadDir(j.Dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var roots []string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(j.Dir, entry.Name())
		text, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var r record
		if err == nil {
			err = json.Unmarshal(text, &r)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if r.Root != "" {
			roots = append(roots, r.Root)
		}
	}
	return roots, nil
}
