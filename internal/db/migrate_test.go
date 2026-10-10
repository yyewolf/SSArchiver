package db_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// openFixture loads testdata/v1.sql into a fresh file database and returns
// it opened (not migrated) together with its path.
func openFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssarchiver.db")
	testutil.LoadSQLFile(t, path, "testdata/v1.sql")
	gdb, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	return gdb, path
}

func count(t *testing.T, gdb *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := gdb.Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrateLegacyFixture(t *testing.T) {
	gdb, path := openFixture(t)
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int64{"players": 3, "scores": 4, "leaderboards": 2, "users": 1, "sessions": 1, "settings": 1, "sync_events": 1, "player_platforms": 3, "sync_feeds": 3} {
		if got := count(t, gdb, table); got != want {
			t.Errorf("%s: %d rows, want %d", table, got, want)
		}
	}
	ctx := context.Background()
	q := query.Use(gdb)

	pp, err := q.PlayerPlatform.WithContext(ctx).Where(q.PlayerPlatform.PlayerID.Eq("111")).First()
	if err != nil {
		t.Fatal(err)
	}
	if pp.Platform != model.PlatformScoreSaber || pp.ExternalID != "111" || !pp.Enabled || pp.LastError != "" ||
		!pp.LinkedAt.Equal(mustTime(t, "2026-10-09T11:00:00Z")) {
		t.Errorf("identity = %+v", pp)
	}

	f, err := q.SyncFeed.WithContext(ctx).Where(q.SyncFeed.PlayerID.Eq("111")).First()
	if err != nil {
		t.Fatal(err)
	}
	if f.Platform != model.PlatformScoreSaber || f.Feed != model.KindScore || !f.Enabled || f.Access != model.AccessNA ||
		f.BackfillState != model.BackfillRunning || f.BackfillPage != 7 || f.BackfillTotalPages != 40 ||
		f.BackfillRetryAt == nil || !f.BackfillRetryAt.Equal(mustTime(t, "2026-10-09T12:05:00Z")) ||
		f.LastPolledAt == nil || !f.LastPolledAt.Equal(mustTime(t, "2026-10-09T12:00:00Z")) ||
		f.LastError != "player not found on ScoreSaber; tracking disabled" ||
		!f.StartedAt.Equal(mustTime(t, "2026-10-09T11:00:00Z")) {
		t.Errorf("feed = %+v", f)
	}
	never, err := q.SyncFeed.WithContext(ctx).Where(q.SyncFeed.PlayerID.Eq("2169974796454690")).First()
	if err != nil || never.LastPolledAt != nil || never.BackfillState != model.BackfillPending || never.BackfillPage != 1 {
		t.Errorf("never-polled feed = %+v %v", never, err)
	}

	lbs, err := q.Leaderboard.WithContext(ctx).Order(q.Leaderboard.ID).Find()
	if err != nil {
		t.Fatal(err)
	}
	if lbs[0].Platform != model.PlatformScoreSaber || lbs[0].ExternalID != "1001" ||
		lbs[0].MapKey != "4640065298e79dc3d61a15695aeb7fed95b42b30/Standard/9" ||
		lbs[1].MapKey != "4850c7bc85d89f832a96c6036347773e3858ced7/OneSaber/7" {
		t.Errorf("leaderboards = %+v %+v", lbs[0], lbs[1])
	}

	scores, err := q.Score.WithContext(ctx).Order(q.Score.ID).Find()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scores {
		if s.Platform != model.PlatformScoreSaber || s.Kind != model.KindScore || s.EndType != model.EndClear || s.ExternalID == "" || s.ReplayURL != nil {
			t.Errorf("score %d = platform %q kind %q end %q ext %q", s.ID, s.Platform, s.Kind, s.EndType, s.ExternalID)
		}
	}
	if scores[0].ExternalID != "5001" || scores[0].ReplayState != model.ReplayArchived || scores[0].ReplaySHA256 != "abc" {
		t.Errorf("archived score changed: %+v", scores[0])
	}

	for _, idx := range []string{"idx_player_platforms_external", "idx_leaderboards_external", "idx_leaderboards_map_key", "idx_scores_external", "idx_scores_player_kind_set", "idx_sync_events_platform"} {
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", idx).Scan(&n)
		if n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}

	backup := filepath.Join(filepath.Dir(path), db.BackupName)
	bdb, err := db.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(bdb) }()
	if got := count(t, bdb, "scores"); got != 4 {
		t.Errorf("backup has %d scores, want 4", got)
	}
	var hasPP int64
	bdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'player_platforms'").Scan(&hasPP)
	if hasPP != 0 {
		t.Error("backup must be taken before the schema changes")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	gdb, path := openFixture(t)
	for i := range 3 {
		if err := db.Migrate(gdb); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if got := count(t, gdb, "player_platforms"); got != 3 {
		t.Errorf("player_platforms = %d after reruns", got)
	}
	if got := count(t, gdb, "sync_feeds"); got != 3 {
		t.Errorf("sync_feeds = %d after reruns", got)
	}
	if got := count(t, gdb, "scores"); got != 4 {
		t.Errorf("scores = %d after reruns", got)
	}
	// The backup is the pre-upgrade state and is never overwritten.
	bdb, err := db.Open(filepath.Join(filepath.Dir(path), db.BackupName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(bdb) }()
	var hasPP int64
	bdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'player_platforms'").Scan(&hasPP)
	if hasPP != 0 {
		t.Error("backup was overwritten by a later run")
	}
}

func TestMigrateFreshDatabase(t *testing.T) {
	gdb := testutil.OpenDB(t)
	var ddl string
	gdb.Raw("SELECT sql FROM sqlite_master WHERE name = 'sync_feeds'").Scan(&ddl)
	if !strings.Contains(ddl, "REFERENCES `player_platforms`(`player_id`,`platform`) ON DELETE CASCADE") {
		t.Fatalf("sync_feeds must carry the composite FK, got %s", ddl)
	}
	gdb.Raw("SELECT sql FROM sqlite_master WHERE name = 'player_platforms'").Scan(&ddl)
	if strings.Contains(ddl, "sync_feeds") {
		t.Fatalf("player_platforms must not reference sync_feeds: %s", ddl)
	}
	ctx := context.Background()
	q := query.Use(gdb)
	now := time.Now().UTC()
	for _, id := range []string{"a1", "a2"} {
		if err := q.Player.WithContext(ctx).Create(&model.Player{ID: id, Name: id, Enabled: true, AddedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{PlayerID: "a1", Platform: model.PlatformScoreSaber, ExternalID: "42", Enabled: true, LinkedAt: now}); err != nil {
		t.Fatal(err)
	}
	err := q.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{PlayerID: "a2", Platform: model.PlatformScoreSaber, ExternalID: "42", Enabled: true, LinkedAt: now})
	if !db.IsDuplicate(err) {
		t.Fatalf("one account on two players must violate the unique index, got %v", err)
	}
	if err := q.SyncFeed.WithContext(ctx).Create(&model.SyncFeed{PlayerID: "a1", Platform: model.PlatformScoreSaber, Feed: model.KindScore, Enabled: true, StartedAt: now, Access: model.AccessNA, BackfillState: model.BackfillPending, BackfillPage: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Player.WithContext(ctx).Where(q.Player.ID.Eq("a1")).Delete(); err != nil {
		t.Fatal(err)
	}
	if got := count(t, gdb, "sync_feeds"); got != 0 {
		t.Fatalf("deleting a player must cascade to its feeds, %d left", got)
	}
}

// TestMigrateAddsDownloadEnabled: an existing database (created before the
// per-feed download pause) is upgraded with every feed resuming downloads.
func TestMigrateAddsDownloadEnabled(t *testing.T) {
	gdb := testutil.OpenDB(t)
	ctx := context.Background()
	q := query.Use(gdb)
	now := time.Now().UTC()
	if err := q.Player.WithContext(ctx).Create(&model.Player{ID: "a1", Name: "a1", Enabled: true, AddedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := q.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{PlayerID: "a1", Platform: model.PlatformScoreSaber, ExternalID: "42", Enabled: true, LinkedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := q.SyncFeed.WithContext(ctx).Create(&model.SyncFeed{PlayerID: "a1", Platform: model.PlatformScoreSaber, Feed: model.KindScore, Enabled: true, StartedAt: now, Access: model.AccessNA, BackfillState: model.BackfillPending, BackfillPage: 1}); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("ALTER TABLE sync_feeds DROP COLUMN download_enabled").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	var on []bool
	if err := gdb.Raw("SELECT download_enabled FROM sync_feeds").Scan(&on).Error; err != nil {
		t.Fatal(err)
	}
	if len(on) != 1 || !on[0] {
		t.Fatalf("upgraded feeds must resume downloads, got %v", on)
	}
}

func TestVerifyMigrationRejectsCountChange(t *testing.T) {
	gdb := testutil.OpenDB(t)
	err := db.VerifyMigration(gdb, db.RowCounts{Players: 99})
	if err == nil || !strings.Contains(err.Error(), "row counts changed") {
		t.Fatalf("err = %v", err)
	}
	if err := db.VerifyMigration(gdb, db.RowCounts{}); err != nil {
		t.Fatalf("empty database must verify, got %v", err)
	}
}

var legacyColumns = []string{"backfill_state", "backfill_page", "backfill_total_pages", "backfill_retry_at", "last_polled_at", "last_error"}

func playerColumns(t *testing.T, gdb *gorm.DB) map[string]bool {
	t.Helper()
	var names []string
	if err := gdb.Raw("SELECT name FROM pragma_table_info('players')").Scan(&names).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func TestMigrateDropsLegacyColumnsKeepingRows(t *testing.T) {
	gdb, _ := openFixture(t)
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	cols := playerColumns(t, gdb)
	for _, c := range legacyColumns {
		if cols[c] {
			t.Errorf("players.%s still present", c)
		}
	}
	for _, c := range []string{"id", "name", "avatar_url", "country", "enabled", "added_at"} {
		if !cols[c] {
			t.Errorf("players.%s missing", c)
		}
	}
	var idx int64
	gdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_players_backfill_state'").Scan(&idx)
	if idx != 0 {
		t.Error("idx_players_backfill_state not dropped")
	}
	// A table rebuild would have cascaded: every row must still be there.
	for table, want := range map[string]int64{"players": 3, "scores": 4, "player_platforms": 3, "sync_feeds": 3, "sessions": 1} {
		if got := count(t, gdb, table); got != want {
			t.Errorf("%s: %d rows, want %d", table, got, want)
		}
	}
	p, err := query.Use(gdb).Player.WithContext(context.Background()).Where(query.Use(gdb).Player.ID.Eq("111")).First()
	if err != nil || p.Name != "Gone" || p.Enabled || !p.AddedAt.Equal(mustTime(t, "2026-10-09T11:00:00Z")) {
		t.Fatalf("player 111 = %+v %v", p, err)
	}
}

func TestMigrateSecondRunAfterDrop(t *testing.T) {
	gdb, path := openFixture(t)
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(path), db.BackupName)
	st1, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("second run: %v", err)
	}
	st2, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) || st1.Size() != st2.Size() {
		t.Fatal("the pre-upgrade backup must not be rewritten")
	}
	if got := count(t, gdb, "scores"); got != 4 {
		t.Fatalf("scores = %d", got)
	}
}
