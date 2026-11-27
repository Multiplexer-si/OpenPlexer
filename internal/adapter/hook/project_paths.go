package hook

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type WriteKind int

const (
	WriteThrough WriteKind = iota
	WriteEntry
)

// WriteTarget describes one file operation. If Source is set and Path is a
// directory, the destination is the source's base name inside that directory.
type WriteTarget struct {
	Path              string
	Kind              WriteKind
	Source            string
	RequiresDirectory bool
}

type ResolvedWrite struct {
	Named  string
	Path   string
	Entry  string
	Remove bool
}

// ResolveWrite follows symlinks in the parent and, for writes through an
// entry, in the final component. Rename and unlink use the entry itself.
func ResolveWrite(target WriteTarget, cwd, home string) (ResolvedWrite, error) {
	path := target.Path
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return ResolvedWrite{}, errors.New("the write path is empty or invalid")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = rawJoin(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	if target.Source != "" && strings.IndexByte(target.Source, 0) >= 0 {
		return ResolvedWrite{}, errors.New("the source path is invalid")
	}
	entry, err := resolvePath(path, cwd, false)
	if err != nil {
		return ResolvedWrite{}, err
	}
	if target.Source != "" {
		info, err := os.Stat(entry)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ResolvedWrite{}, err
		}
		if info != nil && info.IsDir() {
			path += string(filepath.Separator) + filepath.Base(target.Source)
			if entry, err = resolvePath(path, cwd, false); err != nil {
				return ResolvedWrite{}, err
			}
		} else if target.RequiresDirectory {
			return ResolvedWrite{}, errors.New("the destination directory does not exist")
		}
	}
	if target.Kind == WriteEntry {
		return ResolvedWrite{target.Path, entry, entry, true}, nil
	}
	resolved, err := resolvePath(path, cwd, true)
	return ResolvedWrite{target.Path, resolved, entry, false}, err
}

// ResolveGuardPaths returns the directory entry and referent of a guarded path
// when they can be resolved. The original path still protects missing files.
func ResolveGuardPaths(path string) []string {
	paths := []string{path}
	if !filepath.IsAbs(path) {
		return paths
	}
	for _, follow := range []bool{false, true} {
		if resolved, err := resolvePath(path, "", follow); err == nil && resolved != path {
			paths = append(paths, resolved)
		}
	}
	return paths
}

// CheckGuard also blocks removing an ancestor of a guarded path.
func (w ResolvedWrite) CheckGuard(path, why string) (Verdict, bool) {
	if within(w.Path, path) || within(w.Entry, path) || w.Remove && within(path, w.Path) {
		return Verdict{Deny, fmt.Sprintf("OpenPlexer blocked a write to %s, because %s. An agent may not change it. Change it yourself if you need to.", w.Named, why)}, true
	}
	return Verdict{}, false
}

func rawJoin(base, path string) string {
	if filepath.IsAbs(path) || base == "" {
		return path
	}
	return base + string(filepath.Separator) + path
}

// resolvePath walks components in order so that a .. after a symlink is
// relative to the link's destination. A new file can have missing parents.
func resolvePath(path, base string, followFinal bool) (string, error) {
	parts := func(path string) []string {
		names := strings.FieldsFunc(path, func(r rune) bool { return r < 256 && os.IsPathSeparator(uint8(r)) })
		if len(path) > 0 && os.IsPathSeparator(path[len(path)-1]) {
			names = append(names, ".")
		}
		return names
	}
	start := func(path string) (string, []string) {
		volume := filepath.VolumeName(path)
		return volume + string(filepath.Separator), parts(path[len(volume):])
	}
	pending := parts(path)
	if filepath.IsAbs(path) {
		base, pending = start(path)
	} else if !filepath.IsAbs(base) || filepath.VolumeName(path) != "" {
		return "", errors.New("the write path has no absolute base directory")
	}
	links, missing := 0, false
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case ".":
			continue
		case "..":
			if missing {
				return "", errors.New("the write path climbs out of a missing directory")
			}
			base = filepath.Dir(base)
			continue
		}
		next := filepath.Join(base, name)
		if missing || len(pending) == 0 && !followFinal {
			base = next
			continue
		}
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			base, missing = next, true
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 255 {
				return "", errors.New("too many symlinks in the write path")
			}
			link, err := os.Readlink(next)
			if err != nil {
				return "", err
			}
			names := parts(link)
			if filepath.IsAbs(link) {
				base, names = start(link)
			} else if filepath.VolumeName(link) != "" {
				return "", errors.New("cannot resolve the symlink's volume")
			}
			pending = append(names, pending...)
		} else {
			if len(pending) > 0 && !info.IsDir() {
				return "", fmt.Errorf("%s is not a directory", next)
			}
			base = next
		}
	}
	return base, nil
}
