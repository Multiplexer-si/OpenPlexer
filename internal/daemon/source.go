package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Source delivers the newest signed bundle that it holds, as a compact
// JWS. The gate verifies the token, so a source needs no trust. Phase 1
// reads a file. Phase 2 asks the hub.
type Source interface {
	Fetch(ctx context.Context) (token string, err error)
}

// FileSource reads a message of spec/policy/v1/signed-bundle.schema.json
// from a file and delivers its signature. It does not read the bundle and
// version copies of the message, because the signed payload is the
// authority.
func FileSource(path string) Source {
	return fileSource(path)
}

type fileSource string

func (path fileSource) Fetch(context.Context) (string, error) {
	text, err := os.ReadFile(string(path))
	if err != nil {
		return "", err
	}
	var message struct {
		Signature *string `json:"signature"`
	}
	if err := json.Unmarshal(text, &message); err != nil {
		return "", fmt.Errorf("%s is not a signed bundle message: %w", string(path), err)
	}
	if message.Signature == nil {
		return "", errors.New(string(path) + " is not a signed bundle message: it has no signature")
	}
	return *message.Signature, nil
}
