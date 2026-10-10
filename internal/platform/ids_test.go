package platform_test

import (
	"regexp"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/platform"
)

func TestNewPlayerID(t *testing.T) {
	re := regexp.MustCompile(`^[a-hjkmnp-tv-z][0-9a-hjkmnp-tv-z]{11}$`)
	seen := map[string]bool{}
	for range 2000 {
		id := platform.NewPlayerID()
		if !re.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
		if !platform.ValidPlayerID(id) {
			t.Fatalf("ValidPlayerID(%q) = false", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValidPlayerID(t *testing.T) {
	for id, want := range map[string]bool{
		"76561198038925092": true, // legacy ScoreSaber-derived IDs stay valid
		"k7m2q9x4c1ab":      true,
		"a-b":               true,
		"":                  false,
		"../etc":            false,
		"A1":                false,
		"a/b":               false,
		"x.dat":             false,
		"0123456789012345678901234567890123456789x": false, // 41 chars
	} {
		if got := platform.ValidPlayerID(id); got != want {
			t.Errorf("ValidPlayerID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestInternalIDBase(t *testing.T) {
	if platform.InternalIDBase != 1<<62 {
		t.Fatalf("InternalIDBase = %d", platform.InternalIDBase)
	}
}
