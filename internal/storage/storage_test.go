package storage_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/yyewolf/ssarchiver/internal/storage"
)

func newStore(t *testing.T) (*storage.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "replays")
	s, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestPutWritesAtomically(t *testing.T) {
	s, _ := newStore(t)
	content := strings.Repeat("replay-bytes", 100)
	size, sum, err := s.Put("76561198059961776", 42, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(content))
	if size != int64(len(content)) || sum != hex.EncodeToString(want[:]) {
		t.Fatalf("size=%d sum=%s", size, sum)
	}
	got, err := os.ReadFile(s.Path("76561198059961776", 42))
	if err != nil || string(got) != content {
		t.Fatalf("file content mismatch: %v", err)
	}
	if _, err := os.Stat(s.Path("76561198059961776", 42) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tmp file left behind")
	}
}

func TestPutSourceErrorLeavesNothing(t *testing.T) {
	s, _ := newStore(t)
	boom := errors.New("connection reset")
	r := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(boom))
	_, _, err := s.Put("1", 7, r)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped source error", err)
	}
	if errors.Is(err, storage.ErrWrite) {
		t.Fatal("source errors must not be reported as ErrWrite")
	}
	for _, p := range []string{s.Path("1", 7), s.Path("1", 7) + ".tmp"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after failed put", p)
		}
	}
}

func TestPutEmpty(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Put("1", 1, strings.NewReader("")); !errors.Is(err, storage.ErrEmpty) {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

func TestPutRejectsBadPlayerID(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"../etc", "", "A1", "1/2", "a.b"} {
		if _, _, err := s.Put(id, 1, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidID) {
			t.Fatalf("Put(%q) err = %v", id, err)
		}
	}
	if _, _, err := s.Put("k7m2q9x4c1ab", 1, strings.NewReader("x")); err != nil {
		t.Fatalf("opaque IDs must be accepted: %v", err)
	}
}

func TestPutWriteFailureIsErrWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	s, root := newStore(t)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })
	if _, _, err := s.Put("1", 1, strings.NewReader("x")); !errors.Is(err, storage.ErrWrite) {
		t.Fatalf("err = %v, want ErrWrite", err)
	}
}

func TestScanRemovesTmpAndListsDat(t *testing.T) {
	s, root := newStore(t)
	if _, _, err := s.Put("5", 50, strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "5", "51.dat.tmp"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "5", "notes.txt"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	entries, removed, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(entries) != 1 || entries[0].PlayerID != "5" || entries[0].ScoreID != 50 {
		t.Fatalf("removed=%d entries=%+v", removed, entries)
	}
}

func TestRemoveAndRemovePlayer(t *testing.T) {
	s, _ := newStore(t)
	_, _, _ = s.Put("9", 1, strings.NewReader("a"))
	_, _, _ = s.Put("9", 2, strings.NewReader("b"))
	if err := s.Remove("9", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("9", 1); err != nil {
		t.Fatal("removing a missing file must not fail")
	}
	if err := s.RemovePlayer("9"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open("9", 2); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
}

func TestHashFile(t *testing.T) {
	s, _ := newStore(t)
	_, sum, _ := s.Put("3", 3, strings.NewReader("hello"))
	size, got, err := storage.HashFile(s.Path("3", 3))
	if err != nil || size != 5 || got != sum {
		t.Fatalf("HashFile = %d %s %v", size, got, err)
	}
}
