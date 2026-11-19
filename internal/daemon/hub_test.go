package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestAuditCredentialSurfaces(t *testing.T) {
	const project = "prj-shop"
	token := base64.RawURLEncoding.EncodeToString([]byte("disposable-test-credential-00000"))
	signer := newSigner(t)
	keys := signer.keySet(t)
	invalidHeader := func(alg, typ string) string {
		header := fmt.Appendf(nil, `{"alg":%q,"typ":%q}`, alg, typ)
		return base64.RawURLEncoding.EncodeToString(header) + ".e30.AA"
	}
	invalidBundle := []byte(strings.Replace(string(bundleText(project, 2)), `"members": {`, fmt.Sprintf(`"members": {%q: 1,`, token), 1))
	for _, tc := range []struct {
		name, signature, reason string
	}{
		{"algorithm", invalidHeader(token, "mp-policy+jws"), "header alg"},
		{"type", invalidHeader("ES256", token), "header typ"},
		{"policy dictionary", string(signer.sign(t, invalidBundle)), "members"},
		{"HTTP response", "", "hub request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configure(t, home, Settings{HubURL: testHubOrigin, ProjectID: project, MemberID: "mem-ana", Token: token, KeySet: keys})
			g := open(t, home, loadedAt)
			load(t, g, signer.sign(t, bundleText(project, 1)))
			useHubTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/.well-known/jwks.json" {
					rec := httptest.NewRecorder()
					rec.Write(keys)
					return rec.Result(), nil
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("policy request missing the test credential")
				}
				if tc.signature == "" {
					return nil, fmt.Errorf("malformed HTTP response %q", token)
				}
				rec := httptest.NewRecorder()
				fmt.Fprintf(rec, `{"version":2,"bundle":{},"signature":%q}`, tc.signature)
				return rec.Result(), nil
			}))

			check := func(surface string, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), tc.reason) {
					t.Errorf("%s did not identify the rejection", surface)
				}
				if err != nil && strings.Contains(err.Error(), token) {
					t.Errorf("%s disclosed the member bearer", surface)
				}
			}
			client, err := g.config.client()
			if err != nil {
				t.Fatal(err)
			}
			_, err = (hubSource{hubClient: client}).Fetch(context.Background())
			check("fetch error", err)
			status, err := g.Sync(context.Background())
			check("sync error", err)
			if status.State != Verified || status.Version != 1 {
				t.Error("failed sync changed the policy in force")
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var reported error
			pollHub(ctx, home, at(loadedAt), func(err error) {
				reported = err
				cancel()
			})
			check("daemon log", reported)
			if strings.Contains(string(readFile(t, filepath.Join(home, auditFile))), token) {
				t.Error("audit log disclosed the member bearer")
			}
		})
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

func TestPolicySyncBoundsTheHubResponseRead(t *testing.T) {
	const limit = 4 << 20
	body := strings.NewReader(strings.Repeat("x", limit+2))
	useHubTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/projects/p/policy.bundle" {
			t.Fatalf("unexpected request for %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(body)}, nil
	}))
	home := t.TempDir()
	configure(t, home, Settings{
		KeySet: newSigner(t).keySet(t), HubURL: "https://hub.example", ProjectID: "p", MemberID: "mem-ana",
		Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	})
	status, err := open(t, home, loadedAt).Sync(t.Context())
	if err == nil || !strings.Contains(err.Error(), "4194304-byte size limit") || status.State != Absent {
		t.Fatalf("sync = %+v, %v, want the response-size error and no policy", status, err)
	}
	if body.Len() != 1 {
		t.Fatalf("sync read %d response bytes, want %d", body.Size()-int64(body.Len()), limit+1)
	}
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
