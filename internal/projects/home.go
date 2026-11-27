package projects

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ResolveHome selects an explicit home first, then OPENPLEXER_HOME, then the
// deepest registered checkout containing the current directory. With no
// matching checkout or no registry it returns the legacy ~/.openplexer home.
// A broken registry or an unusable matching home is an error.
func ResolveHome(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if home := os.Getenv("OPENPLEXER_HOME"); home != "" {
		return home, nil
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	registry, err := Read(user)
	if errors.Is(err, fs.ErrNotExist) {
		return filepath.Join(user, ".openplexer"), nil
	}
	if err != nil {
		return "", err
	}
	cwd, _, err := Directory(".")
	if err != nil {
		return "", err
	}
	project, err := registry.Lookup(cwd)
	if errors.Is(err, ErrUnregistered) {
		return filepath.Join(user, ".openplexer"), nil
	}
	if err != nil {
		return "", err
	}
	if err := project.CheckHome(); err != nil {
		return "", err
	}
	return project.Home, nil
}
