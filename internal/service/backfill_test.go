package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestDuePlayer(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	p, err := svc.DuePlayer(ctx, 10*time.Minute)
	if err != nil || p == nil {
		t.Fatalf("never-polled player must be due: %v %v", p, err)
	}
	_ = svc.MarkPolled(ctx, "1001")
	clk.Advance(time.Minute)
	_ = svc.MarkPolled(ctx, "1002")
	if p, _ := svc.DuePlayer(ctx, 10*time.Minute); p != nil {
		t.Fatalf("nobody should be due, got %s", p.ID)
	}
	clk.Advance(10 * time.Minute)
	if p, _ := svc.DuePlayer(ctx, 10*time.Minute); p == nil || p.ID != "1001" {
		t.Fatalf("oldest poll first, got %+v", p)
	}
	_ = svc.SetPlayerEnabled(ctx, "1001", false)
	if p, _ := svc.DuePlayer(ctx, 10*time.Minute); p == nil || p.ID != "1002" {
		t.Fatalf("disabled players are never due, got %+v", p)
	}
	at, ok, err := svc.NextPollAt(ctx, 10*time.Minute)
	if err != nil || !ok || !at.Equal(testutil.T0.Add(11*time.Minute)) {
		t.Fatalf("NextPollAt = %v %v %v", at, ok, err)
	}
}

func TestMarkPlayerErrorAndProfile(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	if err := svc.MarkPlayerError(ctx, "1001", "player not found", true); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.GetPlayer(ctx, "1001")
	if p.Enabled || p.LastError != "player not found" {
		t.Fatalf("player = %+v", p)
	}
	_ = svc.UpdatePlayerProfile(ctx, "1001", scoresaber.Player{Name: "Alice2", Avatar: "a.jpg"})
	p, _ = svc.GetPlayer(ctx, "1001")
	if p.Name != "Alice2" || p.AvatarURL != "a.jpg" || p.Country != "FR" {
		t.Fatalf("profile update = %+v (empty fields must not overwrite)", p)
	}
	_ = svc.MarkPolled(ctx, "1001")
	p, _ = svc.GetPlayer(ctx, "1001")
	if p.LastError != "" {
		t.Fatal("MarkPolled must clear last_error")
	}
}

func TestNextBackfillPlayer(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	if p, _ := svc.NextBackfillPlayer(ctx, ""); p != nil {
		t.Fatal("players must be polled once before backfilling")
	}
	_ = svc.MarkPolled(ctx, "1001")
	_ = svc.MarkPolled(ctx, "1002")
	if p, _ := svc.NextBackfillPlayer(ctx, ""); p == nil || p.ID != "1001" {
		t.Fatalf("got %+v", p)
	}
	if p, _ := svc.NextBackfillPlayer(ctx, "1001"); p == nil || p.ID != "1002" {
		t.Fatalf("round robin: got %+v", p)
	}
	if err := svc.SetBackfill(ctx, "1002", model.BackfillDone, 85, 84); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.NextBackfillPlayer(ctx, "1001"); p == nil || p.ID != "1001" {
		t.Fatalf("done players are skipped: got %+v", p)
	}
	if err := svc.DeferBackfill(ctx, "1001", clk.Now().Add(5*time.Minute), "502"); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.NextBackfillPlayer(ctx, ""); p != nil {
		t.Fatalf("deferred player returned early: %+v", p)
	}
	clk.Advance(5 * time.Minute)
	p, _ := svc.NextBackfillPlayer(ctx, "")
	if p == nil || p.ID != "1001" || p.LastError != "502" {
		t.Fatalf("deferred player after delay: %+v", p)
	}
	_ = svc.SetBackfill(ctx, "1001", model.BackfillRunning, 3, 84)
	p, _ = svc.GetPlayer(ctx, "1001")
	if p.BackfillPage != 3 || p.BackfillTotalPages != 84 || p.BackfillRetryAt != nil {
		t.Fatalf("SetBackfill = %+v", p)
	}
}
