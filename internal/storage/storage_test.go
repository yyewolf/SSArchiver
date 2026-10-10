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
	loc := storage.Loc{PlayerID: "76561198059961776", RowID: 42, Ext: ".dat"}
	size, sum, err := s.Put(loc, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(content))
	if size != int64(len(content)) || sum != hex.EncodeToString(want[:]) {
		t.Fatalf("size=%d sum=%s", size, sum)
	}
	got, err := os.ReadFile(s.Path(loc))
	if err != nil || string(got) != content {
		t.Fatalf("file content mismatch: %v", err)
	}
	if _, err := os.Stat(s.Path(loc) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tmp file left behind")
	}
}

func TestPutSourceErrorLeavesNothing(t *testing.T) {
	s, _ := newStore(t)
	boom := errors.New("connection reset")
	r := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(boom))
	loc := storage.Loc{PlayerID: "1", RowID: 7, Ext: ".dat"}
	_, _, err := s.Put(loc, r)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped source error", err)
	}
	if errors.Is(err, storage.ErrWrite) {
		t.Fatal("source errors must not be reported as ErrWrite")
	}
	for _, p := range []string{s.Path(loc), s.Path(loc) + ".tmp"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after failed put", p)
		}
	}
}

func TestPutEmpty(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Put(storage.Loc{PlayerID: "1", RowID: 1, Ext: ".dat"}, strings.NewReader("")); !errors.Is(err, storage.ErrEmpty) {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

func TestPutRejectsBadPlayerID(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"../etc", "", "A1", "1/2", "a.b"} {
		if _, _, err := s.Put(storage.Loc{PlayerID: id, RowID: 1, Ext: ".dat"}, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidID) {
			t.Fatalf("Put(%q) err = %v", id, err)
		}
	}
	if _, _, err := s.Put(storage.Loc{PlayerID: "k7m2q9x4c1ab", RowID: 1, Ext: ".dat"}, strings.NewReader("x")); err != nil {
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
	if _, _, err := s.Put(storage.Loc{PlayerID: "1", RowID: 1, Ext: ".dat"}, strings.NewReader("x")); !errors.Is(err, storage.ErrWrite) {
		t.Fatalf("err = %v, want ErrWrite", err)
	}
}

func TestScanRemovesTmpAndListsDat(t *testing.T) {
	s, root := newStore(t)
	if _, _, err := s.Put(storage.Loc{PlayerID: "5", RowID: 50, Ext: ".dat"}, strings.NewReader("a")); err != nil {
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
	if removed != 1 || len(entries) != 1 || entries[0].PlayerID != "5" || entries[0].RowID != 50 {
		t.Fatalf("removed=%d entries=%+v", removed, entries)
	}
}

func TestRemoveAndRemovePlayer(t *testing.T) {
	s, _ := newStore(t)
	_, _, _ = s.Put(storage.Loc{PlayerID: "9", RowID: 1, Ext: ".dat"}, strings.NewReader("a"))
	_, _, _ = s.Put(storage.Loc{PlayerID: "9", RowID: 2, Ext: ".dat"}, strings.NewReader("b"))
	if err := s.Remove(storage.Loc{PlayerID: "9", RowID: 1, Ext: ".dat"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(storage.Loc{PlayerID: "9", RowID: 1, Ext: ".dat"}); err != nil {
		t.Fatal("removing a missing file must not fail")
	}
	if err := s.RemovePlayer("9"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(storage.Loc{PlayerID: "9", RowID: 2, Ext: ".dat"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
}

func TestHashFile(t *testing.T) {
	s, _ := newStore(t)
	loc := storage.Loc{PlayerID: "3", RowID: 3, Ext: ".dat"}
	_, sum, _ := s.Put(loc, strings.NewReader("hello"))
	size, got, err := storage.HashFile(s.Path(loc))
	if err != nil || size != 5 || got != sum {
		t.Fatalf("HashFile = %d %s %v", size, got, err)
	}
}

func TestPlatformLayout(t *testing.T) {
	s, root := newStore(t)
	l := storage.Loc{PlayerID: "k7m2q9x4c1ab", Dir: "beatleader", RowID: 1 << 62, Ext: ".bsor"}
	if _, _, err := s.Put(l, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "k7m2q9x4c1ab", "beatleader", "4611686018427387904.bsor")
	if s.Path(l) != want {
		t.Fatalf("Path = %s", s.Path(l))
	}
	legacy := storage.Loc{PlayerID: "k7m2q9x4c1ab", RowID: 5, Ext: ".dat"}
	if _, _, err := s.Put(legacy, strings.NewReader("y")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "k7m2q9x4c1ab", "beatleader", "junk.txt"), []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := s.Scan()
	if err != nil || len(entries) != 2 {
		t.Fatalf("Scan = %+v %v", entries, err)
	}
	for _, e := range entries {
		if e.Path != s.Path(e.Loc) {
			t.Fatalf("entry %+v does not round-trip", e)
		}
	}
	for _, bad := range []storage.Loc{
		{PlayerID: "a", Dir: "../x", RowID: 1, Ext: ".dat"},
		{PlayerID: "a", Dir: "Bad", RowID: 1, Ext: ".dat"},
		{PlayerID: "a", RowID: 1, Ext: "dat"},
		{PlayerID: "a", RowID: 1, Ext: ".d/t"},
	} {
		if _, _, err := s.Put(bad, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidID) {
			t.Errorf("Put(%+v) err = %v", bad, err)
		}
	}
}
