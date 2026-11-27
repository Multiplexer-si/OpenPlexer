package enrollment

import (
	"strings"
	"testing"
)

func TestJoinLink(t *testing.T) {
	code, fp := strings.Repeat("A", 43), strings.Repeat("b", 64)
	suffix := "/join/" + code + "#fp=" + fp
	for _, origin := range []string{"https://hub.example.test:443", "http://localhost:8787", "http://127.0.0.1:1", "http://[::1]:1"} {
		link, err := ParseLink(origin + suffix)
		if err != nil || link.Origin != origin || link.Fingerprint != fp || link.code != code {
			t.Fatal("valid link did not parse")
		}
	}
	base := "https://hub.example.test"
	invalid := map[string]string{
		"no scheme": "hub.example.test" + suffix, "insecure": "http://hub.example.test" + suffix,
		"credentials": "https://user:password@hub.example.test" + suffix, "host missing": "https://" + suffix,
		"scheme case": "HTTPS://hub.example.test" + suffix, "path case": base + strings.Replace(suffix, "join", "Join", 1),
		"subpath": base + "/extra" + suffix, "slash": base + strings.Replace(suffix, "#", "/#", 1),
		"query": base + strings.Replace(suffix, "#", "?x=1#", 1), "empty query": base + strings.Replace(suffix, "#", "?#", 1),
		"escaped path":     base + strings.Replace(suffix, "join", "j%6fin", 1),
		"escaped fragment": base + strings.Replace(suffix, "fp", "%66p", 1),
		"duplicate fp":     base + suffix + "&fp=" + fp, "unknown fragment": base + suffix + "&other=1",
		"uppercase fp": base + "/join/" + code + "#fp=" + strings.ToUpper(fp),
		"missing fp":   base + "/join/" + code, "short code": base + "/join/A#fp=" + fp,
		"trailing bits": base + "/join/" + code[:42] + "B#fp=" + fp, "padded code": base + "/join/" + code + "=#fp=" + fp,
		"line break": base + "/join/" + code[:20] + "\n" + code[20:] + "#fp=" + fp,
	}
	for name, address := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseLink(address); err == nil || strings.Contains(err.Error(), code) || strings.Contains(err.Error(), address) {
				t.Fatal("expected a sanitized malformed-link error")
			}
		})
	}
}
