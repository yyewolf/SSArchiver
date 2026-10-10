package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/alexedwards/argon2id"
	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

// ScoreFeedKey is the key of a ScoreSaber account's score feed.
func ScoreFeedKey(playerID string) service.FeedKey {
	return service.FeedKey{PlayerID: playerID, Platform: model.PlatformScoreSaber, Kind: model.KindScore}
}

// ScoreFeed returns a player's headline score feed (PlayerSummary.Sync).
func ScoreFeed(t testing.TB, svc *service.Service, playerID string) model.SyncFeed {
	t.Helper()
	sum, err := svc.GetPlayerSummary(context.Background(), playerID)
	if err != nil {
		t.Fatal(err)
	}
	return sum.Sync()
}

// NewService returns a Service over a temp DB/store with a fake clock and
// resolver. Player IDs are p00000000001, p00000000002, … in creation order.
func NewService(t testing.TB) (*service.Service, *Resolver, *Clock) {
	t.Helper()
	svc, _, res, clk := NewServiceWithDB(t)
	return svc, res, clk
}

// NewServiceWithDB is NewService that also returns the database.
func NewServiceWithDB(t testing.TB) (*service.Service, *gorm.DB, *Resolver, *Clock) {
	t.Helper()
	res := &Resolver{Players: DefaultPlayers()}
	svc, gdb, clk := NewServiceWith(t, scoresaber.NewPlatform(res, nil))
	return svc, gdb, res, clk
}

// NewServiceWith builds a test service over the given platforms. Player IDs
// are p00000000001, p00000000002, … in creation order.
func NewServiceWith(t testing.TB, plats ...platform.Platform) (*service.Service, *gorm.DB, *Clock) {
	t.Helper()
	gdb := OpenDB(t)
	store, err := storage.New(filepath.Join(t.TempDir(), "replays"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := platform.NewRegistry(plats...)
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(gdb, store, reg)
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	clk := NewClock(T0)
	svc.SetClock(clk.Now)
	var n atomic.Int64
	svc.SetIDGenerator(func() string { return fmt.Sprintf("p%011d", n.Add(1)) })
	return svc, gdb, clk
}

// AddPlayer adds a player by reference (platform auto-detected) and returns its ID.
func AddPlayer(t testing.TB, svc *service.Service, ref string) string {
	t.Helper()
	p, err := svc.AddPlayer(context.Background(), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// Upsert stores ScoreSaber score items for a player.
func Upsert(t testing.TB, svc *service.Service, playerID string, items ...scoresaber.ScoreItem) service.UpsertResult {
	t.Helper()
	res, err := svc.UpsertPlays(context.Background(), playerID, model.PlatformScoreSaber, scoresaber.Plays(items))
	if err != nil {
		t.Fatal(err)
	}
	return res
}
