package scoresaber_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

func newClient(t *testing.T, h http.HandlerFunc) (*scoresaber.Client, *scoresaber.Limiter) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	l := scoresaber.NewLimiter(300)
	return scoresaber.NewClient(l, scoresaber.WithBaseURL(srv.URL), scoresaber.WithUserAgent("test-agent")), l
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlayer(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/players/1922350521131465/basic" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write(fixture(t, "player.json"))
	})
	p, err := c.Player(context.Background(), "1922350521131465")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "1922350521131465" || p.Name != "oermer" || p.Country != "US" || !strings.HasSuffix(p.Avatar, ".jpg") {
		t.Fatalf("unexpected player: %+v", p)
	}
}

func TestScores(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v2/players/42/scores" || q.Get("sort") != "recent" || q.Get("limit") != "100" ||
			q.Get("personalBest") != "all" || q.Get("page") != "3" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write(fixture(t, "scores_page.json"))
	})
	page, err := c.Scores(context.Background(), "42", 3)
	if err != nil {
		t.Fatal(err)
	}
	if page.Metadata.TotalPages != 84 || len(page.Data) != 2 {
		t.Fatalf("metadata/data wrong: %+v", page.Metadata)
	}
	s := page.Data[0]
	if s.Score.ID != 94461650 || !s.Score.HasReplay || s.Score.Accuracy < 0.97 || s.Score.Mods[0] != "BE" ||
		s.Score.Device.HMD != "Quest 3" || s.Score.Player.Name != "oermer" {
		t.Fatalf("score wrong: %+v", s.Score)
	}
	if !s.Score.CreatedAt.Equal(time.Date(2026, 10, 4, 20, 39, 10, 461_000_000, time.UTC)) {
		t.Fatalf("createdAt = %v", s.Score.CreatedAt)
	}
	lb := page.Data[1].Leaderboard
	if lb.ID != 700290 || lb.Map.SongName != "Just The Way You Are" || lb.Map.LevelAuthorName != "Rail Zen" ||
		lb.Difficulty.Difficulty != 7 || lb.Realm.LeaderboardStatus != "RANKED" || lb.Realm.Stars != 7.25 {
		t.Fatalf("leaderboard wrong: %+v", lb)
	}
}

func TestReplayStreamsBody(t *testing.T) {
	payload := bytes.Repeat([]byte("ScoreSaber Replay "), 1000)
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/scores/94461650/replay" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	})
	rc, err := c.Replay(context.Background(), 94461650)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatal("payload mismatch")
	}
}

func TestNotFound(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Not Found","code":"NOT_FOUND"}`))
	})
	if _, err := c.Replay(context.Background(), 1); !errors.Is(err, scoresaber.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := c.Player(context.Background(), "1"); !errors.Is(err, scoresaber.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRateLimitedFeedsLimiter(t *testing.T) {
	c, l := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-remaining-short", "0")
		w.Header().Set("x-ratelimit-reset-short", "9")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := c.Scores(context.Background(), "1", 1); !errors.Is(err, scoresaber.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if l.Snapshot().BlockedUntil.IsZero() {
		t.Fatal("limiter not blocked after 429")
	}
}

func TestServerError(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	})
	_, err := c.Scores(context.Background(), "1", 1)
	var se *scoresaber.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusBadGateway || se.Body != "upstream down" {
		t.Fatalf("err = %#v", err)
	}
}

func TestMalformedJSON(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{nope")) })
	if _, err := c.Scores(context.Background(), "1", 1); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v, want decode error", err)
	}
}
