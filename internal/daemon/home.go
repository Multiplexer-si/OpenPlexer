package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/multiplexer-si/openplexer/internal/jsonedit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

const (
	configFile = "config.json"
	keysFile   = "keys.json"
	policyFile = "policy.json"
	auditFile  = "audit.jsonl"
	lockFile   = "lock"
)

const defaultStaleAfterDays = 14

type config struct {
	MemberID       string `json:"member_id,omitempty"`
	StaleAfterDays int    `json:"stale_after_days,omitempty"`
	HubURL         string `json:"hub_url,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	Token          string `json:"token,omitempty"`
}

func (c config) staleAfter() time.Duration {
	days := c.StaleAfterDays
	if days == 0 {
		days = defaultStaleAfterDays
	}
	return time.Duration(days) * 24 * time.Hour
}

func readJSON(home, name string, fields map[string]any) (bool, error) {
	path := filepath.Join(home, name)
	read := os.ReadFile
	if name == policyFile {
		read = readPolicyFile
	}
	text, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc, err := jsonedit.Parse(text)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	root := doc.Root()
	if root.Kind != jsonedit.Object {
		return false, fmt.Errorf("%s must be a JSON object", name)
	}
	if member, duplicate := root.DuplicateKey(); duplicate {
		return false, fmt.Errorf("%s: duplicate key %q", name, member.Key)
	}
	for _, member := range root.Members {
		if target, ok := fields[member.Key]; ok {
			if member.Value.Kind == jsonedit.Null || doc.Decode(member.Value, target) != nil {
				return false, fmt.Errorf("%s: invalid %s", name, member.Key)
			}
		}
	}
	return true, nil
}

func readPolicyFile(path string) ([]byte, error) {
	// Leave space for JSON escaping, including older unencoded JWS payloads.
	const maxStoredPolicyBytes = 8 * policy.MaxBundleBytes
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStoredPolicyBytes+1))
	if len(data) > maxStoredPolicyBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", policyFile, maxStoredPolicyBytes)
	}
	return data, err
}

func readConfig(home string) (config, error) {
	var c config
	_, err := readJSON(home, configFile, map[string]any{
		"member_id": &c.MemberID, "stale_after_days": &c.StaleAfterDays,
		"hub_url": &c.HubURL, "project_id": &c.ProjectID, "token": &c.Token,
	})
	if err != nil {
		return config{}, err
	}
	if c.StaleAfterDays < 0 {
		return config{}, fmt.Errorf("%s: stale_after_days must not be negative", configFile)
	}
	if c.hasHubSettings() {
		if err := c.checkHub(); err != nil {
			return config{}, err
		}
	}
	return c, nil
}

func readKeys(home string) (*policy.KeySet, error) {
	text, err := os.ReadFile(filepath.Join(home, keysFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys, err := policy.ParseKeySet(text)
	if err != nil {
		return nil, err
	}
	return &keys, nil
}

func readStoredPolicy(home string) (*storedPolicy, error) {
	var stored storedPolicy
	found, err := readJSON(home, policyFile, map[string]any{
		"token": &stored.Token, "confirmed_at": &stored.ConfirmedAt,
	})
	if err != nil || !found {
		return nil, err
	}
	if stored.Token == "" {
		return nil, fmt.Errorf("%s holds no token", policyFile)
	}
	return &stored, nil
}

// Readers of the home take no lock, so writeFile replaces a file by rename:
// a reader sees the old content or the new content, and never a part.
func writeFile(home, name string, content []byte) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(home, name+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), filepath.Join(home, name))
}

func writeJSON(home, name string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(home, name, append(content, '\n'))
}

// Settings is a change to the local configuration. A zero field keeps the
// configured value.
type Settings struct {
	// KeySet is the JSON Web Key Set that verifies a bundle.
	KeySet []byte
	// MemberID is the member id of the developer in the bundle.
	MemberID string
	// StaleAfterDays is the number of days after which a policy that its
	// source has not confirmed is stale.
	StaleAfterDays int
	HubURL         string
	ProjectID      string
	Token          string
}

// Configure applies a change to the configuration in the home directory.
func Configure(home string, change Settings) error {
	if change.StaleAfterDays < 0 {
		return errors.New("stale-after-days must not be negative")
	}
	if change.KeySet != nil {
		if _, err := policy.ParseKeySet(change.KeySet); err != nil {
			return err
		}
	}
	unlock, err := lockHome(home)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := readConfig(home)
	if err != nil {
		return err
	}
	next := current
	if change.HubURL != "" {
		next.HubURL = change.HubURL
	}
	if change.ProjectID != "" {
		next.ProjectID = change.ProjectID
	}
	if change.Token != "" {
		next.Token = change.Token
	}
	if change.MemberID != "" {
		next.MemberID = change.MemberID
	}
	if change.StaleAfterDays != 0 {
		next.StaleAfterDays = change.StaleAfterDays
	}
	if next.hasHubSettings() {
		if err := next.checkHub(); err != nil {
			return err
		}
		if current.HubURL == "" && change.KeySet == nil {
			return errors.New("configure the hub with a trusted key set (--keys)")
		}
		if current.HubURL != "" && (next.HubURL != current.HubURL || next.ProjectID != current.ProjectID || next.MemberID != current.MemberID) {
			return errors.New("use a separate OpenPlexer home to change the hub, project or member")
		}
	}
	if change.KeySet != nil {
		if err := writeFile(home, keysFile, change.KeySet); err != nil {
			return err
		}
	}
	if next == current {
		return nil
	}
	return writeJSON(home, configFile, next)
}

func lockHome(home string) (unlock func(), err error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(home, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFileExclusive(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", home, err)
	}
	return func() { file.Close() }, nil
}
