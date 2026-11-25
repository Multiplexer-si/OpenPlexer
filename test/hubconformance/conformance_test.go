package hubconformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/testbin"
)

var (
	hubURL           = flag.String("hub-url", "", "origin of a seeded hub (default: start a Go hub)")
	wranglerDev      = flag.Bool("wrangler-dev", false, "use small bodies for early bearer rejections against Wrangler dev")
	leadTokenFile    = flag.String("lead-token-file", "", "file containing the lead token")
	memberTokenFile  = flag.String("member-token-file", "", "file containing the member token")
	revokedTokenFile = flag.String("revoked-token-file", "", "optional file containing a previously revoked token")
	project          = flag.String("project", "conformance", "project ID with an initial policy")
	policyAudit      = flag.Bool("policy-audit", true, "check the audit export around rejected policy writes")
	secondProject    = flag.String("second-project", "conformance-other", "second project ID with the same repo")
)

const (
	policyKeysPath = "/.well-known/jwks.json"
	auditKeysPath  = "/.well-known/audit-jwks.json"
	auditPath      = "/api/v1/audit/export"
	checkpoint     = "OpenPlexer-Audit-Checkpoint"
	policyLimit    = 1 << 20
	roomLimit      = 64 << 10
	seedRepo       = "R&D"
)

type fixture struct {
	origin, lead, member, revoked, unknown string
	missing                                string
	local                                  bool
	client                                 *http.Client
}

func setup(t *testing.T) *fixture {
	t.Helper()
	id := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	if !id.MatchString(*project) || !id.MatchString(*secondProject) || *project == *secondProject {
		t.Fatal("-project and -second-project must be distinct hub IDs")
	}
	f := &fixture{
		local: *hubURL == "", missing: "missing-" + rand.Text(),
		client: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	t.Cleanup(f.client.CloseIdleConnections)
	if f.local {
		if *leadTokenFile != "" || *memberTokenFile != "" || *revokedTokenFile != "" {
			t.Fatal("token file flags require -hub-url")
		}
		f.startGoHub(t)
	} else {
		origin, err := url.Parse(*hubURL)
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") ||
			origin.Hostname() == "" || origin.User != nil || (origin.EscapedPath() != "" && origin.EscapedPath() != "/") ||
			origin.ForceQuery || origin.RawQuery != "" || strings.Contains(*hubURL, "#") {
			t.Fatal("-hub-url must be an HTTP or HTTPS origin")
		}
		f.origin = origin.Scheme + "://" + origin.Host
		f.lead = readToken(t, "-lead-token-file", *leadTokenFile)
		f.member = readToken(t, "-member-token-file", *memberTokenFile)
		if *revokedTokenFile != "" {
			f.revoked = readToken(t, "-revoked-token-file", *revokedTokenFile)
		}
	}
	if f.lead == f.member || f.revoked == f.lead || f.revoked == f.member {
		t.Fatal("lead, member, and revoked credentials must be distinct")
	}
	token := make([]byte, 32)
	rand.Read(token)
	f.unknown = base64.RawURLEncoding.EncodeToString(token)
	if !f.local {
		f.call(t, get, policyKeysPath, "", "", http.StatusOK)
	}
	return f
}

func readToken(t *testing.T, flag, path string) string {
	t.Helper()
	if path == "" {
		t.Fatalf("%s is required with -hub-url", flag)
	}
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s (%T)", flag, err)
	}
	return parseToken(t, flag, text)
}

func parseToken(t *testing.T, source string, text []byte) string {
	t.Helper()
	token := strings.TrimSpace(string(text))
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		t.Fatalf("%s must supply one unpadded base64url token encoding 32 bytes", source)
	}
	return token
}

func (f *fixture) startGoHub(t *testing.T) {
	t.Helper()
	// Like test/multiagent, reserve a port because serve does not report its bound address.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot bind a loopback TCP port on 127.0.0.1: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	dir := t.TempDir()
	binDir, err := testbin.Dir("openplexer-hub")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "openplexer-hub")
	if binDir == "" {
		bin = filepath.Join(dir, "openplexer-hub")
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./apps/openplexer-hub")
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build openplexer-hub: %v\n%s", err, out)
		}
	}
	data := filepath.Join(dir, "hub-data")
	admin := func(name string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		args = append([]string{name, "-data-dir", data}, args...)
		out, err := exec.CommandContext(ctx, bin, args...).Output()
		if err != nil {
			t.Fatalf("openplexer-hub %s failed: %v", name, err)
		}
		return out
	}
	admin("init")
	f.lead = parseToken(t, "add-member lead", admin("add-member", "-id", "lead", "-role", "lead"))
	f.member = parseToken(t, "add-member member", admin("add-member", "-id", "member"))
	f.revoked = parseToken(t, "add-member revoked", admin("add-member", "-id", "revoked"))
	admin("remove-member", "-id", "revoked")
	for _, id := range []string{*project, *secondProject} {
		admin("add-project", "-id", id, "-repo", seedRepo)
	}
	payload := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(payload, []byte(bundleJSON(*project, seedRepo, 1, "")), 0600); err != nil {
		t.Fatal(err)
	}
	admin("publish-policy", "-project", *project, "-file", payload)

	var stderr bytes.Buffer
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), bin, "serve", "-data-dir", data, "-listen", addr)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start openplexer-hub on %s: %v", addr, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	f.origin = "http://" + addr
	const startupSeconds = 60
	ctx, cancel := context.WithTimeout(t.Context(), startupSeconds*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		res, err := f.send(ctx, http.MethodGet, policyKeysPath, "", "", "")
		if err == nil && res.status == http.StatusOK {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("openplexer-hub exited while starting on %s: %v\n%s", addr, err, f.redact(stderr.String()))
		case <-ctx.Done():
			t.Fatalf("openplexer-hub did not answer on %s within %d seconds", addr, startupSeconds)
		case <-ticker.C:
		}
	}
}

func projectPath(id string) string { return "/api/v1/projects/" + id }
func eventsPath(id string) string  { return "/api/v1/rooms/" + id + "/events" }
func claimsPath(id string) string  { return "/api/v1/rooms/" + id + "/claims" }

func bundleJSON(id, repo string, version int, extra string) string {
	return fmt.Sprintf(`{"areas":[],"members":{},"project_id":%s,"repos":[%s],"roles":{},"version":%d%s}`,
		jsonString(id), jsonString(repo), version, extra)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (f *fixture) send(ctx context.Context, method, path, token, body, key string, authorization ...string) (response, error) {
	req, err := http.NewRequestWithContext(ctx, method, f.origin+path, strings.NewReader(body))
	if err != nil {
		return response{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, value := range authorization {
		req.Header.Add("Authorization", value)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return f.do(req)
}

func (f *fixture) do(req *http.Request) (response, error) {
	res, err := f.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	return response{res.StatusCode, res.Header, data}, err
}

func (f *fixture) call(t *testing.T, method, path, token, body string, want int, keys ...string) response {
	t.Helper()
	key := ""
	if len(keys) > 0 {
		key = keys[0]
	}
	res, err := f.send(t.Context(), method, path, token, body, key)
	path = f.redact(path)
	if err != nil {
		t.Fatalf("%s %s failed: %s", method, path, f.redact(err.Error()))
	}
	res.expect(t, method, path, want)
	return res
}

func (f *fixture) redact(text string) string {
	for _, token := range []string{f.lead, f.member, f.revoked, f.unknown} {
		if token != "" {
			text = strings.ReplaceAll(text, token, "[redacted]")
		}
	}
	return text
}

func (res response) expect(t *testing.T, method, path string, want int) {
	t.Helper()
	if res.status != want {
		t.Fatalf("%s %s: status %d, want %d", method, path, res.status, want)
	}
	if want == http.StatusMethodNotAllowed {
		contentType := res.header.Values("Content-Type")
		if len(contentType) == 1 && contentType[0] == "text/plain; charset=utf-8" &&
			string(res.body) == "Method Not Allowed\n" {
			for name := range res.header {
				if strings.EqualFold(name, "Cache-Control") {
					t.Fatalf("%s %s: plain-text 405 must omit Cache-Control", method, path)
				}
			}
			return
		}
	}
	res.expectJSON(t, method, path)
	if want == http.StatusUnauthorized && res.header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("%s %s: missing WWW-Authenticate: Bearer", method, path)
	}
}

func (res response) expectJSON(t *testing.T, method, path string) {
	t.Helper()
	if media, _, _ := mime.ParseMediaType(res.header.Get("Content-Type")); media != "application/json" {
		t.Fatalf("%s %s: expected a JSON response", method, path)
	}
	if res.header.Get("Cache-Control") != "no-store" {
		t.Errorf("%s %s: missing Cache-Control: no-store", method, path)
	}
	if method == http.MethodHead {
		if len(res.body) != 0 {
			t.Errorf("HEAD %s returned a body", path)
		}
	} else if res.status >= 400 {
		var body map[string]string
		decodeWire(t, res.body, &body)
		if len(body) != 1 || strings.TrimSpace(body["error"]) == "" {
			t.Errorf("%s %s: expected an error envelope", method, path)
		}
	}
}

func decodeWire(t *testing.T, data []byte, dst any) {
	t.Helper()
	// Select final members before checking field types, then match names exactly.
	if err := jsonv2.Unmarshal(mustCanonical(t, data), dst, jsontext.AllowInvalidUTF8(true)); err != nil {
		t.Fatalf("invalid %T response (%T)", dst, err)
	}
}
