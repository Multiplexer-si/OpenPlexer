package policy

import "testing"

func TestParsersRejectALoneSurrogate(t *testing.T) {
	documents := map[string]func(text string) error{
		"bundle": func(text string) error {
			_, err := ParseBundle([]byte(`{"version":1,"project_id":"p","repos":["api"],"areas":[{"name":"w","patterns":["` + text + `"]}],"roles":{},"members":{}}`))
			return err
		},
		"query": func(text string) error {
			_, err := ParseQuery([]byte(`{"repo":"api","path":"` + text + `","action":"read","member":"m","now":"2026-01-15T12:00:00Z"}`))
			return err
		},
		"override": func(text string) error {
			_, err := ParseOverride([]byte(`{"id":"o","area":"` + text + `","actions":["read"],"scope":"until","expires_at":"2026-01-15T12:00:00Z"}`))
			return err
		},
	}
	texts := map[string]struct {
		text  string
		valid bool
	}{
		"a high surrogate with no low surrogate":   {`a\ud800/**`, false},
		"a low surrogate with no high surrogate":   {`a\udc00/**`, false},
		"a high surrogate before a character":      {`\ud83dx`, false},
		"a high surrogate before another escape":   {"\\ud83d\\u0041", false},
		"two high surrogates":                      {`\ud83d\ud83d`, false},
		"a high surrogate at the end":              {`a\uD83D`, false},
		"a surrogate pair":                         {"\\ud83d\\ude00", true},
		"a surrogate pair in uppercase":            {"\\uD83D\\uDE00", true},
		"an escaped backslash before u and digits": {`\\ud800`, true},
		"the replacement character":                {"a\\ufffd/\ufffd", true},
	}
	for document, parse := range documents {
		for name, c := range texts {
			t.Run(document+"/"+name, func(t *testing.T) {
				err := parse(c.text)
				if valid := err == nil; valid != c.valid {
					t.Fatalf("valid = %v, want %v (%v)", valid, c.valid, err)
				}
				if err != nil {
					requireReasons(t, err)
				}
			})
		}
	}
}
