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
	"unicode"
	"unicode/utf8"
)

var (
	errInvalid      = errors.New("invalid input")
	errConflict     = errors.New("already exists or conflicts with current state")
	errUnauthorized = errors.New("invalid bearer token")
	errForbidden    = errors.New("forbidden")
	errNotFound     = errors.New("not found")
)

type Role string

const (
	Lead   Role = "lead"
	Member Role = "member"
)

type member struct {
	Role      Role              `json:"role"`
	TokenHash [sha256.Size]byte `json:"token_hash"`
}

type Project struct {
	ID   string `json:"id"`
	Repo string `json:"repo"`
}

type state struct {
	SigningKey []byte                  `json:"signing_key"`
	Members    map[string]member       `json:"members"`
	Projects   map[string]Project      `json:"projects"`
	Policies   map[string]SignedBundle `json:"policies"`
}

// Store is a data directory shared by the server and local admin commands.
// Every operation reloads state while holding the same process-safe lock.
type Store struct{ dir string }

func Open(dir string) *Store { return &Store{dir: dir} }

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
		return s.save(&state{
			SigningKey: private,
			Members:    make(map[string]member),
			Projects:   make(map[string]Project),
			Policies:   make(map[string]SignedBundle),
		})
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
	if !idPattern.MatchString(id) || (role != Lead && role != Member) {
		return "", fmt.Errorf("%w: member needs a valid id and role lead or member", errInvalid)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	err := s.update(func(st *state) error {
		if _, exists := st.Members[id]; exists {
			return fmt.Errorf("%w: member %q", errConflict, id)
		}
		st.Members[id] = member{Role: role, TokenHash: sha256.Sum256([]byte(token))}
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) RemoveMember(id string) error {
	return s.update(func(st *state) error {
		if _, exists := st.Members[id]; !exists {
			return fmt.Errorf("%w: member %q", errNotFound, id)
		}
		delete(st.Members, id)
		return nil
	})
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
		return nil
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
