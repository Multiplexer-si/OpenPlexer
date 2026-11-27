package enrollment

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/projects"
)

// Request identifies the checkout and the original or refreshed invite.
type Request struct {
	UserHome, Root, Home string
	Link                 Link
	Now                  func() time.Time
}

type Result struct {
	Root, Home, PendingID string
	Receipt               Receipt
	Status                daemon.Status
	Grants                policy.Role
	Temporary             []policy.TemporaryGrant
}

type pending struct {
	Origin   string `json:"origin"`
	CodeHash string `json:"code_sha256"`
	Bearer   string `json:"bearer"`
}

type identity struct {
	Root      string  `json:"root"`
	Home      string  `json:"home"`
	Origin    string  `json:"origin"`
	CodeHash  string  `json:"code_sha256"`
	TokenHash string  `json:"token_sha256"`
	Receipt   Receipt `json:"receipt"`
}

const identityFile = "enrollment.json"

var errNoIdentity = errors.New("the home already exists without a join identity")

// Join enrolls a checkout or resumes its saved enrollment. Optional installs
// run in the CLI after the home and registry are available.
func Join(ctx context.Context, req Request) (Result, error) {
	return runJoin(ctx, req, nil)
}

func runJoin(ctx context.Context, req Request, checkpoint func(string) error) (Result, error) {
	if !validCode(req.Link.code) || !validHex(req.Link.Fingerprint, 64) {
		return Result{}, errors.New("invalid invite link")
	}
	if checkpoint == nil {
		checkpoint = func(string) error { return nil }
	}
	if req.Now == nil {
		req.Now = time.Now
	}
	var result Result
	err := projects.WithJoin(req.UserHome, req.Root, func(j projects.JoinLock) error {
		p, home, err := joinHome(j, req.Home)
		if err != nil {
			return err
		}
		return j.WithHome(home, func() error {
			result, err = joinLocked(ctx, j, home, p, req.Link, req.Now, checkpoint)
			return err
		})
	})
	return result, err
}

func joinHome(j projects.JoinLock, explicit string) (*projects.PendingJoin, string, error) {
	p, err := j.Pending()
	if err != nil {
		return nil, "", err
	}
	home := explicit
	if home == "" {
		if p != nil {
			home = p.Home
		} else {
			home, err = j.RegisteredHome()
			if err != nil {
				return nil, "", err
			}
		}
	}
	home, err = j.HomePath(home)
	if err != nil {
		return nil, "", err
	}
	if p != nil && home != p.Home {
		return nil, "", errors.New("a pending join reserves a different home; resume it or use openplexer join --discard-pending")
	}
	return p, home, nil
}

func joinLocked(ctx context.Context, j projects.JoinLock, home string, p *projects.PendingJoin,
	link Link, now func() time.Time, checkpoint func(string) error) (Result, error) {
	if err := j.Check(home); err != nil {
		return Result{}, err
	}
	codeHash := digest(link.code)
	var claim pending
	if p != nil && (jsonv2.Unmarshal(p.Data, &claim, jsonv2.RejectUnknownMembers(true)) != nil ||
		claim.Origin != link.Origin || claim.CodeHash != codeHash || !validCode(claim.Bearer)) {
		return Result{}, errors.New("a different invite is pending for this checkout; resume it or use openplexer join --discard-pending")
	}
	saved, err := readIdentity(home)
	if err != nil {
		return Result{}, err
	}
	registered, err := j.RegisteredHome()
	if err != nil {
		return Result{}, err
	}
	if saved != nil && (saved.Root != j.Root || saved.Home != home || saved.Origin != link.Origin || saved.CodeHash != codeHash ||
		p == nil && registered != home || p != nil && saved.TokenHash != digest(claim.Bearer)) {
		return Result{}, errors.New("an existing home belongs to a different enrollment")
	}
	if saved == nil && registered != "" {
		return Result{}, errors.New("the checkout is already registered with a different home")
	}
	keys, err := daemon.EnrollmentKeys(ctx, link.Origin, link.Fingerprint)
	if err != nil {
		return Result{}, err
	}

	if saved != nil {
		if p != nil && registered == "" {
			// Renamed but still unpublished. Accept a refreshed trusted link
			// while retaining the staged policy's version floor.
			if _, err := openEnrollment(home, *saved, now); err != nil {
				return Result{}, err
			}
			if err := daemon.Configure(home, daemon.Settings{KeySet: keys}); err != nil {
				return Result{}, err
			}
		}
		result, err := syncEnrollment(ctx, home, *saved, link.Fingerprint, now)
		if err != nil {
			return Result{}, err
		}
		if p != nil {
			if registered == "" {
				if err := j.Publish(*p, false, nil); err != nil {
					return Result{}, err
				}
			}
			if err := releasePending(j, *p); err != nil {
				return Result{}, err
			}
		}
		return result, nil
	}

	if p == nil {
		var secret [32]byte
		rand.Read(secret[:])
		claim = pending{Origin: link.Origin, CodeHash: codeHash, Bearer: base64.RawURLEncoding.EncodeToString(secret[:])}
		data, err := jsonv2.Marshal(claim)
		if err != nil {
			return Result{}, err
		}
		p = &projects.PendingJoin{Root: j.Root, Home: home, Data: data}
		if err := j.Reserve(*p); err != nil {
			return Result{}, err
		}
		if err := checkpoint("reserved"); err != nil {
			return Result{}, err
		}
	}
	tokenHash := digest(claim.Bearer)
	receipt, pendingID, err := link.redeem(ctx, tokenHash)
	if err != nil {
		if err == redeemStatus(http.StatusGone) {
			err = errors.Join(err, releasePending(j, *p))
		}
		return Result{}, err
	}
	if pendingID != "" {
		return Result{PendingID: pendingID}, nil
	}
	if strings.Contains(receipt.ProjectID, claim.Bearer) || strings.Contains(receipt.MemberID, claim.Bearer) ||
		strings.Contains(receipt.Repo, claim.Bearer) {
		return Result{}, errors.New("the hub returned an invalid invite receipt")
	}

	stage, err := j.Stage(*p)
	if err != nil {
		return Result{}, err
	}
	settings := daemon.Settings{
		HubURL: link.Origin, ProjectID: receipt.ProjectID, MemberID: receipt.MemberID,
		Token: claim.Bearer, KeySet: keys,
	}
	if err := daemon.Configure(stage, settings); err != nil {
		return Result{}, err
	}
	if err := checkpoint("configured"); err != nil {
		return Result{}, err
	}
	enrolled := identity{
		Root: j.Root, Home: home, Origin: link.Origin, CodeHash: codeHash,
		TokenHash: tokenHash, Receipt: receipt,
	}
	result, err := syncEnrollment(ctx, stage, enrolled, link.Fingerprint, now)
	if err != nil {
		return Result{}, err
	}
	if err := checkpoint("synced"); err != nil {
		return Result{}, err
	}
	data, err := jsonv2.Marshal(enrolled)
	if err != nil {
		return Result{}, err
	}
	if err := projects.WriteJoinFile(filepath.Join(stage, identityFile), data); err != nil {
		return Result{}, err
	}
	if err := checkpoint("marked"); err != nil {
		return Result{}, err
	}
	if err := j.Publish(*p, true, func() error { return checkpoint("renamed") }); err != nil {
		return Result{}, err
	}
	if err := checkpoint("registered"); err != nil {
		return Result{}, err
	}
	return result, releasePending(j, *p)
}

func readIdentity(home string) (*identity, error) {
	if _, err := os.Lstat(home); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	data, err := projects.ReadJoinFile(filepath.Join(home, identityFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoIdentity
	}
	if err != nil {
		return nil, errors.New("the home already exists without a readable join identity")
	}
	saved := new(identity)
	if jsonv2.Unmarshal(data, saved, jsonv2.RejectUnknownMembers(true)) != nil ||
		!saved.Receipt.valid() || !validHex(saved.CodeHash, 64) || !validHex(saved.TokenHash, 64) {
		return nil, errors.New("the home has an invalid join identity")
	}
	return saved, nil
}

func openEnrollment(home string, id identity, now func() time.Time) (*daemon.Gate, error) {
	gate, err := daemon.Open(home, now)
	if err != nil {
		return nil, err
	}
	if !gate.MatchesEnrollment(id.Origin, id.Receipt.ProjectID, id.Receipt.MemberID, id.TokenHash) {
		return nil, errors.New("the home configuration differs from its saved enrollment")
	}
	return gate, nil
}

func syncEnrollment(ctx context.Context, path string, id identity, fingerprint string, now func() time.Time) (Result, error) {
	gate, err := openEnrollment(path, id, now)
	if err != nil {
		return Result{}, err
	}
	status, err := gate.SyncEnrollment(ctx, fingerprint, id.Receipt.Repo)
	if err != nil {
		return Result{}, fmt.Errorf("initial policy sync: %w", err)
	}
	if status.State != daemon.Verified || !gate.MatchesEnrollment(id.Origin, id.Receipt.ProjectID, id.Receipt.MemberID, id.TokenHash) {
		return Result{}, errors.New("the joined home has no verified policy for its enrollment")
	}
	grants, temporary := gate.EnrollmentGrants()
	return Result{Root: id.Root, Home: id.Home, Status: status, Receipt: id.Receipt, Grants: grants, Temporary: temporary}, nil
}

func releasePending(j projects.JoinLock, p projects.PendingJoin) error {
	return j.Release(p, func() (bool, error) {
		saved, err := readIdentity(p.Home)
		if errors.Is(err, errNoIdentity) {
			return false, nil
		}
		if err != nil || saved == nil {
			return false, err
		}
		var claim pending
		if jsonv2.Unmarshal(p.Data, &claim, jsonv2.RejectUnknownMembers(true)) != nil || !validCode(claim.Bearer) {
			return false, errors.New("cannot check the pending identity for the existing home")
		}
		return saved.Root == p.Root && saved.Home == p.Home && saved.Origin == claim.Origin &&
			saved.CodeHash == claim.CodeHash && saved.TokenHash == digest(claim.Bearer), nil
	})
}

// Discard releases private pending state. It never contacts the hub.
func Discard(user, root, explicitHome string) (bool, error) {
	var discarded bool
	err := projects.WithJoin(user, root, func(j projects.JoinLock) error {
		p, home, err := joinHome(j, explicitHome)
		if err != nil || p == nil {
			return err
		}
		return j.WithHome(home, func() error {
			if err := releasePending(j, *p); err != nil {
				return err
			}
			discarded = true
			return nil
		})
	})
	return discarded, err
}
