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
					f.call(t, r.method, r.path, "short", oversized, http.StatusUnauthorized)
					f.call(t, r.method, r.path, f.unknown, oversized, http.StatusRequestEntityTooLarge)
				})
			}
		})
	}
}

func (f *fixture) methods(t *testing.T) {
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
