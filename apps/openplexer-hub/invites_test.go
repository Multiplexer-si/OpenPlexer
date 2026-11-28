package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/enrollment"
	"github.com/multiplexer-si/openplexer/internal/hub"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestInviteLocalAndRemoteParity(t *testing.T) {
	const origin = "https://hub.example.test"
	dir := t.TempDir()
	store := hub.Open(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProject("demo", "team/demo"); err != nil {
		t.Fatal(err)
	}
	lead, err := store.AddMember("lee", hub.Lead)
	if err != nil {
		t.Fatal(err)
	}
	handler := store.Handler()
	transport := adminHandler(handler)
	keys, err := store.PolicyKeys()
	if err != nil {
		t.Fatal(err)
	}
	keyJSON, _ := json.Marshal(keys)
	fp, err := policy.FingerprintKeySet(keyJSON)
	if err != nil {
		t.Fatal(err)
	}

	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			run := func(command []string, flags ...string) (string, string) {
				t.Helper()
				args := append([]string{}, command...)
				args = append(args, flags...)
				if remote {
					args = append(args, "--hub", origin)
				} else {
					args = append(args, "--data-dir", dir)
				}
				var stdout, stderr bytes.Buffer
				if status := runWithInput(args, strings.NewReader(lead), &stdout, &stderr, transport); status != 0 {
					t.Fatalf("invite command returned %d", status)
				}
				return stdout.String(), stderr.String()
			}

			flags := []string{"--project", "demo", "--member", name, "--role", "lead", "--expires-in", "60s", "--require-approval"}
			if !remote {
				flags = append(flags, "--origin", origin)
			}
			link, details := run([]string{"invite", "create"}, flags...)
			parsed, err := enrollment.ParseLink(strings.TrimSuffix(link, "\n"))
			if err != nil || parsed.Origin != origin || parsed.Fingerprint != fp {
				t.Fatal("join could not parse the lead's pinned link")
			}
			u, err := url.Parse(strings.TrimSpace(link))
			if err != nil || u.Scheme+"://"+u.Host != origin || u.Fragment != "fp="+fp ||
				!strings.HasPrefix(u.Path, "/join/") || strings.Count(link, "\n") != 1 {
				t.Fatal("create did not print one pinned link")
			}
			code := strings.TrimPrefix(u.Path, "/join/")
			if !validMemberToken(code) || strings.Contains(details, code) || strings.Contains(details, lead) ||
				!strings.Contains(details, "Project demo") || !strings.Contains(details, "hub role lead") {
				t.Fatal("create mixed invite secrets with public details")
			}
			digest := sha256.Sum256([]byte(testToken(name)))
			body, _ := json.Marshal(map[string]string{"code": code, "token_hash": hex.EncodeToString(digest[:])})
			res, err := transport.RoundTrip(mustInviteRequest(t, origin+"/api/v1/invites/redeem", body))
			if err != nil {
				t.Fatal("redeem transport error")
			}
			var pending struct {
				InviteID string `json:"invite_id"`
			}
			if res.StatusCode != 202 || json.NewDecoder(res.Body).Decode(&pending) != nil || pending.InviteID == "" {
				t.Fatal("claim did not enter pending approval")
			}
			res.Body.Close()
			list, _ := run([]string{"invites"})
			var page hub.InvitePage
			if strings.Contains(list, code) || json.Unmarshal([]byte(list), &page) != nil {
				t.Fatal("invalid invite list")
			}
			found := false
			for _, invite := range page.Invites {
				if invite.ID == pending.InviteID && invite.MemberID == name && invite.Role == hub.Lead {
					found = true
				}
			}
			if !found {
				t.Fatal("list did not include the created invite")
			}
			for _, command := range []struct{ name, state string }{{"approve-invite", "approved"}, {"revoke-invite", "revoked"}} {
				reply, _ := run([]string{command.name}, "--id", pending.InviteID)
				var result hub.InviteResult
				if strings.Contains(reply, code) || json.Unmarshal([]byte(reply), &result) != nil ||
					result.Invite.ID != pending.InviteID || string(result.Invite.State) != command.state {
					t.Fatal("invite state command returned the wrong result")
				}
			}
		})
	}
}

func mustInviteRequest(t *testing.T, address string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		t.Fatal("could not create redeem request")
	}
	return req
}

func TestInviteCreateValidatesKeysBeforeIssuing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	calls := 0
	transport := adminTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/.well-known/jwks.json" || req.Header.Get("Authorization") != "" {
			t.Fatal("create sent the lead credential before fetching the public keys")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"keys":[]}`))}, nil
	})
	status := runWithInput([]string{"invite", "create", "--hub", "https://hub.example.test", "--project", "demo", "--member", "ana"},
		strings.NewReader(testToken("lead")), &stdout, &stderr, transport)
	if status != 1 || calls != 1 || stdout.Len() != 0 {
		t.Fatal("create accepted invalid policy keys")
	}
}
