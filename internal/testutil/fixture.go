package testutil

import (
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/db"
)

// LoadSQLFile creates dbPath from a one-statement-per-line SQL file
// (internal/db/testdata/v1.sql) without migrating it.
func LoadSQLFile(t testing.TB, dbPath, sqlPath string) {
	t.Helper()
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	gdb, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(gdb) }()
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if _, err := sqlDB.Exec(line); err != nil {
			t.Fatalf("fixture: %v\n%s", err, line)
		}
	}
}
