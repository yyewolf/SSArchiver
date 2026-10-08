// Package storage stores replay files on disk as {root}/{player}/{score}.dat.
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
)

var (
	ErrWrite     = errors.New("storage: write failed")
	ErrEmpty     = errors.New("storage: empty replay")
	ErrInvalidID = errors.New("storage: invalid player id")
)

var playerIDRe = regexp.MustCompile(`^[0-9]{1,32}$`)

// ValidPlayerID reports whether id is a ScoreSaber player id (digits only).
func ValidPlayerID(id string) bool { return playerIDRe.MatchString(id) }

type Store struct{ root string }

type Entry struct {
	PlayerID string
	ScoreID  int64
	Path     string
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("%w: create %s: %w", ErrWrite, root, err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Path(playerID string, scoreID int64) string {
	return filepath.Join(s.root, playerID, strconv.FormatInt(scoreID, 10)+".dat")
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
func (s *Store) Put(playerID string, scoreID int64, r io.Reader) (int64, string, error) {
	if !ValidPlayerID(playerID) {
		return 0, "", fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	dir := filepath.Join(s.root, playerID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, "", fmt.Errorf("%w: mkdir %s: %w", ErrWrite, dir, err)
	}
	final := s.Path(playerID, scoreID)
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

func (s *Store) Open(playerID string, scoreID int64) (*os.File, error) {
	if !ValidPlayerID(playerID) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	return os.Open(s.Path(playerID, scoreID))
}

func (s *Store) Remove(playerID string, scoreID int64) error {
	if !ValidPlayerID(playerID) {
		return fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	if err := os.Remove(s.Path(playerID, scoreID)); err != nil && !errors.Is(err, os.ErrNotExist) {
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

// Scan deletes leftover *.tmp files and lists every {player}/{score}.dat.
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
		idStr, ok := strings.CutSuffix(name, ".dat")
		if !ok {
			return nil
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		player := filepath.Base(filepath.Dir(path))
		if err != nil || !ValidPlayerID(player) {
			return nil
		}
		entries = append(entries, Entry{PlayerID: player, ScoreID: id, Path: path})
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
