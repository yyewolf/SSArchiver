package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/api"
	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

type statusStub struct{}

func (statusStub) Status() archiver.Status { return archiver.Status{State: archiver.StateIdle} }

func newAPI(t *testing.T, admin bool) (*service.Service, http.Handler) {
	t.Helper()
	svc, _, _ := testutil.NewService(t)
	return svc, apiHandler(svc, admin)
}

// newMultiAPI also registers the fake third platform ("testplat").
func newMultiAPI(t *testing.T, admin bool) (*service.Service, http.Handler) {
	t.Helper()
	svc, _, _ := testutil.NewMultiService(t)
	return svc, apiHandler(svc, admin)
}

func apiHandler(svc *service.Service, admin bool) http.Handler {
	mux := http.NewServeMux()
	api.Register(mux, svc, statusStub{}, "test")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := httpx.WithBaseURL(r.Context(), "https://replays.example.com")
		if admin {
			ctx = httpx.WithUser(ctx, &model.User{ID: 1, Username: "admin"})
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func call(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any, []any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var obj map[string]any
	var arr []any
	raw := rec.Body.Bytes()
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &arr)
	} else {
		_ = json.Unmarshal(raw, &obj)
	}
	return rec.Code, obj, arr
}

func seed(t *testing.T, svc *service.Service) string {
	t.Helper()
	ctx := context.Background()
	p, err := svc.AddPlayer(ctx, "1001", "")
	if err != nil {
		t.Fatal(err)
	}
	items := []scoresaber.ScoreItem{
		testutil.Item("1001", 1, 501, testutil.T0.Add(2*time.Minute), true),
		testutil.Item("1001", 2, 502, testutil.T0.Add(time.Minute), true),
	}
	if _, err := svc.UpsertPlays(ctx, p.ID, model.PlatformScoreSaber, scoresaber.Plays(items)); err != nil {
		t.Fatal(err)
	}
	sc, err := svc.GetScore(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	size, sum, _ := svc.PutReplay(sc, strings.NewReader("replay"))
	if err := svc.MarkReplayArchived(ctx, 1, size, sum); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestPublicReads(t *testing.T) {
	svc, h := newAPI(t, false)
	id := seed(t, svc)

	code, _, players := call(t, h, http.MethodGet, "/api/v1/players", nil)
	if code != 200 || len(players) != 1 {
		t.Fatalf("list players = %d %v", code, players)
	}
	p := players[0].(map[string]any)
	if p["name"] != "Alice" || p["replays"].(map[string]any)["archived"].(float64) != 1 || p["url"] != "https://replays.example.com/p/"+id {
		t.Fatalf("player = %v", p)
	}

	code, one, _ := call(t, h, http.MethodGet, "/api/v1/players/"+id, nil)
	ids, _ := one["identities"].([]any)
	if code != 200 || len(ids) != 1 {
		t.Fatalf("player = %d %v", code, one)
	}
	id0 := ids[0].(map[string]any)
	feeds, _ := id0["feeds"].([]any)
	if id0["platform"] != "scoresaber" || id0["id"] != "1001" || len(feeds) != 1 ||
		feeds[0].(map[string]any)["kind"] != "score" || feeds[0].(map[string]any)["access"] != "n/a" {
		t.Fatalf("identities = %v", ids)
	}
	if one["backfill"].(map[string]any)["state"] == "" {
		t.Fatalf("deprecated backfill must still be filled: %v", one["backfill"])
	}

	code, byAcc, _ := call(t, h, http.MethodGet, "/api/v1/players/by/scoresaber/1001", nil)
	if code != 200 || byAcc["name"] != "Alice" {
		t.Fatalf("by-account lookup = %d %v", code, byAcc)
	}
	if code, _, _ := call(t, h, http.MethodGet, "/api/v1/players/by/scoresaber/9999", nil); code != 404 {
		t.Fatalf("unknown account = %d", code)
	}

	code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/"+id+"/scores?state=archived", nil)
	items := page["items"].([]any)
	if code != 200 || page["total"].(float64) != 1 || len(items) != 1 {
		t.Fatalf("scores = %d %v", code, page)
	}
	replay := items[0].(map[string]any)["replay"].(map[string]any)
	if replay["download_url"] != "https://replays.example.com/r/1.dat" || replay["embed_url"] != "https://replays.example.com/embed/1" {
		t.Fatalf("replay = %v", replay)
	}
	s1 := items[0].(map[string]any)
	if s1["id"].(float64) != 1 || s1["platform"] != "scoresaber" || s1["kind"] != "score" || s1["end_type"] != "clear" ||
		s1["external_id"] != "1" || s1["leaderboard"].(map[string]any)["external_id"] != "501" {
		t.Fatalf("score fields = %v", s1)
	}
	if c := ids[0].(map[string]any)["counts"].(map[string]any); c["archived"].(float64) != 1 || c["scores"].(float64) != 2 {
		t.Fatalf("account counts = %v", c)
	}

	code, sc, _ := call(t, h, http.MethodGet, "/api/v1/scores/2", nil)
	r2 := sc["replay"].(map[string]any)
	if code != 200 || r2["state"] != "pending" || r2["download_url"] != nil {
		t.Fatalf("pending score = %d %v", code, sc)
	}
	if lb := sc["leaderboard"].(map[string]any); lb["difficulty"] != "Expert+" || lb["song_name"] != "Song 502" {
		t.Fatalf("leaderboard = %v", lb)
	}

	for path, want := range map[string]int{
		"/api/v1/players/9":                               404,
		"/api/v1/players/9/scores":                        404,
		"/api/v1/scores/999":                              404,
		"/api/v1/players/" + id + "/scores?per_page=1000": 422,
		"/api/v1/players/" + id + "/scores?state=bogus":   422,
	} {
		if code, _, _ := call(t, h, http.MethodGet, path, nil); code != want {
			t.Errorf("%s = %d, want %d", path, code, want)
		}
	}
}

func TestSetFeedDownload(t *testing.T) {
	svc, h := newAPI(t, true)
	id := seed(t, svc)
	feedPath := "/api/v1/players/" + id + "/identities/scoresaber/feeds/score"

	code, f, _ := call(t, h, http.MethodPatch, feedPath+"/download", map[string]any{"enabled": false})
	if code != 200 || f["download_enabled"] != false {
		t.Fatalf("pause = %d %v", code, f)
	}
	if sc, err := svc.NextReplay(context.Background(), service.TierNew, "", nil); err != nil || sc != nil {
		t.Fatalf("paused feed offered %v (%v)", sc, err)
	}
	code, f, _ = call(t, h, http.MethodPatch, feedPath+"/download", map[string]any{"enabled": true})
	if code != 200 || f["download_enabled"] != true {
		t.Fatalf("resume = %d %v", code, f)
	}
	if code, _, _ := call(t, h, http.MethodPatch, "/api/v1/players/"+id+"/identities/scoresaber/feeds/attempt/download", map[string]any{"enabled": true}); code != 404 {
		t.Fatalf("unknown feed = %d, want 404", code)
	}
}

func TestAdminRequiresSession(t *testing.T) {
	_, h := newAPI(t, false)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/players"},
		{http.MethodPatch, "/api/v1/players/1001"},
		{http.MethodDelete, "/api/v1/players/1001"},
		{http.MethodPost, "/api/v1/players/1001/poll"},
		{http.MethodPost, "/api/v1/players/1001/identities"},
		{http.MethodPatch, "/api/v1/players/1001/identities/scoresaber"},
		{http.MethodDelete, "/api/v1/players/1001/identities/scoresaber"},
		{http.MethodPatch, "/api/v1/players/1001/identities/scoresaber/feeds/attempt"},
		{http.MethodPatch, "/api/v1/players/1001/identities/scoresaber/feeds/attempt/download"},
		{http.MethodPost, "/api/v1/players/1001/identities/scoresaber/feeds/attempt/check"},
		{http.MethodPost, "/api/v1/players/1001/merge"},
		{http.MethodGet, "/api/v1/sync"},
		{http.MethodPost, "/api/v1/sync/pause"},
		{http.MethodPost, "/api/v1/sync/retry"},
	} {
		code, _, _ := call(t, h, c.method, c.path, map[string]any{"ref": "1001", "enabled": true})
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, code)
		}
	}
}

func TestAdminWrites(t *testing.T) {
	svc, h := newAPI(t, true)
	ctx := context.Background()

	code, p, _ := call(t, h, http.MethodPost, "/api/v1/players", map[string]any{"ref": "https://scoresaber.com/u/1002"})
	if code != 201 || p["name"] != "Bob" {
		t.Fatalf("add = %d %v", code, p)
	}
	id := p["id"].(string)
	for ref, want := range map[string]int{"1002": 409, "nope": 422, "9999": 404} {
		if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players", map[string]any{"ref": ref}); code != want {
			t.Errorf("add %q = %d, want %d", ref, code, want)
		}
	}
	code, p, _ = call(t, h, http.MethodPatch, "/api/v1/players/"+id, map[string]any{"enabled": false})
	if code != 200 || p["enabled"] != false {
		t.Fatalf("patch = %d %v", code, p)
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players/"+id+"/poll", nil); code != 202 {
		t.Fatalf("poll = %d", code)
	}
	code, st, _ := call(t, h, http.MethodGet, "/api/v1/sync", nil)
	if code != 200 || st["state"] != "idle" {
		t.Fatalf("sync = %d %v", code, st)
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/sync/pause", nil); code != 204 {
		t.Fatalf("pause = %d", code)
	}
	if s, _ := svc.Settings(ctx); !s.WorkerPaused {
		t.Fatal("not paused")
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/sync/resume", nil); code != 204 {
		t.Fatalf("resume = %d", code)
	}
	code, rr, _ := call(t, h, http.MethodPost, "/api/v1/sync/retry", map[string]any{})
	if code != 200 || rr["requeued"].(float64) != 0 {
		t.Fatalf("retry = %d %v", code, rr)
	}
	if code, _, _ := call(t, h, http.MethodDelete, "/api/v1/players/"+id+"?delete_files=true", nil); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, _, _ := call(t, h, http.MethodDelete, "/api/v1/players/"+id, nil); code != 404 {
		t.Fatalf("second delete = %d", code)
	}
}

func TestOpenAPIDocument(t *testing.T) {
	_, h := newAPI(t, false)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/openapi.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`"list-players"`, `"add-player"`, `"session"`, `"ssa_session"`, `"link-identity"`, `"merge-player"`, `"update-feed"`, `"check-feed"`, `"get-play"`, `"min_score"`} {
		if !strings.Contains(body, want) {
			t.Errorf("openapi missing %s", want)
		}
	}
	docs := httptest.NewRecorder()
	h.ServeHTTP(docs, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/docs", nil))
	if docs.Code != 200 {
		t.Errorf("docs = %d", docs.Code)
	}
}

func TestPublicReadsAreCrossOrigin(t *testing.T) {
	_, h := newAPI(t, false)

	get := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
		req.Header.Set("Origin", "https://overlay.example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{"/api/v1/players", "/api/v1/players/1/scores", "/api/v1/scores/1"} {
		rec := get(http.MethodGet, path)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s GET: Access-Control-Allow-Origin = %q, want *", path, got)
		}
	}

	rec := get(http.MethodOptions, "/api/v1/players")
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS /api/v1/players = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("OPTIONS: Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, HEAD, OPTIONS" {
		t.Errorf("OPTIONS: Access-Control-Allow-Methods = %q, want GET, HEAD, OPTIONS", got)
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/sync"},
		{http.MethodOptions, "/api/v1/sync"},
		{http.MethodOptions, "/api/v1/sync/retry"},
	} {
		rec := get(tc.method, tc.path)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s %s: Access-Control-Allow-Origin = %q, want none (admin surface)", tc.method, tc.path, got)
		}
	}
}

func TestScoreTypeParam(t *testing.T) {
	svc, h := newMultiAPI(t, false)
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	fail := testutil.FakePlay(model.KindAttempt, "a1", "lb-a", testutil.T0, true)
	fail.EndType, fail.EndTime = model.EndFail, new(61.5)
	testutil.UpsertFake(t, svc, tess, testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0, true), fail)
	for q, want := range map[string]float64{"": 1, "type=fail": 1, "type=all": 2, "type=complete,fail": 2} {
		code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess+"/scores?"+q, nil)
		if code != 200 || page["total"].(float64) != want {
			t.Errorf("%q = %d total %v, want %v", q, code, page["total"], want)
		}
	}
	code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess+"/scores?type=fail", nil)
	a := page["items"].([]any)[0].(map[string]any)
	if code != 200 || a["kind"] != "attempt" || a["end_type"] != "fail" || a["end_time"] == nil ||
		a["url"] != "https://replays.example.com/s/tp/attempt/a1" {
		t.Fatalf("attempt DTO = %v", a)
	}
	if code, _, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess+"/scores?type=bogus", nil); code != 422 {
		t.Fatalf("bogus type = %d", code)
	}
}
