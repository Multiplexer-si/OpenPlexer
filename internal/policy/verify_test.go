package policy

import "testing"

// The expected texts follow the UTF-8 decoder of the WHATWG Encoding
// Standard: one U+FFFD for each cut sequence, and one for each byte that
// starts no sequence.
func TestDecodeUTF8ReplacesEachInvalidSequenceOnce(t *testing.T) {
	for _, c := range []struct{ name, raw, want string }{
		{"valid text is unchanged", "a\u00e9\u20ac\U0001F600", "a\u00e9\u20ac\U0001F600"},
		{"a byte order mark at the start is removed", "\xEF\xBB\xBFa", "a"},
		{"a byte order mark after the start stays", "a\xEF\xBB\xBF", "a\xEF\xBB\xBF"},
		{"a three-byte sequence cut after two bytes", "\xE2\x82A", "\ufffdA"},
		{"a four-byte sequence cut after two bytes", "\xF0\x9FA", "\ufffdA"},
		{"a sequence cut by the end of the text", "a\xE2\x82", "a\ufffd"},
		{"an overlong form is two invalid bytes", "\xC0\x80", "\ufffd\ufffd"},
		{"an encoded surrogate is three invalid bytes", "\xED\xA0\x80", "\ufffd\ufffd\ufffd"},
		{"a lone continuation byte", "a\x80b", "a\ufffdb"},
	} {
		if got := string(decodeUTF8([]byte(c.raw))); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParseKeySetRejectsWhatTheSchemaRejects(t *testing.T) {
	for text, valid := range map[string]bool{
		`{"keys": []}`:               true,
		`{"keys": [{"kty": "EC"}]}`:  true,
		`{}`:                         false,
		`{"keys": null}`:             false,
		`{"keys": {}}`:               false,
		`{"keys": [1]}`:              false,
		`{"keys": [null]}`:           false,
		`[]`:                         false,
		`{"keys": []} {"keys": []}`:  false,
		`not JSON`:                   false,
		`{"keys": [], "unknown": 1}`: true,
	} {
		if _, err := ParseKeySet([]byte(text)); (err == nil) != valid {
			t.Errorf("%s: valid = %v, want %v (%v)", text, err == nil, valid, err)
		}
	}
}
