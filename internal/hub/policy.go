package hub

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

const MaxPolicyBytes = policy.MaxBundleBytes

const (
	policyAlg = "ES256"
	policyTyp = "mp-policy+jws"
)

var base64URL = base64.RawURLEncoding.EncodeToString

type SignedBundle struct {
	Version   int64           `json:"version"`
	Bundle    json.RawMessage `json:"bundle"`
	Signature string          `json:"signature"`
}

func (s *Store) PublishPolicy(project string, text []byte) (SignedBundle, error) {
	var signed SignedBundle
	err := s.update(func(st *state) error {
		var err error
		signed, err = s.publishPolicy(st, project, text, nil)
		return err
	})
	return signed, err
}

func (s *Store) publishPolicy(st *state, project string, text []byte, actor *string) (SignedBundle, error) {
	p, exists := st.Projects[project]
	if !exists {
		return SignedBundle{}, fmt.Errorf("%w: project %q", errNotFound, project)
	}
	if len(text) > MaxPolicyBytes {
		return SignedBundle{}, fmt.Errorf("%w: policy exceeds %d bytes", errInvalid, MaxPolicyBytes)
	}
	bundle, err := policy.ParseBundle(text)
	if err != nil {
		return SignedBundle{}, fmt.Errorf("%w: policy: %w", errInvalid, err)
	}
	if bundle.ProjectID != p.ID || len(bundle.Repos) != 1 || bundle.Repos[0] != p.Repo {
		return SignedBundle{}, fmt.Errorf("%w: bundle must target project %q and its repository %q", errInvalid, p.ID, p.Repo)
	}
	canonical, err := audit.Canonical(text)
	if err != nil {
		return SignedBundle{}, fmt.Errorf("%w: policy cannot be canonicalized", errInvalid)
	}
	if len(canonical) > MaxPolicyBytes {
		return SignedBundle{}, fmt.Errorf("%w: canonical policy exceeds %d bytes", errInvalid, MaxPolicyBytes)
	}
	if latest, exists := st.Policies[project]; exists && bundle.Version <= latest.Version {
		// The display copy may have been escaped by the file's JSON encoder.
		previous, err := audit.Canonical(latest.Bundle)
		if err != nil {
			return SignedBundle{}, err
		}
		if bundle.Version == latest.Version && bytes.Equal(canonical, previous) {
			return latest, nil
		}
		return SignedBundle{}, fmt.Errorf("%w: policy version must be higher than %d", errConflict, latest.Version)
	}
	for id := range bundle.Members {
		if _, exists := st.Members[id]; !exists {
			return SignedBundle{}, fmt.Errorf("%w: members references an unknown member", errInvalid)
		}
	}
	for _, grant := range bundle.TemporaryGrants {
		if _, exists := st.Members[grant.MemberID]; !exists {
			return SignedBundle{}, fmt.Errorf("%w: temporary_grants member_id references an unknown member", errInvalid)
		}
	}
	key, err := signingKey(st)
	if err != nil {
		return SignedBundle{}, err
	}
	token, err := signES256(key, policyTyp, keyID(&key.PublicKey), canonical)
	if err != nil {
		return SignedBundle{}, err
	}
	signed := SignedBundle{Version: bundle.Version, Bundle: canonical, Signature: token}
	st.Policies[project] = signed
	err = st.appendAudit(audit.Record{ActorMemberID: actor, Action: "policy.updated", Target: "project:" + project,
		Meta: map[string]any{"project_id": project, "version": bundle.Version}}, s.now())
	return signed, err
}

func signES256(key *ecdsa.PrivateKey, typ, kid string, payload []byte) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": policyAlg, "typ": typ, "kid": kid})
	if err != nil {
		return "", err
	}
	input := base64URL(header) + "." + base64URL(payload)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	var signature [64]byte
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return input + "." + base64URL(signature[:]), nil
}

func signingKey(st *state) (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), st.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("read policy signing key: %w", err)
	}
	return key, nil
}

func keyID(key *ecdsa.PublicKey) string {
	point := elliptic.Marshal(key.Curve, key.X, key.Y)
	digest := sha256.Sum256(point)
	return base64URL(digest[:])
}

// PolicyKeys returns the same public key set that the HTTP endpoint advertises.
func (s *Store) PolicyKeys() (keys map[string]any, err error) {
	err = s.view(func(st *state) error {
		keys, err = publicKeys(st)
		return err
	})
	return
}

func publicKeys(st *state) (map[string]any, error) {
	key, err := signingKey(st)
	if err != nil {
		return nil, err
	}
	return map[string]any{"keys": []map[string]string{publicJWK(&key.PublicKey, keyID(&key.PublicKey))}}, nil
}

func publicJWK(key *ecdsa.PublicKey, kid string) map[string]string {
	return map[string]string{
		"alg": policyAlg, "use": "sig", "kid": kid,
		"kty": "EC", "crv": "P-256",
		"x": base64URL(key.X.FillBytes(make([]byte, 32))),
		"y": base64URL(key.Y.FillBytes(make([]byte, 32))),
	}
}

// PolicyVersion reads the current published version without recording a fetch.
func (s *Store) PolicyVersion(project string) (int64, error) {
	var version int64
	err := s.view(func(st *state) error {
		signed, ok := st.Policies[project]
		if !ok {
			return errNotFound
		}
		version = signed.Version
		return nil
	})
	return version, err
}

// PolicyVersionHeader names the version header for policy downloads and HEAD requests.
const PolicyVersionHeader = "OpenPlexer-Policy-Version"

func (s *Store) policyBundleEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w = headWriter{w}
	}
	s.endpoint(0, func(st *state, req request) (any, error) {
		signed, ok := st.Policies[req.project()]
		if !ok {
			return nil, errNotFound
		}
		if req.Method == http.MethodGet {
			if err := s.observe(st.Members[req.member].EnrollmentID, req.project(), &signed.Version); err != nil {
				return nil, err
			}
		}
		w.Header().Set(PolicyVersionHeader, strconv.FormatInt(signed.Version, 10))
		return signed, nil
	})(w, r)
}
