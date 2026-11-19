package daemon

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

var hubID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (c config) hasHubSettings() bool {
	return c.HubURL != "" || c.ProjectID != "" || c.Token != ""
}

func (c config) checkHub() error {
	if _, err := hubOrigin(c.HubURL); err != nil {
		return err
	}
	if !hubID.MatchString(c.ProjectID) || !hubID.MatchString(c.MemberID) {
		return errors.New("configure the hub with valid project and member IDs")
	}
	token, err := base64.RawURLEncoding.Strict().DecodeString(c.Token)
	if err != nil || len(token) != 32 || len(c.Token) != base64.RawURLEncoding.EncodedLen(32) {
		return errors.New("configure the hub with a valid member bearer token")
	}
	return nil
}

func hubOrigin(address string) (string, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || (u.Path != "" && u.EscapedPath() != "/") {
		return "", errors.New("the hub URL must be an origin")
	}
	if u.Scheme != "https" && (u.Scheme != "http" ||
		!net.ParseIP(u.Hostname()).IsLoopback() && !strings.EqualFold(u.Hostname(), "localhost")) {
		return "", errors.New("the hub URL must use HTTPS except on loopback")
	}
	return u.Scheme + "://" + u.Host, nil
}

func (c config) checkBundle(bundle *policy.Bundle) error {
	if c.ProjectID != "" && (bundle.ProjectID != c.ProjectID || len(bundle.Repos) != 1 || bundle.Repos[0] == "" || bundle.Repos[0] == "*") {
		return errors.New("the signed bundle must target the configured project and name one repository")
	}
	return nil
}

type hubClient struct {
	origin, project, member, token string
}

func (c config) client() (hubClient, error) {
	if c.HubURL == "" {
		return hubClient{}, errors.New("configure a hub first")
	}
	origin, err := hubOrigin(c.HubURL)
	return hubClient{origin: origin, project: c.ProjectID, member: c.MemberID, token: c.Token}, err
}

var hubHTTP = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type hubStatus int

func (s hubStatus) Error() string { return fmt.Sprintf("hub returned HTTP %d", s) }

// net/http errors can contain data from the peer, including malformed status
// lines and headers. Preserve error matching without exposing those bytes.
type hubIOError struct {
	message string
	cause   error
}

func (e hubIOError) Error() string        { return e.message }
func (e hubIOError) Is(target error) bool { return errors.Is(e.cause, target) }

func (h hubClient) request(ctx context.Context, method, path string, body io.Reader, contentType, key string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, h.origin+path, body)
	if err != nil {
		return nil, hubIOError{"could not create the hub request", err}
	}
	// net/http can log unsolicited bytes received on an idle HTTP connection.
	req.Close = true
	if path != "/.well-known/jwks.json" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := hubHTTP.Do(req)
	if err != nil {
		return nil, hubIOError{"hub request failed", err}
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, hubStatus(response.StatusCode)
	}
	return response, nil
}

func readResponse(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, hubIOError{"could not read the hub response body", err}
	}
	if int64(len(body)) > limit {
		return nil, errors.New("hub response exceeds its size limit")
	}
	return body, nil
}

// hubSource checks that the bundle verifies with the hub's advertised keys.
// Gate.Load also verifies it with the locally pinned keys before storing it.
type hubSource struct{ hubClient }

func (h hubSource) Fetch(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := h.request(ctx, http.MethodGet, "/api/v1/projects/"+h.project+"/policy.bundle", nil, "", "")
	if err != nil {
		return "", err
	}
	body, err := readResponse(response, 4<<20)
	if err != nil {
		return "", err
	}
	token, err := signedToken(body)
	if err != nil {
		return "", err
	}
	response, err = h.request(ctx, http.MethodGet, "/.well-known/jwks.json", nil, "", "")
	if err != nil {
		return "", err
	}
	body, err = readResponse(response, 64<<10)
	if err != nil {
		return "", err
	}
	keys, err := policy.ParseKeySet(body)
	if err != nil {
		return "", err
	}
	// Let admission reject and record an oversized payload whose advertised signature verified.
	if _, err := policy.VerifySignedBundle(token, keys); err != nil && !errors.Is(err, policy.ErrBundlePayloadTooLarge) {
		return "", fmt.Errorf("bundle does not verify with the advertised key set: %w", err)
	}
	return token, nil
}

// Sync fetches and admits the latest policy from the configured hub.
func (g *Gate) Sync(ctx context.Context) (Status, error) {
	client, err := g.config.client()
	if err != nil {
		return g.status, err
	}
	return g.Load(ctx, hubSource{client})
}
