package platform_test

import (
	"testing"

	"github.com/yyewolf/ssarchiver/internal/platform"
)

func TestMapKey(t *testing.T) {
	for _, c := range []struct {
		hash, mode string
		diff       int
		want       string
	}{
		// ScoreSaber: uppercase hash, "Solo" prefix.
		{"4640065298E79DC3D61A15695AEB7FED95B42B30", "SoloStandard", 9, "4640065298e79dc3d61a15695aeb7fed95b42b30/Standard/9"},
		// BeatLeader: lowercase hash, bare mode — must give the same key.
		{"4640065298e79dc3d61a15695aeb7fed95b42b30", "Standard", 9, "4640065298e79dc3d61a15695aeb7fed95b42b30/Standard/9"},
		{"ABC", "SoloOneSaber", 7, "abc/OneSaber/7"},
		{"abc", "OneSaber", 7, "abc/OneSaber/7"},
		{"abc", "Solo90Degree", 5, "abc/90Degree/5"},
		{"abc", "Lawless", 1, "abc/Lawless/1"},
	} {
		if got := platform.MapKey(c.hash, c.mode, c.diff); got != c.want {
			t.Errorf("MapKey(%q, %q, %d) = %q, want %q", c.hash, c.mode, c.diff, got, c.want)
		}
	}
}
