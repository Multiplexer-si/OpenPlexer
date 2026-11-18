package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

func agentDir(env, dotDir string) (string, error) {
	if dir := os.Getenv(env); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dotDir), nil
}

func agentConfig() ([]string, error) {
	claudeDir, err := agentDir("CLAUDE_CONFIG_DIR", ".claude")
	if err != nil {
		return nil, err
	}
	codexDir, err := agentDir("CODEX_HOME", ".codex")
	if err != nil {
		return nil, err
	}
	return []string{
		filepath.Join(claudeDir, ".credentials.json"),
		filepath.Join(claudeDir, "settings.json"),
		filepath.Join(claudeDir, "settings.local.json"),
		filepath.Join(claudeDir, "hooks"),
		filepath.Join(codexDir, "config.toml"),
		filepath.Join(codexDir, "hooks.json"),
		filepath.Join(codexDir, "auth.json"),
		filepath.Join(codexDir, "rules"),
		filepath.Join(codexDir, "plugins"),
	}, nil
}

func fingerprint(paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, root := range paths {
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			out[root] = "absent"
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(path)
				out[path] = "link:" + target
				return err
			case d.IsDir():
				out[path] = "dir"
				return nil
			}
			data, err := os.ReadFile(path)
			sum := sha256.Sum256(data)
			out[path] = "sha256:" + hex.EncodeToString(sum[:])
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("fingerprint %s: %w", root, err)
		}
	}
	return out, nil
}

func changed(before, after map[string]string) []string {
	var out []string
	for path, sum := range before {
		if after[path] != sum {
			out = append(out, path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
