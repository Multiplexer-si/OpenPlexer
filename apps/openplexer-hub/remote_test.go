package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/hub"
)

type adminTransport func(*http.Request) (*http.Response, error)

func (roundTrip adminTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return roundTrip(r)
}

func adminHandler(handler http.Handler) http.RoundTripper {
	return adminTransport(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res.Result(), nil
	})
}

func testToken(label string) string {
	sum := sha256.Sum256([]byte(label))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestRemoteAdminWorkflow(t *testing.T) {
	dir := t.TempDir()
	bootstrap, issued := testToken("bootstrap fixture"), testToken("new member fixture")
	leadPath := filepath.Join(dir, "lead.token")
	policyPath := filepath.Join(dir, "policy.json")
	const policy = `{"project_id":"demo"}`
	if err := os.WriteFile(policyPath, []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	var leadToken string
	initCalls := 0
	check := func(r *http.Request, token, body string) {
		t.Helper()
		if r.URL.Host != "hub.example.test" || r.Header.Get("Authorization") != "Bearer "+token ||
			!r.Close || (body != "" && r.Header.Get("Content-Type") != "application/json") {
			t.Fatalf("%s %s used the wrong origin or headers", r.Method, r.URL.Path)
		}
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body {
			t.Fatalf("%s %s used the wrong body", r.Method, r.URL.Path)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/admin/init", func(w http.ResponseWriter, r *http.Request) {
		info, err := os.Stat(leadPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("the lead token was not saved in a private file before the request")
		}
		token, err := readCredential(nil, leadPath, false)
		if err != nil || token == bootstrap || (leadToken != "" && token != leadToken) {
			t.Fatal("invalid first lead token or retry changed the token")
		}
		leadToken = token
		digest := sha256.Sum256([]byte(token))
		check(r, bootstrap, string(marshal(t, map[string]string{"member_id": "lee", "token_hash": hex.EncodeToString(digest[:])})))
		initCalls++
		if initCalls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("POST /api/v1/admin/members", func(w http.ResponseWriter, r *http.Request) {
		check(r, leadToken, `{"id":"ana","role":"member"}`)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": issued})
	})
	mux.HandleFunc("DELETE /api/v1/admin/members/ana", func(_ http.ResponseWriter, r *http.Request) {
		check(r, leadToken, "")
	})
	mux.HandleFunc("POST /api/v1/admin/projects", func(_ http.ResponseWriter, r *http.Request) {
		check(r, leadToken, `{"id":"demo","repo":"api"}`)
	})
	mux.HandleFunc("PUT /api/v1/projects/demo/policy", func(_ http.ResponseWriter, r *http.Request) {
		check(r, leadToken, policy)
	})
	transport := adminHandler(mux)
	command := func(status int, output, stdin, name string, flags ...string) {
		t.Helper()
		var out, stderr bytes.Buffer
		args := append([]string{name, "--hub", "https://hub.example.test/"}, flags...)
		if got := runWithInput(args, strings.NewReader(stdin), &out, &stderr, transport); got != status ||
			out.String() != output || (status == 0 && stderr.Len() != 0) {
			t.Fatalf("%s exit %d, want %d, or unexpected output", name, got, status)
		}
		for _, token := range []string{bootstrap, leadToken, issued} {
			if token != "" && strings.Contains(stderr.String(), token) {
				t.Fatalf("%s disclosed a credential", name)
			}
		}
	}
	command(1, "", bootstrap+"\n", "init", "--id", "lee", "--lead-token-file", leadPath)
	command(0, "", bootstrap+"\n", "init", "--id", "lee", "--lead-token-file", leadPath)
	if initCalls != 2 {
		t.Fatalf("got %d init requests, want 2", initCalls)
	}
	command(0, issued+"\n", "", "add-member", "--token-file", leadPath, "--id", "ana")
	command(0, "", "", "add-project", "--token-file", leadPath, "--id", "demo", "--repo", "api")
	command(0, "", leadToken, "publish-policy", "--project", "demo", "--file", policyPath)
	command(0, "", leadToken, "remove-member", "--token-file", "-", "--id", "ana")
}

func TestRemoteInitRejectsIdenticalCredentials(t *testing.T) {
	token := testToken("shared fixture")
	path := filepath.Join(t.TempDir(), "lead.token")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	transport := adminTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("sent init with identical bootstrap and lead credentials")
		return nil, nil
	})
	var out, stderr bytes.Buffer
	code := runWithInput([]string{"init", "--hub", "https://hub.example.test", "--id", "lee", "--lead-token-file", path},
		strings.NewReader(token), &out, &stderr, transport)
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "bootstrap token and first lead token must be different") {
		t.Fatalf("exit %d or unexpected output for identical credentials", code)
	}
}

func TestRemoteAddMemberTokenResponse(t *testing.T) {
	lead, issued, other := testToken("lead"), testToken("issued"), testToken("other")
	for _, tc := range []struct {
		name, body, token string
	}{
		{name: "wrong case", body: `{"Token":"` + issued + `"}`},
		{name: "final null", body: `{"token":"` + issued + `","token":null}`},
		{name: "wrong type", body: `{"token":42}`},
		{name: "invalid syntax", body: `{"token":"` + issued[:42] + `+"}`},
		{name: "nonzero padding bits", body: `{"token":"` + issued[:42] + `B"}`},
		{name: "valid", body: `{"token":"` + issued + `"}`, token: issued},
		{name: "final exact name", body: `{"token":null,"token":"` + issued + `","Token":"` + other + `"}`, token: issued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := adminHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			var out, stderr bytes.Buffer
			code := runWithInput([]string{"add-member", "--hub", "https://hub.example.test", "--id", "ana"},
				strings.NewReader(lead), &out, &stderr, transport)
			if tc.token != "" {
				if code != 0 || out.String() != tc.token+"\n" || stderr.Len() != 0 {
					t.Fatalf("exit %d or unexpected output for a valid response", code)
				}
			} else if code != 1 || out.Len() != 0 || stderr.String() != "openplexer-hub add-member: invalid token response from hub\n" {
				t.Fatalf("exit %d or unexpected output for an invalid response", code)
			}
		})
	}
}

func TestRemoteAdminRejectsUnsafeInputsAndRedactsErrors(t *testing.T) {
	token := testToken("lead fixture")
	private := filepath.Join(t.TempDir(), "public.token")
	if err := os.WriteFile(private, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, origin, stdin string
		flags               []string
		response            int
		want                string
	}{
		{name: "plaintext off loopback", origin: "http://hub.example.test", want: "must use HTTPS"},
		{name: "path in origin", origin: "https://hub.example.test/other", want: "must be an origin"},
		{name: "userinfo in origin", origin: "https://user:" + token + "@hub.example.test", want: "must be an origin"},
		{name: "public credential file", flags: []string{"--token-file", private}, want: "must be a private"},
		{name: "invalid stdin", stdin: "invalid", want: "invalid credential"},
		{name: "same-origin redirect", origin: "http://127.0.0.1:8787", stdin: token, response: 307, want: "HTTP 307"},
		{name: "HTTP error", stdin: token, response: 403, want: "HTTP 403"},
		{name: "response decoding error", stdin: token, response: 200, want: "invalid token response"},
		{name: "transport error", stdin: token, response: -1, want: "hub request failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := tc.origin
			if origin == "" {
				origin = "https://hub.example.test"
			}
			count := 0
			transport := adminTransport(func(r *http.Request) (*http.Response, error) {
				count++
				if tc.response == -1 {
					return nil, errors.New(token)
				}
				if tc.response == 0 || count != 1 {
					t.Fatal("sent a request after rejecting credentials or a redirect")
				}
				res := httptest.NewRecorder()
				res.Header().Set("Location", "/redirected")
				res.WriteHeader(tc.response)
				_, _ = res.WriteString(token)
				return res.Result(), nil
			})
			var out, stderr bytes.Buffer
			args := append([]string{"add-member", "--hub", origin, "--id", "ana"}, tc.flags...)
			code := runWithInput(args, strings.NewReader(tc.stdin), &out, &stderr, transport)
			if code != 1 || out.Len() != 0 || strings.Contains(stderr.String(), token) ||
				!strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("exit %d or unexpected output, want a redacted %q error", code, tc.want)
			}
			if tc.response != 0 && count != 1 {
				t.Fatalf("made %d requests, want 1", count)
			}
		})
	}
}

func TestRemoteKeyCommands(t *testing.T) {
	token := testToken("lead fixture")
	const kid = "audit-staged_1"
	for _, tc := range []struct {
		name, command, purpose, kid, reply, method, path, body, output string
		status                                                         int
	}{
		{name: "stage", command: "stage-key", purpose: "audit", reply: `{"kty":"EC","kid":"audit-staged_1"}`,
			method: "POST", path: "/api/v1/admin/keys/audit", output: kid + "\n"},
		{name: "activate", command: "activate-key", purpose: "policy", kid: "staged_1", reply: "{}",
			method: "PUT", path: "/api/v1/admin/keys/policy/active", body: `{"kid":"staged_1"}`},
		{name: "duplicate", command: "stage-key", purpose: "audit", reply: `{"kid":"first","kid":"second"}`,
			method: "POST", path: "/api/v1/admin/keys/audit", status: 1},
		{name: "wrong case", command: "stage-key", purpose: "policy", reply: `{"Kid":"other"}`,
			method: "POST", path: "/api/v1/admin/keys/policy", status: 1},
		{name: "invalid purpose", command: "stage-key", purpose: "../audit", status: 1},
		{name: "invalid kid", command: "activate-key", purpose: "audit", kid: "bad/key", status: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			transport := adminHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatalf("unexpected key request %s %s", r.Method, r.URL.Path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.body || (tc.body != "" && r.Header.Get("Content-Type") != "application/json") {
					t.Fatal("unexpected key request body")
				}
				_, _ = io.WriteString(w, tc.reply)
			}))
			args := []string{tc.command, "--hub", "https://hub.example.test", "--purpose", tc.purpose}
			if tc.kid != "" {
				args = append(args, "--kid", tc.kid)
			}
			var out, stderr bytes.Buffer
			code := runWithInput(args, strings.NewReader(token), &out, &stderr, transport)
			wantCalls := 0
			if tc.method != "" {
				wantCalls = 1
			}
			if code != tc.status || calls != wantCalls || out.String() != tc.output {
				t.Fatalf("exit %d, calls %d, stdout %q, stderr %q", code, calls, out.String(), stderr.String())
			}
		})
	}
	for _, command := range []string{"stage-key", "activate-key"} {
		var stderr bytes.Buffer
		if code := runWithInput([]string{command}, nil, io.Discard, &stderr, nil); code != 2 ||
			!strings.Contains(stderr.String(), "requires --hub") {
			t.Fatalf("%s without --hub: exit %d, stderr %q", command, code, stderr.String())
		}
	}
}

func TestRemoteAuditExport(t *testing.T) {
	store := hub.Open(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	token, err := store.AddMember("lead", hub.Lead)
	if err != nil {
		t.Fatal(err)
	}
	handler := store.Handler()
	const exportPath = "/api/v1/audit/export"
	const keysPath = "/.well-known/audit-jwks.json"
	for _, tc := range []struct {
		name, command, wantError string
		duplicate, keysFailure   bool
	}{
		{name: "valid", command: "export"},
		{name: "existing checkpoint", command: "export-audit", wantError: "publish audit files"},
		{name: "duplicate header", command: "export", duplicate: true, wantError: "invalid or missing audit checkpoint"},
		{name: "JWKS failure", command: "export", keysFailure: true, wantError: "HTTP 503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			transport := adminHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				auth := r.Header.Get("Authorization")
				switch r.URL.Path {
				case exportPath:
					if auth != "Bearer "+token {
						t.Fatal("export omitted lead credential")
					}
				case keysPath:
					if auth != "" {
						t.Fatal("public JWKS request carried the lead credential")
					}
					if tc.keysFailure {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			if tc.duplicate {
				base := transport
				transport = adminTransport(func(r *http.Request) (*http.Response, error) {
					res, err := base.RoundTrip(r)
					if err == nil && r.URL.Path == exportPath {
						res.Header.Add("OpenPlexer-Audit-Checkpoint", "a.b.c")
					}
					return res, err
				})
			}
			out := filepath.Join(t.TempDir(), "export")
			kept := filepath.Join(out, "checkpoint.jws")
			if tc.name == "existing checkpoint" {
				if err := os.Mkdir(out, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(kept, []byte("kept"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var stderr bytes.Buffer
			code := runWithInput([]string{tc.command, "--hub", "https://hub.example.test", "--out", out},
				strings.NewReader(token), io.Discard, &stderr, transport)
			wantPaths := []string{exportPath, keysPath}
			if tc.duplicate {
				wantPaths = wantPaths[:1]
			}
			if !slices.Equal(paths, wantPaths) {
				t.Fatalf("requested %v, want %v", paths, wantPaths)
			}
			files, err := os.ReadDir(out)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" {
				if code != 1 || !strings.Contains(stderr.String(), tc.wantError) {
					t.Fatalf("exit %d, stderr %q", code, stderr.String())
				}
				if tc.name == "existing checkpoint" {
					if len(files) != 1 || string(readAuditFile(t, kept)) != "kept" {
						t.Fatal("export changed the existing checkpoint or left other files")
					}
				} else if len(files) != 0 {
					t.Fatal("export left files after an HTTP failure")
				}
				return
			}
			if code != 0 || len(files) != 3 {
				t.Fatalf("exit %d, files %v, stderr %q", code, files, stderr.String())
			}
			dir, err := os.Stat(out)
			if err != nil || dir.Mode().Perm() != 0700 {
				t.Fatal("export directory is not private")
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("%s is not private", file.Name())
				}
			}
			result := audit.VerifyExport(readAuditFile(t, filepath.Join(out, "export.json")),
				readAuditFile(t, filepath.Join(out, "jwks.json")), string(readAuditFile(t, kept)))
			if !result.OK || result.CheckpointEntries == nil || *result.CheckpointEntries != result.Entries {
				t.Fatalf("verification failed: %+v", result)
			}
		})
	}
}
