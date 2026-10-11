package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// importService builds the shared import fixture: a service with the fake
// BeatLeader platform registered and its profile "abc" tracked.
func importService(t *testing.T) (*service.Service, string) {
	t.Helper()
	fp := testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader")
	svc, _, _ := testutil.NewServiceWith(t, fp.Platform())
	return svc, testutil.AddPlayer(t, svc, "https://bl.example/u/abc")
}

// upsertBL stores fake plays on the BeatLeader platform (testutil.UpsertFake
// is bound to the testplat name).
func upsertBL(t *testing.T, svc *service.Service, playerID string, plays ...platform.Play) {
	t.Helper()
	if _, err := svc.UpsertPlays(context.Background(), playerID, model.PlatformBeatLeader, plays); err != nil {
		t.Fatal(err)
	}
}

// bsorFixture is the BSOR file of leaderboard LB1, with the game mod's
// uppercase hash (spec §2.1).
func bsorFixture(playerID, hash string, score int64, failTime float64) []byte {
	return testutil.BSORReplay(playerID, hash, "Song LB1", "Cirrina", "Expert", "Standard", score, 1791712056, failTime)
}

func importFile(t *testing.T, svc *service.Service, name string, body []byte) service.ImportResult {
	t.Helper()
	res, err := svc.ImportReplay(context.Background(), name, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ptr(f float64) *float64 { return &f }

// replayFile reads a row's stored replay file.
func replayFile(t *testing.T, svc *service.Service, sc *model.Score) []byte {
	t.Helper()
	path, err := svc.ReplayPath(sc)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// assertStored verifies the row archived the exact uploaded bytes.
func assertStored(t *testing.T, svc *service.Service, sc *model.Score, body []byte) {
	t.Helper()
	sum := sha256.Sum256(body)
	if sc.ReplaySHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("ReplaySHA256 = %s, want %s", sc.ReplaySHA256, hex.EncodeToString(sum[:]))
	}
	if stored := replayFile(t, svc, sc); !bytes.Equal(stored, body) {
		t.Fatalf("stored file is not byte-identical: %d bytes, want %d", len(stored), len(body))
	}
}

func TestImportPBClear(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false))
	row := testutil.Row(t, svc, player, "t1")

	name := "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor"
	body := bsorFixture("abc", "HASH-LB1", 900, 0)
	res := importFile(t, svc, name, body)
	if res.Status != service.ImportStatusImported || res.ScoreID != row.ID || res.Map != "Song LB1 [Expert]" ||
		res.URL != "/s/bl/t1" || res.Reason != "" || res.PlayerID != player {
		t.Fatalf("result = %+v", res)
	}
	got := testutil.Row(t, svc, player, "t1")
	if got.ReplayState != model.ReplayArchived || got.ReplaySize <= 0 {
		t.Fatalf("row not archived: %+v", got)
	}
	assertStored(t, svc, got, body)
}

func TestImportFailByEndTime(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakeAttempt("a1", "LB1", testutil.T0, model.EndFail, ptr(67.91858), 426137, false))
	row := testutil.Row(t, svc, player, "a1")

	name := "76561198038925092-fail-X-Expert-Standard-HASH-LB1-1000.bsor"
	body := bsorFixture("abc", "HASH-LB1", 426137, 67.91858)
	res := importFile(t, svc, name, body)
	if res.Status != service.ImportStatusImported || res.ScoreID != row.ID || res.URL != "/s/bl/attempt/a1" {
		t.Fatalf("result = %+v", res)
	}
	assertStored(t, svc, testutil.Row(t, svc, player, "a1"), body)
}

// TestImportLargeReplayStoredIntact pins byte-identity for a file larger than
// the bufio read chunk: real replays carry kilobytes of frames after info.
func TestImportLargeReplayStoredIntact(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false))

	body := append(bsorFixture("abc", "HASH-LB1", 900, 0), make([]byte, 10000)...)
	res := importFile(t, svc, "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor", body)
	if res.Status != service.ImportStatusImported {
		t.Fatalf("result = %+v", res)
	}
	assertStored(t, svc, testutil.Row(t, svc, player, "t1"), body)
}

func TestImportFailScoreMismatch(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakeAttempt("a1", "LB1", testutil.T0, model.EndFail, ptr(67.91858), 426137, false))

	res := importFile(t, svc, "76561198038925092-fail-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 999999, 67.91858))
	if res.Status != service.ImportStatusSkipped || res.Reason != "no matching play for Song LB1 [Expert, fail, score 999999]" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportFailEndTimeMismatch(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakeAttempt("a1", "LB1", testutil.T0, model.EndFail, ptr(67.91858), 426137, false))

	res := importFile(t, svc, "76561198038925092-fail-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 426137, 12.5))
	if res.Status != service.ImportStatusSkipped || res.Reason != "no matching play for Song LB1 [Expert, fail, score 426137]" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportFilenameEndTypes(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player,
		testutil.FakeAttempt("aq", "LB1", testutil.T0, model.EndQuit, nil, 900, false),
		testutil.FakeAttempt("ap", "LB1", testutil.T0, model.EndPractice, nil, 900, false))
	quit := testutil.Row(t, svc, player, "aq")
	practice := testutil.Row(t, svc, player, "ap")

	res := importFile(t, svc, "76561198038925092-quit-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 900, 0))
	if res.Status != service.ImportStatusImported || res.ScoreID != quit.ID {
		t.Fatalf("quit result = %+v", res)
	}
	res = importFile(t, svc, "76561198038925092-practice-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 900, 0))
	if res.Status != service.ImportStatusImported || res.ScoreID != practice.ID {
		t.Fatalf("practice result = %+v", res)
	}
}

func TestImportUnknownEndTypeSingleCandidate(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindAttempt, "a1", "LB1", testutil.T0, false))
	row := testutil.Row(t, svc, player, "a1")

	res := importFile(t, svc, "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 900, 0))
	if res.Status != service.ImportStatusImported || res.ScoreID != row.ID || res.URL != "/s/bl/attempt/a1" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportAmbiguous(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player,
		testutil.FakeAttempt("a1", "LB1", testutil.T0, model.EndFail, ptr(67.91858), 426137, false),
		testutil.FakeAttempt("a2", "LB1", testutil.T0, model.EndFail, ptr(67.91858), 426137, false))

	res := importFile(t, svc, "76561198038925092-fail-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 426137, 67.91858))
	if res.Status != service.ImportStatusSkipped || res.Reason != "ambiguous: 2 matching plays" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportRenamedFile(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false))
	row := testutil.Row(t, svc, player, "t1")

	res := importFile(t, svc, "vacation photo.bsor", bsorFixture("abc", "HASH-LB1", 900, 0))
	if res.Status != service.ImportStatusImported || res.ScoreID != row.ID {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportTwice(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false))

	body := bsorFixture("abc", "HASH-LB1", 900, 0)
	name := "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor"
	if res := importFile(t, svc, name, body); res.Status != service.ImportStatusImported {
		t.Fatalf("first import = %+v", res)
	}
	archived := testutil.Row(t, svc, player, "t1")
	res := importFile(t, svc, name, body)
	if res.Status != service.ImportStatusAlready || res.Reason != "already archived" {
		t.Fatalf("second import = %+v", res)
	}
	if again := testutil.Row(t, svc, player, "t1"); again.ReplaySHA256 != archived.ReplaySHA256 || again.ReplaySize != archived.ReplaySize {
		t.Fatalf("row changed by re-import: %+v", again)
	}
}

func TestImportPendingSkipped(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, true))

	res := importFile(t, svc, "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 900, 0))
	if res.Status != service.ImportStatusSkipped || res.Reason != "download in progress" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportAlreadyArchived(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false))
	testutil.Archive(t, svc, testutil.Row(t, svc, player, "t1"), "x")

	// The row is already archived, the same state as TestImportTwice's second
	// run, so the outcome is the same `already` status.
	res := importFile(t, svc, "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 900, 0))
	if res.Status != service.ImportStatusAlready || res.Reason != "already archived" {
		t.Fatalf("result = %+v", res)
	}
}

// TestImportAlreadyArchivedWithDecoy re-imports a file whose own play is
// archived while another replay-less attempt of the same map exists; the
// outcome must be `already`, not "no matching play".
func TestImportAlreadyArchivedWithDecoy(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player,
		testutil.FakeAttempt("a1", "LB1", testutil.T0, model.EndFail, ptr(67.91858), 426137, false),
		testutil.FakeAttempt("a2", "LB1", testutil.T0, model.EndFail, ptr(30.5), 123456, false))

	name := "76561198038925092-fail-X-Expert-Standard-HASH-LB1-1000.bsor"
	body := bsorFixture("abc", "HASH-LB1", 426137, 67.91858)
	if res := importFile(t, svc, name, body); res.Status != service.ImportStatusImported {
		t.Fatalf("first import = %+v", res)
	}
	// a1 is archived now; a2 is a replay-less decoy the file must not match.
	res := importFile(t, svc, name, body)
	if res.Status != service.ImportStatusAlready || res.Reason != "already archived" {
		t.Fatalf("re-import = %+v", res)
	}
}

func TestImportUntrackedPlayer(t *testing.T) {
	svc, _ := importService(t)

	res := importFile(t, svc, "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("zzz", "HASH-LB1", 900, 0))
	if res.Status != service.ImportStatusSkipped || res.Reason != "no tracked player for zzz" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportUnknownMap(t *testing.T) {
	svc, _ := importService(t)

	res := importFile(t, svc, "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "BeThereForYou", 900, 0))
	if res.Status != service.ImportStatusSkipped || res.Reason != "map not in the archive" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportSupersededExactScore(t *testing.T) {
	svc, player := importService(t)
	old := testutil.FakePlay(model.KindScore, "t-old", "LB1", testutil.T0, false)
	old.ModifiedScore, old.UnmodifiedScore, old.PersonalBest = 100, 100, false
	upsertBL(t, svc, player, old, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false))
	oldRow := testutil.Row(t, svc, player, "t-old")

	res := importFile(t, svc, "76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor", bsorFixture("abc", "HASH-LB1", 100, 0))
	if res.Status != service.ImportStatusImported || res.ScoreID != oldRow.ID {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportNotBSOR(t *testing.T) {
	svc, _ := importService(t)

	res, err := svc.ImportReplay(context.Background(), "x.bsor", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != service.ImportStatusSkipped || res.Reason != "not a BSOR replay" {
		t.Fatalf("result = %+v", res)
	}
}

func TestGuardedMarkRejectsArchived(t *testing.T) {
	svc, player := importService(t)
	upsertBL(t, svc, player, testutil.FakePlay(model.KindScore, "t1", "LB1", testutil.T0, false))
	sc := testutil.Row(t, svc, player, "t1")
	ctx := context.Background()

	ok, err := service.MarkReplayImported(svc, ctx, sc, 1234, "cafe")
	if err != nil || !ok {
		t.Fatalf("first mark = (%v, %v)", ok, err)
	}
	archived := testutil.Row(t, svc, player, "t1")
	if archived.ReplayState != model.ReplayArchived || archived.ReplaySize != 1234 || archived.ReplaySHA256 != "cafe" {
		t.Fatalf("row not archived: %+v", archived)
	}

	ok, err = service.MarkReplayImported(svc, ctx, archived, 999, "ffff")
	if err != nil || ok {
		t.Fatalf("second mark = (%v, %v)", ok, err)
	}
	if again := testutil.Row(t, svc, player, "t1"); again.ReplayState != model.ReplayArchived ||
		again.ReplaySize != 1234 || again.ReplaySHA256 != "cafe" {
		t.Fatalf("archived row touched: %+v", again)
	}
}
