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
	want := service.DefaultSettings
	want.InstanceTitle = "My Replays"
	want.PollInterval = 15 * time.Minute
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

func TestViewerSettings(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	st, err := svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ViewerShowHeadset || st.ViewerHeadsetColor != "#878787" || st.ViewerHeadsetAlpha != 1 || st.ReplayViewer != service.ViewerBeatLeader {
		t.Fatalf("viewer defaults = %+v", st)
	}
	st.ViewerShowHeadset = true
	st.ViewerHeadsetColor = "#Ff0080"
	st.ViewerHeadsetAlpha = 0.5
	st.ReplayViewer = service.ViewerArcViewer
	if err := svc.UpdateViewerSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Settings(ctx)
	if !got.ViewerShowHeadset || got.ViewerHeadsetColor != "#ff0080" || got.ViewerHeadsetAlpha != 0.5 || got.ReplayViewer != service.ViewerArcViewer {
		t.Fatalf("round trip = %+v", got)
	}
	if err := svc.UpdateSettings(ctx, service.Settings{InstanceTitle: "My Replays", PollInterval: 15 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Settings(ctx)
	if got.InstanceTitle != "My Replays" || !got.ViewerShowHeadset || got.ReplayViewer != service.ViewerArcViewer {
		t.Fatalf("general update must not touch viewer settings: %+v", got)
	}
	for _, bad := range []service.Settings{
		{ReplayViewer: "arc"},
		{ViewerHeadsetColor: "ff0080"},
		{ViewerHeadsetColor: "#ff008", ViewerHeadsetAlpha: 1},
		{ViewerHeadsetColor: "#ff0080", ViewerHeadsetAlpha: 1.5},
		{ViewerHeadsetColor: "#ff0080", ViewerHeadsetAlpha: -0.1},
	} {
		if err := svc.UpdateViewerSettings(ctx, bad); !errors.Is(err, service.ErrInvalidSettings) {
			t.Fatalf("UpdateViewerSettings(%+v) err = %v", bad, err)
		}
	}
}
