package hub

import (
	"bytes"
	"crypto/ecdsa"

	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/audit"
	"github.com/multiplexer-si/openplexer/internal/policy"
)

func TestTypeScriptSignedVectors(t *testing.T) {
	data, err := os.ReadFile("../../apps/hub-cloudflare/test/vectors/ts-signed.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Policy struct {
			Bundle    json.RawMessage
			JWKS      json.RawMessage
			Signature string
		}
		Audit struct {
			Export    json.RawMessage
			Payload   json.RawMessage
			JWKS      json.RawMessage
			Signature string
		}
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}

	checkKid := func(t *testing.T, jwks json.RawMessage, derive func(*ecdsa.PublicKey) string) {
		t.Helper()
		var doc struct{ Keys []map[string]any }
		if err := json.Unmarshal(jwks, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Keys) != 1 {
			t.Fatalf("got %d keys, want one", len(doc.Keys))
		}
		key, err := policy.ES256PublicKey(doc.Keys[0])
		if err != nil {
			t.Fatal(err)
		}
		if want := derive(key); doc.Keys[0]["kid"] != want {
			t.Errorf("kid = %v, want %s", doc.Keys[0]["kid"], want)
		}
	}
	checkPayload := func(t *testing.T, signed []byte, payload json.RawMessage) {
		t.Helper()
		want, err := audit.Canonical(payload)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(signed, want) {
			t.Errorf("signed payload = %s, want %s", signed, want)
		}
	}

	t.Run("policy", func(t *testing.T) {
		keys, err := policy.ParseKeySet(vectors.Policy.JWKS)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := policy.VerifySignedBundle(vectors.Policy.Signature, keys)
		if err != nil {
			t.Fatal(err)
		}
		checkPayload(t, verified.Payload, vectors.Policy.Bundle)
		checkKid(t, vectors.Policy.JWKS, keyID)
	})

	t.Run("checkpoint", func(t *testing.T) {
		result := audit.VerifyExport(vectors.Audit.Export, vectors.Audit.JWKS, vectors.Audit.Signature)
		if !result.OK {
			t.Fatal(result.Diagnostic())
		}
		if result.Entries < 2 || result.CheckpointEntries == nil || *result.CheckpointEntries != result.Entries {
			t.Fatalf("checkpoint must cover the whole chain of at least two records: %+v", result)
		}
		body, err := base64.RawURLEncoding.Strict().DecodeString(strings.Split(vectors.Audit.Signature, ".")[1])
		if err != nil {
			t.Fatal(err)
		}
		checkPayload(t, body, vectors.Audit.Payload)
		checkKid(t, vectors.Audit.JWKS, checkpointKeyID)
	})
}
