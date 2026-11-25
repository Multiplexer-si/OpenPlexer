package hubconformance

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

const (
	get  = http.MethodGet
	head = http.MethodHead
	post = http.MethodPost
	put  = http.MethodPut

	statusPost  = `{"type":"status","agent":{"id":"session","vendor":"codex","parent":"parent"},"body":{"summary":"working"}}`
	claimPost   = `{"agent":{"id":"session","vendor":"codex"},"item":"work","paths":["src/main.go"],"intent":"edit"}`
	renewPost   = `{"session_id":"session","ttl_seconds":10}`
	releasePost = `{"session_id":"session","outcome":"done"}`
)

type route struct {
	name, method, path, body string
	limit                    int
	public, leadOnly         bool
}

func (f *fixture) routes() []route {
	p, claims := projectPath(*project), claimsPath(*project)
	return []route{
		{name: "policy-jwks", method: get, path: policyKeysPath, public: true},
		{name: "audit-jwks", method: get, path: auditKeysPath, public: true},
		{name: "audit-export", method: get, path: auditPath, leadOnly: true},
		{name: "project", method: get, path: p},
		{name: "policy-bundle", method: get, path: p + "/policy.bundle"},
		{name: "publish-policy", method: put, path: p + "/policy", body: "{}", limit: policyLimit, leadOnly: true},
		{name: "replay-events", method: get, path: eventsPath(*project)},
		{name: "post-event", method: post, path: eventsPath(*project), body: statusPost, limit: roomLimit},
		{name: "create-claim", method: post, path: claims, body: claimPost, limit: roomLimit},
		{name: "renew-claim", method: post, path: claims + "/" + f.missing + "/renew", body: renewPost, limit: roomLimit},
		{name: "release-claim", method: post, path: claims + "/" + f.missing + "/release", body: releasePost, limit: roomLimit},
	}
}

func (f *fixture) auth(t *testing.T) {
	for _, r := range f.routes() {
		t.Run(r.name, func(t *testing.T) {
			for _, tc := range []struct{ name, token string }{
				{"no-token", ""}, {"malformed-token", "short"}, {"unknown-token", f.unknown}, {"revoked-token", f.revoked},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if tc.name == "revoked-token" && tc.token == "" {
						t.Skip("provide -revoked-token-file with a token removed during seeding")
					}
					want := http.StatusUnauthorized
					if r.public {
						want = http.StatusOK
					}
					res := f.call(t, r.method, r.path, tc.token, r.body, want)
					if r.public {
						keyIDs(t, r.path, res)
					}
					if r.path == auditPath && res.header.Get(checkpoint) != "" {
						t.Error("unauthorized export included a checkpoint")
					}
				})
			}
			if r.leadOnly {
				t.Run("member-forbidden", func(t *testing.T) {
					body := r.body
					if r.name == "publish-policy" {
						body = bundleJSON(*project, f.repo(t, *project), 2, "")
					}
					res := f.call(t, r.method, r.path, f.member, body, http.StatusForbidden)
					if r.path == auditPath && res.header.Get(checkpoint) != "" {
						t.Error("forbidden export included a checkpoint")
					}
				})
			}
			if r.limit != 0 {
				t.Run("auth-and-body-order", func(t *testing.T) {
					oversized := pad(r.body, r.limit+1)
					rejectedBody := r.earlyRejectionBody(f.target, oversized)
					f.call(t, r.method, r.path, "short", rejectedBody, http.StatusUnauthorized)
					f.call(t, r.method, r.path, f.unknown, oversized, http.StatusRequestEntityTooLarge)
					for _, fields := range [][]string{
						{"Basic " + f.unknown},
						{"Bearer " + f.unknown, "Bearer " + f.unknown},
						// Workerd folds these fields into a bearer value with a 43-byte token.
						{"Bearer " + strings.Repeat("a", 20), strings.Repeat("b", 21)},
					} {
						res, err := f.send(t.Context(), r.method, r.path, "", rejectedBody, "", fields...)
						if err != nil {
							t.Fatal(f.redact(err.Error()))
						}
						res.expect(t, r.method, r.path, http.StatusUnauthorized)
					}
					commaToken := strings.Repeat("a", 21) + "," + strings.Repeat("a", 21)
					f.call(t, r.method, r.path, commaToken[1:], rejectedBody, http.StatusUnauthorized)
					f.checkCommaBearer(t, r, commaToken, oversized)
				})
			}
		})
	}
}

func (f *fixture) checkCommaBearer(t *testing.T, r route, token, oversized string) {
	t.Helper()
	switch f.target {
	case builtInGo:
		f.call(t, r.method, r.path, token, oversized, http.StatusRequestEntityTooLarge)
	case externalHub:
		path := f.redact(r.path)
		res, err := f.send(t.Context(), r.method, r.path, token, oversized, "")
		if err != nil {
			t.Fatalf("%s %s failed: %s", r.method, path, f.redact(err.Error()))
		}
		if res.status != http.StatusUnauthorized && res.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s %s: status %d, want 401 or 413", r.method, path, res.status)
		}
		res.expect(t, r.method, path, res.status)
	}
	f.call(t, r.method, r.path, token, r.body, http.StatusUnauthorized)
}

func (r route) earlyRejectionBody(target hubTarget, oversized string) string {
	// Wrangler dev can truncate a 401 while a rejected upload is still in flight.
	if target == localWrangler {
		return r.body
	}
	return oversized
}

func (f *fixture) methods(t *testing.T) {
	t.Run("escaped-paths", f.escapedPaths)
	seen := map[string]bool{}
	for _, r := range f.routes() {
		if r.method == get {
			t.Run(r.name+"/head", func(t *testing.T) {
				token := f.member
				if r.leadOnly {
					token = f.lead
				} else if r.public {
					token = ""
				}
				res := f.call(t, head, r.path, token, "", http.StatusOK)
				if r.path == auditPath {
					f.readAudit(t, res.header.Get(checkpoint))
				}
			})
		}
		if seen[r.path] {
			continue
		}
		seen[r.path] = true
		t.Run(r.name+"/wrong-method", func(t *testing.T) {
			var want []string
			for _, other := range f.routes() {
				if other.path == r.path {
					want = append(want, other.method)
					if other.method == get {
						want = append(want, head)
					}
				}
			}
			res := f.call(t, http.MethodDelete, r.path, f.lead, "", http.StatusMethodNotAllowed)
			got := strings.Split(strings.Join(res.header.Values("Allow"), ","), ",")
			for i := range got {
				got[i] = strings.TrimSpace(got[i])
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("Allow must contain exactly %v", want)
			}
		})
	}
}

func (f *fixture) escapedPaths(t *testing.T) {
	p := projectPath(*project)
	for _, tc := range []struct {
		path, token            string
		workerStatus, goStatus int
	}{
		{path: "/.well-known/%6Awks.json", workerStatus: http.StatusOK},
		{path: p + "/%70olicy.bundle", token: f.member, workerStatus: http.StatusOK},
		{path: "/.well-known%2Fjwks.json", workerStatus: http.StatusNotFound},
		{path: p + "%2Fpolicy.bundle", workerStatus: http.StatusUnauthorized},
		{path: p + "%2Fpolicy.bundle", token: f.member, workerStatus: http.StatusNotFound},
		{path: projectPath("%"), workerStatus: http.StatusBadRequest},
		{path: projectPath("%2"), workerStatus: http.StatusBadRequest},
		{path: projectPath("%GG"), workerStatus: http.StatusBadRequest},
		// Go decodes arbitrary bytes, then fails to find the project.
		{path: projectPath("%FF"), token: f.member, workerStatus: http.StatusBadRequest, goStatus: http.StatusNotFound},
		// Go redirects literal dot segments and treats escaped dots as wildcard values.
		{path: projectPath("."), workerStatus: http.StatusNotFound, goStatus: http.StatusTemporaryRedirect},
		{path: projectPath(".."), workerStatus: http.StatusNotFound, goStatus: http.StatusTemporaryRedirect},
		{path: projectPath("%2E"), workerStatus: http.StatusNotFound, goStatus: http.StatusUnauthorized},
		{path: projectPath(".%2e"), workerStatus: http.StatusNotFound, goStatus: http.StatusUnauthorized},
		{path: projectPath("%2F"), workerStatus: http.StatusNotFound},
		{path: projectPath(""), workerStatus: http.StatusNotFound},
	} {
		// Opaque sends the path verbatim, including escapes rejected by the Go client URL parser.
		req, err := http.NewRequestWithContext(t.Context(), get, f.origin, nil)
		if err != nil {
			t.Fatal(f.redact(err.Error()))
		}
		req.URL.Opaque = tc.path
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		res, err := f.do(req)
		if err != nil {
			t.Fatalf("GET %s failed: %s", tc.path, f.redact(err.Error()))
		}
		// Go can return plain text for parser errors and unmatched routes.
		want := []int{tc.workerStatus}
		if tc.goStatus != 0 {
			switch f.target {
			case builtInGo:
				want[0] = tc.goStatus
			case externalHub:
				// An external hub without -wrangler-dev could use either backend.
				want = append(want, tc.goStatus)
			}
		}
		if !slices.Contains(want, res.status) {
			t.Errorf("GET %s (authenticated: %t): status %d, want %v", tc.path, tc.token != "", res.status, want)
		}
	}
}

func pad(body string, size int) string { return body + strings.Repeat(" ", size-len(body)) }

func TestHub(t *testing.T) {
	f := setup(t)
	t.Run("auth", f.auth)
	t.Run("methods", f.methods)
	t.Run("jwks", f.jwks)
	t.Run("projects-and-policy", f.projectsAndPolicy)
	t.Run("room-events", f.roomEvents)
	t.Run("claims", f.claims)
	t.Run("audit-export", f.auditExport)
	t.Run("concurrency", f.concurrentWrites)
}

func TestHubLiveStream(t *testing.T) {
	t.Skip("#87 adds the shared GET /api/v1/rooms/{room}/stream route")
}
