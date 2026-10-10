package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
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

// running is an app served on a random port with first-run setup done.
type running struct {
	Base   string
	Client *http.Client // does not follow redirects
	Cookie *http.Cookie // admin session
	stop   func() error
}

// Stop shuts the app down gracefully (also done at test end).
func (r running) Stop() error { return r.stop() }

func startApp(t *testing.T, opts app.Options) running {
	t.Helper()
	// The serve command configures slog from cfg.LogLevel; mirror it so sync
	// events do not pollute test output while genuine errors stay visible.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	cfg := config.Config{DataDir: t.TempDir(), Listen: "127.0.0.1:0", HourlyBudget: 300, LogLevel: "error"}
	a, err := app.New(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, ln) }()
	var once sync.Once
	var stopErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(20 * time.Second):
				stopErr = errors.New("Serve did not stop")
			}
			_ = a.Close()
		})
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })

	r := running{
		Base:   "http://" + ln.Addr().String(),
		Client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		stop:   stop,
	}
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery"}, "confirm": {"correct horse battery"}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.Base+"/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := r.Client.Do(req)
	if err != nil || res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: %v %v", res, err)
	}
	for _, c := range res.Cookies() {
		if c.Name == httpx.SessionCookie {
			r.Cookie = c
		}
	}
	res.Body.Close()
	if r.Cookie == nil {
		t.Fatal("no session cookie")
	}
	return r
}

// do sends an authenticated request with an optional JSON body.
func (r running) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, r.Base+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(r.Cookie)
	res, err := r.Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// waitArchived polls an API score path until its replay is archived (10s max).
func (r running) waitArchived(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, body, err := getBody(r.Client, r.Base+path)
		if err == nil && status == 200 {
			var s struct {
				Replay struct {
					State string `json:"state"`
				} `json:"replay"`
			}
			_ = json.Unmarshal(body, &s)
			if s.Replay.State == "archived" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not archived within 10s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEndToEnd(t *testing.T) {
	ss := fakeScoreSaber(t)
	r := startApp(t, app.Options{ScoreSaberURL: ss.URL})

	code, body := r.do(t, http.MethodPost, "/api/v1/players", `{"ref":"https://scoresaber.com/u/1001"}`)
	if code != http.StatusCreated {
		t.Fatalf("add player: %s", body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" || created.ID == "1001" {
		t.Fatalf("add player must return an opaque player ID: %q %v", created.ID, err)
	}

	r.waitArchived(t, "/api/v1/scores/777")

	status, got, err := getBody(r.Client, r.Base+"/r/777.dat")
	if err != nil || status != http.StatusOK || !bytes.Equal(got, replayBytes) {
		t.Fatalf("download: %d %q %v", status, got, err)
	}
	for _, p := range []string{"/", "/p/" + created.ID, "/s/777", "/healthz", "/api/docs"} {
		status, _, err := getBody(r.Client, r.Base+p)
		if err != nil || status != 200 {
			t.Fatalf("GET %s: %v %v", p, status, err)
		}
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("Serve returned %v", err)
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
