package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

// The files of the home directory. This package is the only reader and
// writer of each.
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
}

func (c config) staleAfter() time.Duration {
	days := c.StaleAfterDays
	if days == 0 {
		days = defaultStaleAfterDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// readJSON reads one file of the home directory. It returns nil and no
// error for a file that does not exist.
func readJSON[T any](home, name string) (*T, error) {
	text, err := os.ReadFile(filepath.Join(home, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value T
	if err := json.Unmarshal(text, &value); err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", name, err)
	}
	return &value, nil
}

func readConfig(home string) (config, error) {
	c, err := readJSON[config](home, configFile)
	if err != nil {
		return config{}, err
	}
	if c == nil {
		return config{}, nil
	}
	if c.StaleAfterDays < 0 {
		return config{}, fmt.Errorf("%s: stale_after_days must not be negative", configFile)
	}
	return *c, nil
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
	stored, err := readJSON[storedPolicy](home, policyFile)
	if err == nil && stored != nil && stored.Token == "" {
		return nil, fmt.Errorf("%s holds no token", policyFile)
	}
	return stored, err
}

// writeFile replaces one file of the home directory. A reader sees the old
// content or the new content, and never a part of one.
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
	if change.KeySet != nil {
		if err := writeFile(home, keysFile, change.KeySet); err != nil {
			return err
		}
	}
	if change.MemberID == "" && change.StaleAfterDays == 0 {
		return nil
	}
	current, err := readConfig(home)
	if err != nil {
		return err
	}
	if change.MemberID != "" {
		current.MemberID = change.MemberID
	}
	if change.StaleAfterDays != 0 {
		current.StaleAfterDays = change.StaleAfterDays
	}
	return writeJSON(home, configFile, current)
}

// lockHome takes the one lock of the home directory. A load, a change to
// the configuration, and each audit record hold it.
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
