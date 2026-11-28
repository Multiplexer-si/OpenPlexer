package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"path/filepath"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

// EnrollmentKeys checks the public policy pins before a join sends any secrets.
func EnrollmentKeys(ctx context.Context, origin, fingerprint string) ([]byte, error) {
	origin, err := hubOrigin(origin)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return (hubClient{origin: origin}).enrollmentKeys(ctx, fingerprint)
}

func (h hubClient) enrollmentKeys(ctx context.Context, fingerprint string) ([]byte, error) {
	body, err := h.policyKeys(ctx)
	if err != nil {
		return nil, err
	}
	return checkFingerprint(body, fingerprint)
}

func checkFingerprint(body []byte, fingerprint string) ([]byte, error) {
	normalized, got, err := policy.NormalizeKeySet(body)
	if err != nil {
		return nil, err
	}
	if got != fingerprint {
		return nil, errors.New("the hub policy key fingerprint changed or disagrees with the link; confirm it with the lead")
	}
	return normalized, nil
}

// MatchesEnrollment checks a saved receipt against the current private config.
func (g *Gate) MatchesEnrollment(origin, project, member, tokenHash string) bool {
	return g.config.ProjectID == project && g.config.MemberID == member && g.config.matchesEnrollment(origin, tokenHash)
}

// MatchesPendingEnrollment identifies a stage created before stage markers.
func MatchesPendingEnrollment(home, origin, tokenHash string) bool {
	data, err := projects.ReadJoinFile(filepath.Join(home, configFile))
	if err != nil {
		return false
	}
	var c config
	return jsonv2.Unmarshal(data, &c, jsonv2.RejectUnknownMembers(true)) == nil && c.matchesEnrollment(origin, tokenHash)
}

func (c config) matchesEnrollment(origin, tokenHash string) bool {
	hash := sha256.Sum256([]byte(c.Token))
	return c.HubURL == origin && c.checkHub() == nil && hex.EncodeToString(hash[:]) == tokenHash
}

// EnrollmentGrants returns the assigned role grants and current temporary grants.
func (g *Gate) EnrollmentGrants() (policy.Role, []policy.TemporaryGrant) {
	if g.bundle == nil {
		return policy.Role{}, nil
	}
	role, assigned := g.bundle.Roles[g.status.Role]
	if !assigned {
		return policy.Role{}, nil
	}
	var temporary []policy.TemporaryGrant
	now := policy.TimestampOf(g.now())
	for _, grant := range g.bundle.TemporaryGrants {
		if grant.MemberID == g.status.Member && (grant.Scope == policy.GrantForMission || now.Before(grant.ExpiresAt)) {
			temporary = append(temporary, grant)
		}
	}
	return role, temporary
}
