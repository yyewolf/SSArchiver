package archiver_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// tpEnv is a worker over ScoreSaber (fake client) and the fake third platform.
type tpEnv struct {
	svc *service.Service
	fc  *fakeClient
	fp  *testutil.FakePlatform
	w   *archiver.Worker
	clk *testutil.Clock
}

func newTPEnv(t *testing.T) *tpEnv {
	t.Helper()
	fc, fp := newFake(), testutil.NewFakePlatform()
	svc, _, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil), fp.Platform())
	return &tpEnv{svc: svc, fc: fc, fp: fp, w: archiver.New(svc), clk: clk}
}

func (e *tpEnv) drain(t *testing.T) {
	t.Helper()
	for range 100 {
		did, err := e.w.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
	}
	t.Fatal("worker still busy after 100 steps")
}

func (e *tpEnv) events(t *testing.T, kind string) string {
	t.Helper()
	evs, _, err := e.svc.ListEvents(context.Background(), service.EventFilter{Kind: kind, PerPage: 200})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		b.WriteString(ev.Level + ": " + ev.Message + "\n")
	}
	return b.String()
}

func TestPollRefreshesProfileViaResolve(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.Profiles["abc"] = platform.Profile{ExternalID: "abc", Name: "Tess Renamed", Country: "SE"}
	e.fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), false))
	e.drain(t)
	if p, _ := e.svc.GetPlayer(context.Background(), tess); p.Name != "Tess Renamed" {
		t.Fatalf("listings without a profile refresh it through Resolve, name = %q", p.Name)
	}
}

func TestPollResolveNotFoundDisablesAccount(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	delete(e.fp.Profiles, "abc")
	e.drain(t)
	sum, err := e.svc.GetPlayerSummary(context.Background(), tess)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Enabled || sum.Identities[0].Enabled || !strings.Contains(sum.Identities[0].LastError, "not found on TestPlat") {
		t.Fatalf("a vanished player disables the account only: %+v", sum.Identities[0])
	}
}

func TestRefusedReplaysAreLogged(t *testing.T) {
	e := newTPEnv(t)
	testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.Refused = 2
	e.fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), false))
	e.drain(t)
	if got := e.events(t, model.KindReplay); !strings.Contains(got, "warn: 2 replays skipped: their URL is not on the TestPlat allowlist") {
		t.Fatalf("events:\n%s", got)
	}
}

func TestChangedURLOfArchivedReplayIsLogged(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	pl := testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), true)
	e.fp.SetPlays(model.KindScore, "abc", pl)
	e.drain(t)
	sc := testutil.Row(t, e.svc, tess, "s1")
	if sc.ReplayState != model.ReplayArchived {
		t.Fatalf("state = %s", sc.ReplayState)
	}
	pl.ReplayURL = "https://tp.example/replays/elsewhere.tpr"
	e.fp.SetPlays(model.KindScore, "abc", pl)
	e.clk.Advance(2 * time.Hour)
	e.drain(t)
	if got := e.events(t, model.KindReplay); !strings.Contains(got, "warn: TestPlat now reports a different replay URL for 1 archived scores; the archived copies are kept") {
		t.Fatalf("events:\n%s", got)
	}
	path, _ := e.svc.ReplayPath(sc)
	if b, err := os.ReadFile(path); err != nil || string(b) != "tp-replay-s1" {
		t.Fatalf("archived file must be untouched: %q %v", b, err)
	}
}

// TestPlayerIDIsNeverAnAccountID gives a player the legacy-looking ID "123"
// while its accounts are ScoreSaber 1001 and testplat abc: any code that
// still treats players.id as a platform ID calls the platform with "123".
func TestPlayerIDIsNeverAnAccountID(t *testing.T) {
	e := newTPEnv(t)
	ctx := context.Background()
	e.svc.SetIDGenerator(func() string { return "123" })
	id := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	if id != "123" {
		t.Fatalf("id = %s", id)
	}
	if _, err := e.svc.LinkIdentity(ctx, id, "1001", model.PlatformScoreSaber); err != nil {
		t.Fatal(err)
	}
	e.fc.scores["1001"] = e.fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	e.fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), true))
	e.drain(t)
	calls := e.fc.calls()
	if len(calls) == 0 {
		t.Fatal("ScoreSaber account never polled")
	}
	for _, c := range append(calls, e.fp.Calls...) {
		if strings.Contains(c, "123") {
			t.Fatalf("a platform was called with the player ID: %v %v", calls, e.fp.Calls)
		}
	}
	if c, _ := e.svc.PlayerCounts(ctx, id); c.Archived != 3 {
		t.Fatalf("both accounts archived under the one player: %+v", c)
	}
}
