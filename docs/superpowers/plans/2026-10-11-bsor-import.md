# Local .bsor Import (gap-fill) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Admins upload local BeatLeader `.bsor` replays on Manage; each file is parsed, routed to its owning tracked player, matched to the replay-less row it belongs to, and archived — with a visible outcome for every file.

**Architecture:** A new `internal/bsor` package parses the BSOR v1 header (magic `0x442D3D69`, sections `[byte type][payload]`, info = 13 length-prefixed strings + int32 + 3 strings + float/bool/4 floats). A new service method `ImportReplay` routes by the header's `playerId` (the BeatLeader account ID), finds candidate score rows without an archived replay, narrows by end type and exact score, stores the file and applies a guarded archive update. A web handler streams the multipart request (never `ParseMultipartForm`) and an "Import replays" card on Manage shows a per-file results panel.

**Tech Stack:** Go 1.27, gorm gen (`internal/db/query`), templ + htmx (existing component set), no new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-11-bsor-import-design.md` (read it first; §2 pins the file format and matching facts, §5 the matching algorithm and reasons, §6 the web behavior).

## Global Constraints

- Module `github.com/yyewolf/ssarchiver`; Go 1.27; **no new dependencies**.
- DB access only via gorm gen (`s.q.*`) — no raw SQL. GORM calls live only in `internal/service`.
- Match the repo's commenting style: doc comments on exported identifiers referencing spec sections (e.g. `// ImportReplay fills a tracked play's missing replay from a local BSOR file (spec §5).`), none inside small bodies.
- End-type values come from the `model.End*` constants (`model.EndFail`, `model.EndQuit`, `model.EndRestart`, `model.EndPractice`, `model.EndClear`); platform name from `model.PlatformBeatLeader`.
- Replay states: `model.ReplayNone/ReplayPending/ReplayArchived/ReplayGone/ReplayFailed`.
- No emoji in code, tests, or UI copy. Results-panel markers are the plain characters `✓`, `=`, `✗` (they appear in the spec's UI contract).
- Run tests with `go test -race ./...` from the repo root; lint with `make lint` at the end of the last task.

## Review Focus

Input classes the spec implies that the task tests below pin down:

1. A **renamed file** (no mod filename pattern) must still import — everything comes from the header. Pinned by Task 1's `TestParseHeaderDecodesAllFields` fixture imported under a garbage name in Task 2's `TestImportRenamedFile`.
2. A **non-BSOR file** (text, another platform's `.dat`) must skip as "not a BSOR replay", never panic or store bytes. Pinned by Task 2's `TestImportNotBSOR`.
3. **Importing the same file twice** must not duplicate or overwrite: the second run reports "already archived". Pinned by Task 2's `TestImportTwice`.
4. **The download worker archiving the row between match and mark** must keep the archive immutable and the uploaded file removed. Pinned by Task 2's `TestGuardedMarkRejectsArchived`.
5. **One bad file in a batch** must not stop later files. Pinned by Task 3's `TestImportMixedBatch`.

---

### Task 1: BSOR v1 header parser (`internal/bsor`)

**Files:**
- Create: `internal/bsor/bsor.go`
- Test: `internal/bsor/bsor_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces (used by Tasks 2–3):

```go
package bsor

type Header struct {
    PlayerID, PlayerName        string
    Timestamp                   int64  // unix seconds; the run's START (spec §2.2)
    Hash, SongName, Mapper      string
    Difficulty, Mode, Modifiers string
    Score                       int64
    StartTime, FailTime, Speed  float64
}

// ParseHeader decodes the info section of a BSOR v1 replay (spec §2.1).
// It reads only up to the end of info; frames/notes are never parsed.
func ParseHeader(r io.Reader) (Header, error)

// FilenameEndType extracts the mod's end-type token: the optional
// fail|quit|restart|practice that follows the leading player ID
// (spec §2.1). ok is false when the name has no recognizable token.
func FilenameEndType(name string) (endType string, ok bool)
```

- [ ] **Step 1: Write the failing tests in `internal/bsor/bsor_test.go`**

Build fixtures with a local helper that emits exactly the §2.1 layout (little-endian; strings as `[int32 length][utf8 bytes]`):

```go
func fixture(t *testing.T, ts int64, playerID, hash, songName, mapper, difficulty, mode string, score int64, failTime float64, afterInfo ...[]byte) []byte
// magic 0x442d3d69 (LE int32), byte 1 (version), byte 0 (info section type),
// then: version-"0.10.0", gameVersion-"1.42.3_15380", timestamp-strconv(ts),
// playerID, playerName-"Yewolf", platform-"steam", trackingSystem-"OpenXR",
// hmd-`"Valve""Deckard MP"`, controller-"Index Controller OpenXR",
// hash, songName, mapper, difficulty, int32 score, mode,
// environment-"DefaultEnvironment", modifiers-"", float32 20.5 (jumpDistance),
// byte 0 (leftHanded), float32 0 (height), float32 0 (startTime),
// float32 failTime, float32 0 (speed), then each of afterInfo.
```

Tests (table where natural):

- `TestParseHeaderDecodesAllFields`: parse `fixture(..., ts: 1791712056, playerID: "76561198038925092", hash: "57511EE48555E00E031BD3B1DF90BA7BE5712B56", songName: "Night Raid with a Dragon", mapper: "nolan121405", difficulty: "ExpertPlus", mode: "Standard", score: 426137, failTime: 67.91858)` followed by a frames stub (`[]byte{1}` + int32(1) + 36 zero bytes — parser must stop after info and not need them). Assert every Header field equals the input (Hash keeps its uppercase).
- `TestParseHeaderRejectsGarbage`: wrong magic (`0x11223344`), wrong version byte (`2`), info section type ≠ 0 as the first section, truncated in the middle of a string, and a string length > 1024 → each returns an error.
- `TestFilenameEndType`: `"76561198038925092-fail-X-ExpertPlus-Standard-H1-1791712056.bsor"` → `("fail", true)`; same with `quit`, `restart`, `practice`; `"76561198038925092-X-ExpertPlus-Standard-H1-1.bsor"` → `("", false)`; `"Yewolf-fail-x.bsor"` (no leading digits) → `("", false)`; `"76561198038925092-quit-fail-x.bsor"` → `("quit", true)`.
- `TestRealSamples` (env-gated): skipped unless `os.Getenv("SSA_BSOR_SAMPLES")` names a directory; for each `*.bsor` in it: `ParseHeader` succeeds, `PlayerID`/`Hash` non-empty, `Timestamp > 1_500_000_000`; when the base filename ends in `-{digits}.bsor`, that number equals `Timestamp`; when the base name contains `-fail-`, `FailTime > 0`. (Verified against 11 real samples in `.worktrees/samples` — do not commit them.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/bsor/ -v`
Expected: FAIL (package does not exist yet — build error).

- [ ] **Step 3: Implement `internal/bsor/bsor.go`**

Read with a `bufio.Reader`: int32 LE via 4 bytes + `encoding/binary`; floats via `math.Float32frombits`. Enforce: magic equals `0x442d3d69`, version byte `1`, first section type `0`, every string `0 ≤ len ≤ 1024`, UTF-8 decodable, `Timestamp` parses via `strconv.ParseInt(s, 10, 64)`. Decode fields in the §2.1 order, discarding platform/trackingSystem/hmd/controller/environment/jumpDistance/leftHanded/height. `FilenameEndType` uses one anchored regexp `^[0-9]{1,32}-(fail|quit|restart|practice)-`. No lookahead past info: return immediately after `speed`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/bsor/ -v`
Expected: PASS (including `TestRealSamples` when `SSA_BSOR_SAMPLES=.worktrees/samples` is set — run that once manually and confirm 11 files parse).

- [ ] **Step 5: Commit**

```bash
git add internal/bsor/
git commit -m "feat: parse BSOR v1 replay headers"
```

### Task 2: Service import — `ImportReplay` (gap-fill matching)

**Files:**
- Create: `internal/service/import.go`
- Create: `internal/testutil/bsor.go` (fixture builder shared by Tasks 2–3)
- Modify: `internal/testutil/fakeplatform.go` (FakePlay gains UnmodifiedScore; add FakeAttempt)
- Test: `internal/service/import_test.go`

**Interfaces:**
- Consumes: Task 1's `bsor.ParseHeader`, `bsor.FilenameEndType`; existing `s.replayLoc(sc)`, `s.store.Put/Remove`, `s.reg.PlayPath` (signature in `internal/platform/links.go:17`), `service.PutReplay(sc, r) (size int64, sha string, err error)`.
- Produces (used by Task 3):

```go
// internal/service/import.go
const (
    ImportStatusImported = "imported"
    ImportStatusAlready  = "already"
    ImportStatusSkipped  = "skipped"
)

type ImportResult struct {
    File     string
    Status   string // ImportStatus*
    Reason   string // empty unless skipped
    PlayerID string // owning player's ID when the file routed to one
    ScoreID  int64  // the row the file landed on
    Map      string // "SongName [Difficulty]" for imported files
    URL      string // public page path for imported files
}

func (s *Service) ImportReplay(ctx context.Context, name string, r io.Reader) (ImportResult, error)
```

The `error` return is only for fatal conditions (cancelled context); every per-file failure is a `skipped` result (spec §5).

```go
// internal/testutil/bsor.go
// BSORReplay builds a minimal BSOR v1 file (info + a one-frame stub) with the
// given info values; it mirrors the fixture layout of internal/bsor's tests.
func BSORReplay(playerID, hash, songName, mapper, difficulty, mode string, score int64, timestamp int64, failTime float64) []byte

// internal/testutil/fakeplatform.go — extend FakePlay with UnmodifiedScore: 900, and add:
// FakeAttempt builds a testplat attempt play with an explicit end type,
// end time and score (FakePlay always reports clear/nil/900).
func FakeAttempt(externalID, lbExternalID string, setAt time.Time, endType string, endTime *float64, score int64, replay bool) platform.Play
```

- [ ] **Step 1: Write the failing tests in `internal/service/import_test.go`**

Setup shared by the tests (package `service_test`, mirroring `scores_test.go`): register a fake platform `testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader")` via `testutil.NewServiceWith`, track a player from `https://bl.example/u/abc` (`testutil.AddPlayer`), and upsert rows with `testutil.UpsertFake`. `FakePlay`/`FakeAttempt` rows on leaderboard `"LB1"` carry hash `HASH-LB1`, `DifficultyRaw "Expert"`, `GameMode "Standard"`, score 900 (both modified and unmodified). Call `svc.ImportReplay(ctx, name, bytes.NewReader(testutil.BSORReplay(...)))`.

Tests:

- `TestImportPBClear`: a score row without replay (`FakePlay(model.KindScore, "t1", "LB1", T0, false)`) + fixture (hash `HASH-LB1`, difficulty `Expert`, mode `Standard`, score 900, failTime 0, name `"76561198038925092-X-Expert-Standard-HASH-LB1-1000.bsor"`). Assert: status `imported`, `ScoreID` = the row's ID, `Map` = `"Song LB1 [Expert]"`, `URL` = `/s/bl/t1`, `Reason` empty; then `testutil.Row(...)` shows `ReplayState == model.ReplayArchived` and `ReplaySize > 0`; the file exists at `svc.ReplayPath(row)`.
- `TestImportFailByEndTime`: an attempt row `FakeAttempt("a1", "LB1", T0, model.EndFail, ptr(67.91858), 426137, false)`; fixture with score 426137, failTime 67.91858. Assert `imported`, `URL == "/s/bl/attempt/a1"`.
- `TestImportFailScoreMismatch`: same attempt row, fixture score 999999 → skipped, reason `no matching play for Song LB1 [Expert, fail, score 999999]`.
- `TestImportFailEndTimeMismatch`: same row, fixture failTime 12.5 → skipped with the same "no matching play" shape (end_time window is ±1 s).
- `TestImportFilenameEndTypes`: attempt rows `end_type = quit` and `end_type = practice` (both without replay, score 900); fixtures named `...-quit-...` and `...-practice-...` → both `imported` onto their rows.
- `TestImportUnknownEndTypeSingleCandidate`: one clear attempt row without replay; fixture without an end-type token and failTime 0 → `imported`.
- `TestImportAmbiguous`: two fail attempt rows, same leaderboard, both score 426137, both `endTime = 67.91858`, neither archived; fixture with `failTime 67.91858` → skipped, reason `ambiguous: 2 matching plays`.
- `TestImportRenamedFile`: the Task-1 PB-clear scenario but the file is named `"vacation photo.bsor"` → still `imported`.
- `TestImportTwice`: import the PB-clear fixture twice; second run → status `already` with reason `already archived`, and the row's `ReplaySHA256` is unchanged.
- `TestImportPendingSkipped`: score row with `HasReplay: true` (state `pending`), no candidates otherwise → skipped `download in progress`.
- `TestImportAlreadyArchived`: score row archived via `testutil.Archive` → skipped `already archived`.
- `TestImportUntrackedPlayer`: fixture `playerID: "zzz"` → skipped `no tracked player for zzz`.
- `TestImportUnknownMap`: fixture hash `BeThereForYou` (also covers real placeholder hashes) → skipped `map not in the archive`.
- `TestImportSupersededExactScore`: two score rows on `LB1`: `"t-old"` score 100 (via a direct `platform.Play` with `UnmodifiedScore/ModifiedScore = 100`, `PersonalBest false`), `"t1"` score 900; fixture score 100 → `imported` with `ScoreID` = the t-old row.
- `TestImportNotBSOR`: `svc.ImportReplay(ctx, "x.bsor", strings.NewReader("hello"))` → skipped `not a BSOR replay`.
- `TestGuardedMarkRejectsArchived`: call the guarded update directly (export it for the test via `internal/service/export_test.go` as `MarkReplayImported(ctx, sc, size, sha) (bool, error)`): on a `none` row → `(true, nil)` and the row is archived; run it again on the now-archived row → `(false, nil)` and the row is untouched. (This method is the `RowsAffected = 0` race path of step 6; Task 2's `ImportReplay` calls it right after `PutReplay`.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/service/ -run TestImport -v`
Expected: FAIL (ImportReplay undefined).

- [ ] **Step 3: Implement `internal/service/import.go`**

Per file (spec §5 is normative; reasons verbatim):

1. `h, err := bsor.ParseHeader(r)` → parse error: skipped `not a BSOR replay`.
2. Account: `s.q.PlayerPlatform.WithContext(ctx).Where(pp.Platform.Eq(model.PlatformBeatLeader), pp.ExternalID.Eq(h.PlayerID)).First()` — `gorm.ErrRecordNotFound` → skipped `no tracked player for {id}`.
3. Leaderboards: `lb.SongHash.Eq(strings.ToLower(h.Hash))`, `lb.GameMode.Eq(h.Mode)`, `lb.DifficultyRaw.Eq(h.Difficulty)`, `lb.Platform.Eq(model.PlatformBeatLeader)`; empty → skipped `map not in the archive`.
4. Load ALL of the player's rows on those leaderboard IDs with `Preload(sc.Leaderboard)`; partition: candidates (`ReplayState.In(none, gone, failed)`), archived, pending. No candidates: any archived → `already archived`; else any pending → `download in progress`; else (map rows exist elsewhere/no rows) → fall through to the no-match message.
5. End type: `FilenameEndType(name)`; if none and `h.FailTime > 0` → `model.EndFail`. Filter candidates: `fail` → attempts with `EndType == fail` and `EndTime != nil` and `|*EndTime − h.FailTime| ≤ 1.0`; `quit/restart/practice` → attempts with that `EndType`; none → score rows and attempts of any end type. Every branch additionally requires the exact score: `sc.UnmodifiedScore == h.Score` (no fallback; degenerate 0 simply never matches). One candidate → step 6; none → skipped `no matching play for {SongName} [{Difficulty}, {endType-or-"unknown"}, score {Score}]`; several → skipped `ambiguous: {n} matching plays`.
6. `size, sum, err := s.PutReplay(row, r)` — error (including the web layer's size cap) → skipped with `err.Error()`. Then the guarded update (exported as `MarkReplayImported` for the race test): `Updates(map[string]any{"replay_state": model.ReplayArchived, "replay_size": size, "replay_sha256": sum, "archived_at": s.Now(), "next_attempt_at": nil, "last_error": ""})` with `Where(q.ID.Eq(row.ID), q.ReplayState.In(model.ReplayNone, model.ReplayGone, model.ReplayFailed))`. `RowsAffected == 0` → `s.store.Remove(loc)` (loc via `s.replayLoc(row)`) and result `already`. Success → `imported` with `Map = lb.SongName + " [" + lb.DifficultyRaw + "]"` and `URL = s.reg.PlayPath("/s", platform.PlayRef{Platform: model.PlatformBeatLeader, Kind: row.Kind, ExternalID: row.ExternalID})`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/service/ -run 'TestImport|TestGuardedMark' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/ internal/testutil/
git commit -m "feat: import local BeatLeader replays into missing replay rows"
```

### Task 3: Web — import card, streamed handler, results panel

**Files:**
- Create: `internal/web/import.go` (handler)
- Modify: `internal/web/web.go` (one route, after the other admin POSTs), `internal/web/views/admin.templ` (+ regenerate `admin_templ.go`), `internal/web/views/urls.go` if it centralizes admin URLs (check; keep the route path in one place)
- Test: `internal/web/import_test.go`

**Interfaces:**
- Consumes: Task 2's `service.ImportReplay`, `service.ImportResult`, `service.ImportStatus*`; the existing `render`, `h.toastOnly`, `h.adminPlayersView(r)`, `views.AdminPlayersTable`, `views.ToastOOB` (see `internal/web/admin.go:34-51`, `internal/web/web.go:184-220`).
- Produces: route `POST /admin/replays/import`; view components `views.ImportCard()`, `views.ImportDone(v views.AdminPlayersView, rs []service.ImportResult) string` (templ components).

- [ ] **Step 1: Write the failing tests in `internal/web/import_test.go`**

Env: `newEnvWith(t, testutil.NewFakePlatformAs("beatleader", "bl", "BeatLeader"))`; seed one player with a replay-less score row on `LB1` (as in Task 2's setup) plus one archived row; build multipart bodies with `mime/multipart.Writer` (field name `files`, filename + fixture bytes from `testutil.BSORReplay`); POST with `e.login()` cookie and `htmx("")`.

For the cap tests, add to `env_test.go` a `newEnvDeps(t *testing.T, fp *testutil.FakePlatform, tune func(*web.Deps)) *testEnv` that delegates to `newEnvCore` after applying `tune` (refactor `newEnvCore` to call it with a nil tune — behavior unchanged).

Multipart helper in the test file:

```go
func multipart(t *testing.T, files ...[2]string) (io.Reader, string) // [name, content]; returns body + content-type
```

Tests:

- `TestImportReplays`: POST one fixture matching the replay-less row → 200; body contains `id="import-results"` with `hx-swap-oob`, the file name, `imported`, and `/s/bl/t1`; `testutil.Row` shows `ReplayState == model.ReplayArchived`; the refreshed table shows the updated archived count.
- `TestImportMixedBatch`: three files — one good, one `hello` text (skipped `not a BSOR replay`), one untracked-player fixture (skipped `no tracked player for zzz`) — in one request, good file LAST: all three outcomes render, the good row is archived.
- `TestImportNoFiles`: POST a multipart body with no `files` part → error toast (`No files uploaded`).
- `TestImportFileCap`: `f := testutil.BSORReplay(...)` (a full valid fixture); env via `newEnvDeps` with `ImportMaxFileBytes: int64(len(f) - 1)` — the header still parses (ParseHeader stops after info) but `PutReplay` crosses the cap → skipped `file too large`, and the row is NOT archived.
- `TestImportRequestCap`: `newEnvDeps` with `ImportMaxTotalBytes: 1`; two parts — the first processes normally (the total is only known after streaming it), the second is skipped `request size limit reached` without calling the service; the request completes 200 with the results panel.
- `TestImportRequiresAdmin`: no session cookie → not 200 / redirect (mirror how `admin_test.go` asserts unauthenticated admin POSTs).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/web/ -run TestImport -v`
Expected: FAIL (route/handler undefined).

- [ ] **Step 3: Implement the handler and views**

`internal/web/web.go`: `mux.HandleFunc("POST /admin/replays/import", h.requireAdmin(h.importReplays))`.

`web.Deps` gains `ImportMaxFileBytes, ImportMaxTotalBytes int64` (defaults 256 MiB / 4 GiB when 0, applied in `web.New`).

`internal/web/import.go`:

- `r.MultipartReader()` — error → `toastOnly(w, r, toast.TypeError, "No files uploaded", "")` (also used when the loop finds no `files` parts).
- Iterate parts; only `part.FormName() == "files"` is imported. Wrap each part in a tiny counting limiter that returns a `file too large` error past `ImportMaxFileBytes` (the error text becomes the skip reason via Task 2 step 6; `multipart.Reader.NextPart` drains the oversized remainder itself). Track cumulative bytes: once the total passes `ImportMaxTotalBytes`, later parts are skipped `request size limit reached` without calling the service.
- `res, err := h.svc.ImportReplay(r.Context(), part.FileName(), lr)`; a non-nil error (fatal only) → `h.serverError`.
- Render `views.ImportDone(v, results)` with `v, err := h.adminPlayersView(r)`.

`internal/web/views/admin.templ`:

- `templ ImportCard()`: card between the "Add a player" card and `@AdminPlayersTable(v)` in `AdminPlayersPage`; title `Import replays`; description `Local .bsor replays are matched to their players automatically and fill in replays the platforms no longer serve. Files already archived or still downloading are left untouched.`; form `hx-post="/admin/replays/import" hx-target="#admin-players" hx-swap="outerHTML" hx-encoding="multipart/form-data"`; `<input type="file" name="files" multiple accept=".bsor" aria-label="Replay files">` (styled with the existing input component classes); submit button `Import`; below the form `<div id="import-results" class="mt-4 empty:hidden"></div>`.
- `templ ImportResults(rs []service.ImportResult)`: one row per file — imported: `✓ {File}` linking `templ.SafeURL(res.URL)` with text `{res.Map} imported`; already: `= {File} already archived`; skipped: `✗ {File} {res.Reason}` (skips in `text-destructive`). Wrap in `<div id="import-results" hx-swap-oob="true" class="mt-4">` so it replaces the placeholder in place (the card is outside `#admin-players`, so the table swap cannot wipe it).
- `templ ImportDone(v AdminPlayersView, rs []service.ImportResult)`: `@AdminPlayersTable(v)` + `@ImportResults(rs)` + summary toast: `toast.TypeSuccess` when every result is `imported` or `already`, else `toast.TypeWarning`; title `Imported {n} of {m} replays`, empty description.

- [ ] **Step 4: Regenerate templates, run the tests**

Run: `go tool templ generate && go test -race ./internal/web/ -run 'TestImport' -v`
Expected: PASS; `internal/web/views/admin_templ.go` updated (commit it).

- [ ] **Step 5: Full verification and commit**

Run: `go test -race ./... && make lint`
Expected: PASS, no lint findings.

```bash
git add internal/web/
git commit -m "feat(web): import local .bsor replays from Manage"
```

### Task 4: Manual verification pass (no code)

- [ ] Run the server locally: `make run`; sign in; on **Manage** import the 11 files from `.worktrees/samples`.
- [ ] Expected: 6 clears and 4 fails import onto their rows (or report `already archived` where the row already has a replay — both are correct per spec §5); `Be There For You` reports `map not in the archive`.
- [ ] Open one imported replay's page: the raw download works and the embed plays it in the BeatLeader hosted viewer.
- [ ] Re-import the same batch: every file reports `already archived` / `download in progress`, and no replay file changed on disk (row `ReplaySHA256` stable).
- [ ] No commit (nothing to commit; report findings instead — fix-code fallout becomes follow-up tasks before the branch review).
