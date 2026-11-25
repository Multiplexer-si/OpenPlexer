package hubconformance

import (
	"bytes"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

func (f *fixture) jwks(t *testing.T) {
	var policyIDs, auditIDs map[string]bool
	t.Run("policy", func(t *testing.T) { policyIDs = f.keyIDs(t, policyKeysPath, "") })
	t.Run("audit", func(t *testing.T) { auditIDs = f.keyIDs(t, auditKeysPath, "audit-") })
	t.Run("separate-key-pairs", func(t *testing.T) {
		if policyIDs == nil {
			policyIDs = f.keyIDs(t, policyKeysPath, "")
		}
		if auditIDs == nil {
			auditIDs = f.keyIDs(t, auditKeysPath, "audit-")
		}
		for id := range auditIDs {
			if policyIDs[id] {
				t.Fatal("policy and audit JWKS reuse a signing key")
			}
		}
	})
}

func (f *fixture) keyIDs(t *testing.T, path, prefix string) map[string]bool {
	t.Helper()
	res := f.call(t, get, path, "", "", http.StatusOK)
	if _, err := policy.ParseKeySet(res.body); err != nil {
		t.Fatal(err)
	}
	var jwks struct{ Keys []map[string]any }
	decode(t, res.body, &jwks)
	if len(jwks.Keys) == 0 {
		t.Fatalf("%s contains no public keys", path)
	}
	ids := make(map[string]bool, len(jwks.Keys))
	for i, key := range jwks.Keys {
		if _, private := key["d"]; private || key["kty"] != "EC" || key["crv"] != "P-256" {
			t.Fatalf("%s key %d must be a public P-256 key", path, i)
		}
		if alg, present := key["alg"]; present && alg != "ES256" {
			t.Fatalf("%s key %d must use ES256", path, i)
		}
		if use, present := key["use"]; present && use != "sig" {
			t.Fatalf("%s key %d must be a signature key", path, i)
		}
		if value, present := key["key_ops"]; present {
			if ops, ok := value.([]any); !ok || len(ops) != 1 || ops[0] != "verify" {
				t.Fatalf("%s key %d must permit only verification", path, i)
			}
		}
		point := make([]byte, 1, 65)
		point[0] = 4 // Uncompressed SEC 1 point used for the key ID.
		for _, field := range []string{"x", "y"} {
			encoded, _ := key[field].(string)
			coord, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
			if err != nil || len(coord) != 32 || base64.RawURLEncoding.EncodeToString(coord) != encoded {
				t.Fatalf("%s key %d has an invalid %s coordinate", path, i, field)
			}
			point = append(point, coord...)
		}
		if !elliptic.P256().IsOnCurve(new(big.Int).SetBytes(point[1:33]), new(big.Int).SetBytes(point[33:])) {
			t.Fatalf("%s key %d is off the P-256 curve", path, i)
		}
		digest := sha256.Sum256(point)
		id := base64.RawURLEncoding.EncodeToString(digest[:])
		if key["kid"] != prefix+id || ids[id] {
			t.Fatalf("%s key %d has an incorrect or duplicate kid", path, i)
		}
		ids[id] = true
	}
	return ids
}

func (f *fixture) repo(t *testing.T, id string) string {
	t.Helper()
	res := f.call(t, get, projectPath(id), f.member, "", http.StatusOK)
	var project struct {
		ID   string `json:"id"`
		Repo string `json:"repo"`
	}
	decodeWire(t, res.body, &project)
	if project.ID != id || project.Repo == "" {
		t.Fatalf("project %s did not return its ID and repo", id)
	}
	return project.Repo
}

type signedBundle struct {
	Version   int             `json:"version"`
	Bundle    json.RawMessage `json:"bundle"`
	Signature string          `json:"signature"`
}

func verifyBundle(t *testing.T, res response, keys policy.KeySet, id, repo string, version int) signedBundle {
	t.Helper()
	var signed signedBundle
	decodeWire(t, res.body, &signed)
	verified, err := policy.VerifySignedBundle(signed.Signature, keys)
	if err != nil {
		t.Fatalf("policy for %s version %d did not verify", id, version)
	}
	if signed.Version != version || verified.Bundle.Version != int64(version) ||
		verified.Bundle.ProjectID != id || len(verified.Bundle.Repos) != 1 || verified.Bundle.Repos[0] != repo {
		t.Fatalf("signed policy does not match project %s, its repo, and version %d", id, version)
	}
	canonical, err := audit.Canonical(signed.Bundle)
	if err != nil || !bytes.Equal(canonical, verified.Payload) {
		t.Fatal("policy display bundle does not match the signed canonical payload")
	}
	return signed
}

func (f *fixture) projectsAndPolicy(t *testing.T) {
	id := *project
	repo := f.repo(t, id)
	if f.repo(t, *secondProject) != repo {
		t.Fatal("the two fixture projects must share a repo")
	}
	keys, err := policy.ParseKeySet(f.call(t, get, policyKeysPath, "", "", http.StatusOK).body)
	if err != nil {
		t.Fatal(err)
	}
	path := projectPath(id)
	t.Run("read-projects-and-seeded-policy", func(t *testing.T) {
		verifyBundle(t, f.call(t, get, path+"/policy.bundle", f.member, "", http.StatusOK), keys, id, repo, 1)
	})
	t.Run("missing-project-and-bundle", func(t *testing.T) {
		missing := projectPath(f.missing)
		f.call(t, get, missing, f.member, "", http.StatusNotFound)
		f.call(t, get, missing+"/policy.bundle", f.member, "", http.StatusNotFound)
		f.call(t, put, missing+"/policy", f.lead, bundleJSON(f.missing, repo, 1, ""), http.StatusNotFound)
		f.call(t, get, projectPath(*secondProject)+"/policy.bundle", f.member, "", http.StatusNotFound)
	})
	t.Run("publish-and-version-floor", func(t *testing.T) {
		text := bundleJSON(id, repo, 2, `,"label":"updated"`)
		published := verifyBundle(t, f.call(t, put, path+"/policy", f.lead, text, http.StatusOK), keys, id, repo, 2)
		canonical, err := audit.Canonical([]byte(text))
		if err != nil || !bytes.Equal(canonical, mustCanonical(t, published.Bundle)) {
			t.Fatal("published bundle differs from the submitted policy")
		}
		before := f.readAudit(t)
		t.Run("identical-republish", func(t *testing.T) {
			retry := verifyBundle(t, f.call(t, put, path+"/policy", f.lead, " \n"+text, http.StatusOK), keys, id, repo, 2)
			if retry.Signature != published.Signature {
				t.Error("republish changed the signature")
			}
		})
		t.Run("conflicting-version", func(t *testing.T) {
			f.call(t, put, path+"/policy", f.lead, bundleJSON(id, repo, 2, `,"label":"conflict"`), http.StatusConflict)
		})
		t.Run("stale-version", func(t *testing.T) {
			f.call(t, put, path+"/policy", f.lead, bundleJSON(id, repo, 1, ""), http.StatusConflict)
		})
		t.Run("invalid-bundle", func(t *testing.T) {
			f.call(t, put, path+"/policy", f.lead, "{}", http.StatusBadRequest)
			f.call(t, put, path+"/policy", f.lead, bundleJSON(f.missing, repo, 3, ""), http.StatusBadRequest)
		})
		current := verifyBundle(t, f.call(t, get, path+"/policy.bundle", f.member, "", http.StatusOK), keys, id, repo, 2)
		if current.Signature != published.Signature || len(f.readAudit(t).entries) != len(before.entries) {
			t.Fatal("retry or rejected publication changed the stored policy or audit log")
		}
	})
	t.Run("body-limit", func(t *testing.T) {
		text := bundleJSON(id, repo, 3, "")
		f.call(t, put, path+"/policy", f.lead, pad(text, policyLimit+1), http.StatusRequestEntityTooLarge)
		verifyBundle(t, f.call(t, put, path+"/policy", f.lead, pad(text, policyLimit), http.StatusOK), keys, id, repo, 3)
	})
	t.Run("canonical-limit", func(t *testing.T) {
		// Canonical JSON expands 1e-6 to 0.000001, adding four bytes.
		text := func(version, canonicalSize int) string {
			const extra = `,"decimal":1e-6,"padding":"%s"`
			base := strings.TrimSuffix(string(mustCanonical(t, []byte(bundleJSON(id, repo, version, "")))), "}")
			empty := base + fmt.Sprintf(extra, "") + "}"
			padding := canonicalSize - len(mustCanonical(t, []byte(empty)))
			return base + fmt.Sprintf(extra, strings.Repeat("x", padding)) + "}"
		}
		tooLarge, exact := text(5, policyLimit+1), text(4, policyLimit)
		for i, body := range []string{exact, tooLarge} {
			if len(body) >= policyLimit || len(mustCanonical(t, []byte(body))) != policyLimit+i {
				t.Fatal("invalid canonical limit fixture")
			}
		}
		f.call(t, put, path+"/policy", f.lead, tooLarge, http.StatusBadRequest)
		verifyBundle(t, f.call(t, put, path+"/policy", f.lead, exact, http.StatusOK), keys, id, repo, 4)
		verifyBundle(t, f.call(t, get, path+"/policy.bundle", f.member, "", http.StatusOK), keys, id, repo, 4)
		// Shrink the local hub's state before the Room cases start writing.
		verifyBundle(t, f.call(t, put, path+"/policy", f.lead, bundleJSON(id, repo, 5, ""), http.StatusOK), keys, id, repo, 5)
	})
}

func mustCanonical(t *testing.T, raw []byte) []byte {
	t.Helper()
	canonical, err := audit.Canonical(raw)
	if err != nil {
		t.Fatal("response is not canonicalizable JSON")
	}
	return canonical
}
