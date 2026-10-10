package platform_test

import (
	"slices"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

func TestLinks(t *testing.T) {
	a, b := plat("alpha", "aa", true, 0, "a.example"), plat("beta", "bb", false, 10, "b.example")
	a.ImageHosts = []string{"https://img.a.example", "https://shared.example"}
	b.ImageHosts = []string{"https://shared.example", "https://img.b.example"}
	r, err := platform.NewRegistry(b, a)
	if err != nil {
		t.Fatal(err)
	}
	legacy := platform.PlayRef{Platform: "alpha", Kind: model.KindScore, ExternalID: "42"}
	score := platform.PlayRef{Platform: "beta", Kind: model.KindScore, ExternalID: "x1"}
	attempt := platform.PlayRef{Platform: "beta", Kind: model.KindAttempt, ExternalID: "a 9"}
	for got, want := range map[string]string{
		r.PlayPath("/s", legacy):     "/s/42",
		r.PlayPath("/s", score):      "/s/bb/x1",
		r.PlayPath("/s", attempt):    "/s/bb/attempt/a%209",
		r.PlayPath("/embed", score):  "/embed/bb/x1",
		r.ReplayPath(legacy):         "/r/42.bin",
		r.ReplayPath(score):          "/r/bb/x1.bin",
		r.ReplayPath(attempt):        "/r/bb/attempt/a%209.bin",
		r.AccountPath("alpha", "42"): "/p/aa/42",
		r.AccountPath("beta", "a/b"): "/p/bb/a%2Fb",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	unknown := platform.PlayRef{Platform: "nope", Kind: model.KindScore, ExternalID: "1"}
	if r.PlayPath("/s", unknown) != "" || r.ReplayPath(unknown) != "" || r.AccountPath("nope", "1") != "" {
		t.Error("unknown platforms have no URL")
	}
	if got := r.ImageHosts(); !slices.Equal(got, []string{"https://img.a.example", "https://shared.example", "https://img.b.example"}) {
		t.Errorf("ImageHosts = %v (registry order, deduplicated)", got)
	}
}
