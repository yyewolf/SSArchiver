// Package testutil holds fakes and helpers shared by tests. It must only be
// imported from _test.go files.
package testutil

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db"
)

// OpenDB returns a migrated database in a temp dir, closed at test end.
func OpenDB(t testing.TB) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	return gdb
}
