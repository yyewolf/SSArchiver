package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

type scoreItem struct {
	id        int64
	hasReplay bool
}

func mustAdd(t *testing.T, svc *service.Service, ref string) string {
	t.Helper()
	return testutil.AddPlayer(t, svc, ref)
}

// upsert inserts items set one minute apart after the current fake time,
// on leaderboard id = score id + 1000.
func upsert(t *testing.T, svc *service.Service, playerID string, clk *testutil.Clock, items []scoreItem) service.UpsertResult {
	t.Helper()
	var list []scoresaber.ScoreItem
	for i, it := range items {
		list = append(list, testutil.Item(playerID, it.id, it.id+1000, clk.Now().Add(time.Duration(i+1)*time.Minute), it.hasReplay))
	}
	res, err := svc.UpsertScores(context.Background(), playerID, list)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
