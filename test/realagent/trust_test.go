package main

import (
	"reflect"
	"testing"
)

func TestCodexTrustChecksEveryInstalledEvent(t *testing.T) {
	const project = "/test/project"
	const preTool = project + "/.codex/hooks.json:pre_tool_use:0:0"
	const startup = project + "/.codex/hooks.json:session_start:0:0"
	for _, bad := range []string{"", "missing", "duplicate", "changed project hash", "swapped flag hashes"} {
		name := bad
		if name == "" {
			name = "both events"
		}
		t.Run(name, func(t *testing.T) {
			var listings int
			list := func(string) ([]hookInfo, error) {
				listings++
				switch listings {
				case 1:
					return nil, nil
				case 2:
					return []hookInfo{{startup, "sha256:start", "untrusted", "project"}, {preTool, "sha256:pre", "untrusted", "project"}}, nil
				case 3:
					hash := "sha256:start"
					if bad == "changed project hash" {
						hash = "sha256:changed"
					}
					return []hookInfo{{preTool, "sha256:pre", "trusted", "project"}, {startup, hash, "trusted", "project"}}, nil
				default:
					t.Fatalf("unexpected project listing %d", listings)
					return nil, nil
				}
			}
			var wroteTrust bool
			write := func(key string, value any) error {
				if key == "hooks.state" {
					wroteTrust = true
					want := map[string]any{
						preTool: map[string]string{"trusted_hash": "sha256:pre"},
						startup: map[string]string{"trusted_hash": "sha256:start"},
					}
					if !reflect.DeepEqual(value, want) {
						t.Errorf("trust write got %v, want %v", value, want)
					}
				}
				return nil
			}
			flags := func() ([]hookInfo, error) {
				pre := hookInfo{"sessionFlags:pre_tool_use:0:0", "sha256:pre", "untrusted", "sessionFlags"}
				start := hookInfo{"sessionFlags:session_start:0:0", "sha256:start", "untrusted", "sessionFlags"}
				switch bad {
				case "missing":
					return []hookInfo{pre}, nil
				case "duplicate":
					return []hookInfo{pre, pre}, nil
				case "swapped flag hashes":
					pre.CurrentHash, start.CurrentHash = start.CurrentHash, pre.CurrentHash
				}
				return []hookInfo{start, pre}, nil
			}
			_, err := checkCodexTrust(project, list, write, flags)
			if (err != nil) != (bad != "") || !wroteTrust {
				t.Errorf("trust write performed %t, trust check returned %v", wroteTrust, err)
			}
		})
	}
}
