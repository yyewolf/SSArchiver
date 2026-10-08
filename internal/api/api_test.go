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
	mux := http.NewServeMux()
	api.Register(mux, svc, statusStub{}, "test")
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := httpx.WithBaseURL(r.Context(), "https://replays.example.com")
		if admin {
			ctx = httpx.WithUser(ctx, &model.User{ID: 1, Username: "admin"})
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	return svc, h
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

func seed(t *testing.T, svc *service.Service) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.AddPlayer(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	items := []scoresaber.ScoreItem{
		testutil.Item("1001", 1, 501, testutil.T0.Add(2*time.Minute), true),
		testutil.Item("1001", 2, 502, testutil.T0.Add(time.Minute), true),
	}
	if _, err := svc.UpsertScores(ctx, "1001", items); err != nil {
		t.Fatal(err)
	}
	size, sum, _ := svc.Store().Put("1001", 1, strings.NewReader("replay"))
	if err := svc.MarkReplayArchived(ctx, 1, size, sum); err != nil {
		t.Fatal(err)
	}
}

func TestPublicReads(t *testing.T) {
	svc, h := newAPI(t, false)
	seed(t, svc)

	code, _, players := call(t, h, http.MethodGet, "/api/v1/players", nil)
	if code != 200 || len(players) != 1 {
		t.Fatalf("list players = %d %v", code, players)
	}
	p := players[0].(map[string]any)
	if p["name"] != "Alice" || p["replays"].(map[string]any)["archived"].(float64) != 1 || p["url"] != "https://replays.example.com/p/1001" {
		t.Fatalf("player = %v", p)
	}

	code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/1001/scores?state=archived", nil)
	items := page["items"].([]any)
	if code != 200 || page["total"].(float64) != 1 || len(items) != 1 {
		t.Fatalf("scores = %d %v", code, page)
	}
	replay := items[0].(map[string]any)["replay"].(map[string]any)
	if replay["download_url"] != "https://replays.example.com/r/1.dat" || replay["embed_url"] != "https://replays.example.com/embed/1" {
		t.Fatalf("replay = %v", replay)
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
		"/api/v1/players/9":                         404,
		"/api/v1/players/9/scores":                  404,
		"/api/v1/scores/999":                        404,
		"/api/v1/players/1001/scores?per_page=1000": 422,
		"/api/v1/players/1001/scores?state=bogus":   422,
	} {
		if code, _, _ := call(t, h, http.MethodGet, path, nil); code != want {
			t.Errorf("%s = %d, want %d", path, code, want)
		}
	}
}

func TestAdminRequiresSession(t *testing.T) {
	_, h := newAPI(t, false)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/players"},
		{http.MethodPatch, "/api/v1/players/1001"},
		{http.MethodDelete, "/api/v1/players/1001"},
		{http.MethodPost, "/api/v1/players/1001/poll"},
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
	for ref, want := range map[string]int{"1002": 409, "nope": 422, "9999": 404} {
		if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players", map[string]any{"ref": ref}); code != want {
			t.Errorf("add %q = %d, want %d", ref, code, want)
		}
	}
	code, p, _ = call(t, h, http.MethodPatch, "/api/v1/players/1002", map[string]any{"enabled": false})
	if code != 200 || p["enabled"] != false {
		t.Fatalf("patch = %d %v", code, p)
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players/1002/poll", nil); code != 202 {
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
	if code, _, _ := call(t, h, http.MethodDelete, "/api/v1/players/1002?delete_files=true", nil); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, _, _ := call(t, h, http.MethodDelete, "/api/v1/players/1002", nil); code != 404 {
		t.Fatalf("second delete = %d", code)
	}
}

func TestOpenAPIDocument(t *testing.T) {
	_, h := newAPI(t, false)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/openapi.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`"list-players"`, `"add-player"`, `"session"`, `"ssa_session"`} {
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
