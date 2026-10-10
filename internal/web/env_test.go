package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
	"github.com/yyewolf/ssarchiver/internal/viewer"
	"github.com/yyewolf/ssarchiver/internal/web"
)

const adminPW = "correct horse battery"

type statusStub struct{ st archiver.Status }

func (s *statusStub) Status() archiver.Status { return s.st }

type testEnv struct {
	t      *testing.T
	svc    *service.Service
	clk    *testutil.Clock
	status *statusStub
	fp     *testutil.FakePlatform // nil unless built with newEnvWith
	h      http.Handler
}

func newEnv(t *testing.T) *testEnv { return newEnvWith(t, nil) }

// newEnvWith also registers the fake third platform when fp is not nil.
func newEnvWith(t *testing.T, fp *testutil.FakePlatform) *testEnv {
	t.Helper()
	plats := []platform.Platform{scoresaber.NewPlatform(&testutil.Resolver{Players: testutil.DefaultPlayers()}, nil)}
	if fp != nil {
		plats = append(plats, fp.Platform())
	}
	svc, _, clk := testutil.NewServiceWith(t, plats...)
	vh := viewer.NewHandler(fstest.MapFS{"index.html.gz": {Data: testutil.Gzip("<html>viewer</html>")}}, "test")
	st := &statusStub{st: archiver.Status{State: archiver.StateIdle, Since: testutil.T0}}
	h := web.New(web.Deps{Service: svc, Status: st, Viewer: vh, Config: config.Config{HourlyBudget: 300, BaseURL: "https://replays.example.com"}})
	mux := http.NewServeMux()
	h.Routes(mux)
	return &testEnv{t: t, svc: svc, clk: clk, status: st, fp: fp, h: h.Middleware(mux)}
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withHeader(k, v string) reqOpt    { return func(r *http.Request) { r.Header.Set(k, v) } }
func htmx(target string) reqOpt {
	return func(r *http.Request) {
		r.Header.Set("HX-Request", "true")
		if target != "" {
			r.Header.Set("HX-Target", target)
		}
	}
}

func (e *testEnv) do(method, path string, form url.Values, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) setup() {
	e.t.Helper()
	if _, err := e.svc.Setup(context.Background(), "admin", adminPW); err != nil {
		e.t.Fatal(err)
	}
}

// login completes setup and returns a valid session cookie.
func (e *testEnv) login() *http.Cookie {
	e.t.Helper()
	e.setup()
	token, err := e.svc.Login(context.Background(), "admin", adminPW)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: httpx.SessionCookie, Value: token}
}

// seed tracks Alice (1001) with three scores: 1 archived (file on disk), 2 pending, 3 without replay.
// It returns the new player's ID.
func (e *testEnv) seed() string {
	e.t.Helper()
	ctx := context.Background()
	p, err := e.svc.AddPlayer(ctx, "1001", "")
	if err != nil {
		e.t.Fatal(err)
	}
	items := []scoresaber.ScoreItem{
		testutil.Item("1001", 1, 501, testutil.T0.Add(3*time.Minute), true),
		testutil.Item("1001", 2, 502, testutil.T0.Add(2*time.Minute), true),
		testutil.Item("1001", 3, 503, testutil.T0.Add(time.Minute), false),
	}
	items[0].Leaderboard.Map.SongName = "Hell of a time"
	items[0].Leaderboard.Realm.LeaderboardStatus = "RANKED"
	if _, err := e.svc.UpsertPlays(ctx, p.ID, model.PlatformScoreSaber, scoresaber.Plays(items)); err != nil {
		e.t.Fatal(err)
	}
	sc, err := e.svc.GetScore(ctx, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	size, sum, err := e.svc.PutReplay(sc, strings.NewReader("ScoreSaber Replay bytes"))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.MarkReplayArchived(ctx, 1, size, sum); err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

// seedTP tracks Tess (testplat account abc): score t1 archived, score t2
// pending, attempt a1 archived. It returns her player ID.
func (e *testEnv) seedTP() string {
	e.t.Helper()
	tess := testutil.AddPlayer(e.t, e.svc, "https://tp.example/u/abc")
	testutil.UpsertFake(e.t, e.svc, tess,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0.Add(2*time.Minute), true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0.Add(time.Minute), true),
		testutil.FakePlay(model.KindAttempt, "a1", "lb-a", testutil.T0, true))
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, tess, "t1"), "TP replay bytes")
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, tess, "a1"), "TP attempt bytes")
	return tess
}

func (e *testEnv) playerID(account string) string {
	e.t.Helper()
	p, err := e.svc.PlayerByIdentity(context.Background(), model.PlatformScoreSaber, account)
	if err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

func contains(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Fatalf("body does not contain %q:\n%s", p, body)
		}
	}
}
