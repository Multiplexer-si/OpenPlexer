package policy

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
)

const MaxKeySetBytes = 64 << 10

// FingerprintKeySet validates every public key and hashes the normalized key set.
func FingerprintKeySet(text []byte) (string, error) {
	_, fingerprint, err := NormalizeKeySet(text)
	return fingerprint, err
}

// NormalizeKeySet returns the canonical public pins and their fingerprint.
func NormalizeKeySet(text []byte) ([]byte, string, error) {
	invalid := errors.New("invalid policy key set")
	if len(text) > MaxKeySetBytes {
		return nil, "", invalid
	}
	var doc struct {
		Keys jsontext.Value `json:"keys"`
	}
	var keys []map[string]any
	if jsonv2.Unmarshal(text, &doc, jsonv2.RejectUnknownMembers(true)) != nil || jsonv2.Unmarshal(doc.Keys, &keys) != nil || len(keys) == 0 {
		return nil, "", invalid
	}
	seen := make(map[string]bool, len(keys))
	normalized := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		for name := range key {
			switch name {
			case "kty", "crv", "kid", "x", "y", "alg", "use", "key_ops":
			default:
				return nil, "", invalid
			}
		}
		kid, _ := key["kid"].(string)
		if kid == "" || seen[kid] {
			return nil, "", invalid
		}
		if _, err := ES256PublicKey(key); err != nil {
			return nil, "", invalid
		}
		seen[kid] = true
		normalized = append(normalized, map[string]string{
			"kid": kid, "kty": "EC", "crv": "P-256", "x": key["x"].(string), "y": key["y"].(string),
		})
	}
	slices.SortFunc(normalized, func(a, b map[string]string) int { return cmp.Compare(a["kid"], b["kid"]) })
	encoded, err := jsonv2.Marshal(map[string][]map[string]string{"keys": normalized})
	if err != nil {
		return nil, "", invalid
	}
	canonical := jsontext.Value(encoded)
	if err := canonical.Canonicalize(); err != nil {
		return nil, "", invalid
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}
