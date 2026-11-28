package daemon

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestHookRefreshReportsErrorsWithoutAStaleVerdict(t *testing.T) {
	for _, name := range []string{"lock", "backoff", "sync audit"} {
		t.Run(name, func(t *testing.T) {
			home, clock, _, setResponse := ageHub(t)
			g, err := Open(home, clock)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := g.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			setResponse(loadedAt.Add(5*time.Minute), 1)
			var want string
			switch name {
			case "lock":
				want = automaticLockFile
				// A reread after the lock error can no longer make a stale decision.
				if err := os.Remove(filepath.Join(home, policyFile)); err != nil {
					t.Fatal(err)
				}
			case "backoff":
				want = automaticFailFile
			case "sync audit":
				want = auditFile
				if err := os.Remove(filepath.Join(home, auditFile)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(home, want), 0o700); err != nil {
				t.Fatal(err)
			}
			err = g.RefreshForHook(t.Context())
			if err == nil || !strings.Contains(err.Error(), want) || name == "sync audit" && !errors.Is(err, ErrNotRecorded) {
				t.Errorf("policy %s, refresh error %v; want the error for %s", g.Status().State, err, want)
			}
		})
	}
}

func TestHubPolicyAge(t *testing.T) {
	home, clock, signature, setResponse := ageHub(t)
	now := clock()
	g, err := Open(home, clock)
	if err != nil {
		t.Fatal(err)
	}
	load(t, g, signature)
	assert := func(want State, decision policy.Decision) {
		t.Helper()
		gate, err := Open(home, clock)
		if err != nil {
			t.Fatal(err)
		}
		v, err := gate.Decide("claude-code", policy.Query{Action: policy.Write, Path: "src/app.ts"})
		got, _ := v.Result()
		if err != nil || v.Policy.State != want || got.Decision != decision {
			t.Errorf("at %s: policy %s, decision %s, error %v; want %s and %s", clock(), v.Policy.State, got.Decision, err, want, decision)
		}
	}
	assert(Stale, policy.Deny)
	if _, err := g.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset   time.Duration
		want     State
		decision policy.Decision
	}{
		{5*time.Minute - time.Nanosecond, Verified, policy.Allow},
		{5 * time.Minute, Stale, policy.Deny},
		{6 * time.Minute, Stale, policy.Deny},
		{4 * time.Minute, Verified, policy.Allow},
		{-time.Nanosecond, Stale, policy.Deny},
	} {
		setResponse(now.Add(tc.offset), 1)
		assert(tc.want, tc.decision)
	}
	for _, response := range []int{-1, 0} {
		setResponse(now.Add(6*time.Minute), response)
		if _, err := g.Sync(t.Context()); err == nil {
			t.Error("unavailable or rejected sync succeeded")
		}
		assert(Stale, policy.Deny)
	}
	setResponse(now.Add(6*time.Minute), 1)
	if _, err := g.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	assert(Verified, policy.Allow)
	load(t, g, signature)
	assert(Stale, policy.Deny)
	records := auditRecords(t, home)
	last := records[len(records)-1]
	if last.Action != "policy.denied" || last.Reason == nil || !strings.Contains(*last.Reason, "stale") {
		t.Error("missing local freshness denial in audit")
	}
}

func TestHubConfirmationTrustChanges(t *testing.T) {
	for _, change := range []string{"credential", "keys", "origin", "project", "member"} {
		t.Run(change, func(t *testing.T) {
			home, clock, _, _ := ageHub(t)
			g := open(t, home, clock())
			if _, err := g.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg := g.config
			switch change {
			case "credential":
				b := make([]byte, 32)
				b[0] = 1
				configure(t, home, Settings{Token: base64.RawURLEncoding.EncodeToString(b)})
				configure(t, home, Settings{Token: cfg.Token})
			case "keys":
				keys := readFile(t, home+"/keys.json")
				extra := strings.Replace(string(newSigner(t).keySet(t)), `"kid": "test"`, `"kid": "other", "use": "enc"`, 1)
				next := string(keys[:len(keys)-2]) + "," + strings.TrimPrefix(extra, `{"keys": [`)
				configure(t, home, Settings{KeySet: []byte(next)})
			case "origin", "project", "member":
				switch change {
				case "origin":
					cfg.HubURL = "https://another.example"
				case "project":
					cfg.ProjectID = "prj-other"
				case "member":
					cfg.MemberID = "mem-other"
				}
				if err := writeJSON(home, configFile, cfg); err != nil {
					t.Fatal(err)
				}
			}
			want := Stale
			if change == "project" {
				want = Rejected
			}
			if status := open(t, home, clock()).Status(); status.State != want {
				t.Errorf("after %s change policy is %s; want %s", change, status.State, want)
			}
		})
	}
}

func ageHub(t *testing.T) (string, func() time.Time, tokenSource, func(time.Time, int)) {
	t.Helper()
	const project = "prj-shop"
	home := t.TempDir()
	key := newSigner(t)
	keys := key.keySet(t)
	credential := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	configure(t, home, Settings{HubURL: testHubOrigin, ProjectID: project, MemberID: "mem-ana", Token: credential, KeySet: keys})
	bundle := bundleText(project, 1)
	encode := base64.RawURLEncoding.EncodeToString
	signature := key.signInput(t, encode([]byte(`{"alg":"ES256","typ":"mp-policy+jws"}`))+"."+encode(bundle))
	message := fmt.Appendf(nil, `{"version":1,"bundle":%s,"signature":%q}`, bundle, signature)
	old := bundleText(project, 0)
	rejected := fmt.Appendf(nil, `{"version":0,"bundle":%s,"signature":%q}`, old, key.sign(t, old))
	now, response := loadedAt, 1
	useHubTransport(t, handlerTransport(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/jwks.json" {
			w.Write(keys)
			return
		}
		switch response {
		case -1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 0:
			w.Write(rejected)
		default:
			now = now.Add(time.Second)
			w.Write(message)
		}
	}))
	return home, func() time.Time { return now }, signature, func(at time.Time, code int) { now, response = at, code }
}

func TestHubSyncRejectsTrustChangeDuringFetch(t *testing.T) {
	home, clock, _, _ := ageHub(t)
	g := open(t, home, clock())
	if _, err := g.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	confirmed := g.Status().HubConfirmedAt
	transport := hubHTTP.Transport
	originalToken := g.config.Token
	changed := make(chan error, 1)
	useHubTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "policy.bundle") {
			go func() {
				b := make([]byte, 32)
				b[0] = 1
				err := Configure(home, Settings{Token: base64.RawURLEncoding.EncodeToString(b)})
				if err == nil {
					err = Configure(home, Settings{Token: originalToken})
				}
				changed <- err
			}()
			select {
			case err := <-changed:
				if err != nil {
					return nil, err
				}
			case <-r.Context().Done():
				t.Error("configure could not acquire the home lock during network I/O")
				return nil, r.Context().Err()
			}
		}
		return transport.RoundTrip(r)
	}))
	if _, err := g.Sync(t.Context()); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Errorf("sync across credential changes = %v", err)
	}
	stored, err := readStoredPolicy(home)
	if err != nil || stored == nil || !stored.HubConfirmedAt.Equal(confirmed) {
		t.Fatalf("in-flight response replaced the confirmation: %v", err)
	}
	if status := open(t, home, clock()).Status(); status.State != Stale || !status.HubConfirmedAt.IsZero() {
		t.Errorf("changed trust still confirms policy: %+v", status)
	}
}

func TestMaxPolicyAgeConfigIsStrict(t *testing.T) {
	home := t.TempDir()
	for _, config := range []string{
		`{"Max_Policy_Age_Seconds":300}`,
		`{"max_policy_age_seconds":300,"max_policy_age_seconds":60}`,
		`{"max_policy_age_seconds":null}`,
		`{"max_policy_age_seconds":60.5}`,
		`{"max_policy_age_seconds":0}`,
		`{"max_policy_age_seconds":3601}`,
	} {
		if err := writeFile(home, configFile, []byte(config)); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(home, at(loadedAt)); err == nil {
			t.Errorf("read invalid configuration %s", config)
		}
	}
}
