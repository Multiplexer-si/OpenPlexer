package daemon

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdmissionKeepsTheFloorWithoutAPolicyInForce(t *testing.T) {
	first, next := newSigner(t), newSigner(t)
	original := bundleText("p", 2)
	multiRepo := bytes.Replace(original, []byte(`["api"]`), []byte(`["api", "web"]`), 1)
	for _, change := range []struct {
		name     string
		original []byte
		apply    func(*testing.T, string)
		signer   signer
		state    State
	}{
		{"key set change", original, func(t *testing.T, home string) {
			configure(t, home, Settings{KeySet: next.keySet(t)})
		}, next, Rejected},
		{"config change", multiRepo, func(t *testing.T, home string) {
			configure(t, home, Settings{
				HubURL: testHubOrigin, ProjectID: "p", MemberID: "mem-wu", KeySet: first.keySet(t),
				Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
			})
		}, first, Rejected},
		{"policy removed", original, removeStoredPolicy, first, Absent},
	} {
		for _, candidate := range []struct {
			name, reason string
			payload      []byte
		}{
			{"older version", "older than version floor 2", bundleText("p", 1)},
			{"different content at the same version", "matches version floor 2 with different content", bytes.Replace(original, []byte("src/**"), []byte("docs/**"), 1)},
		} {
			t.Run(change.name+"/"+candidate.name, func(t *testing.T) {
				home := t.TempDir()
				configure(t, home, Settings{KeySet: first.keySet(t), MemberID: "mem-ana"})
				if status := load(t, open(t, home, loadedAt), first.sign(t, change.original)); status.State != Verified || status.Version != 2 {
					t.Fatalf("first load = %+v, want verified version 2", status)
				}
				change.apply(t, home)
				gate := open(t, home, loadedAt)
				if gate.Status().State != change.state {
					t.Fatalf("reopen = %+v, want %s", gate.Status(), change.state)
				}
				expectFloorRejection(t, gate, change.signer.sign(t, candidate.payload), candidate.reason)
			})
		}
	}
}

func TestSyncKeepsTheFloorWithoutAPolicyInForce(t *testing.T) {
	keys := newSigner(t)
	home := t.TempDir()
	configure(t, home, Settings{
		HubURL: testHubOrigin, ProjectID: "p", MemberID: "mem-ana", KeySet: keys.keySet(t),
		Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	})
	load(t, open(t, home, loadedAt), keys.sign(t, bundleText("p", 2)))
	removeStoredPolicy(t, home)
	token := keys.sign(t, bundleText("p", 1))
	useHubTransport(t, handlerTransport(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			w.Write(keys.keySet(t))
		case "/api/v1/projects/p/policy.bundle":
			fmt.Fprintf(w, `{"version":1,"bundle":{},"signature":%q}`, token)
		default:
			http.NotFound(w, r)
		}
	}))
	status, err := open(t, home, loadedAt).Sync(t.Context())
	if status.State != Absent || !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "older than version floor 2") {
		t.Fatalf("sync = %+v, %v, want a floor rejection", status, err)
	}
}

func TestAdmissionRejectsAnInvalidVersionFloor(t *testing.T) {
	keys := newSigner(t)
	token := keys.sign(t, bundleText("p", 3))
	const digest = `"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`
	const entry = `{"version":2,"sha256":` + digest + `}`
	for _, tc := range []struct {
		name, content, reason string
	}{
		{"unreadable directory", "", "cannot read"},
		{"dangling symlink", "", "cannot read"},
		{"invalid JSON", "{", "invalid JSON"},
		{"invalid root", "null", "JSON object"},
		{"duplicate project", `{"p":` + entry + `,"\u0070":` + entry + `}`, "duplicate key"},
		{"duplicate field", `{"q":{"version":2,"version":1,"sha256":` + digest + `}}`, "duplicate key"},
		{"wrong field casing", `{"p":{"Version":2,"sha256":` + digest + `}}`, "invalid version"},
		{"missing version", `{"p":{"sha256":` + digest + `}}`, "invalid version"},
		{"null version", `{"p":{"version":null,"sha256":` + digest + `}}`, "invalid version"},
		{"invalid digest", `{"p":{"version":2,"sha256":"42"}}`, "invalid sha256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configure(t, home, Settings{KeySet: keys.keySet(t)})
			load(t, open(t, home, loadedAt), keys.sign(t, bundleText("p", 2)))
			path := filepath.Join(home, "policy-floor.json")
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			var err error
			switch tc.name {
			case "unreadable directory":
				err = os.Mkdir(path, 0o700)
			case "dangling symlink":
				err = os.Symlink("missing-floor", path)
			default:
				err = os.WriteFile(path, []byte(tc.content), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			expectFloorRejection(t, open(t, home, loadedAt), token, tc.reason)
		})
	}
}

func TestAdmissionMigratesAnEnforcedPolicyWithoutAFloor(t *testing.T) {
	home := t.TempDir()
	keys := newSigner(t)
	configure(t, home, Settings{KeySet: keys.keySet(t)})
	original := keys.sign(t, bundleText("p", 2))
	if err := writeJSON(home, policyFile, storedPolicy{Token: string(original), ConfirmedAt: loadedAt}); err != nil {
		t.Fatal(err)
	}
	gate := open(t, home, loadedAt)
	if gate.Status().State != Verified || gate.Status().Version != 2 {
		t.Fatalf("legacy policy = %+v, want verified version 2", gate.Status())
	}
	if _, err := os.Lstat(filepath.Join(home, "policy-floor.json")); !os.IsNotExist(err) {
		t.Fatalf("open changed the legacy home: %v", err)
	}
	expectFloorRejection(t, gate, keys.sign(t, bundleText("p", 1)), "older than version floor 2")
	removeStoredPolicy(t, home)
	expectFloorRejection(t, open(t, home, loadedAt), keys.sign(t, bundleText("p", 1)), "older than version floor 2")
	load(t, open(t, home, loadedAt), original)
}

func TestAdmissionMigratesARejectedPolicyWithoutAFloor(t *testing.T) {
	first, next := newSigner(t), newSigner(t)
	original := bundleText("p", 2)
	for _, tc := range []struct {
		name, reason string
		payload      []byte
	}{
		{"older version", "older than version floor 2", bundleText("p", 1)},
		{"different content", "matches version floor 2 with different content", bytes.Replace(original, []byte("src/**"), []byte("docs/**"), 1)},
		{"same canonical content", "", append([]byte("\n  "), original...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configure(t, home, Settings{KeySet: first.keySet(t)})
			if err := writeJSON(home, policyFile, storedPolicy{Token: string(first.sign(t, original)), ConfirmedAt: loadedAt}); err != nil {
				t.Fatal(err)
			}
			configure(t, home, Settings{KeySet: next.keySet(t)})
			gate := open(t, home, loadedAt)
			if gate.Status().State != Rejected {
				t.Fatalf("legacy policy after key rotation = %+v, want rejected", gate.Status())
			}
			candidate := next.sign(t, tc.payload)
			if tc.reason != "" {
				expectFloorRejection(t, gate, candidate, tc.reason)
			} else if status := load(t, gate, candidate); status.State != Verified || status.Version != 2 {
				t.Fatalf("load after re-signing = %+v, want verified version 2", status)
			}
			removeStoredPolicy(t, home)
			expectFloorRejection(t, open(t, home, loadedAt), next.sign(t, bundleText("p", 1)), "older than version floor 2")
		})
	}
}

func TestAdmissionRecoversFromAnUnparseableStoredPolicyWithoutAFloor(t *testing.T) {
	keys := newSigner(t)
	for _, tc := range []struct{ name, content string }{
		{"invalid policy file", "{"},
		{"invalid payload", fmt.Sprintf(`{"token":%q}`, keys.sign(t, []byte(`{"project_id":"p","version":2`)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configure(t, home, Settings{KeySet: keys.keySet(t)})
			if err := os.WriteFile(filepath.Join(home, policyFile), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			gate := open(t, home, loadedAt)
			if gate.Status().State != Rejected {
				t.Fatalf("legacy policy = %+v, want rejected", gate.Status())
			}
			if status := load(t, gate, keys.sign(t, bundleText("p", 1))); status.State != Verified || status.Version != 1 {
				t.Fatalf("load = %+v, want verified version 1", status)
			}
		})
	}
}

func TestAdmissionKeepsSeparateFloorsAcrossProjectSwitches(t *testing.T) {
	home := t.TempDir()
	keys := newSigner(t)
	configure(t, home, Settings{KeySet: keys.keySet(t)})
	bundle := bundleText("p", 2)
	load(t, open(t, home, loadedAt), keys.sign(t, bundle))
	removeStoredPolicy(t, home)
	load(t, open(t, home, loadedAt), keys.sign(t, bundleText("P", 1)))
	removeStoredPolicy(t, home)
	gate := open(t, home, loadedAt)
	expectFloorRejection(t, gate, keys.sign(t, bundleText("p", 1)), "older than version floor 2")

	reformatted := append([]byte("\n  "), bundle...)
	if status := load(t, gate, keys.sign(t, reformatted)); status.ProjectID != "p" || status.Version != 2 {
		t.Fatalf("load of the original content = %+v, want project p version 2", status)
	}

	removeStoredPolicy(t, home)
	if err := os.Remove(filepath.Join(home, floorFile)); err != nil {
		t.Fatal(err)
	}
	load(t, open(t, home, loadedAt), keys.sign(t, bundleText("p", 1)))
}

func TestAdmissionCanRetryAfterThePolicyWriteFails(t *testing.T) {
	home := t.TempDir()
	keys := newSigner(t)
	configure(t, home, Settings{KeySet: keys.keySet(t)})
	path := filepath.Join(home, policyFile)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	gate := open(t, home, loadedAt)
	bundle := keys.sign(t, bundleText("p", 2))
	if status, err := gate.Load(t.Context(), bundle); err == nil || status.State != Rejected {
		t.Fatalf("load with an unwritable policy file = %+v, %v", status, err)
	}
	removeStoredPolicy(t, home)
	gate = open(t, home, loadedAt)
	expectFloorRejection(t, gate, keys.sign(t, bundleText("p", 1)), "older than version floor 2")
	load(t, gate, bundle)
}

func removeStoredPolicy(t *testing.T, home string) {
	t.Helper()
	if err := os.Remove(filepath.Join(home, policyFile)); err != nil {
		t.Fatal(err)
	}
}

func expectFloorRejection(t *testing.T, gate *Gate, source Source, reason string) {
	t.Helper()
	before := gate.Status()
	status, err := gate.Load(t.Context(), source)
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "floor") || !strings.Contains(err.Error(), reason) {
		t.Errorf("load = %+v, %v, want a floor rejection containing %q", status, err, reason)
	}
	if status != before || open(t, gate.home, loadedAt).Status() != before {
		t.Errorf("load changed the stored policy: before %+v, after %+v", before, status)
	}
	records := auditRecords(t, gate.home)
	if last := records[len(records)-1]; last.Action != "policy.rejected" || last.Reason == nil || !strings.Contains(*last.Reason, reason) {
		t.Errorf("last record = %+v, want policy.rejected with reason containing %q", last, reason)
	}
}
