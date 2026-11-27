package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

const (
	DefaultInviteExpiry = 48 * 60 * 60
	DefaultInvitePage   = 100
	inviteLiveLimit     = 1024
	inviteRetention     = 30 * 24 * time.Hour
	inviteReceiptWindow = 24 * time.Hour
	redeemFailureLimit  = 20
	redeemWindowLength  = time.Minute
)

var (
	inviteIDPattern  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	errInvalidInvite = fmt.Errorf("%w: invalid invite request", errInvalid)
	errInviteGone    = errors.New("invite expired or revoked")
)

type inviteState string

const (
	issued          inviteState = "issued"
	pendingApproval inviteState = "pending_approval"
	approved        inviteState = "approved"
	redeemed        inviteState = "redeemed"
	revoked         inviteState = "revoked"
	expired         inviteState = "expired"
)

type Invite struct {
	ID              string      `json:"id"`
	ProjectID       string      `json:"project_id"`
	MemberID        string      `json:"member_id"`
	Role            Role        `json:"hub_role"`
	RequireApproval bool        `json:"require_approval"`
	State           inviteState `json:"state"`
	CreatedAt       time.Time   `json:"created_at"`
	ExpiresAt       time.Time   `json:"expires_at"`
}

type inviteRecord struct {
	Invite
	CodeHash     [sha256.Size]byte  `json:"code_hash"`
	ClaimHash    *[sha256.Size]byte `json:"claim_hash,omitempty"`
	EndedAt      *time.Time         `json:"ended_at,omitempty"`
	EnrollmentID string             `json:"enrollment_id,omitempty"`
	Repo         string             `json:"repo,omitempty"`
}

type InviteInput struct {
	ProjectID       string  `json:"project_id"`
	MemberID        string  `json:"member_id"`
	Role            Role    `json:"hub_role"`
	ExpiresIn       float64 `json:"expires_in_seconds"`
	RequireApproval bool    `json:"require_approval"`
}

type InviteResult struct {
	Invite Invite `json:"invite"`
}

type CreatedInvite struct {
	InviteResult
	Code string `json:"code"`
}

type InvitePage struct {
	Invites    []Invite `json:"invites"`
	NextCursor *string  `json:"next_cursor"`
}

type redeemInput struct {
	Code      string `json:"code"`
	TokenHash string `json:"token_hash"`
}

type redeemResult struct {
	State     inviteState `json:"state"`
	InviteID  string      `json:"invite_id,omitempty"`
	ProjectID string      `json:"project_id,omitempty"`
	Repo      string      `json:"repo,omitempty"`
	MemberID  string      `json:"member_id,omitempty"`
	Role      Role        `json:"hub_role,omitempty"`
}

type redeemFailureWindow struct {
	Until time.Time `json:"until"`
	Count int       `json:"count"`
}

type redeemThrottled struct{ retryAfter int }

func (redeemThrottled) Error() string { return "too many failed redeem attempts" }

func (s *Store) limitRedeemFailure(st *state, now time.Time, failure error) error {
	window := st.RedeemFailures
	if window == nil || !now.Before(window.Until) || now.Before(window.Until.Add(-redeemWindowLength)) {
		window = &redeemFailureWindow{Until: now.Add(redeemWindowLength)}
		st.RedeemFailures = window
	}
	if window.Count >= redeemFailureLimit {
		remaining := window.Until.Sub(now)
		return redeemThrottled{retryAfter: int((remaining + time.Second - 1) / time.Second)}
	}
	window.Count++
	if err := s.save(st); err != nil {
		return err
	}
	return failure
}

func (r inviteRecord) at(now time.Time) Invite {
	invite := r.Invite
	if r.EndedAt == nil && !now.Before(r.ExpiresAt) {
		invite.State = expired
	}
	return invite
}

func (r inviteRecord) pastRetention(now time.Time) bool {
	end := r.ExpiresAt
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	return !now.Before(end.Add(inviteRetention))
}

func (st *state) purgeInvites(now time.Time) bool {
	changed := false
	for id, r := range st.Invites {
		if r.pastRetention(now) {
			delete(st.Invites, id)
			changed = true
		}
	}
	return changed
}

func (st *state) liveInvites(memberID string, now time.Time) (count int, reserved bool) {
	for _, r := range st.Invites {
		if r.EndedAt == nil && now.Before(r.ExpiresAt) {
			count++
			reserved = reserved || r.MemberID == memberID
		}
	}
	return
}

func (s *Store) CreateInvite(input InviteInput) (result CreatedInvite, err error) {
	err = s.update(func(st *state) error {
		result, err = st.createInvite(input, nil, s.inviteNow())
		return err
	})
	return
}

func (s *Store) inviteNow() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

func (input InviteInput) Validate() error {
	if !idPattern.MatchString(input.ProjectID) || !idPattern.MatchString(input.MemberID) ||
		(input.Role != Lead && input.Role != Member) || input.ExpiresIn < 60 || input.ExpiresIn > 604800 ||
		math.Trunc(input.ExpiresIn) != input.ExpiresIn {
		return errInvalidInvite
	}
	return nil
}

func (st *state) createInvite(input InviteInput, actor *string, now time.Time) (CreatedInvite, error) {
	if err := input.Validate(); err != nil {
		return CreatedInvite{}, err
	}
	if _, ok := st.Projects[input.ProjectID]; !ok {
		return CreatedInvite{}, errNotFound
	}
	count, reserved := st.liveInvites(input.MemberID, now)
	if _, exists := st.Members[input.MemberID]; exists || reserved || count >= inviteLiveLimit {
		return CreatedInvite{}, errConflict
	}
	id := hex.EncodeToString(randomBytes(16))
	code := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	r := inviteRecord{
		Invite: Invite{
			ID: id, ProjectID: input.ProjectID, MemberID: input.MemberID, Role: input.Role,
			RequireApproval: input.RequireApproval, State: issued, CreatedAt: now,
			ExpiresAt: now.Add(time.Duration(input.ExpiresIn) * time.Second),
		},
		CodeHash: sha256.Sum256([]byte(code)),
	}
	if st.Invites == nil {
		st.Invites = make(map[string]inviteRecord)
	}
	st.purgeInvites(now)
	if _, exists := st.Invites[id]; exists {
		return CreatedInvite{}, errors.New("duplicate generated invite ID")
	}
	for _, existing := range st.Invites {
		if subtle.ConstantTimeCompare(existing.CodeHash[:], r.CodeHash[:]) == 1 {
			return CreatedInvite{}, errors.New("duplicate generated invite digest")
		}
	}
	st.Invites[id] = r
	if err := st.auditInvite(r, "invite.created", actor, now); err != nil {
		return CreatedInvite{}, err
	}
	return CreatedInvite{InviteResult{r.Invite}, code}, nil
}

func randomBytes(n int) []byte {
	data := make([]byte, n)
	rand.Read(data)
	return data
}

func ValidInvitePage(after string, limit int) bool {
	return (after == "" || inviteIDPattern.MatchString(after)) && limit >= 1 && limit <= DefaultInvitePage
}

func (s *Store) ListInvites(after string, limit int) (page InvitePage, err error) {
	err = s.view(func(st *state) error {
		page, err = s.listInvites(st, after, limit)
		return err
	})
	return
}

func (s *Store) listInvites(st *state, after string, limit int) (InvitePage, error) {
	if !ValidInvitePage(after, limit) {
		return InvitePage{}, errInvalidInvite
	}
	now := s.inviteNow()
	if st.purgeInvites(now) {
		if err := s.save(st); err != nil {
			return InvitePage{}, err
		}
	}
	var ids []string
	for id := range st.Invites {
		if id > after {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	page := InvitePage{Invites: make([]Invite, 0, min(limit, len(ids)))}
	for _, id := range ids[:min(limit, len(ids))] {
		page.Invites = append(page.Invites, st.Invites[id].at(now))
	}
	if len(ids) > limit {
		page.NextCursor = &ids[limit-1]
	}
	return page, nil
}

func (s *Store) ApproveInvite(id string) (InviteResult, error) {
	return s.changeLocalInvite(id, approved)
}

func (s *Store) RevokeInvite(id string) (InviteResult, error) {
	return s.changeLocalInvite(id, revoked)
}

func (s *Store) changeLocalInvite(id string, to inviteState) (result InviteResult, err error) {
	err = s.view(func(st *state) error {
		result, err = s.changeInvite(st, id, to, nil)
		return err
	})
	return
}

func (s *Store) changeInvite(st *state, id string, to inviteState, actor *string) (InviteResult, error) {
	if !inviteIDPattern.MatchString(id) {
		return InviteResult{}, errInvalidInvite
	}
	now := s.inviteNow()
	r, ok := st.Invites[id]
	if !ok || r.pastRetention(now) {
		return InviteResult{}, errNotFound
	}
	current := r.at(now).State
	if current == to || (to == approved && current == redeemed && r.RequireApproval) {
		return InviteResult{r.Invite}, nil
	}
	action := "invite.approved"
	if to == revoked {
		if current == redeemed || current == expired {
			return InviteResult{}, errConflict
		}
		r.EndedAt, r.ClaimHash = &now, nil
		action = "invite.revoked"
	} else if current != pendingApproval {
		return InviteResult{}, errConflict
	}
	r.State = to
	st.Invites[id] = r
	if err := st.auditInvite(r, action, actor, now); err != nil {
		return InviteResult{}, err
	}
	return InviteResult{r.Invite}, s.save(st)
}

func (s *Store) redeemInvite(st *state, input redeemInput, now time.Time) (redeemResult, error) {
	if len(input.Code) != 43 || !hashPattern.MatchString(input.TokenHash) {
		return redeemResult{}, errInvalidInvite
	}
	code, err := base64.RawURLEncoding.Strict().DecodeString(input.Code)
	if err != nil || len(code) != 32 {
		return redeemResult{}, errInvalidInvite
	}
	var claim [sha256.Size]byte
	_, _ = hex.Decode(claim[:], []byte(input.TokenHash))
	codeHash := sha256.Sum256([]byte(input.Code))
	for _, r := range st.Invites {
		if subtle.ConstantTimeCompare(r.CodeHash[:], codeHash[:]) == 1 && !r.pastRetention(now) {
			return s.redeemKnownInvite(st, r, claim, now)
		}
	}
	return redeemResult{}, errNotFound
}

func (s *Store) redeemKnownInvite(st *state, r inviteRecord, claim [sha256.Size]byte, now time.Time) (redeemResult, error) {
	current := r.at(now).State
	if current == revoked || current == expired {
		return redeemResult{}, errInviteGone
	}
	if r.ClaimHash != nil && subtle.ConstantTimeCompare(r.ClaimHash[:], claim[:]) != 1 {
		return redeemResult{}, errConflict
	}
	receipt := redeemResult{State: redeemed, ProjectID: r.ProjectID, Repo: r.Repo, MemberID: r.MemberID, Role: r.Role}
	existing, occupied := st.Members[r.MemberID]
	if current == redeemed {
		if r.EndedAt == nil || !occupied || existing.EnrollmentID != r.EnrollmentID || existing.Role != r.Role ||
			subtle.ConstantTimeCompare(existing.TokenHash[:], claim[:]) != 1 || !now.Before(r.EndedAt.Add(inviteReceiptWindow)) {
			return redeemResult{}, errConflict
		}
		return receipt, nil
	}
	if current == pendingApproval {
		return redeemResult{State: pendingApproval, InviteID: r.ID}, nil
	}
	project, ok := st.Projects[r.ProjectID]
	if !ok || occupied || st.hasTokenHash(claim) {
		return redeemResult{}, errConflict
	}
	if r.RequireApproval && current != approved {
		r.ClaimHash, r.State = &claim, pendingApproval
		st.Invites[r.ID] = r
		if err := st.auditInvite(r, "invite.requested", &r.MemberID, now); err != nil {
			return redeemResult{}, err
		}
		if err := s.save(st); err != nil {
			return redeemResult{}, err
		}
		return redeemResult{State: pendingApproval, InviteID: r.ID}, nil
	}
	m := newMember(r.Role, claim)
	st.Members[r.MemberID] = m
	r.State, r.ClaimHash, r.EndedAt = redeemed, &claim, &now
	r.Repo, r.EnrollmentID = project.Repo, m.EnrollmentID
	st.Invites[r.ID] = r
	if err := st.auditInvite(r, "invite.redeemed", &r.MemberID, now); err != nil {
		return redeemResult{}, err
	}
	if err := st.appendAudit(audit.Record{Action: "member.added", Target: "member:" + r.MemberID,
		Meta: map[string]any{"role": r.Role, "invite_id": r.ID}}, now); err != nil {
		return redeemResult{}, err
	}
	receipt.Repo = r.Repo
	return receipt, s.save(st)
}

func (st *state) auditInvite(r inviteRecord, action string, actor *string, now time.Time) error {
	return st.appendAudit(audit.Record{Action: action, Target: "invite:" + r.ID, ActorMemberID: actor,
		Meta: map[string]any{"project_id": r.ProjectID, "member_id": r.MemberID, "hub_role": r.Role}}, now)
}
