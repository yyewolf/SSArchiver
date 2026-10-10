package beatleader_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/beatleader"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type env struct {
	c        *beatleader.Client
	srv      *httptest.Server
	api, cdn *beatleader.Limiter
	hits     atomic.Int64
}

func newClient(t *testing.T, h http.HandlerFunc) *env {
	t.Helper()
	e := &env{api: beatleader.NewAPILimiter(), cdn: beatleader.NewCDNLimiter()}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(e.srv.Close)
	e.c = beatleader.NewClient(e.api, e.cdn, beatleader.WithBaseURL(e.srv.URL), beatleader.WithUserAgent("test-agent"))
	return e
}

func TestPlayer(t *testing.T) {
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/76561198038925092" || r.URL.Query().Get("stats") != "false" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write(fixture(t, "player.json"))
	})
	p, err := e.c.Player(context.Background(), "76561198038925092")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "76561198038925092" || p.Name != "Yewolf" || p.Country != "FR" || !strings.HasPrefix(p.Avatar, "https://avatars.akamai.steamstatic.com/") {
		t.Fatalf("player = %+v", p)
	}
}

func TestScores(t *testing.T) {
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/player/42/scores" || q.Get("sortBy") != "date" || q.Get("order") != "desc" ||
			q.Get("page") != "2" || q.Get("count") != "100" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write(fixture(t, "scores.json"))
	})
	sp, err := e.c.Scores(context.Background(), "42", 2)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Metadata.Total != 3 || sp.Metadata.TotalPages() != 1 || len(sp.Data) != 3 {
		t.Fatalf("page = %+v", sp.Metadata)
	}
	s := sp.Data[0]
	if s.ID != 12164051 || s.BaseScore != 825292 || s.HMD != 256 || s.LeaderboardID != "1d3f5x71" ||
		!s.Timeset.Time().Equal(time.Date(2024, 1, 27, 11, 59, 48, 0, time.UTC)) {
		t.Fatalf("score = %+v", s)
	}
	lb := s.Leaderboard
	if lb.ID != "1d3f5x71" || lb.Song.Hash != "57511ee48555e00e031bd3b1df90ba7be5712b56" || lb.Difficulty.ModeName != "Standard" ||
		lb.Difficulty.Status != 3 || lb.Difficulty.Stars == nil || *lb.Difficulty.Stars != 7.2064004 {
		t.Fatalf("leaderboard = %+v", lb)
	}
	if sp.Data[1].Leaderboard.Difficulty.Stars != nil || sp.Data[2].Replay != "" || sp.Data[2].Leaderboard.Song.SubName != "" {
		t.Fatalf("nulls must decode to zero values: %+v", sp.Data[1:])
	}
}

func TestUnixTimeAcceptsNumbersStringsAndNull(t *testing.T) {
	for in, want := range map[string]int64{`"1706356788"`: 1706356788, `1706356788`: 1706356788, `null`: 0, `""`: 0} {
		var u beatleader.UnixTime
		if err := u.UnmarshalJSON([]byte(in)); err != nil || int64(u) != want {
			t.Errorf("%s → %d %v", in, u, err)
		}
	}
	var u beatleader.UnixTime
	if err := u.UnmarshalJSON([]byte(`"soon"`)); err == nil {
		t.Error("garbage must not decode")
	}
}

func TestStatusErrors(t *testing.T) {
	reset := time.Now().UTC().Add(7 * time.Second).Truncate(time.Second)
	status := map[string]int{"/player/a": 404, "/player/b": 401, "/player/c": 403, "/player/d": 429, "/player/e": 500}
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player/d" {
			w.Header().Set("x-rate-limit-remaining", "0")
			w.Header().Set("x-rate-limit-reset", reset.Format(time.RFC3339Nano))
		}
		w.WriteHeader(status[r.URL.Path])
		_, _ = w.Write([]byte("nope"))
	})
	ctx := context.Background()
	if _, err := e.c.Player(ctx, "a"); !errors.Is(err, beatleader.ErrNotFound) {
		t.Errorf("404 → %v", err)
	}
	for _, id := range []string{"b", "c"} {
		if _, err := e.c.Player(ctx, id); !errors.Is(err, beatleader.ErrUnauthorized) {
			t.Errorf("%s → %v", id, err)
		}
	}
	if _, err := e.c.Player(ctx, "d"); !errors.Is(err, beatleader.ErrRateLimited) {
		t.Errorf("429 → %v", err)
	}
	if ok, at := e.api.Ready(time.Now()); ok || !at.Equal(reset) {
		t.Errorf("a 429 with an exhausted window must block until the reset: ok=%v at=%v want %v", ok, at, reset)
	}
	var se *beatleader.StatusError
	e2 := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if _, err := e2.c.Scores(ctx, "e", 1); !errors.As(err, &se) || se.StatusCode != 500 {
		t.Errorf("500 → %v", err)
	}
}

func TestReplayAllowlist(t *testing.T) {
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cdn-replays/redirect-out.bsor":
			http.Redirect(w, r, "/elsewhere/x.bsor", http.StatusFound)
		case "/cdn-replays/redirect-in.bsor":
			http.Redirect(w, r, "/otherreplays/target.bsor", http.StatusFound)
		default:
			_, _ = w.Write([]byte("bsor:" + r.URL.Path))
		}
	})
	ctx := context.Background()
	read := func(u string) (string, error) {
		rc, err := e.c.Replay(ctx, u)
		if err != nil {
			return "", err
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		return string(b), err
	}

	for path, limiter := range map[string]*beatleader.Limiter{
		"/cdn-replays/1.bsor": e.cdn, "/replays-storage/2.bsor": e.api, "/otherreplays/3.bsor": e.api,
	} {
		before := limiter.Snapshot().Windows[0].Used
		if got, err := read(e.srv.URL + path); err != nil || got != "bsor:"+path {
			t.Errorf("%s: %q %v", path, got, err)
		}
		if limiter.Snapshot().Windows[0].Used != before+1 {
			t.Errorf("%s must consume the %s limiter", path, limiter.Name())
		}
	}

	hits := e.hits.Load()
	for _, u := range []string{
		e.srv.URL + "/elsewhere/x.bsor",
		e.srv.URL + "/cdn-replays/../player/1",
		"https://evil.example/cdn-replays/x.bsor",
		"https://cdn.replays.beatleader.xyz.evil.example/x.bsor",
		"",
	} {
		if _, err := read(u); !errors.Is(err, beatleader.ErrReplayURL) {
			t.Errorf("%q must be refused, got %v", u, err)
		}
	}
	if e.hits.Load() != hits {
		t.Fatal("refused URLs must never be requested")
	}

	if _, err := read(e.srv.URL + "/cdn-replays/redirect-out.bsor"); !errors.Is(err, beatleader.ErrReplayURL) {
		t.Errorf("a redirect off the allowlist must be refused, got %v", err)
	}
	if got, err := read(e.srv.URL + "/cdn-replays/redirect-in.bsor"); err != nil || got != "bsor:/otherreplays/target.bsor" {
		t.Errorf("a redirect inside the allowlist is followed: %q %v", got, err)
	}
}

func TestDefaultAllowlist(t *testing.T) {
	c := beatleader.NewClient(nil, nil)
	for u, want := range map[string]bool{
		"https://cdn.replays.beatleader.xyz/1-2-Expert-Standard-ABC.bsor":     true,
		"https://api.beatleader.xyz/replays-storage/1-2-Expert-Standard.bsor": true,
		"https://api.beatleader.xyz/otherreplays/34897106.bsor":               true,
		"https://api.beatleader.xyz/player/1":                                 false,
		"http://cdn.replays.beatleader.xyz/1.bsor":                            false,
		"https://cdn.replays.beatleader.xyz.evil.example/1.bsor":              false,
	} {
		if c.ReplayAllowed(u) != want {
			t.Errorf("ReplayAllowed(%s) = %v", u, !want)
		}
	}
}
