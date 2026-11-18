package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
)

// Source delivers the newest signed bundle that it holds, as a compact
// JWS. The gate verifies the token, so a source needs no trust.
type Source interface {
	Fetch(ctx context.Context) (token string, err error)
}

// FileSource reads a message of spec/policy/v1/signed-bundle.schema.json
// from a file and delivers its signature. Only the signed payload is used
// for policy decisions.
func FileSource(path string) Source {
	return fileSource(path)
}

type fileSource string

func (path fileSource) Fetch(context.Context) (string, error) {
	text, err := os.ReadFile(string(path))
	if err != nil {
		return "", err
	}
	token, err := signedToken(text)
	if err != nil {
		return "", fmt.Errorf("%s is not a signed bundle message: %w", string(path), err)
	}
	return token, nil
}

func signedToken(text []byte) (string, error) {
	// Exact names and last-member semantics, as in the policy contract.
	var message map[string]json.RawMessage
	if err := json.Unmarshal(text, &message); err != nil {
		return "", err
	}
	var version *float64
	var signature *string
	if json.Unmarshal(message["version"], &version) != nil || version == nil ||
		math.Abs(*version) > 1<<53-1 || math.Trunc(*version) != *version || message["bundle"] == nil ||
		json.Unmarshal(message["signature"], &signature) != nil || signature == nil {
		return "", errors.New("expected bundle, integer version and string signature")
	}
	return *signature, nil
}
