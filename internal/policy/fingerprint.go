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
	invalid := errors.New("invalid policy key set")
	if len(text) > MaxKeySetBytes {
		return "", invalid
	}
	var doc map[string]jsontext.Value
	var keys []map[string]any
	if jsonv2.Unmarshal(text, &doc) != nil || jsonv2.Unmarshal(doc["keys"], &keys) != nil || len(keys) == 0 {
		return "", invalid
	}
	seen := make(map[string]bool, len(keys))
	normalized := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		kid, _ := key["kid"].(string)
		if kid == "" || seen[kid] {
			return "", invalid
		}
		if _, err := ES256PublicKey(key); err != nil {
			return "", invalid
		}
		seen[kid] = true
		normalized = append(normalized, map[string]string{
			"kid": kid, "kty": "EC", "crv": "P-256", "x": key["x"].(string), "y": key["y"].(string),
		})
	}
	slices.SortFunc(normalized, func(a, b map[string]string) int { return cmp.Compare(a["kid"], b["kid"]) })
	encoded, err := jsonv2.Marshal(map[string][]map[string]string{"keys": normalized})
	if err != nil {
		return "", invalid
	}
	canonical := jsontext.Value(encoded)
	if err := canonical.Canonicalize(); err != nil {
		return "", invalid
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
