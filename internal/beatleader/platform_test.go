package beatleader_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/beatleader"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

func decodeScores(t *testing.T) beatleader.ScorePage {
	t.Helper()
	var sp beatleader.ScorePage
	if err := json.Unmarshal(fixture(t, "scores.json"), &sp); err != nil {
		t.Fatal(err)
	}
	return sp
}

type fakeAPI struct {
	player     beatleader.Player
	page       beatleader.ScorePage
	err        error
	attempts   beatleader.ScorePage
	attemptErr error
	calls      []string // "attempts:page:count"
	replayURL  string
}

func (f *fakeAPI) Player(context.Context, string) (beatleader.Player, error) { return f.player, f.err }

func (f *fakeAPI) Scores(context.Context, string, int) (beatleader.ScorePage, error) {
	return f.page, f.err
}

func (f *fakeAPI) Attempts(_ context.Context, _ string, page, count int) (beatleader.ScorePage, error) {
	f.calls = append(f.calls, fmt.Sprintf("attempts:%d:%d", page, count))
	return f.attempts, f.attemptErr
}

func (f *fakeAPI) ScoreReplay(u string) bool { return beatleader.NewClient(nil, nil).ScoreReplay(u) }

func (f *fakeAPI) Replay(_ context.Context, u string) (io.ReadCloser, error) {
	f.replayURL = u
	return io.NopCloser(strings.NewReader("bsor")), nil
}

// ReplayAllowed of the fake only knows the CDN, so replays-storage URLs count as refused.
func (f *fakeAPI) ReplayAllowed(u string) bool {
	return strings.HasPrefix(u, "https://cdn.replays.beatleader.xyz/")
}

func TestPlaysConversion(t *testing.T) {
	sp := decodeScores(t)
	plays, refused := beatleader.Plays(sp.Data, beatleader.NewClient(nil, nil).ReplayAllowed)
	if refused != 0 || len(plays) != 3 {
		t.Fatalf("plays=%d refused=%d", len(plays), refused)
	}
	p, lb := plays[0], plays[0].Leaderboard
	if p.Kind != model.KindScore || p.EndType != model.EndClear || p.ExternalID != "12164051" || !p.PersonalBest ||
		p.Rank != 1736 || p.ModifiedScore != 825292 || p.UnmodifiedScore != 825292 || p.Accuracy != 0.797295 ||
		p.MissedNotes != 16 || p.BadCuts != 13 || p.MaxCombo != 423 || p.HMD != "Quest 2" || p.Profile != nil ||
		!p.SetAt.Equal(time.Date(2024, 1, 27, 11, 59, 48, 0, time.UTC)) ||
		!p.HasReplay || !strings.HasPrefix(p.ReplayURL, "https://cdn.replays.beatleader.xyz/12164051-") {
		t.Fatalf("play = %+v", p)
	}
	if lb.ExternalID != "1d3f5x71" || lb.Status != "RANKED" || lb.Stars != 7.2064004 || lb.Difficulty != 7 ||
		lb.DifficultyRaw != "Expert" || lb.GameMode != "Standard" || lb.SongName != "Night Raid with a Dragon" ||
		lb.SongAuthor != "Camellia" || lb.Mapper != "nolan121405" || lb.MaxScore != 1035115 ||
		!strings.HasPrefix(lb.CoverURL, "https://eu.cdn.beatsaver.com/") {
		t.Fatalf("leaderboard = %+v", lb)
	}
	// ScoreSaber reports the same map with an uppercase hash and "SoloStandard": one grouping key.
	if platform.MapKey(lb.SongHash, lb.GameMode, lb.Difficulty) != platform.MapKey("57511EE48555E00E031BD3B1DF90BA7BE5712B56", "SoloStandard", 7) {
		t.Fatal("map keys must match across platforms")
	}
	if q := plays[1]; !q.HasReplay || !strings.HasPrefix(q.ReplayURL, "https://api.beatleader.xyz/replays-storage/") ||
		q.Leaderboard.Stars != 0 || q.Leaderboard.Status != "UNRANKED" {
		t.Fatalf("replays-storage play = %+v", q)
	}
	if q := plays[2]; q.HasReplay || q.ReplayURL != "" || q.Mods != "DA,FS" || q.HMD != "9999" ||
		q.Leaderboard.Status != "QUALIFIED" || q.Leaderboard.Difficulty != 9 {
		t.Fatalf("third play = %+v", q)
	}

	none, refused := beatleader.Plays(sp.Data, func(string) bool { return false })
	if refused != 2 || none[0].HasReplay || none[0].ReplayURL != "" || len(none) != 3 {
		t.Fatalf("refused replays must be dropped and counted (a null replay is not refused): refused=%d %+v", refused, none[0])
	}
}

func TestHMDName(t *testing.T) {
	for code, want := range map[int]string{256: "Quest 2", 512: "Quest 3", 64: "Valve Index", 0: "Unknown", 9999: "9999"} {
		if got := beatleader.HMDName(code); got != want {
			t.Errorf("HMDName(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestFeedPage(t *testing.T) {
	ctx := context.Background()
	api := &fakeAPI{page: decodeScores(t)}
	a := beatleader.NewPlatform(api, nil, nil).Adapter
	pg, err := a.FeedPage(ctx, model.KindScore, "76561198038925092", 1)
	if err != nil || len(pg.Plays) != 3 || pg.TotalPages != 1 || pg.Refused != 1 {
		t.Fatalf("page = %+v %v", pg, err)
	}

	api.err = fmt.Errorf("%w: /player/1/scores", beatleader.ErrNotFound)
	pg, err = a.FeedPage(ctx, model.KindScore, "1", 1)
	if err != nil || len(pg.Plays) != 0 {
		t.Fatalf("a scores 404 is the end of the listing (spec §5.2): %+v %v", pg, err)
	}
	api.err = fmt.Errorf("%w: x", beatleader.ErrRateLimited)
	if _, err := a.FeedPage(ctx, model.KindScore, "1", 1); !errors.Is(err, platform.ErrRateLimited) {
		t.Fatalf("other errors pass through: %v", err)
	}
}

func TestResolveAndReplay(t *testing.T) {
	ctx := context.Background()
	api := &fakeAPI{player: beatleader.Player{ID: "76561198038925092", Name: "Yewolf", Avatar: "https://cdn.assets.beatleader.xyz/a.png", Country: "FR"}}
	a := beatleader.NewPlatform(api, nil, nil).Adapter
	prof, err := a.Resolve(ctx, "alias-or-id")
	if err != nil || prof != (platform.Profile{ExternalID: "76561198038925092", Name: "Yewolf", AvatarURL: "https://cdn.assets.beatleader.xyz/a.png", Country: "FR"}) {
		t.Fatalf("profile = %+v %v", prof, err)
	}
	rc, err := a.Replay(ctx, platform.ReplayRef{Kind: model.KindScore, ExternalID: "1", URL: "https://cdn.replays.beatleader.xyz/1.bsor"})
	if err != nil || api.replayURL != "https://cdn.replays.beatleader.xyz/1.bsor" {
		t.Fatalf("replay = %v %q", err, api.replayURL)
	}
	_ = rc.Close()
	if _, err := a.Replay(ctx, platform.ReplayRef{Kind: model.KindScore, ExternalID: "2"}); err == nil {
		t.Fatal("a row without a URL has nothing to download")
	}
	if access, _, err := a.ProbeAccess(ctx, model.KindScore, "1"); access != model.AccessNA || err != nil {
		t.Fatalf("probe = %s %v", access, err)
	}
}

func TestRegistryEntry(t *testing.T) {
	apiL, cdnL := beatleader.NewAPILimiter(), beatleader.NewCDNLimiter()
	bl := beatleader.NewPlatform(&fakeAPI{}, apiL, cdnL)
	reg, err := platform.NewRegistry(scoresaber.NewPlatform(nil, nil), bl)
	if err != nil {
		t.Fatal(err)
	}
	if bl.Name != beatleader.Name || bl.Slug != "bl" || bl.DisplayName != "BeatLeader" || bl.Priority != 10 || !bl.PBOnly ||
		bl.Legacy || bl.ReplayExt != ".bsor" || bl.ProfileURL("7") != "https://beatleader.com/u/7" {
		t.Fatalf("entry = %+v", bl)
	}
	if f, ok := bl.Feed(model.KindScore); !ok || f.Optional || len(bl.Feeds) != 2 {
		t.Fatalf("feeds = %+v", bl.Feeds)
	}
	att, ok := bl.Feed(model.KindAttempt)
	if !ok || !att.Optional || !att.NeedsAccess || att.AccessHint == nil ||
		att.AccessHint.Title != "Attempt history is private on BeatLeader" || len(att.AccessHint.Steps) != 4 ||
		!strings.Contains(att.AccessHint.Steps[2], "Public history (auto-synced)") ||
		!strings.Contains(att.AccessHint.Steps[3], "Reload the page") ||
		att.AccessHint.LinkURL != "https://beatleader.com/settings" || att.AccessHint.Note == "" {
		t.Fatalf("attempt feed = %+v %+v", att, att.AccessHint)
	}
	if a := bl.Adapter; len(a.Limiters()) != 2 || a.FeedLimiter(model.KindScore) != beatleader.APILimiterName ||
		a.ReplayLimiter(model.KindScore) != beatleader.APILimiterName {
		t.Fatal("limiters: listings and replays are paced by the API limiter")
	}
	if a := beatleader.NewPlatform(&fakeAPI{}, nil, nil).Adapter; len(a.Limiters()) != 0 || a.FeedLimiter(model.KindScore) != "" {
		t.Fatal("nil limiters mean no rate limiting")
	}
	for _, c := range []struct{ in, plat, want, id string }{
		{"https://www.beatleader.com/u/76561198038925092", "", "beatleader", "76561198038925092"},
		{"beatleader.xyz/u/yewolf?tab=scores", "", "beatleader", "yewolf"},
		{"https://beatleader.com/u/76561198038925092/", "", "beatleader", "76561198038925092"},
		{"76561198038925092", "", "scoresaber", "76561198038925092"}, // bare IDs stay ScoreSaber's
		{"76561198038925092", "beatleader", "beatleader", "76561198038925092"},
	} {
		p, id, err := reg.ParseRef(c.in, c.plat)
		if err != nil || p.Name != c.want || id != c.id {
			t.Errorf("ParseRef(%q, %q) = %s %s %v", c.in, c.plat, p.Name, id, err)
		}
	}
	if _, _, err := reg.ParseRef("https://beatleader.com/leaderboard/1", ""); err == nil {
		t.Error("only profile URLs are player references")
	}
}

func decodeAttempts(t *testing.T) beatleader.ScorePage {
	t.Helper()
	var sp beatleader.ScorePage
	if err := json.Unmarshal(fixture(t, "attempts.json"), &sp); err != nil {
		t.Fatal(err)
	}
	return sp
}

func TestAttemptPlays(t *testing.T) {
	c := beatleader.NewClient(nil, nil)
	plays, skipped, refused := beatleader.AttemptPlays(decodeAttempts(t).Data, c.ReplayAllowed, c.ScoreReplay)
	if skipped != 2 || refused != 1 || len(plays) != 6 {
		t.Fatalf("plays=%d skipped=%d refused=%d", len(plays), skipped, refused)
	}
	var got []string
	for _, p := range plays {
		got = append(got, p.ExternalID+"/"+p.EndType)
	}
	want := "148437271/quit 148432628/restart 148432157/clear 144609827/practice 26723243/fail 26700001/unknown"
	if strings.Join(got, " ") != want {
		t.Fatalf("plays = %v (the two PB clears, on the CDN and on replays-storage, are skipped)", got)
	}
	q := plays[0]
	if q.Kind != model.KindAttempt || q.PersonalBest || q.EndTime == nil || *q.EndTime != 22.91243 ||
		!q.SetAt.Equal(time.Date(2026, 10, 4, 21, 27, 37, 0, time.UTC)) || !q.HasReplay ||
		!strings.HasPrefix(q.ReplayURL, "https://api.beatleader.xyz/otherreplays/") || q.Leaderboard.ExternalID != "51e10x91" {
		t.Fatalf("quit = %+v", q)
	}
	if clear := plays[2]; !clear.HasReplay || clear.ReplayURL != "https://api.beatleader.xyz/otherreplays/34897106.bsor" {
		t.Fatalf("a non-PB clear is kept with its replay: %+v", clear)
	}
	if pr := plays[3]; pr.ModifiedScore != 38645 || pr.UnmodifiedScore != 193229 || pr.Mods != "SS,NF" {
		t.Fatalf("practice = %+v", pr)
	}
	fail := plays[4]
	if fail.HasReplay || fail.ReplayURL != "" || !fail.SetAt.Equal(time.Date(2024, 1, 27, 12, 2, 18, 0, time.UTC)) ||
		fail.Leaderboard.Status != "RANKED" || fail.Leaderboard.Stars != 10.066875 {
		t.Fatalf("fail without a replay = %+v", fail)
	}
	if u := plays[5]; u.HasReplay || u.ReplayURL != "" || u.HMD != "Quest 3" {
		t.Fatalf("refused replay = %+v", u)
	}
}

func TestAttemptFeedAndProbe(t *testing.T) {
	ctx := context.Background()
	api := &fakeAPI{attempts: decodeAttempts(t)}
	a := beatleader.NewPlatform(api, nil, nil).Adapter

	pg, err := a.FeedPage(ctx, model.KindAttempt, "76561198038925092", 2)
	if err != nil || len(pg.Plays) != 6 || pg.Skipped != 2 || pg.TotalPages != 1 {
		t.Fatalf("page = %d plays, skipped %d, pages %d, %v", len(pg.Plays), pg.Skipped, pg.TotalPages, err)
	}
	if pg.Refused != 5 { // this fake only allowlists the CDN
		t.Fatalf("refused = %d", pg.Refused)
	}
	if len(api.calls) != 1 || api.calls[0] != "attempts:2:100" {
		t.Fatalf("calls = %v", api.calls)
	}

	if access, total, err := a.ProbeAccess(ctx, model.KindAttempt, "x"); access != model.AccessPublic || total != 8 || err != nil {
		t.Fatalf("public probe = %s %d %v", access, total, err)
	}
	if api.calls[1] != "attempts:1:1" {
		t.Fatalf("a probe reads one item: %v", api.calls)
	}
	api.attemptErr = fmt.Errorf("%w: /player/x/scoresstats", beatleader.ErrUnauthorized)
	if access, _, err := a.ProbeAccess(ctx, model.KindAttempt, "x"); access != model.AccessPrivate || err != nil {
		t.Fatalf("private probe = %s %v", access, err)
	}
	if _, err := a.FeedPage(ctx, model.KindAttempt, "x", 1); !errors.Is(err, platform.ErrUnauthorized) {
		t.Fatalf("a private listing reports ErrUnauthorized: %v", err)
	}
	api.attemptErr = fmt.Errorf("%w: x", beatleader.ErrRateLimited)
	if _, _, err := a.ProbeAccess(ctx, model.KindAttempt, "x"); !errors.Is(err, platform.ErrRateLimited) {
		t.Fatalf("other errors pass through: %v", err)
	}
	calls := len(api.calls)
	if access, _, err := a.ProbeAccess(ctx, model.KindScore, "x"); access != model.AccessNA || err != nil || len(api.calls) != calls {
		t.Fatal("the score feed needs no probe")
	}
}
