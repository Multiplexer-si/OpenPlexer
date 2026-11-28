package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	MemberID            string `json:"member_id,omitempty"`
	StaleAfterDays      int    `json:"stale_after_days,omitempty"`
	HubURL              string `json:"hub_url,omitempty"`
	ProjectID           string `json:"project_id,omitempty"`
	Token               string `json:"token,omitempty"`
	MaxPolicyAgeSeconds int    `json:"max_policy_age_seconds,omitempty"`
	HubTrustRevision    string `json:"hub_trust_revision,omitempty"`
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
	if _, duplicate := root.DuplicateKey(); duplicate {
		return false, fmt.Errorf("%s: duplicate member", name)
	}
	for _, member := range root.Members {
		target, ok := fields[member.Key]
		if !ok {
			return false, fmt.Errorf("%s: unexpected member", name)
		}
		if member.Value.Kind == jsonedit.Null || doc.Decode(member.Value, target) != nil {
			return false, fmt.Errorf("%s: invalid %s", name, member.Key)
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
	var maxAge *int
	_, err := readJSON(home, configFile, map[string]any{
		"member_id": &c.MemberID, "stale_after_days": &c.StaleAfterDays,
		"hub_url": &c.HubURL, "project_id": &c.ProjectID, "token": &c.Token,
		"max_policy_age_seconds": &maxAge, "hub_trust_revision": &c.HubTrustRevision,
	})
	if err != nil {
		return config{}, err
	}
	if maxAge != nil {
		if !validMaxPolicyAge(*maxAge) {
			return config{}, errors.New("config.json: max_policy_age_seconds must be between 60 and 3600")
		}
		c.MaxPolicyAgeSeconds = *maxAge
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
	keys, _, err := readPinnedKeys(home)
	return keys, err
}

func readPinnedKeys(home string) (*policy.KeySet, [sha256.Size]byte, error) {
	text, err := os.ReadFile(filepath.Join(home, keysFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, [sha256.Size]byte{}, nil
	}
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	keys, err := policy.ParseKeySet(text)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	return &keys, sha256.Sum256(text), nil
}

// ValidateConfiguredHome checks the settings of an existing local or hub home.
// A home can be registered before its first policy load.
func ValidateConfiguredHome(home string) error {
	if _, err := readConfig(home); err != nil {
		return err
	}
	keys, err := readKeys(home)
	if err != nil {
		return err
	}
	if keys != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(home, configFile)); errors.Is(err, fs.ErrNotExist) {
		return errors.New("the OpenPlexer home has no configuration. Run `openplexer configure` with a key set or member first")
	} else {
		return err
	}
}

func readStoredPolicy(home string) (*storedPolicy, error) {
	var stored storedPolicy
	found, err := readJSON(home, policyFile, map[string]any{
		"token": &stored.Token, "confirmed_at": &stored.ConfirmedAt,
		"hub_confirmed_at": &stored.HubConfirmedAt, "hub_binding": &stored.HubBinding,
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
	return replaceFile(home, name, content, (*os.File).Close)
}

// prepare runs after sync and before publish. The caller owns any file it
// leaves open on success.
func replaceFile(home, name string, content []byte, prepare func(*os.File) error) (err error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(home, name+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			temporary.Close()
		}
		os.Remove(temporary.Name())
	}()
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := prepare(temporary); err != nil {
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
	StaleAfterDays      int
	HubURL              string
	ProjectID           string
	Token               string
	MaxPolicyAgeSeconds int
}

// Configure applies a change to the configuration in the home directory.
func Configure(home string, change Settings) error {
	return configureSettings(home, change, nil)
}

// ConfigureAndRegister saves settings and publishes the prepared project registration.
// The home lock covers both writes and any restoration if publication fails.
func ConfigureAndRegister(home string, change Settings, publish func() error) error {
	return configureSettings(home, change, publish)
}

func configureSettings(home string, change Settings, publish func() error) error {
	if change.MaxPolicyAgeSeconds != 0 && !validMaxPolicyAge(change.MaxPolicyAgeSeconds) {
		return errors.New("maximum policy age must be between 1 minute and 1 hour")
	}
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
	if change.MaxPolicyAgeSeconds != 0 {
		next.MaxPolicyAgeSeconds = change.MaxPolicyAgeSeconds
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
	var files []settingsFile
	keysChanged := false
	if change.KeySet != nil {
		previous, err := os.ReadFile(filepath.Join(home, keysFile))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		keysChanged = !bytes.Equal(previous, change.KeySet)
		files = append(files, settingsFile{keysFile, change.KeySet})
	}
	if next.hasHubSettings() && (next.Token != current.Token || keysChanged) {
		// Restoring an earlier pin or credential must not revive its confirmation.
		next.HubTrustRevision = rand.Text()
	}
	if next != current {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		files = append(files, settingsFile{configFile, append(data, '\n')})
	}
	if publish != nil {
		if len(files) == 0 {
			if err := ValidateConfiguredHome(home); err != nil {
				return err
			}
			return publish()
		}
		if change.KeySet == nil {
			if _, err := readKeys(home); err != nil {
				return err
			}
		}
	}
	return saveSettings(home, files, publish)
}

type settingsFile struct {
	name string
	data []byte
}

func saveSettings(home string, files []settingsFile, publish func() error) (err error) {
	written := 0
	if publish != nil {
		backup, backupErr := os.MkdirTemp(home, ".configure-")
		if backupErr != nil {
			return backupErr
		}
		defer os.RemoveAll(backup)
		saved := make(map[string]string, len(files))
		for _, file := range files {
			path := filepath.Join(backup, file.name)
			linkErr := os.Link(filepath.Join(home, file.name), path)
			if linkErr == nil {
				saved[file.name] = path
			} else if !errors.Is(linkErr, fs.ErrNotExist) {
				return linkErr
			}
		}
		defer func() {
			if err == nil {
				return
			}
			for _, file := range files[:written] {
				path := filepath.Join(home, file.name)
				var restoreErr error
				if original := saved[file.name]; original != "" {
					restoreErr = os.Rename(original, path)
				} else if removeErr := os.Remove(path); !errors.Is(removeErr, fs.ErrNotExist) {
					restoreErr = removeErr
				}
				if restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("cannot restore %s: %w", file.name, restoreErr))
				}
			}
		}()
	}
	for _, file := range files {
		if err := writeFile(home, file.name, file.data); err != nil {
			return err
		}
		written++
	}
	if publish != nil {
		return publish()
	}
	return nil
}

func lockHome(home string) (unlock func(), err error) {
	return lockHomeFile(context.Background(), home, lockFile)
}

func lockHomeFile(ctx context.Context, home, name string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(home, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFileContext(ctx, file); err != nil {
		file.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", home, err)
	}
	return func() { file.Close() }, nil
}
