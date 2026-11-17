package policy

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	signatureAlgorithm = "ES256"
	signatureType      = "mp-policy+jws"
)

// KeySet is a JSON Web Key Set that ParseKeySet read.
type KeySet struct {
	keys []map[string]any
}

// ParseKeySet reads a key set from JSON text and validates it against
// spec/policy/v1/jwks.schema.json. It does not check that a key is usable.
// VerifySignedBundle checks the one key that it chooses.
func ParseKeySet(text []byte) (KeySet, error) {
	var doc struct {
		Keys *[]map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(text, &doc); err != nil {
		return KeySet{}, fmt.Errorf("the key set is not valid: %w", err)
	}
	if doc.Keys == nil {
		return KeySet{}, errors.New("the key set has no keys list")
	}
	if slices.ContainsFunc(*doc.Keys, func(key map[string]any) bool { return key == nil }) {
		return KeySet{}, errors.New("a key of the key set is not an object")
	}
	return KeySet{keys: *doc.Keys}, nil
}

// Verified is a bundle whose signature verified.
type Verified struct {
	Bundle *Bundle
	// Payload is the signed JSON text of the bundle. It holds the fields
	// that Bundle does not model.
	Payload []byte
}

// VerifySignedBundle verifies a compact JWS against a key set, under the
// steps of spec/policy/README.md, and returns the bundle that the token
// signs. The error says which step failed.
func VerifySignedBundle(token string, keys KeySet) (*Verified, error) {
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return nil, fmt.Errorf("the token has %d segments, and a compact JWS has 3", len(segments))
	}
	header, err := readHeader(segments[0])
	if err != nil {
		return nil, err
	}
	unencoded, err := checkHeader(header)
	if err != nil {
		return nil, err
	}
	key, err := keys.choose(header)
	if err != nil {
		return nil, err
	}
	signature, ok := decodeBase64URL(segments[2])
	if !ok || len(signature) != 64 {
		return nil, errors.New("the signature segment is not a 64-byte ES256 signature in base64url")
	}
	digest := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	r, s := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(key, digest[:], r, s) {
		return nil, errors.New("the signature does not verify under the key")
	}
	payload, err := readPayload(segments[1], unencoded)
	if err != nil {
		return nil, err
	}
	bundle, err := ParseBundle(payload)
	if err != nil {
		return nil, fmt.Errorf("the signed payload is not a bundle: %w", err)
	}
	return &Verified{Bundle: bundle, Payload: payload}, nil
}

func readHeader(segment string) (map[string]any, error) {
	raw, ok := decodeBase64URL(segment)
	if !ok {
		return nil, errors.New("the header segment is not base64url")
	}
	raw = bytes.TrimPrefix(raw, byteOrderMark)
	if !utf8.Valid(raw) {
		return nil, errors.New("the header is not UTF-8")
	}
	var header map[string]any
	if err := json.Unmarshal(raw, &header); err != nil || header == nil {
		return nil, errors.New("the header is not a JSON object")
	}
	return header, nil
}

// checkHeader reports whether the payload segment is the payload itself,
// as RFC 7797 defines, and not its base64url form.
func checkHeader(header map[string]any) (unencoded bool, err error) {
	if header["alg"] != signatureAlgorithm {
		return false, fmt.Errorf("the header alg is %v, and it must be %s", header["alg"], signatureAlgorithm)
	}
	if header["typ"] != signatureType {
		return false, fmt.Errorf("the header typ is %v, and it must be %s", header["typ"], signatureType)
	}
	crit, present := header["crit"]
	if !present {
		return false, nil
	}
	names, ok := crit.([]any)
	if !ok || len(names) == 0 || slices.ContainsFunc(names, func(name any) bool { return name != "b64" }) {
		return false, errors.New("the header crit must be a list that names only b64")
	}
	encoded, ok := header["b64"].(bool)
	if !ok {
		return false, errors.New("the header names b64 in crit, and b64 must be true or false")
	}
	return !encoded, nil
}

func (set KeySet) choose(header map[string]any) (*ecdsa.PublicKey, error) {
	kid, hasKid := header["kid"]
	var candidates []map[string]any
	for _, key := range set.keys {
		if isCandidate(key, kid, hasKid) {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("%d keys of the key set fit the header, and exactly one must", len(candidates))
	}
	return publicKey(candidates[0])
}

func isCandidate(key map[string]any, kid any, hasKid bool) bool {
	if key["kty"] != "EC" || key["crv"] != "P-256" {
		return false
	}
	if hasKid {
		if _, ok := kid.(string); !ok || key["kid"] != kid {
			return false
		}
	}
	if alg, present := key["alg"]; present && alg != signatureAlgorithm {
		return false
	}
	if use, present := key["use"]; present && use != "sig" {
		return false
	}
	if ops, present := key["key_ops"]; present {
		list, ok := ops.([]any)
		if !ok || !slices.Contains(list, any("verify")) || !distinctStrings(list) {
			return false
		}
	}
	if ext, present := key["ext"]; present {
		if _, ok := ext.(bool); !ok {
			return false
		}
	}
	return true
}

func distinctStrings(list []any) bool {
	seen := map[string]bool{}
	for _, element := range list {
		text, ok := element.(string)
		if !ok || seen[text] {
			return false
		}
		seen[text] = true
	}
	return true
}

func publicKey(key map[string]any) (*ecdsa.PublicKey, error) {
	if _, private := key["d"]; private {
		return nil, errors.New("the chosen key is a private key")
	}
	if ops, _ := key["key_ops"].([]any); len(ops) > 1 {
		return nil, errors.New("the chosen key allows an operation other than verify")
	}
	point := []byte{4}
	for _, name := range []string{"x", "y"} {
		text, _ := key[name].(string)
		coordinate, err := base64.RawURLEncoding.Strict().DecodeString(text)
		if err != nil || len(coordinate) != 32 {
			return nil, fmt.Errorf("the %s of the chosen key is not 32 bytes in base64url", name)
		}
		point = append(point, coordinate...)
	}
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, errors.New("the chosen key is not a point of the curve P-256")
	}
	return public, nil
}

func readPayload(segment string, unencoded bool) ([]byte, error) {
	if !unencoded {
		raw, ok := decodeBase64URL(segment)
		if !ok {
			return nil, errors.New("the payload segment is not base64url")
		}
		return decodeUTF8(raw), nil
	}
	if strings.ContainsFunc(segment, func(r rune) bool { return r > 0x7f }) {
		return nil, errors.New("the unencoded payload is not ASCII")
	}
	return []byte(segment), nil
}

// decodeBase64URL follows the base64url rule of spec/policy/README.md, which
// accepts white space and padding that RFC 7515 rejects.
func decodeBase64URL(segment string) ([]byte, bool) {
	if strings.ContainsAny(segment, "+/") {
		return nil, false
	}
	text := strings.Map(func(r rune) rune {
		if strings.ContainsRune("\t\n\f\r ", r) {
			return -1
		}
		return r
	}, segment)
	if len(text)%4 == 0 {
		trimmed := strings.TrimSuffix(text, "=")
		text = strings.TrimSuffix(trimmed, "=")
	}
	if len(text)%4 == 1 {
		return nil, false
	}
	alphabet := func(r rune) bool {
		return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
	}
	if strings.ContainsFunc(text, func(r rune) bool { return !alphabet(r) }) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	return raw, err == nil
}

var byteOrderMark = []byte{0xEF, 0xBB, 0xBF}

// decodeUTF8 is the UTF-8 decode of the WHATWG Encoding Standard. It
// removes a byte order mark at the start and replaces each invalid sequence
// with one U+FFFD. Go replaces each byte of a cut sequence, which gives
// another text.
func decodeUTF8(raw []byte) []byte {
	raw = bytes.TrimPrefix(raw, byteOrderMark)
	if utf8.Valid(raw) {
		return raw
	}
	var out []byte
	const first, last = 0x80, 0xBF
	needed, seen, codePoint := 0, 0, rune(0)
	lower, upper := byte(first), byte(last)
	reset := func() { needed, seen, codePoint, lower, upper = 0, 0, 0, first, last }
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if needed == 0 {
			switch {
			case b <= 0x7F:
				out = append(out, b)
			case b >= 0xC2 && b <= 0xDF:
				needed, codePoint = 1, rune(b&0x1F)
			case b >= 0xE0 && b <= 0xEF:
				if b == 0xE0 {
					lower = 0xA0
				}
				if b == 0xED {
					upper = 0x9F
				}
				needed, codePoint = 2, rune(b&0x0F)
			case b >= 0xF0 && b <= 0xF4:
				if b == 0xF0 {
					lower = 0x90
				}
				if b == 0xF4 {
					upper = 0x8F
				}
				needed, codePoint = 3, rune(b&0x07)
			default:
				out = utf8.AppendRune(out, utf8.RuneError)
			}
			continue
		}
		if b < lower || b > upper {
			reset()
			out = utf8.AppendRune(out, utf8.RuneError)
			i--
			continue
		}
		lower, upper = first, last
		codePoint = codePoint<<6 | rune(b&0x3F)
		if seen++; seen == needed {
			out = utf8.AppendRune(out, codePoint)
			reset()
		}
	}
	if needed != 0 {
		out = utf8.AppendRune(out, utf8.RuneError)
	}
	return out
}
