package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestInviteKeySetFingerprint(t *testing.T) {
	const x = "g5-VyBBK1Cw0f6sC137bCeGzJ2NB4v5IQH3tSgixJbs"
	const y = "AQMp4KPB4eNUeEaJ-ohzy8_kDWFvSUEPmhTNIaraFFA"
	const canonical = `{"keys":[{"crv":"P-256","kid":"a","kty":"EC","x":"` + x + `","y":"` + y + `"},` +
		`{"crv":"P-256","kid":"z","kty":"EC","x":"` + x + `","y":"` + y + `"}]}`
	sum := sha256.Sum256([]byte(canonical))
	want := hex.EncodeToString(sum[:])
	key := func(kid string) map[string]any {
		return map[string]any{"x": x, "y": y, "kid": kid, "kty": "EC", "crv": "P-256",
			"alg": "ES256", "use": "sig", "key_ops": []string{"verify"}}
	}
	marshal := func(keys ...map[string]any) string {
		t.Helper()
		raw, err := json.MarshalIndent(map[string]any{"keys": keys}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for _, text := range []string{canonical, marshal(key("z"), key("a"))} {
		if got, err := FingerprintKeySet([]byte(text)); err != nil || got != want {
			t.Fatal("fingerprint did not hash the normalized keys in ID order")
		}
	}
	private, otherOp, badPoint := key("private"), key("operation"), key("point")
	private["d"] = "private"
	otherOp["key_ops"] = []string{"verify", "sign"}
	badPoint["y"] = strings.Repeat("A", 43)
	for _, text := range []string{
		marshal(key("a"), key("a")), marshal(key("")), marshal(private), marshal(otherOp), marshal(badPoint),
		`{"keys":[]}`, strings.Replace(canonical, `"kid":"a"`, `"kid":"b","\u006bid":"a"`, 1),
		canonical + strings.Repeat(" ", MaxKeySetBytes),
	} {
		if _, err := FingerprintKeySet([]byte(text)); err == nil {
			t.Fatal("fingerprint accepted an invalid key set")
		}
	}
}

func runFingerprint(t *testing.T, c fixtureCase) {
	var input struct{ JWKS string }
	var expected struct{ Fingerprint *string }
	if err := json.Unmarshal(c.Input, &input); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(c.Expected, &expected); err != nil {
		t.Fatal(err)
	}
	normalized, got, err := NormalizeKeySet([]byte(input.JWKS))
	want := expected.Fingerprint
	switch {
	case want == nil:
		if err == nil {
			t.Fatal("accepted an invalid key set")
		}
	case err != nil || got != *want:
		t.Fatalf("fingerprint %q, want %q (error: %v)", got, *want, err)
	default:
		if sum := sha256.Sum256(normalized); hex.EncodeToString(sum[:]) != got {
			t.Fatal("fingerprint does not match the normalized pins")
		}
	}
}
