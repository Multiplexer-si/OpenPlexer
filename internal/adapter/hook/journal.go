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
)

// Journal is the directory that holds one record for each hook file that
// install wrote. Each record is its own file, so two installs never write the
// same one.
type Journal struct {
	Dir string
}

// Level is the outermost thing that install put into a hook file. Uninstall
// removes a container that it emptied only when install added it.
type Level int

const (
	// AddedUnknown is the level of a hook that has no record. Uninstall then
	// removes each container that it emptied, and keeps the file.
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
	Agent string `json:"agent"`
	File  string `json:"file"`
	// Root is the project of a project-scope install, and empty otherwise.
	Root  string `json:"root,omitempty"`
	Added Level  `json:"added"`
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
	if err := os.MkdirAll(j.Dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(j.recordPath(r.Agent, r.File), append(text, '\n'), 0o600, func() error { return nil })
}

// Install installs h into the hook file at path, creating the file when it is
// missing, and records what it added. A root that is not empty is recorded as
// the project root of the file.
//
// The record is written before the file, and Uninstall removes it after the
// file, so a run that is killed leaves a record too many and never a hook
// without one.
func (j Journal) Install(path string, h Handler, root string) (Change, error) {
	was, known := j.get(h.Agent, path)
	change, err := editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
		out, change, added, err := Install(text, exists, h)
		if err != nil {
			return nil, false, Unchanged, err
		}
		next := record{Agent: h.Agent, File: filepath.Clean(path), Root: root, Added: was.Added}
		if change == Added {
			next.Added = added
		}
		if !known || was != next {
			err = j.put(next)
		}
		return out, false, change, err
	})
	if err != nil {
		if known {
			j.put(was)
		} else {
			os.Remove(j.recordPath(h.Agent, path))
		}
		return Unchanged, fmt.Errorf("%w. The file is unchanged", err)
	}
	return change, nil
}

// Uninstall removes the handlers of ours for agent from the hook file at
// path, and the record of the file.
func (j Journal) Uninstall(path, agent string) (Change, error) {
	was, _ := j.get(agent, path)
	change, err := editFile(path, func(text []byte, exists bool) ([]byte, bool, Change, error) {
		if !exists {
			return nil, false, Unchanged, nil
		}
		return Uninstall(text, agent, was.Added)
	})
	if err != nil {
		return Unchanged, fmt.Errorf("%w. The file is unchanged", err)
	}
	if err := os.Remove(j.recordPath(agent, path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
