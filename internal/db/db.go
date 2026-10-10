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

// BackupName is the copy of the database db.Migrate takes, next to it, before
// upgrading a v1 database (spec §7 step 0). It is never overwritten.
const BackupName = "ssarchiver.pre-platforms.db"

// syncFeedsDDL creates sync_feeds by hand: GORM cannot express its composite
// foreign key to player_platforms (it emits the constraint on the wrong table).
const syncFeedsDDL = "CREATE TABLE IF NOT EXISTS `sync_feeds` (" +
	"`player_id` text NOT NULL,`platform` text NOT NULL,`feed` text NOT NULL," +
	"`enabled` numeric NOT NULL,`started_at` datetime NOT NULL,`access` text NOT NULL," +
	"`access_checked_at` datetime,`remote_total` integer NOT NULL," +
	"`backfill_state` text NOT NULL,`backfill_page` integer NOT NULL,`backfill_total_pages` integer NOT NULL," +
	"`backfill_retry_at` datetime,`last_polled_at` datetime,`last_error` text NOT NULL," +
	"PRIMARY KEY (`player_id`,`platform`,`feed`)," +
	"CONSTRAINT `fk_sync_feeds_identity` FOREIGN KEY (`player_id`,`platform`) " +
	"REFERENCES `player_platforms`(`player_id`,`platform`) ON DELETE CASCADE)"

// indexDDL creates the indexes on new columns. They run after the legacy
// backfill so unique indexes never see the empty column defaults.
var indexDDL = []string{
	"CREATE UNIQUE INDEX IF NOT EXISTS `idx_player_platforms_external` ON `player_platforms`(`platform`,`external_id`)",
	"CREATE UNIQUE INDEX IF NOT EXISTS `idx_leaderboards_external` ON `leaderboards`(`platform`,`external_id`)",
	"CREATE INDEX IF NOT EXISTS `idx_leaderboards_map_key` ON `leaderboards`(`map_key`)",
	"CREATE UNIQUE INDEX IF NOT EXISTS `idx_scores_external` ON `scores`(`platform`,`kind`,`external_id`)",
	"CREATE INDEX IF NOT EXISTS `idx_scores_player_kind_set` ON `scores`(`player_id`,`kind`,`set_at` desc)",
	"CREATE INDEX IF NOT EXISTS `idx_sync_events_platform` ON `sync_events`(`platform`)",
}

// Migrate creates/updates the schema and upgrades v1 databases onto the
// platform model (spec §7).
func Migrate(gdb *gorm.DB) error {
	legacy, err := hasColumn(gdb, "players", "backfill_state")
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	var before rowCounts
	if legacy {
		if err := backup(gdb); err != nil {
			return fmt.Errorf("db: migrate: backup: %w", err)
		}
		if before, err = countRows(gdb); err != nil {
			return fmt.Errorf("db: migrate: %w", err)
		}
	}
	if err := gdb.AutoMigrate(&model.Player{}, &model.PlayerPlatform{}); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	if err := gdb.Exec(syncFeedsDDL).Error; err != nil {
		return fmt.Errorf("db: migrate: sync_feeds: %w", err)
	}
	if err := gdb.AutoMigrate(model.All()...); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		if legacy {
			if err := backfillLegacy(tx); err != nil {
				return err
			}
		}
		for _, ddl := range indexDDL {
			if err := tx.Exec(ddl).Error; err != nil {
				return fmt.Errorf("index: %w", err)
			}
		}
		if legacy {
			return verifyMigration(tx, before)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("db: migrate: %w (the database before this upgrade was saved as %s next to it)", err, BackupName)
	}
	return nil
}

func hasColumn(gdb *gorm.DB, table, column string) (bool, error) {
	var n int64
	err := gdb.Raw("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&n).Error
	return n > 0, err
}

// backup copies the database next to itself unless a backup already exists
// (the first one is the pre-upgrade state; reruns must not replace it).
func backup(gdb *gorm.DB) error {
	var file string
	if err := gdb.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file).Error; err != nil {
		return err
	}
	if file == "" {
		return nil // in-memory database
	}
	dst := filepath.Join(filepath.Dir(file), BackupName)
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return gdb.Exec("VACUUM INTO ?", dst).Error
}

// backfillLegacy moves v1 rows onto the platform model (spec §7 step 2.1).
// Every statement is guarded, so rerunning it changes nothing.
func backfillLegacy(tx *gorm.DB) error {
	ss := model.PlatformScoreSaber
	steps := []struct {
		sql  string
		args []any
	}{
		{"UPDATE leaderboards SET platform = ?, external_id = CAST(id AS TEXT) WHERE platform = ''", []any{ss}},
		{"UPDATE leaderboards SET map_key = lower(song_hash) || '/' || " +
			"CASE WHEN substr(game_mode, 1, 4) = 'Solo' THEN substr(game_mode, 5) ELSE game_mode END || " +
			"'/' || difficulty WHERE map_key = ''", nil},
		{
			"UPDATE scores SET platform = ?, kind = ?, end_type = ?, external_id = CAST(id AS TEXT) WHERE platform = ''",
			[]any{ss, model.KindScore, model.EndClear},
		},
		{
			"INSERT INTO player_platforms (player_id, platform, external_id, enabled, linked_at, last_error) " +
				"SELECT p.id, ?, p.id, 1, p.added_at, '' FROM players p " +
				"WHERE NOT EXISTS (SELECT 1 FROM player_platforms pp WHERE pp.player_id = p.id AND pp.platform = ?)",
			[]any{ss, ss},
		},
		{
			"INSERT INTO sync_feeds (player_id, platform, feed, enabled, started_at, access, remote_total, " +
				"backfill_state, backfill_page, backfill_total_pages, backfill_retry_at, last_polled_at, last_error) " +
				"SELECT p.id, ?, ?, 1, p.added_at, ?, 0, p.backfill_state, p.backfill_page, p.backfill_total_pages, " +
				"p.backfill_retry_at, p.last_polled_at, p.last_error FROM players p " +
				"WHERE NOT EXISTS (SELECT 1 FROM sync_feeds f WHERE f.player_id = p.id AND f.platform = ? AND f.feed = ?)",
			[]any{ss, model.KindScore, model.AccessNA, ss, model.KindScore},
		},
	}
	for _, s := range steps {
		if err := tx.Exec(s.sql, s.args...).Error; err != nil {
			return fmt.Errorf("legacy backfill: %w", err)
		}
	}
	return nil
}

type rowCounts struct{ Players, Scores, Leaderboards int64 }

func countRows(gdb *gorm.DB) (rowCounts, error) {
	var c rowCounts
	err := gdb.Raw("SELECT (SELECT COUNT(*) FROM players) AS players, (SELECT COUNT(*) FROM scores) AS scores, " +
		"(SELECT COUNT(*) FROM leaderboards) AS leaderboards").Scan(&c).Error
	return c, err
}

// verifyMigration fails the upgrade transaction unless nothing was lost
// (spec §7 step 2.4). foreign_key_check alone would not notice deleted rows.
func verifyMigration(tx *gorm.DB, before rowCounts) error {
	after, err := countRows(tx)
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("row counts changed: before %+v, after %+v", before, after)
	}
	var orphans int64
	if err := tx.Raw("SELECT COUNT(*) FROM players p WHERE "+
		"NOT EXISTS (SELECT 1 FROM player_platforms pp WHERE pp.player_id = p.id) OR "+
		"NOT EXISTS (SELECT 1 FROM sync_feeds f WHERE f.player_id = p.id AND f.feed = ?)", model.KindScore).
		Scan(&orphans).Error; err != nil {
		return err
	}
	if orphans > 0 {
		return fmt.Errorf("%d players have no platform account or score feed", orphans)
	}
	var violations []map[string]any
	if err := tx.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign key check failed: %v", violations)
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
