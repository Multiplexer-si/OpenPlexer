//go:build unix

package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigureRestoresSettingsIfRegistrationFails(t *testing.T) {
	for _, name := range []string{"existing settings", "new settings"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			configPath := filepath.Join(home, configFile)
			keysPath := filepath.Join(home, keysFile)
			var beforeConfig, beforeKeys []byte
			if name == "existing settings" {
				configure(t, home, Settings{MemberID: "mem-lee", KeySet: newSigner(t).keySet(t)})
				beforeConfig, beforeKeys = readFile(t, configPath), readFile(t, keysPath)
			}

			keys := newSigner(t).keySet(t)
			registrationErr := errors.New("cannot publish the project registry")
			err := ConfigureAndRegister(home, Settings{MemberID: "mem-ana", KeySet: keys}, func() error {
				cfg, err := readConfig(home)
				if err != nil || cfg.MemberID != "mem-ana" || string(readFile(t, keysPath)) != string(keys) {
					t.Error("registration ran before the new settings were saved")
				}
				return registrationErr
			})
			if !errors.Is(err, registrationErr) {
				t.Fatal("configure did not report the registration failure")
			}
			for path, before := range map[string][]byte{configPath: beforeConfig, keysPath: beforeKeys} {
				data, err := os.ReadFile(path)
				if before == nil {
					if !errors.Is(err, fs.ErrNotExist) {
						t.Errorf("failed registration left %s in the home", filepath.Base(path))
					}
				} else if err != nil || string(data) != string(before) {
					t.Errorf("failed registration changed %s", filepath.Base(path))
				}
			}
		})
	}
}
