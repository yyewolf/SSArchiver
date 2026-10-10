package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/argon2id"

	"github.com/yyewolf/ssarchiver/internal/app"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
)

var replayBytes = []byte("ScoreSaber Replay e2e payload")

func fakeScoreSaber(t *testing.T) *httptest.Server {
	t.Helper()
	set := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/players/1001/basic", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"1001","name":"Alice","country":"FR","avatar":"https://cdn.scoresaber.com/avatars/1001.jpg"}`))
	})
	mux.HandleFunc("GET /api/v2/players/1001/scores", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"score":{"id":777,"rank":1,"modifiedScore":1000,"accuracy":0.95,"mods":[],"hasReplay":true,"personalBest":true,"createdAt":%q,"player":{"id":"1001","name":"Alice"},"device":{"hmd":"Quest 3"}},
			"leaderboard":{"id":55,"map":{"hash":"ABC","songName":"E2E Song","songAuthorName":"A","levelAuthorName":"M","coverUrl":""},"difficulty":{"difficulty":9,"gameMode":"SoloStandard","rawDifficulty":"_ExpertPlus_SoloStandard"},"maxScore":1100,"realm":{"leaderboardStatus":"UNRANKED","stars":0}}}],
			"metadata":{"page":1,"itemsPerPage":100,"totalItems":1,"totalPages":1}}`, set)
	})
	mux.HandleFunc("GET /api/v2/scores/777/replay", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(replayBytes)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// getBody issues a context-aware GET and returns status + body, always closing it.
func getBody(client *http.Client, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return res.StatusCode, b, err
}

func TestEndToEnd(t *testing.T) {
	// The serve command configures slog from cfg.LogLevel; mirror it so sync
	// events do not pollute test output while genuine errors stay visible.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	ss := fakeScoreSaber(t)
	cfg := config.Config{DataDir: t.TempDir(), Listen: "127.0.0.1:0", HourlyBudget: 300, LogLevel: "error"}
	a, err := app.New(cfg, app.Options{ScoreSaberURL: ss.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 1. first-run setup
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery"}, "confirm": {"correct horse battery"}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil || res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: %v %v", res, err)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == httpx.SessionCookie {
			cookie = c
		}
	}
	res.Body.Close()
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	// 2. add a player through the API
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/api/v1/players", bytes.NewBufferString(`{"ref":"https://scoresaber.com/u/1001"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	res, err = client.Do(req)
	if err != nil {
		t.Fatalf("add player: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add player: %s", body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" || created.ID == "1001" {
		t.Fatalf("add player must return an opaque player ID: %q %v", created.ID, err)
	}

	// 3. the worker polls, lists and archives the replay
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, body, err := getBody(client, base+"/api/v1/scores/777")
		if err == nil && status == 200 {
			var s struct {
				Replay struct {
					State string `json:"state"`
				} `json:"replay"`
			}
			_ = json.Unmarshal(body, &s)
			if s.Replay.State == "archived" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("replay was not archived within 10s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 4. download it and load the public pages
	status, got, err := getBody(client, base+"/r/777.dat")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("download status %d", status)
	}
	if !bytes.Equal(got, replayBytes) {
		t.Fatalf("downloaded %q", got)
	}
	for _, p := range []string{"/", "/p/" + created.ID, "/s/777", "/healthz", "/api/docs"} {
		status, _, err := getBody(client, base+p)
		if err != nil || status != 200 {
			t.Fatalf("GET %s: %v %v", p, status, err)
		}
	}

	// 5. graceful shutdown
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestBeatLeaderRegistered(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir(), Listen: "127.0.0.1:0", HourlyBudget: 300, LogLevel: "error"}
	a, err := app.New(cfg, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if p, ok := a.Service.Platforms().Get("beatleader"); !ok || p.Slug != "bl" {
		t.Fatalf("BeatLeader not registered: %+v", p)
	}
	var names []string
	for _, l := range a.Worker.Status().Limiters {
		names = append(names, l.Name)
	}
	if !slices.Equal(names, []string{"scoresaber", "beatleader/api", "beatleader/cdn"}) {
		t.Fatalf("limiters = %v", names)
	}
}
