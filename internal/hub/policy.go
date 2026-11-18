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

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

const MaxPolicyBytes = 1 << 20

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
		signed, err = st.publishPolicy(project, text)
		return err
	})
	return signed, err
}

func (st *state) publishPolicy(project string, text []byte) (SignedBundle, error) {
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
	header, err := json.Marshal(map[string]string{"alg": policyAlg, "typ": policyTyp, "kid": keyID(&key.PublicKey)})
	if err != nil {
		return SignedBundle{}, err
	}
	input := base64URL(header) + "." + base64URL(canonical)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return SignedBundle{}, err
	}
	var signature [64]byte
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	signed := SignedBundle{Version: bundle.Version, Bundle: canonical, Signature: input + "." + base64URL(signature[:])}
	st.Policies[project] = signed
	return signed, nil
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

func publicKeys(st *state) (map[string]any, error) {
	key, err := signingKey(st)
	if err != nil {
		return nil, err
	}
	jwk := map[string]string{
		"alg": policyAlg, "use": "sig", "kid": keyID(&key.PublicKey),
		"kty": "EC", "crv": "P-256",
		"x": base64URL(key.X.FillBytes(make([]byte, 32))),
		"y": base64URL(key.Y.FillBytes(make([]byte, 32))),
	}
	return map[string]any{"keys": []map[string]string{jwk}}, nil
}
