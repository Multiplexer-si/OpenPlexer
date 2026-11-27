package projects

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPath resolves an existing path and uses the spelling stored on disk.
func CanonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("a path is required")
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = cwd + string(filepath.Separator) + path
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return diskPath(resolved)
}

func diskPath(path string) (string, error) {
	parent := filepath.Dir(path)
	if path == parent {
		return path, nil
	}
	parent, err := diskPath(parent)
	if err != nil {
		return "", err
	}
	path = filepath.Join(parent, filepath.Base(path))
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	name, err := EntryName(path, info)
	return filepath.Join(parent, name), err
}

// EntryName finds the on-disk name of an existing entry from its Lstat info.
// It preserves a symlink as an entry in its parent directory.
func EntryName(path string, info fs.FileInfo) (string, error) {
	dir, name := filepath.Dir(path), filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return name, nil
		}
	}
	var other string
	ambiguous := false
	for _, entry := range entries {
		candidate, err := entry.Info()
		if err == nil && os.SameFile(info, candidate) {
			if strings.EqualFold(entry.Name(), name) {
				return entry.Name(), nil
			}
			ambiguous = other != ""
			other = entry.Name()
		}
	}
	if other == "" || ambiguous {
		return "", errors.New("cannot determine the on-disk path spelling")
	}
	return other, nil
}
