package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestPragmas(t *testing.T) {
	gdb := testutil.OpenDB(t)
	var fk, temp int
	var mode string
	gdb.Raw("PRAGMA foreign_keys").Scan(&fk)
	gdb.Raw("PRAGMA journal_mode").Scan(&mode)
	gdb.Raw("PRAGMA temp_store").Scan(&temp)
	if fk != 1 || mode != "wal" || temp != 2 {
		t.Fatalf("pragmas: foreign_keys=%d journal_mode=%s temp_store=%d", fk, mode, temp)
	}
}

func seed(t *testing.T, q *query.Query) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := q.Player.WithContext(ctx).Create(&model.Player{ID: "1", Name: "p", Enabled: true, AddedAt: now, BackfillState: model.BackfillPending, BackfillPage: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.Leaderboard.WithContext(ctx).Create(&model.Leaderboard{ID: 10, SongName: "Song"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Score.WithContext(ctx).Create(&model.Score{ID: 100, PlayerID: "1", LeaderboardID: 10, SetAt: now, ReplayState: model.ReplayPending}); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripWithPreload(t *testing.T) {
	q := query.Use(testutil.OpenDB(t))
	seed(t, q)
	sc, err := q.Score.WithContext(context.Background()).Preload(q.Score.Leaderboard).Where(q.Score.ID.Eq(100)).First()
	if err != nil {
		t.Fatal(err)
	}
	if sc.Leaderboard == nil || sc.Leaderboard.SongName != "Song" {
		t.Fatalf("leaderboard not preloaded: %+v", sc.Leaderboard)
	}
}

func TestDeletePlayerCascadesScores(t *testing.T) {
	q := query.Use(testutil.OpenDB(t))
	seed(t, q)
	ctx := context.Background()
	if _, err := q.Player.WithContext(ctx).Where(q.Player.ID.Eq("1")).Delete(); err != nil {
		t.Fatal(err)
	}
	n, err := q.Score.WithContext(ctx).Count()
	if err != nil || n != 0 {
		t.Fatalf("scores left after cascade: %d (%v)", n, err)
	}
}

func TestIsDuplicate(t *testing.T) {
	q := query.Use(testutil.OpenDB(t))
	seed(t, q)
	err := q.Player.WithContext(context.Background()).Create(&model.Player{ID: "1", AddedAt: time.Now()})
	if !db.IsDuplicate(err) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if db.IsDuplicate(nil) {
		t.Fatal("nil is not a duplicate")
	}
}
