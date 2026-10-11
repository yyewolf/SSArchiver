package web_test

import (
	"bytes"
	"context"
	"io"
	mp "mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
	"github.com/yyewolf/ssarchiver/internal/web"
)

// upsertBL stores fake plays on the BeatLeader platform (testutil.UpsertFake
// is bound to the testplat name).
func upsertBL(t *testing.T, svc *service.Service, playerID string, plays ...platform.Play) {
	t.Helper()
	if _, err := svc.UpsertPlays(context.Background(), playerID, model.PlatformBeatLeader, plays); err != nil {
		t.Fatal(err)
	}
}

// importFixture is a valid BSOR file of leaderboard LB1 with the game mod's
// uppercase hash (Task 2's fixture values): it matches the replay-less t1 row.
func importFixture(playerID string) []byte {
	return testutil.BSORReplay(playerID, "HASH-LB1", "Song LB1", "Cirrina", "Expert", "Standard", 900, 1791712056, 0)
}

const importFileName = "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor"

// seedBL tracks the BeatLeader account abc with a replay-less score t1 on LB1
// plus one archived row (t2); it returns the player's ID.
func (e *testEnv) seedBL() string {
	e.t.Helper()
	player := testutil.AddPlayer(e.t, e.svc, "https://bl.example/u/abc")
	upsertBL(e.t, e.svc, player,
		testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false),
		testutil.FakePlay(model.KindScore, "t2", "LB1", testutil.T0.Add(time.Minute), false))
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, player, "t2"), "archived replay bytes")
	return player
}

// multipart builds a multipart/form-data body with one file part per entry
// ([name, content]); it returns the body and its Content-Type.
func multipart(t *testing.T, files ...[2]string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := mp.NewWriter(&buf)
	for _, f := range files {
		fw, err := w.CreateFormFile("files", f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

// postMultipart POSTs a raw multipart body to the handler.
func (e *testEnv) postMultipart(path string, body io.Reader, contentType string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, body)
	req.Header.Set("Content-Type", contentType)
	for _, o := range append([]reqOpt{htmx("")}, opts...) {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestImportReplays(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"))
	player := e.seedBL()
	c := e.login()

	body, ct := multipart(t, [2]string{importFileName, string(importFixture("abc"))})
	rec := e.postMultipart("/admin/replays/import", body, ct, withCookie(c))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	contains(t, rec.Body.String(),
		`id="import-results"`, "hx-swap-oob", importFileName, "imported", "/s/bl/t1")
	if row := testutil.Row(t, e.svc, player, "t1"); row.ReplayState != model.ReplayArchived {
		t.Fatalf("row not archived: %+v", row)
	}
	contains(t, rec.Body.String(), "2 / 2") // the refreshed archived count
}

func TestImportMixedBatch(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"))
	player := e.seedBL()
	c := e.login()

	body, ct := multipart(t,
		[2]string{"garbage.bsor", "hello"},
		[2]string{"zzz-X-Expert-Standard-HASH-LB1-1000.bsor", string(importFixture("zzz"))},
		[2]string{importFileName, string(importFixture("abc"))}, // the good file last
	)
	rec := e.postMultipart("/admin/replays/import", body, ct, withCookie(c))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	contains(t, rec.Body.String(),
		"✓", "imported", "/s/bl/t1",
		"✗", "not a BSOR replay", "no tracked player for zzz",
		`data-templ-type="warning"`, "Imported 1 of 3 replays")
	if row := testutil.Row(t, e.svc, player, "t1"); row.ReplayState != model.ReplayArchived {
		t.Fatalf("good row not archived: %+v", row)
	}
}

func TestImportNoFiles(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"))
	e.seedBL()
	c := e.login()

	var buf bytes.Buffer
	w := mp.NewWriter(&buf)
	if err := w.WriteField("ref", "not-a-file"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rec := e.postMultipart("/admin/replays/import", &buf, w.FormDataContentType(), withCookie(c))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Reswap") != "none" {
		t.Fatal("a no-files upload must not swap")
	}
	contains(t, rec.Body.String(), "No files uploaded")
}

func TestImportFileCap(t *testing.T) {
	f := importFixture("abc")
	e := newEnvDeps(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"), func(d *web.Deps) {
		d.ImportMaxFileBytes = int64(len(f) - 1) // the header still parses, storage crosses the cap
	})
	player := e.seedBL()
	c := e.login()

	body, ct := multipart(t, [2]string{importFileName, string(f)})
	rec := e.postMultipart("/admin/replays/import", body, ct, withCookie(c))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	contains(t, rec.Body.String(), "✗", importFileName, "file too large")
	if row := testutil.Row(t, e.svc, player, "t1"); row.ReplayState == model.ReplayArchived {
		t.Fatalf("row archived despite the cap: %+v", row)
	}
}

func TestImportRequestCap(t *testing.T) {
	e := newEnvDeps(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"), func(d *web.Deps) {
		d.ImportMaxTotalBytes = 1
	})
	player := e.seedBL()
	c := e.login()

	body, ct := multipart(t,
		[2]string{importFileName, string(importFixture("abc"))},
		[2]string{"zzz-X-Expert-Standard-HASH-LB1-1000.bsor", string(importFixture("zzz"))},
	)
	rec := e.postMultipart("/admin/replays/import", body, ct, withCookie(c))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	contains(t, rec.Body.String(), `id="import-results"`, "imported", "/s/bl/t1", "request size limit reached")
	if b := rec.Body.String(); strings.Contains(b, "no tracked player for zzz") {
		t.Fatal("the capped part must be skipped without calling the service")
	}
	if row := testutil.Row(t, e.svc, player, "t1"); row.ReplayState != model.ReplayArchived {
		t.Fatalf("first row not archived: %+v", row)
	}
}

func TestImportRequiresAdmin(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"))
	e.seedBL()
	e.setup()

	body, ct := multipart(t, [2]string{importFileName, string(importFixture("abc"))})
	rec := e.postMultipart("/admin/replays/import", body, ct)
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("HX-Redirect"), "/login") {
		t.Fatalf("code=%d redirect=%q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
}
