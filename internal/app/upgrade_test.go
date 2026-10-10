package app_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/app"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestUpgradeFromV1DataDir(t *testing.T) {
	// The serve command configures slog from cfg.LogLevel; mirror it so the
	// missing-viewer-bundle warning does not pollute test output.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, Listen: "127.0.0.1:0", HourlyBudget: 300, LogLevel: "error"}
	testutil.LoadSQLFile(t, cfg.DBPath(), "../db/testdata/v1.sql")
	replay := filepath.Join(cfg.ReplayDir(), "76561198038925092", "5001.dat")
	if err := os.MkdirAll(filepath.Dir(replay), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replay, []byte("v1 replay bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := app.New(cfg, app.Options{ScoreSaberURL: "http://127.0.0.1:1"}) // never reached in this test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		return rec
	}
	if rec := get("/p/76561198038925092"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Yewolf") {
		t.Fatalf("legacy player page = %d", rec.Code)
	}
	if rec := get("/s/5001"); rec.Code != http.StatusOK {
		t.Fatalf("legacy score page = %d", rec.Code)
	}
	if rec := get("/r/5001.dat"); rec.Code != http.StatusOK || rec.Body.String() != "v1 replay bytes" || rec.Header().Get("ETag") != `"abc"` {
		t.Fatalf("legacy replay = %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	if rec := get("/p/ss/76561198038925092"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/p/76561198038925092" {
		t.Fatalf("account link = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec := get("/api/v1/players/76561198038925092")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"identities":[{"platform":"scoresaber","id":"76561198038925092"`) {
		t.Fatalf("player API = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, db.BackupName)); err != nil {
		t.Fatalf("pre-upgrade backup missing: %v", err)
	}
}
