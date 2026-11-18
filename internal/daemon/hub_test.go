package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHubOrigin(t *testing.T) {
	for _, address := range []string{"http://127.0.0.1:8787", "http://[::1]:8787", "http://localhost:8787", "https://hub.example"} {
		if _, err := hubOrigin(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"http://hub.example", "http://localhost.example", "https://user@hub.example", "https://hub.example/api", "https://hub.example?token=x", "file:///tmp"} {
		if _, err := hubOrigin(address); err == nil {
			t.Errorf("accepted %s", address)
		}
	}
}

func TestHubAdvertisedKeyCannotReplacePin(t *testing.T) {
	home := t.TempDir()
	pinned := newSigner(t)
	unpinned := newSigner(t)
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	payload, err := os.ReadFile(filepath.Join("..", "..", "spec", "policy", "v1", "examples", "bundle", "minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A hub project has one repository.
	payload = []byte(strings.Replace(string(payload), `"repos": []`, `"repos": ["api"]`, 1))
	keys := unpinned.keySet(t)
	message, err := json.Marshal(map[string]any{"version": 1, "bundle": json.RawMessage(payload), "signature": unpinned.sign(t, payload)})
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			if r.Header.Get("Authorization") != "" {
				t.Error("sent authorization to public key endpoint")
			}
			w.Write(keys)
		case "/api/v1/projects/prj-shop/policy.bundle":
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("bundle request missing member credential")
			}
			w.Write(message)
		default:
			http.NotFound(w, r)
		}
	})
	useHubTransport(t, handlerTransport(handler))

	change := Settings{HubURL: "http://127.0.0.1", ProjectID: "prj-shop", MemberID: "mem-ana", Token: token}
	if err := Configure(home, change); err == nil {
		t.Fatal("configured a hub without an explicit pin")
	}
	change.KeySet = pinned.keySet(t)
	if err := Configure(home, change); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, configFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("credential permissions = %o", info.Mode().Perm())
	}
	g, err := Open(home, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := g.Sync(context.Background()); !errors.Is(err, ErrRejected) || status.State != Absent {
		t.Fatalf("sync with an unpinned key = %v, %v", status, err)
	}
	if err := Configure(home, Settings{KeySet: unpinned.keySet(t)}); err != nil {
		t.Fatal(err)
	}
	if status, err := g.Sync(context.Background()); err != nil || status.State != Verified {
		t.Fatalf("sync after re-pinning = %v, %v", status, err)
	}
}

type handlerTransport http.HandlerFunc

func (h handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Result(), nil
}

func useHubTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := hubHTTP
	hubHTTP = &http.Client{Transport: transport, CheckRedirect: previous.CheckRedirect}
	t.Cleanup(func() { hubHTTP = previous })
}

func TestSignedBundleMessageUsesExactFinalMembers(t *testing.T) {
	for _, text := range []string{
		`{"version":1,"bundle":{},"Signature":"wrong"}`,
		`{"version":1,"bundle":{},"signature":"old","signature":null}`,
		`{"version":9007199254740992,"bundle":{},"signature":"x"}`,
	} {
		if _, err := signedToken([]byte(text)); err == nil {
			t.Errorf("accepted %s", text)
		}
	}
	token, err := signedToken([]byte(`{"version":1,"bundle":null,"signature":"old","signature":"final","Signature":"ignored"}`))
	if token != "final" || err != nil {
		t.Fatalf("signed token = %q, %v", token, err)
	}
}
