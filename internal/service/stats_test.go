package service_test

import (
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
)

func TestReplayRateAndETA(t *testing.T) {
	if r := service.ReplayRatePerHour(300, 10, 10*time.Minute); r != 240 {
		t.Fatalf("rate = %v, want 240 (300 - 10 players × 6 polls)", r)
	}
	if r := service.ReplayRatePerHour(300, 100, 5*time.Minute); r != 1 {
		t.Fatalf("rate floor = %v, want 1", r)
	}
	if d := service.ETA(480, 240); d != 2*time.Hour {
		t.Fatalf("ETA = %v", d)
	}
	if d := service.ETA(0, 240); d != 0 {
		t.Fatalf("ETA with nothing pending = %v", d)
	}
}
