package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
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
	hash := sha256.Sum256([]byte(g.config.Token))
	return g.config.HubURL == origin && g.config.ProjectID == project &&
		g.config.MemberID == member && g.config.checkHub() == nil && hex.EncodeToString(hash[:]) == tokenHash
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
