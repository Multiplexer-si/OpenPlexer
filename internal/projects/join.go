package projects

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PendingJoin reserves a root and destination. The enrollment package owns Data.
type PendingJoin struct {
	Root string         `json:"root"`
	Home string         `json:"home"`
	Data jsontext.Value `json:"enrollment"`
}

func (p PendingJoin) Stage() string {
	return filepath.Join(filepath.Dir(p.Home), ".openplexer-join-"+pathDigest(p.Root))
}

// JoinLock serializes enrollment and discard for one canonical checkout.
// The registry lock protects reservation and publication. Both are released
// before the caller can start a hook or service.
type JoinLock struct {
	User, Root, dir string
	rootInfo        fs.FileInfo
}

func WithJoin(user, root string, apply func(JoinLock) error) error {
	root, rootInfo, err := Directory(root)
	if err != nil {
		return err
	}
	var joins string
	err = withRegistryLock(user, func(dir string, _ fs.FileInfo, _ Registry) error {
		joins = filepath.Join(dir, "joins")
		if err := os.Mkdir(joins, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		return privateDirectory(joins)
	})
	if err != nil {
		return err
	}
	unlock, err := joinFileLock(joins, "root-"+pathDigest(root)+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	join := JoinLock{User: user, Root: root, dir: joins, rootInfo: rootInfo}
	if err := removeInterruptedWrite(join.pendingPath()); err != nil {
		return err
	}
	return apply(join)
}

func joinFileLock(dir, name string) (func(), error) {
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		file, err = openRegistry(path)
	}
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedWithMode(info, 0o077) {
		file.Close()
		return nil, errors.New("the join lock must be a private file")
	}
	if err := lockRegistry(file); err != nil {
		file.Close()
		return nil, err
	}
	return func() { file.Close() }, nil
}

func (j JoinLock) WithHome(home string, apply func() error) error {
	unlock, err := joinFileLock(j.dir, "home-"+pathDigest(home)+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	return apply()
}

// HomePath resolves the explicit destination or computes the default.
func (j JoinLock) HomePath(home string) (string, error) {
	if home == "" {
		home = filepath.Join(filepath.Dir(j.dir), "homes", pathDigest(j.Root))
	}
	planned, err := plannedPath(home)
	if err != nil {
		return "", err
	}
	if overlaps(planned, j.dir) {
		return "", errors.New("the join home overlaps the private join directory")
	}
	return planned, nil
}

func plannedPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = cwd + string(filepath.Separator) + path
	}
	if canonical, _, err := Directory(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return canonical, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("the home path contains an unresolved directory entry")
	}
	name := filepath.Base(path)
	if name == "." || name == ".." {
		return "", errors.New("the home path cannot be resolved")
	}
	dir, err := plannedPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func pathDigest(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func (j JoinLock) pendingPath() string { return filepath.Join(j.dir, pathDigest(j.Root)+".json") }

func (j JoinLock) Pending() (*PendingJoin, error) {
	p, err := readJoin(j.pendingPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err == nil && p.Root != j.Root {
		return nil, errors.New("the pending join belongs to a different checkout")
	}
	return p, err
}

func readJoin(path string) (*PendingJoin, error) {
	data, err := ReadJoinFile(path)
	if err != nil {
		return nil, err
	}
	p := new(PendingJoin)
	if jsonv2.Unmarshal(data, p, jsonv2.RejectUnknownMembers(true)) != nil ||
		!absoluteClean(p.Root) || !absoluteClean(p.Home) || len(p.Data) == 0 ||
		filepath.Base(path) != pathDigest(p.Root)+".json" {
		return nil, errors.New("invalid pending join reservation")
	}
	return p, nil
}

// ReadJoinFile reads private enrollment metadata without following a symlink.
func ReadJoinFile(path string) ([]byte, error) {
	file, err := openRegistry(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedWithMode(info, 0o077) {
		return nil, errors.New("join state must be a private regular file")
	}
	const limit = 16 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && len(data) > limit {
		err = errors.New("join state is too large")
	}
	return data, err
}

// WriteJoinFile atomically replaces one private enrollment file.
func WriteJoinFile(path string, data []byte) error {
	if err := removeInterruptedWrite(path); err != nil {
		return err
	}
	tmp, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if err := errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func removeInterruptedWrite(path string) error {
	if err := os.Remove(path + ".tmp"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedWithMode(info, 0o077) {
		return errors.New("the join directory must be private and owned by the current user")
	}
	return nil
}

func (j JoinLock) RegisteredHome() (string, error) {
	r, err := Read(j.User)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	claim, err := r.Lookup(j.Root)
	if errors.Is(err, ErrUnregistered) || err == nil && claim.Root != j.Root {
		return "", nil
	}
	return claim.Home, err
}

// Check rejects conflicts before the client contacts the hub.
func (j JoinLock) Check(home string) error {
	return withRegistryLock(j.User, func(dir string, _ fs.FileInfo, r Registry) error {
		return j.check(dir, r, home)
	})
}

func (j JoinLock) check(dir string, r Registry, home string) error {
	if !sameDirectory(j.Root, j.rootInfo) {
		return errors.New("the checkout directory changed during join")
	}
	planned, err := j.HomePath(home)
	if err != nil || planned != home {
		return errors.New("the home path changed during join")
	}
	stage := (PendingJoin{Root: j.Root, Home: home}).Stage()
	for _, c := range r.claims {
		if c.Root == j.Root || c.info != nil && os.SameFile(j.rootInfo, c.info) {
			if c.Home != home {
				return errors.New("the checkout is already registered with another home")
			}
		} else if overlaps(c.Home, home) || overlaps(c.Home, stage) {
			return errors.New("the home conflicts with another registered checkout")
		}
	}
	return checkJoinReservations(dir, j.Root, j.rootInfo, home, true)
}

func checkJoinReservations(dir, root string, rootInfo fs.FileInfo, home string, joining bool) error {
	path := filepath.Join(dir, "joins")
	if err := privateDirectory(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	stage := (PendingJoin{Root: root, Home: home}).Stage()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p, err := readJoin(filepath.Join(path, e.Name()))
		if err != nil {
			return err
		}
		if joining && p.Root == root && p.Home == home {
			continue
		}
		if p.Root == root || sameDirectory(p.Root, rootInfo) ||
			overlaps(p.Home, home) || overlaps(p.Stage(), home) ||
			joining && (overlaps(p.Home, stage) || overlaps(p.Stage(), stage)) {
			return errors.New("a pending join reserves this checkout or home; resume it or use openplexer join --discard-pending")
		}
	}
	return nil
}

func overlaps(a, b string) bool {
	within := func(parent, child string) bool {
		path, err := filepath.Rel(parent, child)
		return err == nil && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}

func (j JoinLock) Reserve(p PendingJoin) error {
	return withRegistryLock(j.User, func(dir string, _ fs.FileInfo, r Registry) error {
		if err := j.check(dir, r, p.Home); err != nil {
			return err
		}
		for _, path := range []string{p.Home, p.Stage(), j.pendingPath()} {
			if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
				return errors.New("a home, stage or pending reservation already exists")
			}
		}
		doc, err := jsonv2.Marshal(p)
		if err != nil {
			return err
		}
		return WriteJoinFile(j.pendingPath(), doc)
	})
}

// Stage opens the private staged home, creating it beside the final home
// so that publication is a rename even for a custom destination.
func (j JoinLock) Stage(p PendingJoin) (string, error) {
	if !sameDirectory(j.Root, j.rootInfo) {
		return "", errors.New("the checkout directory changed during join")
	}
	if err := os.MkdirAll(filepath.Dir(p.Home), 0o700); err != nil {
		return "", err
	}
	if parent, _, err := Directory(filepath.Dir(p.Home)); err != nil || parent != filepath.Dir(p.Home) {
		return "", errors.New("the staged home's parent changed during join")
	}
	path := p.Stage()
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	return path, privateDirectory(path)
}

func (j JoinLock) Publish(p PendingJoin, rename bool, afterRename func() error) error {
	return withRegistryLock(j.User, func(dir string, info fs.FileInfo, r Registry) error {
		if err := j.check(dir, r, p.Home); err != nil {
			return err
		}
		if rename {
			if err := privateDirectory(p.Stage()); err != nil {
				return err
			}
			if _, err := os.Lstat(p.Home); !errors.Is(err, fs.ErrNotExist) {
				return errors.New("another home appeared during join")
			}
			if err := os.Rename(p.Stage(), p.Home); err != nil {
				return err
			}
			if err := afterRename(); err != nil {
				return err
			}
		}
		home, homeInfo, err := Directory(p.Home)
		if err != nil || home != p.Home {
			return errors.New("the published home changed during join")
		}
		return registerLocked(dir, info, r, j.Root, j.Root, j.rootInfo, home, home, homeInfo,
			func(_ string, publish func() error) error { return publish() })
	})
}

func (j JoinLock) Release(p PendingJoin) error {
	return withRegistryLock(j.User, func(_ string, _ fs.FileInfo, r Registry) error {
		home, err := plannedPath(p.Home)
		if err != nil || home != p.Home || !sameDirectory(j.Root, j.rootInfo) {
			return errors.New("a directory changed during join")
		}
		if _, err := os.Lstat(p.Home); err == nil {
			claim, err := r.Lookup(j.Root)
			if err != nil || claim.Root != j.Root || claim.Home != p.Home {
				return errors.New("the home is in place; resume openplexer join to finish registration")
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := privateDirectory(p.Stage()); err == nil {
			if err := os.RemoveAll(p.Stage()); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Remove(j.pendingPath()); err != nil {
			return fmt.Errorf("could not release the pending join: %w", err)
		}
		return nil
	})
}
