package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestSettings(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	st, err := svc.Settings(ctx)
	if err != nil || st != service.DefaultSettings {
		t.Fatalf("defaults = %+v, %v", st, err)
	}
	want := service.Settings{InstanceTitle: "My Replays", PollInterval: 15 * time.Minute}
	if err := svc.UpdateSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Settings(ctx); got != want {
		t.Fatalf("round trip = %+v", got)
	}
	for _, bad := range []service.Settings{
		{InstanceTitle: "", PollInterval: 10 * time.Minute},
		{InstanceTitle: "x", PollInterval: 7 * time.Minute},
	} {
		if err := svc.UpdateSettings(ctx, bad); !errors.Is(err, service.ErrInvalidSettings) {
			t.Fatalf("UpdateSettings(%+v) err = %v", bad, err)
		}
	}
	if err := svc.SetWorkerPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Settings(ctx); !got.WorkerPaused || got.InstanceTitle != "My Replays" {
		t.Fatalf("pause must not touch other settings: %+v", got)
	}
}
