// Package storage stores replay files on disk: {root}/{player}/{row}{ext} for
// the legacy platform, {root}/{player}/{platform}/{row}{ext} for the others
// (spec §5.5).
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/platform"
)

var (
	ErrWrite     = errors.New("storage: write failed")
	ErrEmpty     = errors.New("storage: empty replay")
	ErrInvalidID = errors.New("storage: invalid player id")
)

// ValidPlayerID reports whether id is a well-formed player ID (legacy
// all-digit or opaque); see platform.ValidPlayerID.
func ValidPlayerID(id string) bool { return platform.ValidPlayerID(id) }

type Store struct{ root string }

var (
	dirRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)
	extRe = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
)

// Loc locates one replay file.
type Loc struct {
	PlayerID string
	Dir      string // platform directory; "" = legacy layout
	RowID    int64
	Ext      string // ".dat", ".bsor", …
}

func (l Loc) valid() bool {
	return ValidPlayerID(l.PlayerID) && (l.Dir == "" || dirRe.MatchString(l.Dir)) && extRe.MatchString(l.Ext)
}

func (s *Store) dir(l Loc) string {
	if l.Dir == "" {
		return filepath.Join(s.root, l.PlayerID)
	}
	return filepath.Join(s.root, l.PlayerID, l.Dir)
}

func (s *Store) Path(l Loc) string {
	return filepath.Join(s.dir(l), strconv.FormatInt(l.RowID, 10)+l.Ext)
}

type Entry struct {
	Loc
	Path string
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("%w: create %s: %w", ErrWrite, root, err)
	}
	return &Store{root: root}, nil
}

type trackingReader struct {
	r   io.Reader
	err error
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		t.err = err
	}
	return n, err
}

// Put streams r to a temp file, fsyncs, then renames it into place.
func (s *Store) Put(l Loc, r io.Reader) (int64, string, error) {
	if !l.valid() {
		return 0, "", fmt.Errorf("%w: %+v", ErrInvalidID, l)
	}
	dir := s.dir(l)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, "", fmt.Errorf("%w: mkdir %s: %w", ErrWrite, dir, err)
	}
	final := s.Path(l)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", fmt.Errorf("%w: create %s: %w", ErrWrite, tmp, err)
	}
	fail := func(e error) (int64, string, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return 0, "", e
	}
	h := sha256.New()
	src := &trackingReader{r: r}
	n, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		if src.err != nil {
			return fail(fmt.Errorf("storage: reading replay: %w", src.err))
		}
		return fail(fmt.Errorf("%w: write %s: %w", ErrWrite, tmp, err))
	}
	if n == 0 {
		return fail(ErrEmpty)
	}
	if err := f.Sync(); err != nil {
		return fail(fmt.Errorf("%w: fsync %s: %w", ErrWrite, tmp, err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("%w: close %s: %w", ErrWrite, tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("%w: rename %s: %w", ErrWrite, final, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) Open(l Loc) (*os.File, error) {
	if !l.valid() {
		return nil, fmt.Errorf("%w: %+v", ErrInvalidID, l)
	}
	return os.Open(s.Path(l))
}

func (s *Store) Remove(l Loc) error {
	if !l.valid() {
		return fmt.Errorf("%w: %+v", ErrInvalidID, l)
	}
	if err := os.Remove(s.Path(l)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: remove: %w", err)
	}
	return nil
}

func (s *Store) RemovePlayer(playerID string) error {
	if !ValidPlayerID(playerID) {
		return fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	if err := os.RemoveAll(filepath.Join(s.root, playerID)); err != nil {
		return fmt.Errorf("storage: remove player dir: %w", err)
	}
	return nil
}

// Scan deletes leftover *.tmp files and lists every replay file, both the
// legacy {player}/{row}{ext} and the per-platform {player}/{dir}/{row}{ext}
// layout; anything else is skipped.
func (s *Store) Scan() ([]Entry, int, error) {
	var entries []Entry
	removed := 0
	sroot, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, 0, fmt.Errorf("storage: scan: %w", err)
	}
	defer func() { _ = sroot.Close() }()
	err = filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".tmp") {
			if rel, rerr := filepath.Rel(s.root, path); rerr == nil && sroot.Remove(rel) == nil {
				removed++
			}
			return nil
		}
		rel, rerr := filepath.Rel(s.root, path)
		if rerr != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		var l Loc
		switch len(parts) {
		case 2:
			l.PlayerID = parts[0]
		case 3:
			l.PlayerID, l.Dir = parts[0], parts[1]
		default:
			return nil
		}
		l.Ext = filepath.Ext(name)
		id, err := strconv.ParseInt(strings.TrimSuffix(name, l.Ext), 10, 64)
		if err != nil {
			return nil
		}
		l.RowID = id
		if l.valid() {
			entries = append(entries, Entry{Loc: l, Path: path})
		}
		return nil
	})
	if err != nil {
		return nil, removed, fmt.Errorf("storage: scan: %w", err)
	}
	return entries, removed, nil
}

// HashFile returns size and hex sha256 of a file.
func HashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
