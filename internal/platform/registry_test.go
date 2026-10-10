package platform_test

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

type stubAdapter struct{}

func (stubAdapter) Resolve(context.Context, string) (platform.Profile, error) {
	return platform.Profile{}, nil
}

func (stubAdapter) FeedPage(context.Context, string, string, int) (platform.PlayPage, error) {
	return platform.PlayPage{}, nil
}

func (stubAdapter) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessNA, 0, nil
}

func (stubAdapter) Replay(context.Context, platform.ReplayRef) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (stubAdapter) Limiters() []platform.Limiter { return nil }
func (stubAdapter) FeedLimiter(string) string    { return "" }
func (stubAdapter) ReplayLimiter(string) string  { return "" }

var digits = regexp.MustCompile(`^[0-9]{1,32}$`)

func plat(name, slug string, legacy bool, prio int, host string) platform.Platform {
	re := regexp.MustCompile(`^https://` + regexp.QuoteMeta(host) + `/u/([0-9]+)$`)
	return platform.Platform{
		Name: name, Slug: slug, DisplayName: strings.ToUpper(name), Priority: prio, Legacy: legacy, ReplayExt: ".bin",
		ProfileURL: func(id string) string { return "https://" + host + "/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := re.FindStringSubmatch(in)
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: digits.MatchString,
		Feeds:   []platform.FeedSpec{{Kind: model.KindScore}},
		Adapter: stubAdapter{},
	}
}

func TestRegistryOrderAndLookup(t *testing.T) {
	r, err := platform.NewRegistry(plat("zeta", "zz", false, 10, "z.example"), plat("alpha", "aa", true, 0, "a.example"), plat("beta", "bb", false, 10, "b.example"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range r.All() {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "alpha,beta,zeta" {
		t.Fatalf("order = %v (priority, then name)", names)
	}
	if p, ok := r.BySlug("bb"); !ok || p.Name != "beta" {
		t.Fatalf("BySlug = %+v %v", p, ok)
	}
	if p, ok := r.Legacy(); !ok || p.Name != "alpha" {
		t.Fatalf("Legacy = %+v %v", p, ok)
	}
	if r.Priority("alpha") >= r.Priority("zeta") || r.Priority("nope") <= r.Priority("zeta") {
		t.Fatal("Priority must order known platforms and put unknown ones last")
	}
}

func TestRegistryRejectsInvalid(t *testing.T) {
	good := plat("alpha", "aa", true, 0, "a.example")
	for name, mutate := range map[string]func(p *platform.Platform){
		"bad name":            func(p *platform.Platform) { p.Name = "Alpha" },
		"bad slug":            func(p *platform.Platform) { p.Slug = "a1" },
		"slug map":            func(p *platform.Platform) { p.Slug = "map" },
		"no display name":     func(p *platform.Platform) { p.DisplayName = "" },
		"bad ext":             func(p *platform.Platform) { p.ReplayExt = "dat" },
		"no adapter":          func(p *platform.Platform) { p.Adapter = nil },
		"no score feed":       func(p *platform.Platform) { p.Feeds = []platform.FeedSpec{{Kind: model.KindAttempt, Optional: true}} },
		"optional score feed": func(p *platform.Platform) { p.Feeds = []platform.FeedSpec{{Kind: model.KindScore, Optional: true}} },
		"unknown kind": func(p *platform.Platform) {
			p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: "bogus", Optional: true})
		},
		"duplicate kind": func(p *platform.Platform) { p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: model.KindScore}) },
		"required needs access": func(p *platform.Platform) {
			p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: model.KindAttempt, NeedsAccess: true})
		},
	} {
		p := good
		p.Feeds = append([]platform.FeedSpec(nil), good.Feeds...)
		mutate(&p)
		if _, err := platform.NewRegistry(p); err == nil {
			t.Errorf("%s: NewRegistry accepted %+v", name, p)
		}
	}
	if _, err := platform.NewRegistry(good, plat("alpha", "bb", false, 1, "b.example")); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := platform.NewRegistry(good, plat("beta", "aa", false, 1, "b.example")); err == nil {
		t.Error("duplicate slug accepted")
	}
	if _, err := platform.NewRegistry(good, plat("beta", "bb", true, 1, "b.example")); err == nil {
		t.Error("two legacy platforms accepted")
	}
	if _, err := platform.NewRegistry(); err == nil {
		t.Error("empty registry accepted")
	}
}

func TestParseRef(t *testing.T) {
	r, err := platform.NewRegistry(plat("alpha", "aa", true, 0, "a.example"), plat("beta", "bb", false, 10, "b.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		in, hint, wantPlat, wantID string
		ok                         bool
	}{
		{"https://a.example/u/42", "", "alpha", "42", true},
		{"https://b.example/u/42", "", "beta", "42", true}, // URLs pick their platform
		{"  42 ", "", "alpha", "42", true},                 // bare IDs go to the legacy platform
		{"42", "beta", "beta", "42", true},                 // unless a platform is chosen
		{"https://b.example/u/7", "beta", "beta", "7", true},
		{"https://a.example/u/7", "beta", "", "", false}, // URL of another platform than the chosen one
		{"nope", "", "", "", false},
		{"42", "gamma", "", "", false},
	} {
		p, id, err := r.ParseRef(c.in, c.hint)
		if !c.ok {
			if !errors.Is(err, platform.ErrInvalidRef) {
				t.Errorf("ParseRef(%q, %q) err = %v, want ErrInvalidRef", c.in, c.hint, err)
			}
			continue
		}
		if err != nil || p.Name != c.wantPlat || id != c.wantID {
			t.Errorf("ParseRef(%q, %q) = %s %q %v", c.in, c.hint, p.Name, id, err)
		}
	}
}

func TestFeedLookup(t *testing.T) {
	p := plat("alpha", "aa", true, 0, "a.example")
	p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: model.KindAttempt, Optional: true, NeedsAccess: true})
	if _, err := platform.NewRegistry(p); err != nil {
		t.Fatal(err)
	}
	if f, ok := p.Feed(model.KindAttempt); !ok || !f.Optional {
		t.Fatalf("Feed(attempt) = %+v %v", f, ok)
	}
	if req := p.RequiredFeeds(); len(req) != 1 || req[0].Kind != model.KindScore {
		t.Fatalf("RequiredFeeds = %+v", req)
	}
}
