package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const adminPath = "/api/v1/admin"
const credentialLimit = 4096

var remoteID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type remoteAdmin struct {
	origin, token string
	client        *http.Client
}

func runRemote(address, tokenFile, command string, stdin io.Reader, transport http.RoundTripper, action func(remoteAdmin) error) error {
	origin, err := remoteOrigin(address)
	if err != nil {
		return err
	}
	token, err := readCredential(stdin, tokenFile, command == "init")
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return action(remoteAdmin{origin: origin, token: token, client: client})
}

func remoteOrigin(address string) (string, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		strings.Contains(address, "#") || (u.Path != "" && u.EscapedPath() != "/") {
		return "", errors.New("the hub URL must be an origin")
	}
	if u.Scheme != "https" && (u.Scheme != "http" ||
		!net.ParseIP(u.Hostname()).IsLoopback() && !strings.EqualFold(u.Hostname(), "localhost")) {
		return "", errors.New("the hub URL must use HTTPS except on loopback")
	}
	return u.Scheme + "://" + u.Host, nil
}

func validMemberToken(token string) bool {
	if len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == token
}

func readCredential(stdin io.Reader, path string, bootstrap bool) (string, error) {
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("token file must be a private regular file (mode 0600 or 0400)")
		}
		stdin = file
	}
	data, err := io.ReadAll(io.LimitReader(stdin, credentialLimit+1))
	if err != nil || len(data) > credentialLimit {
		return "", errors.New("could not read credential within its size limit")
	}
	token := strings.TrimSpace(string(data))
	if bootstrap {
		if token != "" && !strings.ContainsFunc(token, func(c rune) bool { return c < '!' || c > '~' || c == ',' }) {
			return token, nil
		}
	} else if validMemberToken(token) {
		return token, nil
	}
	return "", errors.New("invalid credential")
}

func firstLeadToken(path string) (string, error) {
	if path == "" || path == "-" {
		return "", errors.New("--lead-token-file must name a private file")
	}
	existing, err := readCredential(nil, path, false)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return existing, err
	}

	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	file, err := os.CreateTemp(filepath.Dir(path), ".lead-token-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := fmt.Fprintln(file, token); err != nil {
		return "", errors.New("could not write the first lead token")
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	// Publish a complete file without replacing a token saved by another invocation.
	if err := os.Link(file.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readCredential(nil, path, false)
		}
		return "", err
	}
	return token, nil
}

func (h remoteAdmin) request(method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, h.origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not create the hub request")
	}
	req.Close = true
	req.Header.Set("Authorization", "Bearer "+h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, errors.New("hub request failed")
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("hub returned HTTP %d", res.StatusCode)
	}
	return res, nil
}

func (h remoteAdmin) send(method, path string, body []byte) error {
	res, err := h.request(method, path, body)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func (h remoteAdmin) init(id, path string) error {
	if !remoteID.MatchString(id) {
		return errors.New("init requires a valid --id for the first lead")
	}
	token, err := firstLeadToken(path)
	if err != nil {
		return err
	}
	if token == h.token {
		return errors.New("the bootstrap token and first lead token must be different")
	}
	digest := sha256.Sum256([]byte(token))
	body, err := json.Marshal(map[string]string{"member_id": id, "token_hash": hex.EncodeToString(digest[:])})
	if err != nil {
		return err
	}
	return h.send(http.MethodPost, adminPath+"/init", body)
}

func (h remoteAdmin) addMember(id, role string, stdout io.Writer) error {
	body, err := json.Marshal(map[string]string{"id": id, "role": role})
	if err != nil {
		return err
	}
	res, err := h.request(http.MethodPost, adminPath+"/members", body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, credentialLimit+1))
	var reply struct {
		Token string `json:"token"`
	}
	if err != nil || len(data) > credentialLimit || json.Unmarshal(data, &reply) != nil || !validMemberToken(reply.Token) {
		return errors.New("invalid token response from hub")
	}
	_, err = fmt.Fprintln(stdout, reply.Token)
	if err != nil {
		return errors.New("could not write the new member token")
	}
	return nil
}

func (h remoteAdmin) removeMember(id string) error {
	if !remoteID.MatchString(id) {
		return errors.New("remove-member requires a valid --id")
	}
	return h.send(http.MethodDelete, adminPath+"/members/"+id, nil)
}

func (h remoteAdmin) addProject(id, repo string) error {
	body, err := json.Marshal(map[string]string{"id": id, "repo": repo})
	if err != nil {
		return err
	}
	return h.send(http.MethodPost, adminPath+"/projects", body)
}

func (h remoteAdmin) publishPolicy(project, path string) error {
	if !remoteID.MatchString(project) {
		return errors.New("publish-policy requires a valid --project")
	}
	text, err := policyText(path)
	if err != nil {
		return err
	}
	return h.send(http.MethodPut, "/api/v1/projects/"+project+"/policy", text)
}
