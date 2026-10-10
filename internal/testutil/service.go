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

// NewServiceWithDB is NewService that also returns the database, for tests
// that need rows the service cannot create.
func NewServiceWithDB(t testing.TB) (*service.Service, *gorm.DB, *Resolver, *Clock) {
	t.Helper()
	gdb := OpenDB(t)
	store, err := storage.New(filepath.Join(t.TempDir(), "replays"))
	if err != nil {
		t.Fatal(err)
	}
	res := &Resolver{Players: map[string]scoresaber.Player{
		"1001": {ID: "1001", Name: "Alice", Country: "FR", Avatar: "https://cdn.scoresaber.com/avatars/1001.jpg"},
		"1002": {ID: "1002", Name: "Bob", Country: "US", Avatar: "https://cdn.scoresaber.com/avatars/1002.jpg"},
	}}
	svc := service.New(gdb, store, res)
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	clk := NewClock(T0)
	svc.SetClock(clk.Now)
	var n atomic.Int64
	svc.SetIDGenerator(func() string { return fmt.Sprintf("p%011d", n.Add(1)) })
	return svc, gdb, res, clk
}

// AddPlayer adds a player by ScoreSaber ID or URL and returns its player ID.
func AddPlayer(t testing.TB, svc *service.Service, ref string) string {
	t.Helper()
	p, err := svc.AddPlayer(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}
