package enrollment

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const redeemPath = "/api/v1/invites/redeem"

var redeemHTTP = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
var hubID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Receipt struct {
	ProjectID string `json:"project_id"`
	Repo      string `json:"repo"`
	MemberID  string `json:"member_id"`
	HubRole   string `json:"hub_role"`
}

func (r Receipt) valid() bool {
	return hubID.MatchString(r.ProjectID) && hubID.MatchString(r.MemberID) &&
		(r.HubRole == "member" || r.HubRole == "lead") && r.Repo != "" && r.Repo != "*" && len(r.Repo) <= 1024 &&
		!strings.ContainsFunc(r.Repo, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

type redeemStatus int

func (status redeemStatus) Error() string {
	switch status {
	case http.StatusNotFound:
		return "invite not found (HTTP 404); use openplexer join --discard-pending to abandon this join"
	case http.StatusConflict:
		return "invite claim conflicts or the link was already used (HTTP 409); use openplexer join --discard-pending to abandon this join"
	case http.StatusGone:
		return "invite expired or was revoked (HTTP 410); ask the lead for a new link"
	default:
		return fmt.Sprintf("invite redemption returned HTTP %d", status)
	}
}

func (l Link) redeem(ctx context.Context, tokenHash string) (Receipt, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(map[string]string{"code": l.code, "token_hash": tokenHash})
	if err != nil {
		return Receipt{}, "", errors.New("could not encode the invite claim")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.Origin+redeemPath, bytes.NewReader(payload))
	if err != nil {
		return Receipt{}, "", errors.New("could not create the invite claim")
	}
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	res, err := redeemHTTP.Do(req)
	if err != nil {
		return Receipt{}, "", errors.New("could not reach the hub to redeem the invite; rerun openplexer join with the same link")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		return Receipt{}, "", redeemStatus(res.StatusCode)
	}
	invalid := errors.New("the hub returned an invalid invite receipt")
	const limit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(body) > limit {
		return Receipt{}, "", invalid
	}
	if res.StatusCode == http.StatusAccepted {
		var result struct {
			State string `json:"state"`
			ID    string `json:"invite_id"`
		}
		if jsonv2.Unmarshal(body, &result, jsonv2.RejectUnknownMembers(true)) != nil ||
			result.State != "pending_approval" || !validHex(result.ID, 32) {
			return Receipt{}, "", invalid
		}
		return Receipt{}, result.ID, nil
	}
	var result struct {
		State string `json:"state"`
		Receipt
	}
	if jsonv2.Unmarshal(body, &result, jsonv2.RejectUnknownMembers(true)) != nil ||
		result.State != "redeemed" || !result.Receipt.valid() ||
		strings.Contains(result.MemberID, l.code) || strings.Contains(result.ProjectID, l.code) || strings.Contains(result.Repo, l.code) {
		return Receipt{}, "", invalid
	}
	return result.Receipt, "", nil
}
