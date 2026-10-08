// Package db opens the SQLite database and runs migrations.
package db

//go:generate go run ./gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// pragmas: WAL for concurrent reads, FKs for cascades, busy_timeout so the
// worker and HTTP handlers wait instead of failing, temp_store(memory) so
// SQLite never writes temp files outside the data dir (read-only rootfs),
// _txlock=immediate so write transactions take the lock up front (no
// SQLITE_BUSY on lock upgrade; also serialises first-run setup).
const pragmas = "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
	"&_pragma=temp_store(memory)&_pragma=synchronous(NORMAL)&_txlock=immediate"

// Open opens (creating if needed) the database at path.
func Open(path string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("db: create dir: %w", err)
	}
	gdb, err := gorm.Open(sqlite.Open("file:"+path+pragmas), &gorm.Config{
		Logger:         logger.Discard,
		NowFunc:        func() time.Time { return time.Now().UTC() },
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("db: handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(8)
	return gdb, nil
}

// Migrate creates/updates the schema.
func Migrate(gdb *gorm.DB) error {
	if err := gdb.AutoMigrate(model.All()...); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

// Close closes the underlying sql.DB.
func Close(gdb *gorm.DB) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// IsDuplicate reports whether err is a unique/primary-key violation.
func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "UNIQUE constraint failed")
}
