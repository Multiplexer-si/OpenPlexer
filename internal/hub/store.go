package hub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

var (
	errInvalid      = errors.New("invalid input")
	errConflict     = errors.New("already exists or conflicts with current state")
	errUnauthorized = errors.New("invalid bearer token")
	errForbidden    = errors.New("forbidden")
	errNotFound     = errors.New("not found")

	errInvalidMember = fmt.Errorf("%w: member needs a valid id and role lead or member", errInvalid)
)

type Role string

const (
	Lead   Role = "lead"
	Member Role = "member"
)

type member struct {
	Role         Role              `json:"role"`
	EnrollmentID string            `json:"enrollment_id"`
	TokenHash    [sha256.Size]byte `json:"token_hash"`
}

type Project struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
}

type state struct {
	WorkspaceID    string                   `json:"workspace_id,omitempty"`
	CheckpointKey  []byte                   `json:"checkpoint_key,omitempty"`
	CheckpointKeys []map[string]string      `json:"checkpoint_keys,omitempty"`
	Audit          []audit.Record           `json:"audit,omitempty"`
	SigningKey     []byte                   `json:"signing_key"`
	Members        map[string]member        `json:"members"`
	Invites        map[string]inviteRecord  `json:"invites,omitempty"`
	Projects       map[string]Project       `json:"projects"`
	Policies       map[string]SignedBundle  `json:"policies"`
	Rooms          map[string][]eventRecord `json:"rooms,omitempty"`
	Claims         map[string]lease         `json:"claims,omitempty"`
	RedeemFailures *redeemFailureWindow     `json:"redeem_failures,omitempty"`
}

// Store is a data directory shared by the server and local admin commands.
// Every operation reloads state while holding the same process-safe lock.
type Store struct {
	dir string
	now func() time.Time
}

func Open(dir string) *Store { return &Store{dir: dir, now: time.Now} }

func (s *Store) Init() error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return s.locked(func() error {
		if _, err := os.Stat(s.path()); err == nil {
			return fmt.Errorf("%w: hub already initialized", errConflict)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		private, err := key.Bytes()
		if err != nil {
			return err
		}
		st := &state{
			SigningKey: private,
			Members:    make(map[string]member),
			Projects:   make(map[string]Project),
			Policies:   make(map[string]SignedBundle),
		}
		if _, err := st.ensureAudit(); err != nil {
			return err
		}
		return s.save(st)
	})
}

func (s *Store) Check() error {
	return s.view(func(st *state) error {
		_, err := signingKey(st)
		return err
	})
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (s *Store) AddMember(id string, role Role) (string, error) {
	if err := validateMember(id, role); err != nil {
		return "", err
	}
	var token string
	err := s.update(func(st *state) error {
		var err error
		token, err = st.addMember(id, role, nil, s.now())
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func validateMember(id string, role Role) error {
	if !idPattern.MatchString(id) || (role != Lead && role != Member) {
		return errInvalidMember
	}
	return nil
}

func (st *state) addMember(id string, role Role, actor *string, now time.Time) (string, error) {
	if err := validateMember(id, role); err != nil {
		return "", err
	}
	_, reserved := st.liveInvites(id, now)
	if _, exists := st.Members[id]; exists || reserved {
		return "", fmt.Errorf("%w: member %q", errConflict, id)
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	hash := sha256.Sum256([]byte(token))
	if st.hasTokenHash(hash) {
		return "", errors.New("duplicate generated member credential")
	}
	st.Members[id] = newMember(role, hash)
	return token, st.appendAudit(audit.Record{Action: "member.added", Target: "member:" + id,
		ActorMemberID: actor, Meta: map[string]any{"role": role}}, now)
}

func newMember(role Role, hash [sha256.Size]byte) member {
	return member{Role: role, TokenHash: hash, EnrollmentID: rand.Text()}
}

func (st *state) hasTokenHash(hash [sha256.Size]byte) bool {
	_, _, err := st.authenticate(hash)
	return err == nil
}

func (s *Store) RemoveMember(id string) error {
	return s.update(func(st *state) error {
		err := st.removeMember(id, nil, s.now())
		if errors.Is(err, errNotFound) {
			return fmt.Errorf("%w: member %q", err, id)
		}
		return err
	})
}

func (st *state) removeMember(id string, actor *string, now time.Time) error {
	m, exists := st.Members[id]
	if !exists {
		return errNotFound
	}
	if actor != nil && m.Role == Lead {
		leads := 0
		for _, candidate := range st.Members {
			if candidate.Role == Lead {
				leads++
			}
		}
		if leads == 1 {
			return fmt.Errorf("%w: cannot remove the last lead", errConflict)
		}
	}
	delete(st.Members, id)
	return st.appendAudit(audit.Record{Action: "member.removed", Target: "member:" + id, ActorMemberID: actor}, now)
}

func (s *Store) AddProject(id, repo string) error {
	if !idPattern.MatchString(id) || !validRepo(repo) {
		return fmt.Errorf("%w: project needs a valid id and repository identifier", errInvalid)
	}
	return s.update(func(st *state) error {
		if _, exists := st.Projects[id]; exists {
			return fmt.Errorf("%w: project %q", errConflict, id)
		}
		st.Projects[id] = Project{ID: id, Repo: repo}
		return st.appendAudit(audit.Record{Action: "project.added", Target: "project:" + id}, s.now())
	})
}

func validRepo(repo string) bool {
	return repo != "" && repo != "*" && len(repo) <= 1024 && utf8.ValidString(repo) &&
		!strings.ContainsFunc(repo, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func (s *Store) view(read func(*state) error) error {
	return s.locked(func() error {
		st, err := s.load()
		if err != nil {
			return err
		}
		return read(st)
	})
}

func (s *Store) update(change func(*state) error) error {
	return s.view(func(st *state) error {
		if err := change(st); err != nil {
			return err
		}
		return s.save(st)
	})
}

func (s *Store) path() string { return filepath.Join(s.dir, "state.json") }

func (s *Store) load() (*state, error) {
	data, err := os.ReadFile(s.path())
	if err != nil {
		return nil, fmt.Errorf("read hub state: %w", err)
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("read hub state: %w", err)
	}
	if st.Members == nil || st.Projects == nil || st.Policies == nil {
		return nil, errors.New("hub state is missing required data")
	}
	if st.Rooms == nil {
		st.Rooms = make(map[string][]eventRecord)
	}
	if st.Claims == nil {
		st.Claims = make(map[string]lease)
	}
	upgraded := false
	for id, m := range st.Members {
		if m.EnrollmentID == "" {
			st.Members[id] = newMember(m.Role, m.TokenHash)
			upgraded = true
		}
	}
	if upgraded {
		if err := s.save(&st); err != nil {
			return nil, err
		}
	}
	return &st, nil
}

func (s *Store) locked(run func() error) error {
	lock, err := os.OpenFile(filepath.Join(s.dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open hub lock: %w", err)
	}
	defer lock.Close()
	if err := lockExclusive(lock); err != nil {
		return fmt.Errorf("lock hub state: %w", err)
	}
	return run()
}

func (s *Store) save(st *state) error {
	file, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := json.NewEncoder(file).Encode(st); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.path())
}
