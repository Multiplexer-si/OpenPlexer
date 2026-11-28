// Package enrollment joins a checkout to a hub using a private invite link.
package enrollment

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
)

// Link keeps the invite code private to the enrollment client.
type Link struct {
	Origin      string
	Fingerprint string
	code        string
}

// ParseLink accepts the exact CLI link form.
func ParseLink(text string) (Link, error) {
	invalid := errors.New("invalid invite link")
	u, err := url.Parse(text)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return Link{}, invalid
	}
	if u.Scheme != "https" && (u.Scheme != "http" ||
		!net.ParseIP(u.Hostname()).IsLoopback() && !strings.EqualFold(u.Hostname(), "localhost")) {
		return Link{}, errors.New("the invite link must use HTTPS except on loopback")
	}
	code, path := strings.CutPrefix(u.Path, "/join/")
	fp, fragment := strings.CutPrefix(u.Fragment, "fp=")
	origin := u.Scheme + "://" + u.Host
	if !path || !fragment || !validCode(code) || !validHex(fp, 64) ||
		text != origin+"/join/"+code+"#fp="+fp {
		return Link{}, invalid
	}
	return Link{Origin: origin, Fingerprint: fp, code: code}, nil
}

func validCode(code string) bool {
	if len(code) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(code)
	return err == nil && len(b) == 32
}

func validHex(text string, length int) bool {
	if len(text) != length {
		return false
	}
	for _, c := range text {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
