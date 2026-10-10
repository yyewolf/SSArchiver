package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestListEvents(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindScores, PlayerID: new("paaaaaaaaaaa"), Message: "a"})
	clk.Advance(time.Second)
	svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindReplay, PlayerID: new("pbbbbbbbbbbb"), Message: "b"})
	clk.Advance(time.Second)
	svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindReplay, Message: "c"})

	all, total, err := svc.ListEvents(ctx, service.EventFilter{})
	if err != nil || total != 3 || all[0].Message != "c" {
		t.Fatalf("all = %v total=%d err=%v (newest first)", all, total, err)
	}
	errs, total, _ := svc.ListEvents(ctx, service.EventFilter{Level: model.LevelError})
	if total != 1 || errs[0].Message != "b" {
		t.Fatalf("level filter = %v", errs)
	}
	replay, total, _ := svc.ListEvents(ctx, service.EventFilter{Kind: model.KindReplay, PerPage: 1, Page: 2})
	if total != 2 || len(replay) != 1 || replay[0].Message != "b" {
		t.Fatalf("kind filter page 2 = %v", replay)
	}
	alice, total, _ := svc.ListEvents(ctx, service.EventFilter{PlayerID: "paaaaaaaaaaa"})
	if total != 1 || alice[0].Message != "a" {
		t.Fatalf("player filter = %v", alice)
	}
}

func TestPruneEvents(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Message: "old"})
	clk.Advance(8 * 24 * time.Hour)
	for range 6 {
		svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Message: "new"})
	}
	n, err := service.PruneEventsWith(svc, ctx, 7*24*time.Hour, 4)
	if err != nil || n != 3 {
		t.Fatalf("pruned %d, %v; want 3 (1 by age + 2 over the cap)", n, err)
	}
	_, total, _ := svc.ListEvents(ctx, service.EventFilter{})
	if total != 4 {
		t.Fatalf("remaining = %d", total)
	}
}
