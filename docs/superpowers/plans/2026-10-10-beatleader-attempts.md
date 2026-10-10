# BeatLeader Attempts Implementation Plan (multi-platform, part 3 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **Update the Progress Tracking section (below) as you go — it is the hand-off contract between agents.**

**Goal:** Archive BeatLeader attempts — failed, quit, restarted and practice runs, and clears that did not beat the PB — for players whose history is public. The admin switches them on per account, sees exactly what the player must change when the history is private, and anyone can filter plays by type (`complete`, `fail`, `quit`, `restart`, `practice`, `all`).

**Architecture:** Attempts are an optional feed of the BeatLeader platform (`FeedSpec{Kind: attempt, Optional, NeedsAccess, AccessHint}`) whose plays are stored as `scores` rows with `kind=attempt`, so the replay pipeline, storage, routes and merged page from Plans 1–2 apply unchanged. What is new is generic: the service switches optional feeds on/off and probes access, the worker gets a probe tier and pauses a feed that loses access, the score filter gains `Types`, and the UI renders a platform-provided `platform.Hint`. BeatLeader-specific code stays in `internal/beatleader` (the `scoresstats` call, end-type mapping, the skip rule for PB clears, the hint text).

**Tech Stack:** Go 1.27 · GORM + gorm gen · `github.com/glebarez/sqlite` · templ + htmx · huma v2 · golangci-lint v2. No new dependencies.

**Spec:** [`docs/superpowers/specs/2026-10-10-beatleader-design.md`](../specs/2026-10-10-beatleader-design.md) — read §2.2, §4.3, §4.4, §4.6 (attempts), §5.1 (tiers, access probe, access lost), §5.2, §6.1 (attempt pages), §6.2 (feed endpoints, `type` filter) and §6.3 (access hint) before starting any task. Where the spec and this plan differ in a signature, the plan wins.

**Prerequisite: Plan 2 is done.** This plan builds on [Plan 2](2026-10-10-beatleader-scores.md)'s names (`beatleader.Client`, `beatleader.API`, `beatleader.NewPlatform`, `beatleader.play`, `service.LinkIdentity`, `service.Identity.Counts`, `service.ScoreFilter.where`, `testutil.NewMultiService`/`UpsertFake`/`Row`/`Archive`, `newEnvWith`/`seedTP`/`crossSeed`, `views.PlatformName`, `renderRow`, `newMultiAPI`, `tpEnv`…). **Before starting, read Plan 2's Deviations log** and apply its renames to the task you are about to do.

## Global Constraints

Every task's requirements implicitly include all of these.

- Module `github.com/yyewolf/ssarchiver`, `go 1.27`. **No new dependencies; no version bumps.** Pinned: `gorm.io/gorm v1.31.2`, `gorm.io/gen v0.3.29`, `github.com/glebarez/sqlite v1.11.0`, `github.com/danielgtaylor/huma/v2 v2.39.1`, `github.com/a-h/templ v0.3.1070`, golangci-lint `v2.14.0`.
- **No schema change.** `sync_feeds` already has `access`, `access_checked_at`, `remote_total`; `scores` already has `kind`, `end_type`, `end_time`. Never call `Migrator().DropColumn`/`AlterColumn`/`DropTable`.
- **No platform name in generic code.** `service`, `archiver`, `web`, `views`, `api` never mention BeatLeader; they read `platform.FeedSpec` (`Optional`, `NeedsAccess`, `AccessHint`) from the registry. Tests use the fake platform (`testplat`, whose attempt feed is optional and needs access).
- Stored values (exact): feed/row kind `attempt`; end types `clear`, `fail`, `restart`, `quit`, `practice`, `unknown` (`model.End*`); access `n/a`, `unknown`, `public`, `private` (`model.Access*`). BeatLeader `endType`: `unknown(0) clear(1) fail(2) restart(3) quit(4) practice(5)`.
- BeatLeader attempts endpoint: `GET /player/{id}/scoresstats?sortBy=date&order=desc&page=N&count=C` (**`sortBy` must be sent**; it defaults to pp). 200 = public, **401 = private** (also for unknown players). Attempt replays under `https://api.beatleader.xyz/otherreplays/` use the API limiter and answer 401 when the history goes private.
- A `clear` attempt whose replay is a score replay (`cdn.replays.beatleader.xyz` or `api.beatleader.xyz/replays-storage/`) is **skipped**: it is the PB the scores feed archives. Every other attempt is stored, with `personal_best=false`, `set_at = timepost`, `end_time = time`.
- Access timings: a feed with `access=unknown` is probed at once (and again 5 minutes after a failed probe); a `private` feed is re-probed every 24 hours. A 401/403 on a listing or a replay of a feed that needs access flips it to `private`; its rows and files are kept and its pending replays stay pending (no failed attempt is counted).
- Work order (spec §5.1): access probe → poll (score feeds first) → new replays → score backfill listing → score backfill replays → optional backfill listing → other backfill replays. Optional feeds never delay score work.
- `type` filter: `complete` (default) = `kind=score` rows plus `clear` attempts; `fail`/`quit`/`restart`/`practice` = attempts with that end type; `all` = everything; a comma list ORs them. Without attempts, the default returns exactly what Plan 2 returned.
- The access hint is an **admin** concern: it never appears on public pages (`/`, `/p/…`, `/s/…`, `/embed/…`).
- Generated code is committed (`make generate` after `.templ` / Tailwind class changes). Every commit passes `go build ./...`, `go test ./...` and `make lint`. Conventional Commits. Errors wrapped with `%w` and a package prefix; `slog` only; no network in tests (the live smoke test is behind `-tags live`).

## Review Focus

Inputs/failure modes the spec implies that are easy to get wrong. Each has a pinned test in the owning task.

1. **The player's BeatLeader switch looks on but the save failed** (spec §2.2) — the probe answers 401 forever. Expected: the feed stays `private`, is never polled, the hint (with the "reload and check the switch stayed on" step) shows on Manage and Sync, **Check again** re-probes immediately, and the 24 h re-check keeps trying. → Task 2 `TestCheckFeedAccess`, Task 3 `TestPrivateFeedRecheckedDaily`, Task 6 `TestAdminAttemptsSwitch`.
2. **A player makes their history private in the middle of a large backfill** — expected: the next listing or replay 401 flips the feed to private, nothing already archived is lost or re-queued as failed, and work resumes from the same page once access is back. → Task 3 `TestAccessLostMidBackfill`, `TestReplayUnauthorizedPausesFeed`.
3. **A heavy attempt history (tens of thousands of runs)** — expected: no score work (polls, score backfill, score replays, ScoreSaber replays) waits behind attempt work. → Task 3 `TestAttemptWorkWaitsForScoreWork`.
4. **The same PB clear arriving through both feeds** (attempt clear pointing at the PB's replay on either the CDN or `replays-storage`) — expected: skipped, never a duplicate row or a duplicate download; a non-PB clear on `otherreplays` is kept. → Task 1 `TestAttemptPlays`.
5. **API clients and pages that never asked for attempts** — expected: default listings, counts, chips and the merged page are unchanged once attempts exist; attempts appear only with `type`. → Task 4 `TestTypeFilter`, `TestDefaultViewsHideAttempts`.

---

## Progress Tracking

**Rules for every agent working on this plan:**

1. Before starting a task: set its row to `🟡 in progress`, fill `Owner` and today's date in `Started`.
2. Tick each step checkbox (`- [x]`) in the task body as soon as it is done — not in batches.
3. When the task's final commit lands: set `✅ done`, put the short commit SHA in `Commit`, and add one line to **Verification log** with the exact commands run and their result.
4. If you deviate from the plan (renamed function, different library call, extra file), add an entry to **Deviations log** *and* fix any later task text that refers to the old name. Later agents only read their own task.
5. If blocked: set `⛔ blocked`, explain in **Session hand-off**, stop.
6. Before ending a session: update **Session hand-off**. Commit the plan file together with your work (`docs: update plan progress`).

Status legend: `⬜ todo` · `🟡 in progress` · `✅ done` · `⛔ blocked`

| # | Task | Status | Owner | Started | Commit |
|---|------|--------|-------|---------|--------|
| 1 | BeatLeader attempts feed: `scoresstats`, end types, skip rule, access probe, hint | ✅ done | SDD controller | 2026-10-10 | 98e932a |
| 2 | Optional feeds and access in the service | ✅ done | SDD controller | 2026-10-10 | d371eab |
| 3 | Worker: probe tier, access lost mid-run, tier order | ✅ done | SDD controller | 2026-10-10 | 182936c |
| 4 | `type` filter (service, player page, API) | ✅ done | SDD controller | 2026-10-10 | ea5bf5a |
| 5 | API: optional feed switch, access check, feed DTO | ✅ done | SDD controller | 2026-10-10 | 5d066dd |
| 6 | Admin and sync UI: attempts switch, access hint, check again | ✅ done | SDD controller | 2026-10-10 | 7f8989e |
| 7 | Attempt display: pages, chips, counts, viewer check | ✅ done | SDD controller | 2026-10-10 | f5e5d31 |
| 8 | End-to-end attempts test, live smoke test, docs | ✅ done | SDD controller | 2026-10-10 | b7b2526 |
| 5 | API: optional feed switch, access check, feed DTO | ⬜ todo | | | |
| 6 | Admin and sync UI: attempts switch, access hint, check again | ⬜ todo | | | |
| 7 | Attempt display: pages, chips, counts, viewer check | ⬜ todo | | | |
| 8 | End-to-end attempts test, live smoke test, docs | ⬜ todo | | | |

### Session hand-off

_Current task:_ —
_Next step:_ All 8 tasks done (98e932a…b7b2526 + fix e88a7fe) and reviewed via subagent-driven development; whole-branch review clean after one fix wave (rate-limited access checks now warn/429 instead of reporting success).
_Half-done / uncommitted:_ Two manual checks are deferred to the human: Task 7 Step 6 (does the bundled ArcViewer play truncated BSORs? `views.AttemptViewer` stays `true` until checked; flip to `false` and re-run `go test ./internal/web/...` if not) and Task 8 Step 5 (visual pass of the admin switch, hint and Sync row against live BeatLeader).
_Notes for next agent:_ Facts re-verified live on 2026-10-10 while writing this plan: the instance owner's attempts (`76561198038925092`) are public and all date from 2024-01-27; 24 of 27 have `replay: null` and the 3 clears point at their PB's CDN file. A public top player's history has `otherreplays` URLs for quits, restarts, practice runs and non-PB clears (e.g. `otherreplays/34897106.bsor` for a clear). `otherreplays` responses carry the API rate-limit headers, answer 200 to a range request (no 206), and send **no** `Access-Control-Allow-Origin` — ArcViewer can only play them once archived here (Task 7's manual check works around it with a local CORS server). The fixture in Task 1 is trimmed from those responses, with the other player's ID replaced by the owner's.

### Deviations log

| Date | Task | Deviation | Reason | Later tasks updated? |
|------|------|-----------|--------|----------------------|

### Verification log

| Date | Task | Command(s) | Result |
|------|------|------------|--------|
| 2026-10-10 | 1 | `go test -race ./internal/beatleader/ ./internal/platform/ && go test ./... && make lint` | all ok; `0 issues.` (implementer, TDD RED→GREEN) |
| 2026-10-10 | 2 | `go test -race ./internal/service/ ./internal/testutil/ ./internal/archiver/ && go test ./... && make lint` | all ok; `0 issues.` (implementer, TDD RED→GREEN) |
| 2026-10-10 | 3 | `go test -race ./internal/archiver/ && go test ./... && make lint` | all ok; `0 issues.` (implementer, TDD RED→GREEN) |
| 2026-10-10 | 4 | `make generate && go test -race ./internal/service/ ./internal/web/... ./internal/api/ ./internal/archiver/ && go test ./... && make lint` | all ok; `0 issues.` (implementer, TDD RED→GREEN) |
| 2026-10-10 | 5 | `go test -race ./internal/api/ && go test ./... && make lint` | all ok; `0 issues.` (implementer, TDD RED→GREEN) |
| 2026-10-10 | 6 | `make generate && go test -race ./internal/web/... && go test ./... && make lint` | all ok; `0 issues.` (implementer, TDD RED→GREEN; Step 7 visual check deferred to human) |
| 2026-10-10 | 7 | `make generate && go test -race ./internal/web/... && go test ./... && make lint` | all ok; `0 issues.` (implementer, TDD RED→GREEN; Step 6 manual ArcViewer check deferred to human, `AttemptViewer = true` pending it) |
| 2026-10-10 | 8 | `make generate && git diff --exit-code -- '*_templ.go' internal/db/query internal/web/static/css/app.css && go test -race ./... && make lint`; `go test -tags live -run Live ./internal/beatleader/ -v` | no diff; all packages ok; `0 issues.`; live PASS (owner's history public at check time; private path covered by e2e) |
| 2026-10-10 | final review fix | `go test ./... && make lint` at e88a7fe | all ok; `0 issues.` (rate-limited access checks now warn/429 instead of reporting success) |

---

## File Map

```
internal/platform/platform.go               Hint.Intro/Note; PlayPage.Skipped
internal/beatleader/types.go                Score.EndType, Score.Time
internal/beatleader/client.go               Attempts, ScoreReplay
internal/beatleader/platform.go             attempts feed + hint; FeedPage(attempt); ProbeAccess; AttemptPlays
internal/beatleader/testdata/attempts.json  NEW  trimmed live attempts page
internal/service/access.go                  NEW  SetFeedEnabled, CheckFeedAccess, DueProbe, MarkFeedPrivate, AccessRecheck, ProbeRetryDelay
internal/service/feeds.go                   feedSelect (shared by work feeds and probes)
internal/service/scores.go                  ScoreFilter.Types, TypeComplete/TypeAll/PlayTypes, ParseTypes, typeCond
internal/archiver/{worker,poll,download}.go probe tier, access lost → private, Skipped-aware end of listing
internal/api/{dto,feeds,scores}.go          Feed DTO (optional, hint, remote_total, counts), feed PATCH + check, type param
internal/web/{admin,public,admin_sync,web}.go  feed switch/check handlers, type filter, attempt titles
internal/web/views/{access.go,access.templ} NEW  AccessHint, optional-feed helpers
internal/web/views/{admin,sync,player,score}.templ  switch + hint, private queue rows, type select, end badges, attempt pages
internal/testutil/fakeplatform.go           access/feed/replay error scripting, probe and replay call logs
internal/app/beatleader_test.go             attempts end to end
README.md                                   attempts section
```

---
## Task 1: BeatLeader attempts feed — `scoresstats`, end types, skip rule, access probe, hint

BeatLeader gains its optional `attempt` feed (spec §2.2, §4.6, §5.3): the client reads `scoresstats`, the adapter converts attempts (end type, end time, `timepost`), skips the clears that are the scores feed's PB, probes access with `count=1`, and carries the hint text of spec §6.3. Two neutral fields support it: `platform.Hint` gets an intro and a closing note, and `platform.PlayPage.Skipped` counts plays an adapter dropped on purpose (Task 3 uses it so that a page made only of skipped clears does not end a listing).

**Files:**
- Modify: `internal/platform/platform.go` (`Hint.Intro`, `Hint.Note`, `PlayPage.Skipped`)
- Modify: `internal/beatleader/types.go` (`Score.EndType`, `Score.Time`), `internal/beatleader/client.go` (`Attempts`, `ScoreReplay`), `internal/beatleader/platform.go` (feed, hint, `FeedPage`, `ProbeAccess`, `AttemptPlays`)
- Create: `internal/beatleader/testdata/attempts.json`
- Test: `internal/beatleader/client_test.go`, `internal/beatleader/platform_test.go`, `internal/beatleader/live_test.go`

**Interfaces:**
- Consumes: Plan 2 `beatleader.Client.getJSON`, `ReplayAllowed`, `prefixes`, `play(Score) platform.Play`, `NewPlatform`, `API`, `fakeAPI` (test).
- Produces:
  - `platform.Hint{Title, Intro string; Steps []string; LinkText, LinkURL, Note string}`; `platform.PlayPage.Skipped int`
  - `beatleader.Score.EndType int`, `Score.Time float64`
  - `(*beatleader.Client).Attempts(ctx, playerID string, page, count int) (ScorePage, error)`; `(*Client).ScoreReplay(url string) bool`
  - `beatleader.API` gains `Attempts` and `ScoreReplay`
  - `func beatleader.AttemptPlays(items []Score, allowed, scoreReplay func(string) bool) (plays []platform.Play, skipped, refused int)`
  - BeatLeader feeds: `score` (required) and `attempt` (`Optional`, `NeedsAccess`, `AccessHint` = the spec §6.3 text); `ProbeAccess(attempt)` → `public` + total / `private`

- [x] **Step 1: Add the fixture**

`internal/beatleader/testdata/attempts.json` — a `scoresstats` page trimmed from live responses (2026-10-10): the owner's fail and PB clear, a public player's quit/restart/non-PB clear/practice runs (player ID replaced by the owner's), plus two edited items for coverage: a PB clear whose replay is on `replays-storage` and an `unknown` end with an off-allowlist replay.

```json
{
  "metadata": {"itemsPerPage": 100, "page": 1, "total": 8},
  "data": [
    {"id": 148437271, "endType": 4, "time": 22.91243, "timepost": 1791149257, "timeset": null, "baseScore": 96379, "modifiedScore": 96379, "accuracy": 0.93431246, "pp": 0, "rank": 0, "modifiers": "", "badCuts": 0, "missedNotes": 1, "fullCombo": false, "maxCombo": 116, "hmd": 256, "leaderboardId": "51e10x91",
     "replay": "https://api.beatleader.xyz/otherreplays/76561198038925092-1791149257-ExpertPlus-Standard-376634A0239E5652CA1FC54E40F5CD3A0C10878C.bsor",
     "leaderboard": {"id": "51e10x91", "song": {"hash": "376634a0239e5652ca1fc54e40f5cd3a0c10878c", "name": "B2b", "subName": "", "author": "Charli xcx", "mapper": "Bizzy", "coverImage": "https://cdn.beatsaver.com/376634a0239e5652ca1fc54e40f5cd3a0c10878c.jpg"}, "difficulty": {"value": 9, "modeName": "Standard", "difficultyName": "ExpertPlus", "status": 0, "stars": null, "maxScore": 829035}}},
    {"id": 148432628, "endType": 3, "time": 17.06824, "timepost": 1791146892, "timeset": null, "baseScore": 48686, "modifiedScore": 48686, "accuracy": 0.91044414, "pp": 0, "rank": 0, "modifiers": "PM", "badCuts": 0, "missedNotes": 1, "fullCombo": false, "maxCombo": 57, "hmd": 256, "leaderboardId": "4a5c7xx71",
     "replay": "https://api.beatleader.xyz/otherreplays/76561198038925092-1791146892-Expert-Standard-05DB3889513D4E8B24DEEA3192F7C2E8BFF38048.bsor",
     "leaderboard": {"id": "4a5c7xx71", "song": {"hash": "05db3889513d4e8b24deea3192f7c2e8bff38048", "name": "Thank You, New Jersey", "subName": "", "author": "Origami Angel", "mapper": "Tranch", "coverImage": "https://cdn.beatsaver.com/05db3889513d4e8b24deea3192f7c2e8bff38048.jpg"}, "difficulty": {"value": 7, "modeName": "Standard", "difficultyName": "Expert", "status": 0, "stars": null, "maxScore": 924715}}},
    {"id": 148432157, "endType": 1, "time": 90.44275, "timepost": 1791146633, "timeset": null, "baseScore": 771534, "modifiedScore": 771534, "accuracy": 0.87254405, "pp": 0, "rank": 14, "modifiers": "", "badCuts": 9, "missedNotes": 3, "fullCombo": false, "maxCombo": 310, "hmd": 256, "leaderboardId": "42649x71",
     "replay": "https://api.beatleader.xyz/otherreplays/34897106.bsor",
     "leaderboard": {"id": "42649x71", "song": {"hash": "aa9188721ec0ef4fbdc90043d20ca3a95c123c24", "name": "POP IN 2", "subName": "(Oshi No Ko Season 2)", "author": "B-Komachi", "mapper": "MrBeats6000 & CoolWhale", "coverImage": "https://cdn.beatsaver.com/aa9188721ec0ef4fbdc90043d20ca3a95c123c24.jpg"}, "difficulty": {"value": 7, "modeName": "Standard", "difficultyName": "Expert", "status": 0, "stars": null, "maxScore": 884235}}},
    {"id": 144609827, "endType": 5, "time": 106.36218, "timepost": 1788269535, "timeset": null, "baseScore": 193229, "modifiedScore": 38645, "accuracy": 0.38396987, "pp": 0, "rank": 0, "modifiers": "SS,NF", "badCuts": 29, "missedNotes": 73, "fullCombo": false, "maxCombo": 61, "hmd": 256, "leaderboardId": "50cfa91",
     "replay": "https://api.beatleader.xyz/otherreplays/76561198038925092-1788269535-practice-ExpertPlus-Standard-1CCC3FC25A48C9F64781D7B7BCB84C5F58D3C4A4.bsor",
     "leaderboard": {"id": "50cfa91", "song": {"hash": "1ccc3fc25a48c9f64781d7b7bcb84c5f58d3c4a4", "name": "7D", "subName": "feat. kasane vavzed", "author": "7_7", "mapper": "WDG_pagdorn", "coverImage": "https://cdn.beatsaver.com/1ccc3fc25a48c9f64781d7b7bcb84c5f58d3c4a4.jpg"}, "difficulty": {"value": 9, "modeName": "Standard", "difficultyName": "ExpertPlus", "status": 0, "stars": null, "maxScore": 3267035}}},
    {"id": 26723243, "endType": 2, "time": 59.74023, "timepost": 1706356938, "timeset": null, "baseScore": 355698, "modifiedScore": 355698, "accuracy": 0.698989, "pp": 125.32705, "rank": 0, "modifiers": "", "badCuts": 5, "missedNotes": 29, "fullCombo": false, "maxCombo": 0, "hmd": 256, "leaderboardId": "1d3f5x91",
     "replay": null,
     "leaderboard": {"id": "1d3f5x91", "song": {"hash": "57511ee48555e00e031bd3b1df90ba7be5712b56", "name": "Night Raid with a Dragon", "subName": "", "author": "Camellia", "mapper": "nolan121405", "coverImage": "https://eu.cdn.beatsaver.com/57511ee48555e00e031bd3b1df90ba7be5712b56.jpg"}, "difficulty": {"value": 9, "modeName": "Standard", "difficultyName": "ExpertPlus", "status": 3, "stars": 10.066875, "maxScore": 1664395}}},
    {"id": 26724442, "endType": 1, "time": 210.65674, "timepost": 1706356788, "timeset": null, "baseScore": 825292, "modifiedScore": 825292, "accuracy": 0.797295, "pp": 167.50322, "rank": 763, "modifiers": "", "badCuts": 13, "missedNotes": 16, "fullCombo": false, "maxCombo": 423, "hmd": 256, "leaderboardId": "1d3f5x71",
     "replay": "https://cdn.replays.beatleader.xyz/12164051-76561198038925092-Expert-Standard-57511EE48555E00E031BD3B1DF90BA7BE5712B56.bsor",
     "leaderboard": {"id": "1d3f5x71", "song": {"hash": "57511ee48555e00e031bd3b1df90ba7be5712b56", "name": "Night Raid with a Dragon", "subName": "", "author": "Camellia", "mapper": "nolan121405", "coverImage": "https://eu.cdn.beatsaver.com/57511ee48555e00e031bd3b1df90ba7be5712b56.jpg"}, "difficulty": {"value": 7, "modeName": "Standard", "difficultyName": "Expert", "status": 3, "stars": 7.2064004, "maxScore": 1035115}}},
    {"id": 26724000, "endType": 1, "time": 200.1, "timepost": 1706354700, "timeset": null, "baseScore": 609589, "modifiedScore": 609589, "accuracy": 0.850437, "pp": 0, "rank": 71, "modifiers": "", "badCuts": 5, "missedNotes": 4, "fullCombo": false, "maxCombo": 159, "hmd": 256, "leaderboardId": "1075671",
     "replay": "https://api.beatleader.xyz/replays-storage/12163441-76561198038925092-Expert-Standard-346AB7665240AD1C4DC4F3C946227FC738685520.bsor",
     "leaderboard": {"id": "1075671", "song": {"hash": "346ab7665240ad1c4dc4f3c946227fc738685520", "name": "MORE", "subName": "", "author": "KDA", "mapper": "Ben Records", "coverImage": "https://eu.cdn.beatsaver.com/346ab7665240ad1c4dc4f3c946227fc738685520.jpg"}, "difficulty": {"value": 7, "modeName": "Standard", "difficultyName": "Expert", "status": 0, "stars": null, "maxScore": 716795}}},
    {"id": 26700001, "endType": 0, "time": 12.5, "timepost": 1706350000, "timeset": null, "baseScore": 1000, "modifiedScore": 1000, "accuracy": 0.5, "pp": 0, "rank": 0, "modifiers": "", "badCuts": 0, "missedNotes": 2, "fullCombo": false, "maxCombo": 10, "hmd": 512, "leaderboardId": "1d3f5x91",
     "replay": "https://evil.example/otherreplays/x.bsor",
     "leaderboard": {"id": "1d3f5x91", "song": {"hash": "57511ee48555e00e031bd3b1df90ba7be5712b56", "name": "Night Raid with a Dragon", "subName": "", "author": "Camellia", "mapper": "nolan121405", "coverImage": "https://eu.cdn.beatsaver.com/57511ee48555e00e031bd3b1df90ba7be5712b56.jpg"}, "difficulty": {"value": 9, "modeName": "Standard", "difficultyName": "ExpertPlus", "status": 3, "stars": 10.066875, "maxScore": 1664395}}}
  ]
}
```

- [x] **Step 2: Write the failing tests**

Append to `internal/beatleader/client_test.go`:

```go
func TestAttempts(t *testing.T) {
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player/private/scoresstats" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		if r.URL.Path != "/player/42/scoresstats" || q.Get("sortBy") != "date" || q.Get("order") != "desc" ||
			q.Get("page") != "3" || q.Get("count") != "100" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write(fixture(t, "attempts.json"))
	})
	ctx := context.Background()
	sp, err := e.c.Attempts(ctx, "42", 3, 100)
	if err != nil || sp.Metadata.Total != 8 || len(sp.Data) != 8 {
		t.Fatalf("attempts = %+v %v", sp.Metadata, err)
	}
	a := sp.Data[0]
	if a.EndType != 4 || a.Time != 22.91243 || a.Timeset != 0 ||
		!a.Timepost.Time().Equal(time.Date(2026, 10, 4, 21, 27, 37, 0, time.UTC)) || !strings.Contains(a.Replay, "/otherreplays/") {
		t.Fatalf("attempt = %+v", a)
	}
	if _, err := e.c.Attempts(ctx, "private", 1, 1); !errors.Is(err, beatleader.ErrUnauthorized) {
		t.Fatalf("private history = %v", err)
	}
}

func TestScoreReplay(t *testing.T) {
	c := beatleader.NewClient(nil, nil)
	for u, want := range map[string]bool{
		"https://cdn.replays.beatleader.xyz/1-2-Expert-Standard-ABC.bsor":     true,
		"https://api.beatleader.xyz/replays-storage/1-2-Expert-Standard.bsor": true,
		"https://api.beatleader.xyz/otherreplays/34897106.bsor":               false,
		"https://evil.example/cdn.replays.beatleader.xyz/1.bsor":              false,
	} {
		if c.ScoreReplay(u) != want {
			t.Errorf("ScoreReplay(%s) = %v", u, !want)
		}
	}
}
```

In `internal/beatleader/platform_test.go`, give `fakeAPI` the two new methods (the adapter's `API` interface requires them):

```go
type fakeAPI struct {
	player     beatleader.Player
	page       beatleader.ScorePage
	err        error
	attempts   beatleader.ScorePage
	attemptErr error
	calls      []string // "attempts:page:count"
	replayURL  string
}

func (f *fakeAPI) Attempts(_ context.Context, _ string, page, count int) (beatleader.ScorePage, error) {
	f.calls = append(f.calls, fmt.Sprintf("attempts:%d:%d", page, count))
	return f.attempts, f.attemptErr
}

func (f *fakeAPI) ScoreReplay(u string) bool { return beatleader.NewClient(nil, nil).ScoreReplay(u) }
```

In `TestFeedPage`, delete the check that `FeedPage(ctx, model.KindAttempt, …)` errors ("no attempts feed in this plan"): the feed exists now.

In `TestRegistryEntry`, replace the feed assertion (`len(bl.Feeds) != 1`) with:

```go
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
```

and append:

```go
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
```

- [x] **Step 3: Run them to verify they fail**

Run: `go test ./internal/beatleader/`
Expected: FAIL — undefined: `Client.Attempts`, `ScoreReplay`, `AttemptPlays`, `Score.EndType`, `PlayPage.Skipped`.

- [x] **Step 4: Neutral fields**

`internal/platform/platform.go`:

```go
// PlayPage is one page of a feed, newest first.
type PlayPage struct {
	Plays      []Play
	TotalPages int
	Refused    int // plays whose replay URL is not on the platform's allowlist; kept without a replay
	Skipped    int // plays the adapter dropped on purpose (another feed stores them); the page was not empty
}
```

```go
// Hint tells the admin what a player must do to grant access to a feed (spec §6.3).
type Hint struct {
	Title    string
	Intro    string   // one sentence under the title
	Steps    []string // short imperative steps for the player
	LinkText string
	LinkURL  string
	Note     string // closing remark: re-checks, what is lost while waiting
}
```

- [x] **Step 5: Client**

`internal/beatleader/types.go` — add to `Score`:

```go
	EndType int     `json:"endType"` // attempts: unknown(0) clear(1) fail(2) restart(3) quit(4) practice(5)
	Time    float64 `json:"time"`    // attempts: seconds into the song when the run ended
```

`internal/beatleader/client.go`:

```go
// Attempts fetches one page (newest first) of a player's attempts
// (scoresstats; sortBy must be sent, it defaults to pp). 401 while the
// player's history is private, and for unknown players (spec §2.2).
func (c *Client) Attempts(ctx context.Context, playerID string, page, count int) (ScorePage, error) {
	var sp ScorePage
	q := url.Values{
		"sortBy": {"date"}, "order": {"desc"},
		"page": {strconv.Itoa(page)}, "count": {strconv.Itoa(count)},
	}
	err := c.getJSON(ctx, "/player/"+url.PathEscape(playerID)+"/scoresstats", q, &sp)
	return sp, err
}

// ScoreReplay reports whether u is a score replay (CDN or replays-storage)
// rather than an attempt replay (otherreplays).
func (c *Client) ScoreReplay(u string) bool {
	return c.ReplayAllowed(u) && !strings.HasPrefix(u, c.prefixes.Other)
}
```

- [x] **Step 6: Adapter**

`internal/beatleader/platform.go` — extend `API`:

```go
type API interface {
	Player(ctx context.Context, id string) (Player, error)
	Scores(ctx context.Context, playerID string, page int) (ScorePage, error)
	Attempts(ctx context.Context, playerID string, page, count int) (ScorePage, error)
	Replay(ctx context.Context, url string) (io.ReadCloser, error)
	ReplayAllowed(url string) bool
	ScoreReplay(url string) bool
}
```

the hint and feeds:

```go
// attemptsHint is shown while a player's attempt history is private (spec §6.3).
var attemptsHint = &platform.Hint{
	Title: "Attempt history is private on BeatLeader",
	Intro: "SSArchiver can only archive attempts when the player makes their history public:",
	Steps: []string{
		"Sign in on beatleader.com with this account.",
		"Open Settings → Scores.",
		"Turn on Public history (auto-synced).",
		"Reload the page and check the switch is still on (BeatLeader can show it on even when saving failed).",
	},
	LinkText: "Open BeatLeader settings",
	LinkURL:  "https://beatleader.com/settings",
	Note: "SSArchiver also re-checks every 24 hours. Old attempt replays are dropped by BeatLeader over time, " +
		"so the sooner this is on, the more can be saved.",
}
```

and in `NewPlatform`:

```go
		Feeds: []platform.FeedSpec{
			{Kind: model.KindScore},
			{Kind: model.KindAttempt, Optional: true, NeedsAccess: true, AccessHint: attemptsHint},
		},
```

Replace `FeedPage` and `ProbeAccess`:

```go
func (a adapter) FeedPage(ctx context.Context, kind, externalID string, page int) (platform.PlayPage, error) {
	switch kind {
	case model.KindScore:
		sp, err := a.api.Scores(ctx, externalID, page)
		if errors.Is(err, ErrNotFound) {
			// BeatLeader answers 404 for a player without scores: the end of the
			// listing. A vanished player is caught by Resolve (spec §5.2).
			return platform.PlayPage{}, nil
		}
		if err != nil {
			return platform.PlayPage{}, err
		}
		plays, refused := Plays(sp.Data, a.api.ReplayAllowed)
		return platform.PlayPage{Plays: plays, TotalPages: sp.Metadata.TotalPages(), Refused: refused}, nil
	case model.KindAttempt:
		sp, err := a.api.Attempts(ctx, externalID, page, ScoresPageSize)
		if err != nil {
			return platform.PlayPage{}, err // 401 = private history: platform.ErrUnauthorized
		}
		plays, skipped, refused := AttemptPlays(sp.Data, a.api.ReplayAllowed, a.api.ScoreReplay)
		return platform.PlayPage{Plays: plays, TotalPages: sp.Metadata.TotalPages(), Refused: refused, Skipped: skipped}, nil
	}
	return platform.PlayPage{}, fmt.Errorf("beatleader: no %s feed", kind)
}

// ProbeAccess reads one attempt: 200 means the history is public, 401 private
// (spec §2.2). The score feed needs no probe.
func (a adapter) ProbeAccess(ctx context.Context, kind, externalID string) (string, int64, error) {
	if kind != model.KindAttempt {
		return model.AccessNA, 0, nil
	}
	sp, err := a.api.Attempts(ctx, externalID, 1, 1)
	switch {
	case errors.Is(err, ErrUnauthorized):
		return model.AccessPrivate, 0, nil
	case err != nil:
		return "", 0, err
	}
	return model.AccessPublic, int64(sp.Metadata.Total), nil
}
```

Add the conversion:

```go
// attemptEnds maps BeatLeader's endType onto the stored vocabulary (spec §4.4).
var attemptEnds = map[int]string{
	1: model.EndClear, 2: model.EndFail, 3: model.EndRestart, 4: model.EndQuit, 5: model.EndPractice,
}

// AttemptPlays converts an attempts page (spec §4.6). A clear whose replay is
// a score replay is the PB the scores feed archives: it is skipped (second
// result). A replay off the allowlist is dropped and counted (third result).
func AttemptPlays(items []Score, allowed, scoreReplay func(string) bool) (plays []platform.Play, skipped, refused int) {
	for _, s := range items {
		if s.EndType == 1 && s.Replay != "" && scoreReplay(s.Replay) {
			skipped++
			continue
		}
		pl := play(s)
		end, ok := attemptEnds[s.EndType]
		if !ok {
			end = model.EndUnknown
		}
		t := s.Time
		pl.Kind, pl.EndType, pl.EndTime, pl.PersonalBest = model.KindAttempt, end, &t, false
		pl.SetAt = s.Timepost.Time()
		if s.Replay != "" {
			if allowed(s.Replay) {
				pl.HasReplay, pl.ReplayURL = true, s.Replay
			} else {
				refused++
			}
		}
		plays = append(plays, pl)
	}
	return plays, skipped, refused
}
```

`*Client` satisfies the extended `API` (both methods exist from Step 5).

- [x] **Step 7: Extend the live smoke test**

Append to `TestLiveBeatLeader` in `internal/beatleader/live_test.go`:

```go
	att, err := c.Attempts(ctx, livePlayerID, 1, 1)
	switch {
	case errors.Is(err, beatleader.ErrUnauthorized):
		t.Log("the owner's attempt history is private right now: probe path verified, listing skipped")
	case err != nil:
		t.Fatalf("attempts: %v", err)
	case att.Metadata.Total == 0 || len(att.Data) != 1:
		t.Fatalf("attempts page = %+v", att.Metadata)
	}
```

(import `"errors"`). Run: `go vet -tags live ./internal/beatleader/` → no output.

- [x] **Step 8: Run, lint, commit**

Run: `go test -race ./internal/beatleader/ ./internal/platform/ && go test ./... && make lint`
Expected: all ok; `0 issues.` (Registering the attempt feed changes nothing else yet: optional feeds have no `sync_feeds` row until switched on.)

```bash
git add internal/platform/platform.go internal/beatleader
git commit -m "feat(beatleader): optional attempts feed with access probe and the public-history hint"
```

---
## Task 2: Optional feeds and access in the service

The generic machinery of spec §4.3 and §5.1 for optional feeds:

- **`SetFeedEnabled`** creates an optional feed's `sync_feeds` row on first use. Switching it on (again) restarts its clock (`started_at`) and, for feeds that need a probe, resets `access` to `unknown`. Switching it off keeps the cursor, rows and files.
- **`CheckFeedAccess`** asks the adapter and stores `access`, `access_checked_at` and `remote_total`. It logs a warning with the hint title when access turns private.
- **`DueProbe`** is the worker's tier-1 query.
- **`MarkFeedPrivate`** records access lost mid-run.

The fake platform learns to script all of it.

**Files:**
- Create: `internal/service/access.go`
- Modify: `internal/service/feeds.go` (`feedSelect`)
- Modify: `internal/testutil/fakeplatform.go` (scripting)
- Test: `internal/service/access_test.go` (new)

**Interfaces:**
- Consumes: Plan 1 `FeedKey`, `WorkFeed`, `Busy`, `newFeed`, `updateFeed`, `workFeeds`, `NextReplay`; Plan 2 `LinkIdentity`, `SetIdentityEnabled`, `displayName`; Task 1 `platform.Hint`.
- Produces:
  - `service.AccessRecheck = 24 * time.Hour`, `service.ProbeRetryDelay = 5 * time.Minute`, `service.ErrFeedNotOptional`
  - `(*Service).SetFeedEnabled(ctx, k FeedKey, enabled bool) (*model.SyncFeed, error)`
  - `(*Service).CheckFeedAccess(ctx, k FeedKey) (*model.SyncFeed, error)` — returns the stored feed even when the probe failed (the error is also recorded in `last_error`); `nil` feed only when the feed or account does not exist
  - `(*Service).DueProbe(ctx, busy Busy) (*WorkFeed, error)`
  - `(*Service).MarkFeedPrivate(ctx, k FeedKey) error`
  - `testutil.FakePlatform` fields: `Access map[string]string` (key `kind/account`, default `public`), `AccessTotal int64`, `ProbeErr error`, `Probes []string` (`kind:account`), `FeedErr map[string]error` (key `kind/account`), `ReplayErr map[string]error` (key replay URL), `ReplayCalls []string`

- [x] **Step 1: Script the fake platform**

`internal/testutil/fakeplatform.go` — add the fields to `FakePlatform`:

```go
	Access      map[string]string // kind/account → what ProbeAccess reports (default public)
	AccessTotal int64             // remote total ProbeAccess reports
	ProbeErr    error             // ProbeAccess fails with it when set
	Probes      []string          // "kind:account" per probe
	FeedErr     map[string]error  // kind/account → FeedPage fails with it
	ReplayErr   map[string]error  // replay URL → Replay fails with it
	ReplayCalls []string          // replay URLs requested
```

initialise the three maps in `NewFakePlatformAs` (`Access: map[string]string{}, FeedErr: map[string]error{}, ReplayErr: map[string]error{}`), and update the adapter methods:

```go
func (f *FakePlatform) FeedPage(_ context.Context, kind, account string, page int) (platform.PlayPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprintf("%s:%s:%d", kind, account, page))
	if err := f.FeedErr[kind+"/"+account]; err != nil {
		return platform.PlayPage{}, err
	}
	all := f.plays[kind+"/"+account]
	total := (len(all) + f.PerPage - 1) / f.PerPage
	var out []platform.Play
	if start := (page - 1) * f.PerPage; start < len(all) {
		out = all[start:min(start+f.PerPage, len(all))]
	}
	return platform.PlayPage{Plays: out, TotalPages: total, Refused: f.Refused}, nil
}

func (f *FakePlatform) ProbeAccess(_ context.Context, kind, account string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind == model.KindScore {
		return model.AccessNA, 0, nil
	}
	f.Probes = append(f.Probes, kind+":"+account)
	if f.ProbeErr != nil {
		return "", 0, f.ProbeErr
	}
	if a, ok := f.Access[kind+"/"+account]; ok {
		return a, f.AccessTotal, nil
	}
	return model.AccessPublic, f.AccessTotal, nil
}

func (f *FakePlatform) Replay(_ context.Context, ref platform.ReplayRef) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReplayCalls = append(f.ReplayCalls, ref.URL)
	if err := f.ReplayErr[ref.URL]; err != nil {
		return nil, err
	}
	b, ok := f.Replays[ref.URL]
	if !ok {
		return nil, fmt.Errorf("%w: %s", platform.ErrNotFound, ref.URL)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
```

- [x] **Step 2: Write the failing tests**

`internal/service/access_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func attemptKey(playerID string) service.FeedKey {
	return service.FeedKey{PlayerID: playerID, Platform: "testplat", Kind: model.KindAttempt}
}

func pollEvents(t *testing.T, svc *service.Service) string {
	t.Helper()
	evs, _, err := svc.ListEvents(context.Background(), service.EventFilter{Kind: model.KindPoll, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range evs {
		b.WriteString(e.Level + ": " + e.Message + "\n")
	}
	return b.String()
}

func TestSetFeedEnabled(t *testing.T) {
	svc, _, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	<-svc.WakeC()
	k := attemptKey(tess)
	clk.Advance(time.Hour)

	f, err := svc.SetFeedEnabled(ctx, k, true)
	if err != nil || !f.Enabled || f.Access != model.AccessUnknown || !f.StartedAt.Equal(testutil.T0.Add(time.Hour)) ||
		f.BackfillState != model.BackfillPending || f.BackfillPage != 1 {
		t.Fatalf("enabled feed = %+v %v", f, err)
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("switching a feed on must wake the worker")
	}
	if err := svc.SetFeedBackfill(ctx, k, model.BackfillRunning, 4, 9); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.SetFeedEnabled(ctx, k, false); f.Enabled || f.BackfillPage != 4 {
		t.Fatalf("switching off keeps the cursor: %+v", f)
	}
	clk.Advance(time.Hour)
	f, _ = svc.SetFeedEnabled(ctx, k, true)
	if !f.Enabled || f.BackfillPage != 4 || !f.StartedAt.Equal(testutil.T0.Add(2*time.Hour)) || f.Access != model.AccessUnknown {
		t.Fatalf("switching on again restarts the clock and the access check, not the cursor: %+v", f)
	}

	for _, bad := range []struct {
		k    service.FeedKey
		want error
	}{
		{service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore}, service.ErrFeedNotOptional},
		{service.FeedKey{PlayerID: tess, Platform: model.PlatformScoreSaber, Kind: model.KindAttempt}, service.ErrNotFound},
		{service.FeedKey{PlayerID: tess, Platform: "nope", Kind: model.KindAttempt}, service.ErrNotFound},
		{attemptKey("p99999999999"), service.ErrNotFound},
	} {
		if _, err := svc.SetFeedEnabled(ctx, bad.k, true); !errors.Is(err, bad.want) {
			t.Errorf("%v: %v, want %v", bad.k, err, bad.want)
		}
	}

	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "def", "testplat"); err != nil {
		t.Fatal(err)
	}
	if f, err := svc.SetFeedEnabled(ctx, attemptKey(alice), false); err != nil || f.Enabled {
		t.Fatalf("switching off a feed that was never on = %+v %v", f, err)
	}
	if feeds, _ := svc.Feeds(ctx, alice); len(feeds) != 2 {
		t.Fatalf("it stores nothing: %d feeds", len(feeds))
	}
}

func TestCheckFeedAccess(t *testing.T) {
	svc, fp, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	k := attemptKey(tess)
	if _, err := svc.SetFeedEnabled(ctx, k, true); err != nil {
		t.Fatal(err)
	}

	fp.Access["attempt/abc"] = model.AccessPrivate
	f, err := svc.CheckFeedAccess(ctx, k)
	if err != nil || f.Access != model.AccessPrivate || f.AccessCheckedAt == nil || !f.AccessCheckedAt.Equal(testutil.T0) {
		t.Fatalf("private = %+v %v", f, err)
	}
	_, _ = svc.CheckFeedAccess(ctx, k)
	if ev := pollEvents(t, svc); strings.Count(ev, "warn: History is private") != 1 {
		t.Fatalf("one warning when access turns private, not one per check:\n%s", ev)
	}

	fp.Access["attempt/abc"] = model.AccessPublic
	fp.AccessTotal = 42
	clk.Advance(time.Minute)
	f, _ = svc.CheckFeedAccess(ctx, k)
	if f.Access != model.AccessPublic || f.RemoteTotal != 42 || !f.AccessCheckedAt.Equal(testutil.T0.Add(time.Minute)) {
		t.Fatalf("public = %+v", f)
	}
	if ev := pollEvents(t, svc); !strings.Contains(ev, "info: TestPlat attempt history is public: archiving it") {
		t.Fatalf("events:\n%s", ev)
	}

	fp.ProbeErr = errors.New("boom")
	clk.Advance(time.Minute)
	f, err = svc.CheckFeedAccess(ctx, k)
	if err == nil || f == nil || f.Access != model.AccessPublic || f.LastError != "access check failed: boom" ||
		!f.AccessCheckedAt.Equal(testutil.T0.Add(2*time.Minute)) {
		t.Fatalf("failed probe = %+v %v", f, err)
	}
	fp.ProbeErr = fmt.Errorf("%w: slow down", platform.ErrRateLimited)
	clk.Advance(time.Minute)
	if f, err := svc.CheckFeedAccess(ctx, k); !errors.Is(err, platform.ErrRateLimited) || !f.AccessCheckedAt.Equal(testutil.T0.Add(2*time.Minute)) {
		t.Fatalf("a rate-limited probe records nothing: %+v %v", f, err)
	}

	probes := len(fp.Probes)
	score := service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore}
	if f, err := svc.CheckFeedAccess(ctx, score); err != nil || f.Access != model.AccessNA || len(fp.Probes) != probes {
		t.Fatalf("feeds without NeedsAccess are not probed: %+v %v", f, err)
	}
	if _, err := svc.CheckFeedAccess(ctx, attemptKey("p99999999999")); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown feed = %v", err)
	}
}

func TestDueProbe(t *testing.T) {
	svc, fp, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	k := attemptKey(tess)
	due := func() *service.WorkFeed {
		t.Helper()
		f, err := svc.DueProbe(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if due() != nil {
		t.Fatal("no optional feed yet")
	}
	_, _ = svc.SetFeedEnabled(ctx, k, true)
	if f := due(); f == nil || f.Key() != k || f.ExternalID != "abc" || f.PlayerName != "Tess" {
		t.Fatalf("an unknown feed is probed at once: %+v", f)
	}
	if f, _ := svc.DueProbe(ctx, service.Busy{{Platform: "testplat", Kind: model.KindAttempt}: true}); f != nil {
		t.Fatal("busy platforms are skipped")
	}

	fp.Access["attempt/abc"] = model.AccessPrivate
	_, _ = svc.CheckFeedAccess(ctx, k)
	if due() != nil {
		t.Fatal("a private feed waits for the daily re-check")
	}
	clk.Advance(23 * time.Hour)
	if due() != nil {
		t.Fatal("not yet")
	}
	clk.Advance(2 * time.Hour)
	if due() == nil {
		t.Fatal("re-checked after 24h")
	}
	fp.Access["attempt/abc"] = model.AccessPublic
	_, _ = svc.CheckFeedAccess(ctx, k)
	if due() != nil {
		t.Fatal("a public feed is not probed")
	}

	_, _ = svc.SetFeedEnabled(ctx, k, false)
	_, _ = svc.SetFeedEnabled(ctx, k, true)
	fp.ProbeErr = errors.New("boom")
	_, _ = svc.CheckFeedAccess(ctx, k)
	if due() != nil {
		t.Fatal("a failed probe waits before retrying")
	}
	clk.Advance(6 * time.Minute)
	if due() == nil {
		t.Fatal("retried after 5 minutes")
	}
	_ = svc.SetIdentityEnabled(ctx, tess, "testplat", false)
	if due() != nil {
		t.Fatal("paused accounts are not probed")
	}
}

func TestOnlyAccessibleFeedsAreWorked(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	k := attemptKey(tess)
	_, _ = svc.SetFeedEnabled(ctx, k, true)
	_ = svc.MarkFeedPolled(ctx, service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore})
	testutil.UpsertFake(t, svc, tess, testutil.FakePlay(model.KindAttempt, "a1", "lb-a", testutil.T0.Add(-time.Hour), true))

	work := func() (*service.WorkFeed, *model.Score) {
		t.Helper()
		f, err := svc.DueFeed(ctx, time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		sc, err := svc.NextReplay(ctx, service.TierBackfillOther, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return f, sc
	}
	if f, sc := work(); f != nil || sc != nil {
		t.Fatalf("access unknown: no work (%+v, %+v)", f, sc)
	}
	if _, err := svc.CheckFeedAccess(ctx, k); err != nil {
		t.Fatal(err)
	}
	if f, sc := work(); f == nil || f.Key() != k || sc == nil || sc.ExternalID != "a1" {
		t.Fatalf("public: polled and downloaded (%+v, %+v)", f, sc)
	}
	if err := svc.MarkFeedPrivate(ctx, k); err != nil {
		t.Fatal(err)
	}
	if f, sc := work(); f != nil || sc != nil {
		t.Fatalf("private: no work (%+v, %+v)", f, sc)
	}
	if fs, _ := svc.Feeds(ctx, tess); fs[0].Access != model.AccessPrivate && fs[1].Access != model.AccessPrivate {
		t.Fatalf("feeds = %+v", fs)
	}
}
```

- [x] **Step 3: Run them to verify they fail**

Run: `go test ./internal/service/ -run 'FeedEnabled|CheckFeedAccess|DueProbe|AccessibleFeeds'`
Expected: FAIL — undefined: `SetFeedEnabled`, `CheckFeedAccess`, `DueProbe`, `MarkFeedPrivate`, `ErrFeedNotOptional`.

- [x] **Step 4: Share the feed join**

`internal/service/feeds.go` — replace `workFeedSQL`:

```go
// feedSelect joins enabled feeds to their enabled account and player; the
// account ID is what the platform is called with (never the player ID).
const feedSelect = "SELECT f.*, pp.external_id AS external_id, p.name AS player_name FROM sync_feeds f " +
	"JOIN player_platforms pp ON pp.player_id = f.player_id AND pp.platform = f.platform " +
	"JOIN players p ON p.id = f.player_id " +
	"WHERE f.enabled AND pp.enabled AND p.enabled"

// workFeedSQL selects the feeds the worker may read: no probe needed, or
// access public (spec §5.1).
const workFeedSQL = feedSelect + " AND f.access IN (?, ?)"
```

- [x] **Step 5: Implement access**

`internal/service/access.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// Access re-check timings (spec §5.1).
const (
	AccessRecheck   = 24 * time.Hour  // a private feed is probed again after this
	ProbeRetryDelay = 5 * time.Minute // an unknown feed whose probe failed waits this long
)

var ErrFeedNotOptional = errors.New("only optional feeds can be switched on or off")

// feedSpec finds a feed's declaration in the registry.
func (s *Service) feedSpec(platformName, kind string) (platform.Platform, platform.FeedSpec, error) {
	p, ok := s.reg.Get(platformName)
	if !ok {
		return platform.Platform{}, platform.FeedSpec{}, fmt.Errorf("%w: platform %s", ErrNotFound, platformName)
	}
	f, ok := p.Feed(kind)
	if !ok {
		return p, platform.FeedSpec{}, fmt.Errorf("%w: %s has no %s feed", ErrNotFound, p.DisplayName, kind)
	}
	return p, f, nil
}

func (s *Service) getFeed(ctx context.Context, k FeedKey) (*model.SyncFeed, error) {
	f := s.q.SyncFeed
	row, err := f.WithContext(ctx).Where(f.PlayerID.Eq(k.PlayerID), f.Platform.Eq(k.Platform), f.Feed.Eq(k.Kind)).First()
	if err != nil {
		return nil, notFound(err, "feed "+k.String())
	}
	return row, nil
}

func (s *Service) account(ctx context.Context, playerID, platformName string) (*model.PlayerPlatform, error) {
	pp := s.q.PlayerPlatform
	link, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).First()
	if err != nil {
		return nil, notFound(err, platformName+" account of player "+playerID)
	}
	return link, nil
}

// SetFeedEnabled switches an optional feed on or off (spec §4.3). Its row is
// created the first time it is switched on. Switching on (again) restarts
// its clock — plays from then on are "new" work — and, for a feed that needs
// access, the access check. Switching off keeps the cursor, rows and files.
func (s *Service) SetFeedEnabled(ctx context.Context, k FeedKey, enabled bool) (*model.SyncFeed, error) {
	p, spec, err := s.feedSpec(k.Platform, k.Kind)
	if err != nil {
		return nil, err
	}
	if !spec.Optional {
		return nil, ErrFeedNotOptional
	}
	if _, err := s.account(ctx, k.PlayerID, k.Platform); err != nil {
		return nil, err
	}
	now := s.Now()
	access := model.AccessNA
	if spec.NeedsAccess {
		access = model.AccessUnknown
	}
	cur, err := s.getFeed(ctx, k)
	switch {
	case errors.Is(err, ErrNotFound) && !enabled:
		off := newFeed(k.PlayerID, k.Platform, k.Kind, now, access) // nothing to store: it was never on
		off.Enabled = false
		return off, nil
	case errors.Is(err, ErrNotFound):
		if err := s.q.SyncFeed.WithContext(ctx).Create(newFeed(k.PlayerID, k.Platform, k.Kind, now, access)); err != nil {
			return nil, fmt.Errorf("service: create feed: %w", err)
		}
	case err != nil:
		return nil, err
	case enabled && !cur.Enabled:
		upd := map[string]any{"enabled": true, "started_at": now, "last_error": ""}
		if spec.NeedsAccess {
			upd["access"], upd["access_checked_at"] = model.AccessUnknown, nil
		}
		if err := s.updateFeed(ctx, k, upd); err != nil {
			return nil, err
		}
	case !enabled && cur.Enabled:
		if err := s.updateFeed(ctx, k, map[string]any{"enabled": false}); err != nil {
			return nil, err
		}
	}
	state := "off"
	if enabled {
		state = "on"
		s.Wake()
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(k.PlayerID), Platform: new(k.Platform), Feed: new(k.Kind),
		Message: fmt.Sprintf("archiving of %s %ss switched %s", p.DisplayName, k.Kind, state)})
	return s.getFeed(ctx, k)
}

// CheckFeedAccess probes whether this instance may read a feed and stores
// the result (spec §5.1): access, access_checked_at, and remote_total when
// public. A failed probe is recorded in last_error (except rate limiting,
// which records nothing) and returned along with the stored feed.
func (s *Service) CheckFeedAccess(ctx context.Context, k FeedKey) (*model.SyncFeed, error) {
	p, spec, err := s.feedSpec(k.Platform, k.Kind)
	if err != nil {
		return nil, err
	}
	cur, err := s.getFeed(ctx, k)
	if err != nil {
		return nil, err
	}
	if !spec.NeedsAccess {
		return cur, nil
	}
	link, err := s.account(ctx, k.PlayerID, k.Platform)
	if err != nil {
		return nil, err
	}
	access, total, perr := p.Adapter.ProbeAccess(ctx, k.Kind, link.ExternalID)
	if perr != nil {
		if ctx.Err() == nil && !errors.Is(perr, platform.ErrRateLimited) {
			if err := s.updateFeed(ctx, k, map[string]any{"access_checked_at": s.Now(), "last_error": "access check failed: " + perr.Error()}); err != nil {
				return cur, err
			}
		}
		f, err := s.getFeed(ctx, k)
		if err != nil {
			return cur, err
		}
		return f, fmt.Errorf("service: access check: %w", perr)
	}
	upd := map[string]any{"access": access, "access_checked_at": s.Now(), "last_error": ""}
	if access == model.AccessPublic {
		upd["remote_total"] = total
	}
	if err := s.updateFeed(ctx, k, upd); err != nil {
		return cur, err
	}
	if access != cur.Access {
		ev := model.SyncEvent{Kind: model.KindPoll, PlayerID: new(k.PlayerID), Platform: new(k.Platform), Feed: new(k.Kind)}
		switch access {
		case model.AccessPrivate:
			ev.Level, ev.Message = model.LevelWarn, accessDeniedMessage(p, spec)
			s.Log(ctx, ev)
		case model.AccessPublic:
			ev.Level, ev.Message = model.LevelInfo, fmt.Sprintf("%s %s history is public: archiving it", p.DisplayName, k.Kind)
			s.Log(ctx, ev)
			s.Wake()
		}
	}
	return s.getFeed(ctx, k)
}

// accessDeniedMessage is the warning logged when a feed turns private: its
// hint title when the platform gives one.
func accessDeniedMessage(p platform.Platform, spec platform.FeedSpec) string {
	if spec.AccessHint != nil && spec.AccessHint.Title != "" {
		return spec.AccessHint.Title
	}
	return fmt.Sprintf("%s refused access to the %s feed", p.DisplayName, spec.Kind)
}

// DueProbe returns the feed whose access check is most overdue, skipping
// busy platforms: unknown feeds at once (5 minutes after a failed probe),
// private ones every 24 hours (spec §5.1). nil when none is due.
func (s *Service) DueProbe(ctx context.Context, busy Busy) (*WorkFeed, error) {
	now := s.Now()
	var feeds []*WorkFeed
	err := s.db.WithContext(ctx).Raw(feedSelect+
		" AND ((f.access = ? AND (f.access_checked_at IS NULL OR f.access_checked_at <= ?))"+
		" OR (f.access = ? AND (f.access_checked_at IS NULL OR f.access_checked_at <= ?)))"+
		" ORDER BY f.access_checked_at, f.player_id, f.platform, f.feed", // SQLite sorts NULL first
		model.AccessUnknown, now.Add(-ProbeRetryDelay), model.AccessPrivate, now.Add(-AccessRecheck)).Scan(&feeds).Error
	if err != nil {
		return nil, fmt.Errorf("service: due probe: %w", err)
	}
	for _, f := range feeds {
		if !busy.has(f.Platform, f.Feed) {
			return f, nil
		}
	}
	return nil, nil
}

// MarkFeedPrivate records a feed that lost access mid-run (a 401/403 on a
// listing or a replay, spec §5.1): it stops until a probe says otherwise.
// Its rows, files and cursor are kept.
func (s *Service) MarkFeedPrivate(ctx context.Context, k FeedKey) error {
	return s.updateFeed(ctx, k, map[string]any{"access": model.AccessPrivate, "access_checked_at": s.Now()})
}
```

- [x] **Step 6: Run the tests**

Run: `go test -race ./internal/service/ ./internal/testutil/ ./internal/archiver/`
Expected: PASS.

- [x] **Step 7: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/service internal/testutil
git commit -m "feat(service): switch optional feeds on and off and check their access"
```

---
## Task 3: Worker — probe tier, access lost mid-run, tier order

The worker learns the rest of spec §5.1:

- **Tier 1** probes feeds whose access is unknown or due for a re-check, before any poll.
- **Access lost:** a 401/403 (`platform.ErrUnauthorized`) on a listing or a replay download flips a feed that needs access to `private`. It logs the hint title, keeps everything, and leaves the replay pending without counting a failed attempt.
- **Skipped pages:** a page whose plays were all skipped (`PlayPage.Skipped`) no longer ends a listing.

The existing tier order already puts optional backfill after every score tier; a test pins it.

**Files:**
- Modify: `internal/archiver/worker.go` (`Step`, `nextProbe`)
- Modify: `internal/archiver/poll.go` (`clientError`, empty-page rule, `needsAccess`, `accessLostMessage`)
- Modify: `internal/archiver/download.go` (unauthorized replay)
- Test: `internal/archiver/access_test.go` (new)

**Interfaces:**
- Consumes: Task 2 `DueProbe`, `CheckFeedAccess`, `MarkFeedPrivate`, `SetFeedEnabled`, fake scripting (`Access`, `FeedErr`, `ReplayErr`, `ReplayCalls`, `Probes`); Task 1 `PlayPage.Skipped`; Plan 2 `tpEnv` (`newTPEnv`, `drain`, `events`) from `internal/archiver/semantics_test.go`.
- Produces: worker behaviour only (no new exported names). Status text while probing: `"Checking access · {player}"`. Events: `warn`/`poll` with the hint title + `"; paused until access is back"` when access is lost; `warn`/`poll` `"access check failed: …"` when a probe errors.

- [x] **Step 1: Write the failing tests**

`internal/archiver/access_test.go`:

```go
package archiver_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// attempts returns n testplat attempts (newest first) one minute apart
// before T0, each with a replay.
func attempts(n int) []platform.Play {
	var out []platform.Play
	for i := range n {
		p := testutil.FakePlay(model.KindAttempt, fmt.Sprintf("a%02d", i), "lb-a", testutil.T0.Add(-time.Duration(i+1)*time.Minute), true)
		p.EndType = model.EndFail
		out = append(out, p)
	}
	return out
}

func (e *tpEnv) attemptFeed(t *testing.T, playerID string) model.SyncFeed {
	t.Helper()
	sum, err := e.svc.GetPlayerSummary(context.Background(), playerID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range sum.Identities {
		if id.Platform == "testplat" {
			return id.Feed(model.KindAttempt)
		}
	}
	t.Fatal("no testplat account")
	return model.SyncFeed{}
}

func (e *tpEnv) enableAttempts(t *testing.T, playerID string) service.FeedKey {
	t.Helper()
	k := service.FeedKey{PlayerID: playerID, Platform: "testplat", Kind: model.KindAttempt}
	if _, err := e.svc.SetFeedEnabled(context.Background(), k, true); err != nil {
		t.Fatal(err)
	}
	return k
}

func hasCall(calls []string, prefix string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func TestAccessProbeComesFirst(t *testing.T) {
	e := newTPEnv(t)
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(3)...)

	if did, err := e.w.Step(ctx); err != nil || !did {
		t.Fatalf("step = %v %v", did, err)
	}
	if len(e.fp.Probes) != 1 || len(e.fp.Calls) != 0 {
		t.Fatalf("the first unit of work is the probe: probes=%v calls=%v", e.fp.Probes, e.fp.Calls)
	}
	e.drain(t)
	if hasCall(e.fp.Calls, "attempt:") {
		t.Fatalf("a private feed is never listed: %v", e.fp.Calls)
	}
	if f := e.attemptFeed(t, tess); f.Access != model.AccessPrivate {
		t.Fatalf("feed = %+v", f)
	}
	if ev := e.events(t, model.KindPoll); !strings.Contains(ev, "warn: History is private") {
		t.Fatalf("events:\n%s", ev)
	}
}

func TestPrivateFeedRecheckedDaily(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(3)...)
	e.drain(t)

	e.clk.Advance(23 * time.Hour)
	e.drain(t)
	if len(e.fp.Probes) != 1 {
		t.Fatalf("no re-check before 24h: %v", e.fp.Probes)
	}
	e.fp.Access["attempt/abc"] = model.AccessPublic
	e.clk.Advance(2 * time.Hour)
	e.drain(t)
	if len(e.fp.Probes) != 2 {
		t.Fatalf("re-checked after 24h: %v", e.fp.Probes)
	}
	if c, _ := e.svc.GetPlayerSummary(context.Background(), tess); c.Identities[0].Counts[model.KindAttempt].Archived != 3 {
		t.Fatalf("once public, attempts are listed and archived: %+v", c.Identities[0].Counts)
	}
}

func TestAccessLostMidBackfill(t *testing.T) {
	e := newTPEnv(t)
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(14)...) // 7 pages: the first poll reads 5
	// Run until the attempt feed's first poll is done, then lose access.
	for range 50 {
		if f := e.attemptFeed(t, tess); f.LastPolledAt != nil {
			break
		}
		if _, err := e.w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f := e.attemptFeed(t, tess); f.BackfillPage != 6 {
		t.Fatalf("expected a backfill to resume at page 6: %+v", f)
	}
	e.fp.FeedErr["attempt/abc"] = fmt.Errorf("%w: scoresstats", platform.ErrUnauthorized)
	e.drain(t)
	f := e.attemptFeed(t, tess)
	if f.Access != model.AccessPrivate || f.BackfillPage != 6 {
		t.Fatalf("access lost: %+v", f)
	}
	if c, _ := e.svc.GetPlayerSummary(ctx, tess); c.Identities[0].Counts[model.KindAttempt].Scores != 10 {
		t.Fatalf("rows are kept: %+v", c.Identities[0].Counts)
	}
	if ev := e.events(t, model.KindPoll); !strings.Contains(ev, "warn: History is private; paused until access is back") {
		t.Fatalf("events:\n%s", ev)
	}

	// Access comes back: the backfill resumes where it stopped.
	delete(e.fp.FeedErr, "attempt/abc")
	e.clk.Advance(25 * time.Hour)
	e.drain(t)
	if f := e.attemptFeed(t, tess); f.Access != model.AccessPublic || f.BackfillState != model.BackfillDone {
		t.Fatalf("resumed: %+v", f)
	}
	if c, _ := e.svc.GetPlayerSummary(ctx, tess); c.Identities[0].Counts[model.KindAttempt].Archived != 14 {
		t.Fatalf("all attempts archived: %+v", c.Identities[0].Counts)
	}
}

func TestReplayUnauthorizedPausesFeed(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	plays := attempts(1)
	e.fp.SetPlays(model.KindAttempt, "abc", plays...)
	e.fp.ReplayErr[plays[0].ReplayURL] = fmt.Errorf("%w: otherreplays", platform.ErrUnauthorized)
	e.drain(t)
	if f := e.attemptFeed(t, tess); f.Access != model.AccessPrivate {
		t.Fatalf("a 401 on a replay flips the feed: %+v", f)
	}
	sc := testutil.Row(t, e.svc, tess, "a00")
	if sc.ReplayState != model.ReplayPending || sc.Attempts != 0 {
		t.Fatalf("the replay waits for access, no failed attempt counted: %+v", sc)
	}
}

func TestUnauthorizedOnARequiredFeedIsAnError(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.FeedErr["score/abc"] = fmt.Errorf("%w: scores", platform.ErrUnauthorized)
	e.drain(t)
	sum, _ := e.svc.GetPlayerSummary(context.Background(), tess)
	if f := sum.Identities[0].Feed(model.KindScore); f.Access != model.AccessNA || !strings.Contains(f.LastError, "unauthorized") {
		t.Fatalf("feeds without NeedsAccess are never made private: %+v", f)
	}
}

func TestAttemptWorkWaitsForScoreWork(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.enableAttempts(t, tess)
	var scores []platform.Play
	for i := range 12 { // 6 pages: page 6 is backfill work
		scores = append(scores, testutil.FakePlay(model.KindScore, fmt.Sprintf("s%02d", i), fmt.Sprintf("lb-%d", i), testutil.T0.Add(-time.Duration(i+1)*time.Hour), true))
	}
	e.fp.SetPlays(model.KindScore, "abc", scores...)
	e.fp.SetPlays(model.KindAttempt, "abc", attempts(12)...)
	e.drain(t)

	idx := func(calls []string, match func(string) bool, last bool) int {
		i := -1
		for j, c := range calls {
			if match(c) {
				if !last {
					return j
				}
				i = j
			}
		}
		return i
	}
	isScoreReplay := func(u string) bool { return strings.Contains(u, "/s") }
	isAttemptReplay := func(u string) bool { return strings.Contains(u, "/a") }
	if lastScore, firstAttempt := idx(e.fp.ReplayCalls, isScoreReplay, true), idx(e.fp.ReplayCalls, isAttemptReplay, false); lastScore < 0 || firstAttempt < lastScore {
		t.Fatalf("every score replay before any attempt replay: %v", e.fp.ReplayCalls)
	}
	if s6, a6 := slices.Index(e.fp.Calls, "score:abc:6"), slices.Index(e.fp.Calls, "attempt:abc:6"); s6 < 0 || a6 < s6 {
		t.Fatalf("score backfill listing before attempt backfill listing: %v", e.fp.Calls)
	}
}
```

(`FakePlay` URLs are `https://tp.example/replays/{id}.tpr`, so score replays contain `/s` and attempt replays `/a`.)

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/archiver/ -run 'AccessProbe|RecheckedDaily|AccessLost|ReplayUnauthorized|RequiredFeed|WaitsForScoreWork'`
Expected: FAIL — no probe (fp.Probes empty), 401s logged as generic errors, feed never private.

- [x] **Step 3: Probe tier**

`internal/archiver/worker.go` — in `Step`, right after computing `feedBusy, replayBusy, _ := w.busy()`:

```go
	if did, err := w.nextProbe(ctx, feedBusy); did || err != nil {
		return did, err
	}
```

and add:

```go
// nextProbe checks the access of one feed that needs it (spec §5.1, tier 1).
func (w *Worker) nextProbe(ctx context.Context, busy service.Busy) (bool, error) {
	wf, err := w.svc.DueProbe(ctx, busy)
	if err != nil || wf == nil {
		return false, err
	}
	w.setStatus(StateRunning, "Checking access · "+wf.PlayerName)
	if _, err := w.svc.CheckFeedAccess(ctx, wf.Key()); err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		msg := "access check failed: " + err.Error()
		if errors.Is(err, platform.ErrRateLimited) {
			msg = "rate limited during an access check; retrying when the limit resets"
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindPoll, msg))
	}
	return true, nil
}
```

(`worker.go` imports `errors` for this.) A rate-limited probe records nothing, so the next step would pick it again — but its limiter is then not ready, so `busy` skips it; fakes without a limiter never return `ErrRateLimited` here.

- [x] **Step 4: Access lost on listings, and the empty-page rule**

`internal/archiver/poll.go`:

```go
// needsAccess reports whether a feed's access is probed (only those can turn private).
func needsAccess(p platform.Platform, kind string) bool {
	f, ok := p.Feed(kind)
	return ok && f.NeedsAccess
}

// accessLostMessage is logged when a feed turns private mid-run.
func accessLostMessage(p platform.Platform, kind string) string {
	if f, ok := p.Feed(kind); ok && f.AccessHint != nil && f.AccessHint.Title != "" {
		return f.AccessHint.Title + "; paused until access is back"
	}
	return p.DisplayName + " refused access to the " + kind + " feed; paused until access is back"
}
```

In `clientError`, add a case before `ErrNotFound`:

```go
	case errors.Is(err, platform.ErrUnauthorized) && needsAccess(p, wf.Feed):
		if err := w.svc.MarkFeedPrivate(ctx, k); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindPoll, accessLostMessage(p, wf.Feed)))
		return nil
```

A page counts as empty only when the adapter skipped nothing. In `poll`, the end condition becomes:

```go
		if res.Known > 0 || (len(pg.Plays) == 0 && pg.Skipped == 0) || page >= pg.TotalPages {
```

and in `backfill`:

```go
	if (len(pg.Plays) == 0 && pg.Skipped == 0) || page >= total {
```

- [x] **Step 5: Access lost on replays**

`internal/archiver/download.go` — in the error `switch` after the download, before the `ErrNotFound` case:

```go
	case errors.Is(err, platform.ErrUnauthorized) && needsAccess(p, sc.Kind):
		if err := w.svc.MarkFeedPrivate(ctx, service.FeedKey{PlayerID: sc.PlayerID, Platform: sc.Platform, Kind: sc.Kind}); err != nil {
			return err
		}
		ev(model.LevelWarn, accessLostMessage(p, sc.Kind)) // the replay stays pending, no attempt is counted
		return nil
```

- [x] **Step 6: Run the tests**

Run: `go test -race ./internal/archiver/`
Expected: PASS — including every existing archiver test (ScoreSaber and the fake's score feed have no `NeedsAccess`, so a 401 there stays an ordinary poll error).

- [x] **Step 7: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/archiver
git commit -m "feat(archiver): probe feed access first and pause feeds that lose it"
```

---
## Task 4: `type` filter (service, player page, API)

Spec §6.2: `type` is a comma list of `complete` (default), `fail`, `quit`, `restart`, `practice`, `all`. `complete` = score rows plus `clear` attempts. The default keeps every existing listing, count and page exactly as it was. The filter lives in the shared SQL builder (Plan 2 `ScoreFilter.where`), so the row listing, the merged map view and the map fragment agree. The player page gets a type select once a player has attempts; the API gets the `type` parameter.

**Files:**
- Modify: `internal/service/scores.go` (`ScoreFilter.Types`, `TypeComplete`, `TypeAll`, `PlayTypes`, `ParseTypes`, `typeCond`, `ErrInvalidFilter`)
- Modify: `internal/testutil/service.go` (`Row` lists every type)
- Modify: `internal/web/public.go` (`scoreFilter` reads `type`), `internal/web/views/urls.go` (`scoreQuery` writes it), `internal/web/views/player.templ` (type select; `PlayerView.HasAttempts`)
- Modify: `internal/api/scores.go` (`type` param)
- Test: `internal/service/types_test.go` (new), `internal/web/public_test.go`, `internal/web/views/urls_test.go`, `internal/api/api_test.go`

**Interfaces:**
- Consumes: Plan 2 `ScoreFilter.where`, `scoreFilter(q, playerID, reg)`, `scoreQuery(f)`, `PlayerView`, `ListScoresInput`.
- Produces:
  - `service.ScoreFilter.Types []string` (nil = `complete`)
  - `service.TypeComplete = "complete"`, `service.TypeAll = "all"`, `service.PlayTypes = []string{"complete", "fail", "quit", "restart", "practice", "all"}`
  - `func service.ParseTypes(s string) ([]string, error)` (`""` → nil; unknown → `ErrInvalidFilter`)
  - `views.PlayerView.HasAttempts bool`; page/fragment query param `type`
  - API `GET /players/{id}/scores?type=fail,quit`

- [x] **Step 1: Write the failing tests**

`internal/service/types_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// typesFixture: Tess (testplat) with one score and one attempt of each end
// type except unknown, each on its own map; the fail shares the score's map.
func typesFixture(t *testing.T) (*service.Service, string) {
	t.Helper()
	svc, _, _ := testutil.NewMultiService(t)
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	plays := []platform.Play{testutil.FakePlay(model.KindScore, "s1", "lb-s", testutil.T0, true)}
	for i, end := range []string{model.EndClear, model.EndFail, model.EndQuit, model.EndRestart, model.EndPractice} {
		lb := "lb-" + end
		if end == model.EndFail {
			lb = "lb-s"
		}
		p := testutil.FakePlay(model.KindAttempt, "a-"+end, lb, testutil.T0.Add(-time.Duration(i+1)*time.Minute), true)
		p.EndType = end
		plays = append(plays, p)
	}
	testutil.UpsertFake(t, svc, tess, plays...)
	return svc, tess
}

func TestTypeFilter(t *testing.T) {
	svc, tess := typesFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		types []string
		want  int64
	}{
		{nil, 2}, // the score and the clear attempt
		{[]string{service.TypeComplete}, 2},
		{[]string{model.EndFail}, 1},
		{[]string{model.EndFail, model.EndQuit}, 2},
		{[]string{service.TypeComplete, model.EndPractice}, 3},
		{[]string{service.TypeAll}, 6},
		{[]string{model.EndRestart, service.TypeAll}, 6},
	} {
		l, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: tess, Types: c.types})
		if err != nil || l.Total != c.want {
			t.Errorf("types %v: total %d, want %d (%v)", c.types, l.Total, c.want, err)
		}
	}
	groups, _ := svc.ListMapGroups(ctx, service.ScoreFilter{PlayerID: tess})
	if groups.Total != 2 { // lb-s (score) and lb-clear
		t.Fatalf("by default the merged view hides maps that only have non-clear attempts: %d", groups.Total)
	}
	fails, _ := svc.ListMapGroups(ctx, service.ScoreFilter{PlayerID: tess, Types: []string{model.EndFail}})
	if fails.Total != 1 || len(fails.Groups[0].Chips) != 1 || fails.Groups[0].Latest.ExternalID != "a-fail" {
		t.Fatalf("type=fail lists the map with its PB chip and the fail as newest play: %+v", fails.Groups)
	}
}

func TestParseTypes(t *testing.T) {
	if got, err := service.ParseTypes(""); err != nil || got != nil {
		t.Fatalf("empty = %v %v", got, err)
	}
	if got, err := service.ParseTypes(" fail, quit,fail "); err != nil || len(got) != 2 || got[0] != "fail" || got[1] != "quit" {
		t.Fatalf("list = %v %v", got, err)
	}
	if _, err := service.ParseTypes("fail,bogus"); !errors.Is(err, service.ErrInvalidFilter) {
		t.Fatalf("unknown type = %v", err)
	}
}

func TestDefaultViewsHideAttempts(t *testing.T) {
	svc, tess := typesFixture(t)
	ctx := context.Background()
	sum, _ := svc.GetPlayerSummary(ctx, tess)
	if sum.Counts.Scores != 1 || sum.Identities[0].Counts[model.KindAttempt].Scores != 5 {
		t.Fatalf("headline counts are score rows; attempts are counted per feed: %+v / %+v", sum.Counts, sum.Identities[0].Counts)
	}
	if row := testutil.Row(t, svc, tess, "a-quit"); row.Kind != model.KindAttempt {
		t.Fatal("testutil.Row finds rows of every type")
	}
}
```

Append to `internal/web/public_test.go`:

```go
func TestPlayerPageTypeFilter(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	tess := e.seedTP() // t1 and t2 scores, attempt a1 (a clear) on t1's map
	fail := testutil.FakePlay(model.KindAttempt, "a2", "lb-c", testutil.T0.Add(-time.Hour), true)
	fail.EndType = model.EndFail
	testutil.UpsertFake(t, e.svc, tess, fail)

	page := e.do(http.MethodGet, "/p/"+tess, nil).Body.String()
	contains(t, page, `name="type"`, "Completed", "Fails", "Everything")
	if strings.Contains(page, "Song lb-c") {
		t.Fatal("a map with only a failed attempt is hidden by default")
	}
	fails := e.do(http.MethodGet, "/p/"+tess+"?type=fail", nil, htmx("scores")).Body.String()
	if !strings.Contains(fails, "Song lb-c") || strings.Contains(fails, "Song lb-b") {
		t.Fatalf("type=fail:\n%s", fails)
	}
	if bogus := e.do(http.MethodGet, "/p/"+tess+"?type=bogus", nil, htmx("scores")).Body.String(); strings.Contains(bogus, "Song lb-c") {
		t.Fatal("an invalid type falls back to the default")
	}
	alice := e.seed()
	if strings.Contains(e.do(http.MethodGet, "/p/"+alice, nil).Body.String(), `name="type"`) {
		t.Fatal("players without attempts get no type select")
	}
}
```

Append to `TestMapPlaysURL` in `internal/web/views/urls_test.go`:

```go
	if got := PlayerScoresURL("p1", service.ScoreFilter{Types: []string{"fail", "quit"}}, 1); got != "/p/p1?type=fail%2Cquit" {
		t.Errorf("PlayerScoresURL(types) = %s", got)
	}
	if got := PlayerScoresURL("p1", service.ScoreFilter{Types: []string{service.TypeComplete}}, 1); got != "/p/p1" {
		t.Errorf("the default type is not encoded: %s", got)
	}
```

In `internal/api/api_test.go`, append:

```go
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
```

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/service/ ./internal/web/... ./internal/api/`
Expected: FAIL — undefined: `ScoreFilter.Types`, `TypeComplete`, `TypeAll`, `ParseTypes`, `ErrInvalidFilter`; attempts appear in default listings.

- [x] **Step 3: Service**

`internal/service/scores.go` — add `Types []string // play types; nil = complete (spec §6.2)` to `ScoreFilter` (after `MapKey`), and:

```go
// Play types of ScoreFilter.Types and the API's type parameter (spec §6.2).
const (
	TypeComplete = "complete" // scores, plus attempts that cleared the map
	TypeAll      = "all"
)

// PlayTypes are the accepted play types, in display order.
var PlayTypes = []string{TypeComplete, model.EndFail, model.EndQuit, model.EndRestart, model.EndPractice, TypeAll}

var ErrInvalidFilter = errors.New("invalid filter")

// ParseTypes reads a comma list of play types; "" is nil (complete).
func ParseTypes(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if !slices.Contains(PlayTypes, t) {
			return nil, fmt.Errorf("%w: unknown type %q", ErrInvalidFilter, t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// typeCond is the SQL condition of a type filter ("" for all).
func typeCond(types []string) (string, []any) {
	if len(types) == 0 {
		types = []string{TypeComplete}
	}
	if slices.Contains(types, TypeAll) {
		return "", nil
	}
	var parts []string
	var args []any
	for _, t := range types {
		if t == TypeComplete {
			parts = append(parts, "(s.kind = ? OR (s.kind = ? AND s.end_type = ?))")
			args = append(args, model.KindScore, model.KindAttempt, model.EndClear)
			continue
		}
		parts = append(parts, "(s.kind = ? AND s.end_type = ?)")
		args = append(args, model.KindAttempt, t)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}
```

and in `where`, before `return`:

```go
	if cond, targs := typeCond(f.Types); cond != "" {
		add(cond, targs...)
	}
```

(`scores.go` imports `errors` for `ErrInvalidFilter`.) `ListMapGroups`' chips query is separate and still lists only `kind=score` PBs: chips never show attempts.

`internal/testutil/service.go` — `Row` must see every type:

```go
	list, err := svc.ListScores(context.Background(), service.ScoreFilter{PlayerID: playerID, PerPage: 100, Types: []string{service.TypeAll}})
```

- [x] **Step 4: Player page**

`internal/web/public.go` — in `scoreFilter`, after the score bounds:

```go
	if types, err := service.ParseTypes(q.Get("type")); err == nil {
		f.Types = types
	}
```

In `player`, set `HasAttempts` on the view:

```go
	v.HasAttempts = hasAttempts(sum)
```

```go
// hasAttempts reports whether a player has any stored attempt.
func hasAttempts(sum service.PlayerSummary) bool {
	for _, id := range sum.Identities {
		if id.Counts[model.KindAttempt].Scores > 0 {
			return true
		}
	}
	return false
}
```

`internal/web/views/urls.go` — in `scoreQuery`, before `return q`:

```go
	if len(f.Types) > 0 && !(len(f.Types) == 1 && f.Types[0] == service.TypeComplete) {
		q.Set("type", strings.Join(f.Types, ","))
	}
```

`internal/web/views/player.templ` — add `HasAttempts bool // show the type select` to `PlayerView`, a label helper in the Go section:

```go
// typeLabel names a play type in the type select.
func typeLabel(t string) string {
	switch t {
	case service.TypeComplete:
		return "Completed"
	case model.EndFail:
		return "Fails"
	case model.EndQuit:
		return "Quits"
	case model.EndRestart:
		return "Restarts"
	case model.EndPractice:
		return "Practice"
	}
	return "Everything"
}

func typeSelected(f service.ScoreFilter, t string) bool {
	if len(f.Types) == 0 {
		return t == service.TypeComplete
	}
	return len(f.Types) == 1 && f.Types[0] == t
}
```

and in `scoreFilters`, after the platform select:

```templ
		if v.HasAttempts {
			@nativeselect.NativeSelect(nativeselect.Props{Name: "type", Attributes: templ.Attributes{"aria-label": "Play type"}}) {
				for _, t := range service.PlayTypes {
					@nativeselect.Option(nativeselect.OptionProps{Value: t, Selected: typeSelected(v.Filter, t)}) {
						{ typeLabel(t) }
					}
				}
			}
		}
```

- [x] **Step 5: API**

`internal/api/scores.go` — add to `ListScoresInput`:

```go
	Type string `query:"type" pattern:"^(complete|fail|quit|restart|practice|all)(,(complete|fail|quit|restart|practice|all))*$" doc:"Comma list: complete (default: scores and cleared attempts), fail, quit, restart, practice, all"`
```

and in the handler, after building `f`:

```go
		types, err := service.ParseTypes(in.Type)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		f.Types = types
```

- [x] **Step 6: Run the tests**

Run: `make generate && go test -race ./internal/service/ ./internal/web/... ./internal/api/ ./internal/archiver/`
Expected: PASS — including Plan 2's listing and page tests (no attempts there, or attempts that the default hides on purpose).

- [x] **Step 7: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/service internal/testutil internal/web internal/api
git commit -m "feat: filter plays by type (complete by default, fails, quits, restarts, practice, all)"
```

---
## Task 5: API — optional feed switch, access check, feed DTO

Spec §6.2: `PATCH /players/{id}/identities/{platform}/feeds/{kind}` switches an optional feed (422 for required feeds). Switching it on probes access at once and answers with the feed — `access`, `remote_total`, and the `hint` while private. `POST …/feeds/{kind}/check` re-probes ("Check again"). Every feed in the player DTO gains `optional`, `access_checked_at`, `remote_total`, `hint` (only while private) and per-feed `counts` (spec §6.2: counts of other kinds are reported per feed).

**Files:**
- Modify: `internal/api/dto.go` (`Hint`, `Feed` fields, `feedDTO`, `playerDTO`)
- Modify: `internal/api/api.go` (`ErrFeedNotOptional` → 422; register feeds)
- Create: `internal/api/feeds.go`
- Test: `internal/api/feeds_test.go` (new), `internal/api/api_test.go`

**Interfaces:**
- Consumes: Task 2 `SetFeedEnabled`, `CheckFeedAccess`, `ErrFeedNotOptional`; Task 1 `platform.Hint`; Plan 2 `IdentityPath`, `playerID`, `platformName`, `replayCounts`, `apiHandler`, `Identity.Counts`.
- Produces:
  - `PATCH /api/v1/players/{id}/identities/{platform}/feeds/{kind}` (`update-feed`, body `{enabled}`) → `Feed`; `POST …/feeds/{kind}/check` (`check-feed`) → `Feed`
  - `api.Feed{Kind, Optional, Enabled, Access, AccessCheckedAt, Hint *Hint, RemoteTotal, StartedAt, LastPolledAt, LastError, Backfill, Counts}`; `api.Hint{Title, Intro, Steps, LinkText, LinkURL, Note}`
  - test helper `newFakeAPI(t, admin) (*service.Service, *testutil.FakePlatform, http.Handler)`

- [x] **Step 1: Write the failing tests**

`internal/api/feeds_test.go`:

```go
package api_test

import (
	"net/http"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func newFakeAPI(t *testing.T, admin bool) (*service.Service, *testutil.FakePlatform, http.Handler) {
	t.Helper()
	svc, fp, _ := testutil.NewMultiService(t)
	return svc, fp, apiHandler(svc, admin)
}

func feedOf(t *testing.T, player map[string]any, platformName, kind string) map[string]any {
	t.Helper()
	for _, id := range player["identities"].([]any) {
		idm := id.(map[string]any)
		if idm["platform"] != platformName {
			continue
		}
		for _, f := range idm["feeds"].([]any) {
			if fm := f.(map[string]any); fm["kind"] == kind {
				return fm
			}
		}
	}
	t.Fatalf("no %s %s feed in %v", platformName, kind, player)
	return nil
}

func TestFeedEndpoints(t *testing.T) {
	svc, fp, h := newFakeAPI(t, true)
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	base := "/api/v1/players/" + tess + "/identities/testplat/feeds/"

	fp.Access["attempt/abc"] = model.AccessPrivate
	code, f, _ := call(t, h, http.MethodPatch, base+"attempt", map[string]any{"enabled": true})
	hint, _ := f["hint"].(map[string]any)
	if code != 200 || f["kind"] != "attempt" || f["optional"] != true || f["enabled"] != true || f["access"] != "private" ||
		f["access_checked_at"] == nil || hint["title"] != "History is private" || len(hint["steps"].([]any)) != 2 ||
		hint["link_url"] != "https://tp.example/settings" {
		t.Fatalf("switch on, private = %d %v", code, f)
	}

	fp.Access["attempt/abc"] = model.AccessPublic
	fp.AccessTotal = 7
	code, f, _ = call(t, h, http.MethodPost, base+"attempt/check", nil)
	if code != 200 || f["access"] != "public" || f["remote_total"].(float64) != 7 || f["hint"] != nil {
		t.Fatalf("check again = %d %v", code, f)
	}

	_, p, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess, nil)
	if af := feedOf(t, p, "testplat", "attempt"); af["optional"] != true || af["counts"] == nil {
		t.Fatalf("player DTO feed = %v", af)
	}
	if sf := feedOf(t, p, "testplat", "score"); sf["optional"] != false || sf["access"] != "n/a" {
		t.Fatalf("required feed = %v", sf)
	}

	if code, _, _ := call(t, h, http.MethodPatch, base+"score", map[string]any{"enabled": false}); code != 422 {
		t.Fatalf("required feeds cannot be switched: %d", code)
	}
	if code, _, _ := call(t, h, http.MethodPatch, "/api/v1/players/"+tess+"/identities/scoresaber/feeds/attempt", map[string]any{"enabled": true}); code != 404 {
		t.Fatalf("no such feed: %d", code)
	}
	if code, _, _ := call(t, h, http.MethodPatch, base+"bogus", map[string]any{"enabled": true}); code != 422 {
		t.Fatalf("kind is an enum: %d", code)
	}
	code, f, _ = call(t, h, http.MethodPatch, base+"attempt", map[string]any{"enabled": false})
	if code != 200 || f["enabled"] != false || f["access"] != "public" {
		t.Fatalf("switch off = %d %v", code, f)
	}
	alice := testutil.AddPlayer(t, svc, "1001")
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players/"+alice+"/identities/testplat/feeds/attempt/check", nil); code != 404 {
		t.Fatalf("check without an account: %d", code)
	}
}
```

In `TestAdminRequiresSession`, add:

```go
		{http.MethodPatch, "/api/v1/players/1001/identities/scoresaber/feeds/attempt"},
		{http.MethodPost, "/api/v1/players/1001/identities/scoresaber/feeds/attempt/check"},
```

and `"update-feed"`, `"check-feed"` to `TestOpenAPIDocument`'s list.

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/api/`
Expected: FAIL — 404/405 on the feed routes; no `optional` in the DTO.

- [x] **Step 3: DTO**

`internal/api/dto.go`:

```go
// Hint is what a player must do to grant access to a feed (spec §6.3).
type Hint struct {
	Title    string   `json:"title"`
	Intro    string   `json:"intro,omitempty"`
	Steps    []string `json:"steps"`
	LinkText string   `json:"link_text,omitempty"`
	LinkURL  string   `json:"link_url,omitempty"`
	Note     string   `json:"note,omitempty"`
}

type Feed struct {
	Kind            string       `json:"kind" enum:"score,attempt"`
	Optional        bool         `json:"optional" doc:"Switched on and off by the admin (PATCH …/feeds/{kind})"`
	Enabled         bool         `json:"enabled"`
	Access          string       `json:"access" enum:"n/a,unknown,public,private"`
	AccessCheckedAt *time.Time   `json:"access_checked_at,omitempty"`
	Hint            *Hint        `json:"hint,omitempty" doc:"What the player must change; only while access is private"`
	RemoteTotal     int64        `json:"remote_total" doc:"Items the platform reported at the last access check"`
	StartedAt       time.Time    `json:"started_at"`
	LastPolledAt    *time.Time   `json:"last_polled_at,omitempty"`
	LastError       string       `json:"last_error,omitempty"`
	Backfill        Backfill     `json:"backfill"`
	Counts          ReplayCounts `json:"counts" doc:"This feed's rows"`
}

func hintDTO(h *platform.Hint) *Hint {
	if h == nil {
		return nil
	}
	return &Hint{Title: h.Title, Intro: h.Intro, Steps: h.Steps, LinkText: h.LinkText, LinkURL: h.LinkURL, Note: h.Note}
}

// feedDTO describes one feed of an account of platform p.
func feedDTO(p platform.Platform, f model.SyncFeed, c service.Counts) Feed {
	spec, _ := p.Feed(f.Feed)
	out := Feed{
		Kind: f.Feed, Optional: spec.Optional, Enabled: f.Enabled, Access: f.Access, AccessCheckedAt: f.AccessCheckedAt,
		RemoteTotal: f.RemoteTotal, StartedAt: f.StartedAt, LastPolledAt: f.LastPolledAt, LastError: f.LastError,
		Backfill: Backfill{State: f.BackfillState, NextPage: f.BackfillPage, TotalPages: f.BackfillTotalPages},
		Counts:   replayCounts(c),
	}
	if f.Access == model.AccessPrivate {
		out.Hint = hintDTO(spec.AccessHint)
	}
	return out
}
```

In `playerDTO`, build feeds with it:

```go
		pl, _ := reg.Get(id.Platform)
		if pl.ProfileURL != nil {
			dto.ProfileURL = pl.ProfileURL(id.ExternalID)
		}
		for _, f := range id.Feeds {
			dto.Feeds = append(dto.Feeds, feedDTO(pl, f, id.Counts[f.Feed]))
		}
```

- [x] **Step 4: Endpoints**

`internal/api/api.go` — `mapErr` gains `case errors.Is(err, service.ErrFeedNotOptional): return huma.Error422UnprocessableEntity(err.Error())` (before the `ErrNotFound` case); `Register` calls `a.registerFeeds()` after `a.registerIdentities()`.

`internal/api/feeds.go`:

```go
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type FeedPath struct {
	IdentityPath
	Kind string `path:"kind" enum:"score,attempt"`
}

type UpdateFeedInput struct {
	FeedPath
	Body struct {
		Enabled bool `json:"enabled" doc:"Archive this feed"`
	}
}

type FeedOutput struct{ Body Feed }

func (a *API) registerFeeds() {
	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "update-feed", Method: http.MethodPatch, Path: "/api/v1/players/{id}/identities/{platform}/feeds/{kind}",
		Summary: "Switch an optional feed on or off", Tags: []string{"Players"},
		Description: "Only optional feeds (BeatLeader attempts). Switching one on checks access at once: a private feed " +
			"comes back with `access: private` and a `hint` saying what the player must change. 422 for required feeds.",
	}), func(ctx context.Context, in *UpdateFeedInput) (*FeedOutput, error) {
		k, err := a.feedKey(ctx, in.FeedPath)
		if err != nil {
			return nil, err
		}
		f, err := a.svc.SetFeedEnabled(ctx, k, in.Body.Enabled)
		if err != nil {
			return nil, mapErr(err)
		}
		if in.Body.Enabled {
			// A failed probe is recorded on the feed (last_error); the response shows it.
			if checked, err := a.svc.CheckFeedAccess(ctx, k); checked != nil {
				f = checked
			} else if err != nil {
				return nil, mapErr(err)
			}
		}
		return a.feedOutput(ctx, k, f)
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "check-feed", Method: http.MethodPost, Path: "/api/v1/players/{id}/identities/{platform}/feeds/{kind}/check",
		Summary: "Check a feed's access again", Tags: []string{"Players"},
		Description: "Probes the platform now (the worker also re-checks private feeds every 24 hours).",
	}), func(ctx context.Context, in *FeedPath) (*FeedOutput, error) {
		k, err := a.feedKey(ctx, *in)
		if err != nil {
			return nil, err
		}
		f, err := a.svc.CheckFeedAccess(ctx, k)
		if f == nil {
			return nil, mapErr(err)
		}
		return a.feedOutput(ctx, k, f)
	})
}

func (a *API) feedKey(ctx context.Context, in FeedPath) (service.FeedKey, error) {
	id, err := a.playerID(ctx, in.ID)
	if err != nil {
		return service.FeedKey{}, err
	}
	if _, err := a.platformName(in.Platform); err != nil {
		return service.FeedKey{}, err
	}
	return service.FeedKey{PlayerID: id, Platform: in.Platform, Kind: in.Kind}, nil
}

// feedOutput answers with the feed as the player DTO shows it (with counts);
// a feed switched off before it ever existed has no row, so f stands in.
func (a *API) feedOutput(ctx context.Context, k service.FeedKey, f *model.SyncFeed) (*FeedOutput, error) {
	sum, err := a.svc.GetPlayerSummary(ctx, k.PlayerID)
	if err != nil {
		return nil, mapErr(err)
	}
	p, _ := a.svc.Platforms().Get(k.Platform)
	for _, id := range sum.Identities {
		if id.Platform != k.Platform {
			continue
		}
		for _, fd := range id.Feeds {
			if fd.Feed == k.Kind {
				return &FeedOutput{Body: feedDTO(p, fd, id.Counts[fd.Feed])}, nil
			}
		}
	}
	if f == nil {
		return nil, mapErr(errors.Join(service.ErrNotFound, errors.New("feed "+k.String())))
	}
	return &FeedOutput{Body: feedDTO(p, *f, service.Counts{})}, nil
}
```

- [x] **Step 5: Run, lint, commit**

Run: `go test -race ./internal/api/ && go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/api
git commit -m "feat(api): switch optional feeds, check access, report hints and per-feed counts"
```

---
## Task 6: Admin and sync UI — attempts switch, access hint, check again

Spec §6.3 in the admin UI:

- each account's badge menu gets one button per optional feed ("Archive attempts" / "Stop archiving attempts"). Switching on confirms first, with a storage warning and the last known `remote_total`;
- switching on probes at once. While a feed is private, the account badge turns destructive with a warning icon and the row shows the **access hint** (the platform's title, intro, steps, settings link, note) with a **Check again** button;
- on **Sync**, a private feed's queue row says "Private", shows the hint title, the settings link and **Check again**; a feed being probed says "Checking access"; a switched-off feed says "(off)";
- the hint never appears on public pages.

**Files:**
- Create: `internal/web/views/access.go` (`FeedState`, `OptionalFeeds`, `FeedSpecOf`, `FeedURL`, `FeedName`, `feedConfirm`, `feedStatus`, `hasPrivateFeed`), `internal/web/views/access.templ` (`AccessHint`)
- Modify: `internal/web/views/admin.templ` (badge state, feed buttons, hint; `PlayerRowToast` takes a toast type)
- Modify: `internal/web/views/sync.templ` (queue row access state; `QueueRow.active`)
- Modify: `internal/web/admin.go` (`renderRow` takes a toast type, `setFeedEnabled`, `checkFeedAccess`, `accessToast`), `internal/web/web.go` (two routes)
- Test: `internal/web/attempts_admin_test.go` (new)

**Interfaces:**
- Consumes: Task 2 `SetFeedEnabled`, `CheckFeedAccess`, `ErrFeedNotOptional`, fake `Access`/`AccessTotal`; Plan 2 `AdminPlayerRow(pl, v)`, `identityMenu`, `identityVariant`, `identityURL`, `menuClass`, `renderRow`, `QueueRow`, `feedLabel`, `newEnvWith`.
- Produces:
  - routes `POST /admin/players/{id}/identities/{platform}/feeds/{kind}` (form `enabled`) and `POST /admin/players/{id}/identities/{platform}/feeds/{kind}/check` (`?from=sync` answers with a toast only)
  - `views.AccessHint(h *platform.Hint, check templ.Attributes)`; `views.FeedState{Spec, Feed, Counts}` + `On()`, `Private()`; `views.OptionalFeeds(ctx, service.Identity) []FeedState`; `views.FeedSpecOf(ctx, platformName, kind) platform.FeedSpec`; `views.FeedURL(playerID, platformName, kind string) string`; `views.FeedName(kind) string` (`"attempts"`)
  - `views.PlayerRowToast(pl, v, t toast.Type, title string)`; `(*Handler).renderRow(w, r, id string, t toast.Type, title string)`
  - toasts: `"Archiving attempts"`, `"Stopped archiving attempts"`, `"Access is private"` (warning), `"Access granted"`, `"Could not check access"` (error), `"Could not change feed"` (error)

- [x] **Step 1: Write the failing tests**

`internal/web/attempts_admin_test.go`:

```go
package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

const feedPath = "/identities/testplat/feeds/attempt"

func TestAdminAttemptsSwitch(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	row := "player-" + tess
	base := "/admin/players/" + tess + feedPath

	page := e.do(http.MethodGet, "/admin", nil, withCookie(c)).Body.String()
	contains(t, page, "Archive attempts", "several GB", `hx-post="`+base+`"`)

	on := e.do(http.MethodPost, base, url.Values{"enabled": {"true"}}, withCookie(c), htmx(row)).Body.String()
	contains(t, on, `id="player-`+tess+`"`, "Access is private", "History is private", "Make history public",
		"Open settings", `href="https://tp.example/settings"`, "Check again", `hx-post="`+base+`/check"`, "Stop archiving attempts")

	e.fp.Access["attempt/abc"] = model.AccessPublic
	e.fp.AccessTotal = 9
	ok := e.do(http.MethodPost, base+"/check", nil, withCookie(c), htmx(row)).Body.String()
	contains(t, ok, "Access granted", "attempts: public")
	if strings.Contains(ok, "Check again") {
		t.Fatal("the hint goes away once access is public")
	}

	off := e.do(http.MethodPost, base, url.Values{"enabled": {"false"}}, withCookie(c), htmx(row)).Body.String()
	contains(t, off, "Stopped archiving attempts", "Archive attempts")
	contains(t, e.do(http.MethodGet, "/admin", nil, withCookie(c)).Body.String(), "TestPlat reported 9 attempts at the last check")

	bad := e.do(http.MethodPost, "/admin/players/"+tess+"/identities/testplat/feeds/score", url.Values{"enabled": {"true"}}, withCookie(c), htmx(row))
	if bad.Header().Get("HX-Reswap") != "none" {
		t.Fatal("a refused switch must not swap the row")
	}
	contains(t, bad.Body.String(), "Could not change feed")
}

func TestSyncShowsPrivateFeed(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	k := service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindAttempt}
	_, _ = e.svc.SetFeedEnabled(ctx, k, true)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	_, _ = e.svc.CheckFeedAccess(ctx, k)

	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	check := "/admin/players/" + tess + feedPath + "/check?from=sync"
	contains(t, body, "TestPlat · attempts", "Private", "History is private", `hx-post="`+check+`"`)
	rec := e.do(http.MethodPost, check, nil, withCookie(c), htmx(""))
	if rec.Header().Get("HX-Reswap") != "none" {
		t.Fatal("from the sync page, Check again only toasts (the live panel refreshes itself)")
	}
	contains(t, rec.Body.String(), "Access is private")
}

func TestHintNeverOnPublicPages(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	ctx := context.Background()
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	k := service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindAttempt}
	_, _ = e.svc.SetFeedEnabled(ctx, k, true)
	e.fp.Access["attempt/abc"] = model.AccessPrivate
	_, _ = e.svc.CheckFeedAccess(ctx, k)
	for _, p := range []string{"/", "/p/" + tess} {
		body := e.do(http.MethodGet, p, nil).Body.String()
		if strings.Contains(body, "History is private") || strings.Contains(body, "Check again") {
			t.Fatalf("%s shows the admin hint", p)
		}
	}
}
```

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/ -run 'AttemptsSwitch|PrivateFeed|NeverOnPublic'`
Expected: FAIL — no "Archive attempts", feed routes 404/405.

- [x] **Step 3: View helpers and the hint component**

`internal/web/views/access.go`:

```go
package views

import (
	"context"
	"fmt"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// FeedState is one optional feed of an account: its declaration, its row
// (zero when it was never switched on) and its counts.
type FeedState struct {
	Spec   platform.FeedSpec
	Feed   model.SyncFeed
	Counts service.Counts
}

func (f FeedState) On() bool      { return f.Feed.Enabled }
func (f FeedState) Private() bool { return f.Feed.Enabled && f.Feed.Access == model.AccessPrivate }

// OptionalFeeds lists the optional feeds of an account's platform.
func OptionalFeeds(ctx context.Context, id service.Identity) []FeedState {
	reg := Platforms(ctx)
	if reg == nil {
		return nil
	}
	p, ok := reg.Get(id.Platform)
	if !ok {
		return nil
	}
	var out []FeedState
	for _, spec := range p.Feeds {
		if spec.Optional {
			out = append(out, FeedState{Spec: spec, Feed: id.Feed(spec.Kind), Counts: id.Counts[spec.Kind]})
		}
	}
	return out
}

// FeedSpecOf is a feed's declaration (zero when unknown).
func FeedSpecOf(ctx context.Context, platformName, kind string) platform.FeedSpec {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(platformName); ok {
			f, _ := p.Feed(kind)
			return f
		}
	}
	return platform.FeedSpec{}
}

// hasPrivateFeed reports whether the platform refuses one of an account's switched-on feeds.
func hasPrivateFeed(id service.Identity) bool {
	for _, f := range id.Feeds {
		if f.Enabled && f.Access == model.AccessPrivate {
			return true
		}
	}
	return false
}

// FeedName is a feed's plural noun: "attempts".
func FeedName(kind string) string { return kind + "s" }

func FeedURL(playerID, platformName, kind string) string {
	return "/admin/players/" + playerID + "/identities/" + platformName + "/feeds/" + kind
}

// feedConfirm asks before switching a feed on: it downloads a whole history.
func feedConfirm(ctx context.Context, pl service.PlayerSummary, id service.Identity, fs FeedState) templ.Attributes {
	if fs.On() {
		return templ.Attributes{}
	}
	name := PlatformName(ctx, id.Platform)
	msg := fmt.Sprintf("Archive every %s %s of %s? This history can reach several GB of replays.", name, fs.Spec.Kind, pl.Name)
	if fs.Feed.RemoteTotal > 0 {
		msg += fmt.Sprintf(" %s reported %s %s at the last check.", name, Number(fs.Feed.RemoteTotal), FeedName(fs.Spec.Kind))
	}
	return templ.Attributes{"hx-confirm": msg}
}

// feedStatus is the one-line state under a switched-on feed: "attempts: public · 12 archived".
func feedStatus(fs FeedState) string {
	access := map[string]string{
		model.AccessPublic: "public", model.AccessPrivate: "private", model.AccessUnknown: "checking access", model.AccessNA: "on",
	}[fs.Feed.Access]
	return fmt.Sprintf("%s: %s · %s archived", FeedName(fs.Spec.Kind), access, Number(fs.Counts.Archived))
}
```

`internal/web/views/access.templ`:

```templ
package views

import (
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/web/components/alert"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
)

// AccessHint tells the admin what a player must change for a private feed
// (spec §6.3). check carries the "Check again" button's htmx attributes.
// Admin pages only.
templ AccessHint(h *platform.Hint, check templ.Attributes) {
	if h != nil {
		@alert.Alert(alert.Props{}) {
			@icon.TriangleAlert()
			@alert.Title() {
				{ h.Title }
			}
			@alert.Description() {
				<div class="flex flex-col gap-2">
					if h.Intro != "" {
						<p>{ h.Intro }</p>
					}
					if len(h.Steps) > 0 {
						<ol class="ml-4 list-decimal">
							for _, s := range h.Steps {
								<li>{ s }</li>
							}
						</ol>
					}
					<div class="flex flex-wrap items-center gap-2">
						if h.LinkURL != "" {
							<a class="font-medium underline underline-offset-4" href={ templ.SafeURL(h.LinkURL) } target="_blank" rel="noopener">{ h.LinkText }</a>
						}
						@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Attributes: check}) {
							@icon.RefreshCw()
							Check again
						}
					</div>
					if h.Note != "" {
						<p class="text-xs">{ h.Note }</p>
					}
				</div>
			}
		}
	}
}
```

- [x] **Step 4: Admin row**

`internal/web/views/admin.templ`:

- `identityVariant` treats a private feed like an error:

```go
func identityVariant(id service.Identity) badge.Variant {
	switch {
	case id.LastError != "" || hasPrivateFeed(id):
		return badge.VariantDestructive
	case !id.Enabled:
		return badge.VariantOutline
	}
	return badge.VariantSecondary
}
```

- in `identityMenu`, the badge's warning icon condition becomes `if id.LastError != "" || hasPrivateFeed(id)`, and after the pause/resume form add:

```templ
			for _, fs := range OptionalFeeds(ctx, id) {
				<form hx-post={ FeedURL(pl.ID, id.Platform, fs.Spec.Kind) } hx-target={ "#player-" + pl.ID } hx-swap="outerHTML" { feedConfirm(ctx, pl, id, fs)... }>
					<input type="hidden" name="enabled" value={ strconv.FormatBool(!fs.On()) }/>
					@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantOutline, Size: button.SizeSm, Class: "w-full"}) {
						if fs.On() {
							{ "Stop archiving " + FeedName(fs.Spec.Kind) }
						} else {
							{ "Archive " + FeedName(fs.Spec.Kind) }
						}
					}
				</form>
				if fs.On() {
					<p class="text-muted-foreground">{ feedStatus(fs) }</p>
				}
			}
```

- in `AdminPlayerRow`'s accounts cell, after the badges `div`:

```templ
			for _, id := range pl.Identities {
				for _, fs := range OptionalFeeds(ctx, id) {
					if fs.Private() {
						<div class="mt-2 max-w-md">
							@AccessHint(fs.Spec.AccessHint, templ.Attributes{
								"hx-post": FeedURL(pl.ID, id.Platform, fs.Spec.Kind) + "/check", "hx-target": "#player-" + pl.ID, "hx-swap": "outerHTML",
							})
						</div>
					}
				}
			}
```

- `PlayerRowToast` takes the toast type:

```templ
templ PlayerRowToast(pl service.PlayerSummary, v AdminPlayersView, t toast.Type, title string) {
	@AdminPlayerRow(pl, v)
	@ToastOOB(t, title, pl.Name)
}
```

- [x] **Step 5: Sync queue row**

`internal/web/views/sync.templ` — a feed waiting for access is not "active":

```go
// active reports whether the worker runs this feed.
func (q QueueRow) active() bool {
	return q.Player.Enabled && q.Identity.Enabled && q.Feed.Enabled &&
		q.Feed.Access != model.AccessPrivate && q.Feed.Access != model.AccessUnknown
}
```

and in `queueCard`, the account cell becomes:

```templ
								@table.Cell() {
									{ feedLabel(ctx, q) }
									if q.Player.Enabled && !q.Identity.Enabled {
										<span class="ml-1 text-xs text-muted-foreground">(paused)</span>
									}
									if !q.Feed.Enabled {
										<span class="ml-1 text-xs text-muted-foreground">(off)</span>
									} else if q.Feed.Access == model.AccessUnknown {
										@badge.Badge(badge.Props{Variant: badge.VariantOutline, Class: "ml-1"}) {
											Checking access
										}
									} else if q.Feed.Access == model.AccessPrivate {
										@badge.Badge(badge.Props{Variant: badge.VariantDestructive, Class: "ml-1"}) {
											Private
										}
										{{ spec := FeedSpecOf(ctx, q.Identity.Platform, q.Feed.Feed) }}
										<p class="mt-1 max-w-72 text-xs text-muted-foreground">
											if spec.AccessHint != nil {
												{ spec.AccessHint.Title } ·
												if spec.AccessHint.LinkURL != "" {
													<a class="underline underline-offset-4" href={ templ.SafeURL(spec.AccessHint.LinkURL) } target="_blank" rel="noopener">{ spec.AccessHint.LinkText }</a> ·
												}
											}
											<button type="button" class="underline underline-offset-4" hx-post={ FeedURL(q.Player.ID, q.Identity.Platform, q.Feed.Feed) + "/check?from=sync" } hx-swap="none">Check again</button>
										</p>
									}
								}
```

- [x] **Step 6: Handlers and routes**

`internal/web/admin.go` — `renderRow` takes the toast type (update its four callers from Plan 2 to pass `toast.TypeSuccess`):

```go
// renderRow re-renders one player's row with a toast.
func (h *Handler) renderRow(w http.ResponseWriter, r *http.Request, id string, t toast.Type, title string) {
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	pl, err := h.playerSummary(r, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.PlayerRowToast(pl, v, t, title))
}

// accessToast says what an access check found.
func accessToast(f *model.SyncFeed, okTitle string) (toast.Type, string) {
	switch {
	case f.Access == model.AccessPrivate:
		return toast.TypeWarning, "Access is private"
	case f.LastError != "":
		return toast.TypeError, "Could not check access"
	}
	return toast.TypeSuccess, okTitle
}

func feedKey(r *http.Request) service.FeedKey {
	return service.FeedKey{PlayerID: r.PathValue("id"), Platform: r.PathValue("platform"), Kind: r.PathValue("kind")}
}

func (h *Handler) setFeedEnabled(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	ctx := r.Context()
	k := feedKey(r)
	enabled := r.PostFormValue("enabled") == "true"
	f, err := h.svc.SetFeedEnabled(ctx, k, enabled)
	switch {
	case errors.Is(err, service.ErrFeedNotOptional):
		h.toastOnly(w, r, toast.TypeError, "Could not change feed", sentence(err))
		return
	case isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Account not found", "")
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	name := views.FeedName(k.Kind)
	if !enabled {
		h.renderRow(w, r, k.PlayerID, toast.TypeSuccess, "Stopped archiving "+name)
		return
	}
	if checked, _ := h.svc.CheckFeedAccess(ctx, k); checked != nil { // a failed probe is recorded on the feed
		f = checked
	}
	t, title := accessToast(f, "Archiving "+name)
	h.renderRow(w, r, k.PlayerID, t, title)
}

func (h *Handler) checkFeedAccess(w http.ResponseWriter, r *http.Request) {
	k := feedKey(r)
	f, err := h.svc.CheckFeedAccess(r.Context(), k)
	if f == nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Feed not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	t, title := accessToast(f, "Access granted")
	if r.URL.Query().Get("from") == "sync" { // the live panel refreshes on its own
		h.toastOnly(w, r, t, title, f.LastError)
		return
	}
	h.renderRow(w, r, k.PlayerID, t, title)
}
```

(imports: `internal/model`.)

`internal/web/web.go`, after the identity routes:

```go
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/feeds/{kind}", h.requireAdmin(h.setFeedEnabled))
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/feeds/{kind}/check", h.requireAdmin(h.checkFeedAccess))
```

- [x] **Step 7: Run the tests and look at it**

Run: `make generate && go test -race ./internal/web/...`
Expected: PASS (Plan 2's admin tests too, with the new `renderRow` signature).

Then run `make dev`, add a BeatLeader player whose history is private (it is private by default; the instance owner can switch theirs off), open the BeatLeader badge menu → **Archive attempts** → confirm. Check by eye that the hint reads like spec §6.3, the settings link opens BeatLeader settings in a new tab, and **Check again** re-renders the row. On **Sync**, the attempts row shows "Private" and its **Check again** toasts.

- [x] **Step 8: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/web
git commit -m "feat(web): switch attempts archiving per account and show the access hint"
```

---
## Task 7: Attempt display — pages, chips, counts, viewer check

Attempts reuse every page Plan 2 built. This task makes them read as attempts (spec §6.1):

- an end-type badge ("Failed", "Quit", …) on the score page and on chips;
- "Ended at m:ss" instead of the rank;
- a page title such as "Failed attempt by Tess";
- a precise message when BeatLeader kept no replay (most old attempts);
- "N attempts archived" in the player header for accounts that archive them.

The spec also leaves one decision for implementation: whether the bundled ArcViewer plays replays of runs that ended early. Step 6 checks it by hand and records the answer in `views.AttemptViewer`; when it is `false`, attempt pages hide the viewer (and `/embed/…/attempt/…` answers 404) but keep the download.

**Files:**
- Modify: `internal/web/views/format.go` (`EndLabel`, `SongTime`, `AttemptViewer`, `ViewerPlays`), `internal/web/views/urls.go` (`ScoreSummary` for attempts)
- Modify: `internal/web/views/player.templ` (`EndBadge`, chip, header counts), `internal/web/views/score.templ` (badge, "Ended at", no-replay text, viewer gate)
- Modify: `internal/web/public.go` (attempt titles), `internal/web/replay.go` (embed gate)
- Test: `internal/web/views/format_test.go`, `internal/web/attempts_pages_test.go` (new)

**Interfaces:**
- Consumes: Plan 2 `scorePage`, `embedPage`, `PlayChip`, `accounts`, `plural` (sync.templ), `seedTP`; Task 2 `SetFeedEnabled`, `CheckFeedAccess`.
- Produces: `views.EndLabel(end string) string`, `views.SongTime(*float64) string`, `views.EndBadge(*model.Score)`, `const views.AttemptViewer bool`, `views.ViewerPlays(*model.Score) bool`.

- [x] **Step 1: Write the failing tests**

Append to `internal/web/views/format_test.go` (import `internal/model`):

```go
func TestAttemptText(t *testing.T) {
	for got, want := range map[string]string{
		EndLabel(model.EndFail): "Failed", EndLabel(model.EndQuit): "Quit", EndLabel(model.EndRestart): "Restarted",
		EndLabel(model.EndPractice): "Practice", EndLabel(model.EndClear): "Cleared", EndLabel(model.EndUnknown): "Ended",
		SongTime(new(59.74)): "0:59", SongTime(new(210.65674)): "3:30", SongTime(nil): "—",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	a := &model.Score{Kind: model.KindAttempt, EndType: model.EndFail, EndTime: new(59.74), Accuracy: 0.698989}
	if got := ScoreSummary(a); got != "Failed at 0:59 · 69.90%" {
		t.Errorf("ScoreSummary(attempt) = %q", got)
	}
	if !ViewerPlays(&model.Score{Kind: model.KindScore}) || !ViewerPlays(&model.Score{Kind: model.KindAttempt, EndType: model.EndClear}) {
		t.Error("scores and clears always play")
	}
	if ViewerPlays(a) != AttemptViewer {
		t.Error("runs that ended early play only when the bundled viewer handles them")
	}
}
```

`internal/web/attempts_pages_test.go`:

```go
package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

// seedAttempts is seedTP plus a failed attempt with a replay (a2) and a quit
// without one (a3), on t1's map.
func (e *testEnv) seedAttempts() string {
	e.t.Helper()
	tess := e.seedTP()
	fail := testutil.FakePlay(model.KindAttempt, "a2", "lb-a", testutil.T0.Add(-time.Minute), true)
	fail.EndType, fail.EndTime = model.EndFail, new(59.74)
	quit := testutil.FakePlay(model.KindAttempt, "a3", "lb-a", testutil.T0.Add(-2*time.Minute), false)
	quit.EndType, quit.EndTime = model.EndQuit, new(12.0)
	testutil.UpsertFake(e.t, e.svc, tess, fail, quit)
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, tess, "a2"), "fail bytes")
	return tess
}

func TestAttemptPage(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	e.seedAttempts()

	page := e.do(http.MethodGet, "/s/tp/attempt/a2", nil)
	body := page.Body.String()
	if page.Code != 200 {
		t.Fatalf("attempt page = %d", page.Code)
	}
	contains(t, body, "Failed", "Ended at", "0:59", "Download .tpr", "Failed attempt by Tess")
	if hasViewer := strings.Contains(body, `src="/embed/tp/attempt/a2"`); hasViewer != views.AttemptViewer {
		t.Fatalf("viewer shown = %v, AttemptViewer = %v", hasViewer, views.AttemptViewer)
	}
	embed := e.do(http.MethodGet, "/embed/tp/attempt/a2", nil).Code
	if (embed == 200) != views.AttemptViewer {
		t.Fatalf("embed = %d with AttemptViewer = %v", embed, views.AttemptViewer)
	}
	contains(t, e.do(http.MethodGet, "/s/tp/attempt/a3", nil).Body.String(), "Quit", "TestPlat kept no replay for this attempt.")
	if got := e.do(http.MethodGet, "/r/tp/attempt/a2.tpr", nil).Body.String(); got != "fail bytes" {
		t.Fatalf("download = %q", got)
	}
}

func TestAttemptChipsAndCounts(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	tess := e.seedAttempts()
	ctx := context.Background()
	if _, err := e.svc.SetFeedEnabled(ctx, service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindAttempt}, true); err != nil {
		t.Fatal(err)
	}
	page := e.do(http.MethodGet, "/p/"+tess, nil).Body.String()
	contains(t, page, "2 attempts archived") // a1 (seedTP) and a2
	frag := e.do(http.MethodGet, "/p/"+tess+"/map?key=hash-lb-a%2FStandard%2F7&type=all", nil, htmx("plays-0")).Body.String()
	contains(t, frag, `href="/s/tp/attempt/a2"`, "Failed", `href="/s/tp/attempt/a3"`, "Quit")
}
```

(`FakePlay` builds map keys as `lower("HASH-" + lb) + "/Standard/7"`, so lb-a's key is `hash-lb-a/Standard/7`.)

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/...`
Expected: FAIL — undefined: `EndLabel`, `SongTime`, `ViewerPlays`, `AttemptViewer`; no "Ended at".

- [x] **Step 3: Text helpers**

`internal/web/views/format.go` (import `internal/model`):

```go
// EndLabel names how an attempt ended.
func EndLabel(end string) string {
	switch end {
	case model.EndClear:
		return "Cleared"
	case model.EndFail:
		return "Failed"
	case model.EndQuit:
		return "Quit"
	case model.EndRestart:
		return "Restarted"
	case model.EndPractice:
		return "Practice"
	}
	return "Ended"
}

// SongTime formats seconds into a song as m:ss.
func SongTime(sec *float64) string {
	if sec == nil {
		return "—"
	}
	s := int(*sec)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// AttemptViewer records whether the bundled ArcViewer plays replays of runs
// that ended early (fail, quit, restart, practice). Checked by hand when
// attempts were added (Plan 3, Task 7) — see the Verification log.
const AttemptViewer = true

// ViewerPlays reports whether the embedded viewer can play a row's replay.
func ViewerPlays(s *model.Score) bool {
	return s.Kind != model.KindAttempt || s.EndType == model.EndClear || AttemptViewer
}
```

and in `internal/web/views/urls.go`, make `ScoreSummary` describe attempts by how they ended:

```go
func ScoreSummary(s *model.Score) string {
	if s.Kind == model.KindAttempt {
		return EndLabel(s.EndType) + " at " + SongTime(s.EndTime) + " · " + Percent(s.Accuracy)
	}
	// … the existing body for scores is unchanged …
```

- [x] **Step 4: Components**

`internal/web/views/player.templ`:

```go
func endVariant(end string) badge.Variant {
	switch end {
	case model.EndFail:
		return badge.VariantDestructive
	case model.EndClear:
		return badge.VariantSecondary
	}
	return badge.VariantOutline
}
```

```templ
// EndBadge says how an attempt ended (nothing for scores).
templ EndBadge(s *model.Score) {
	if s.Kind == model.KindAttempt {
		@badge.Badge(badge.Props{Variant: endVariant(s.EndType)}) {
			{ EndLabel(s.EndType) }
		}
	}
}
```

In `PlayChip`, after the platform name span: `@EndBadge(s)`. In `accounts`, after the replays span:

```templ
				if id.Feed(model.KindAttempt).Enabled {
					<span class="tabular-nums text-muted-foreground">{ plural(id.Counts[model.KindAttempt].Archived, "attempt") } archived</span>
				}
```

`internal/web/views/score.templ`:

- in the badge row, after `@DifficultyBadge(v.Score)`: `@EndBadge(v.Score)`;
- in the stats grid, replace `@stat("Rank", "#"+strconv.Itoa(v.Score.Rank))` with:

```templ
			if v.Score.Kind == model.KindAttempt {
				@stat("Ended at", SongTime(v.Score.EndTime))
			} else {
				@stat("Rank", "#"+strconv.Itoa(v.Score.Rank))
			}
```

- in `replaySection`'s archived case, the iframe block becomes:

```templ
			if v.ViewerAvailable && ViewerPlays(v.Score) {
				<div class="aspect-video w-full overflow-hidden rounded-lg border bg-muted">
					<iframe src={ EmbedPath(ctx, v.Score) } title="Replay viewer" class="size-full border-0" allow="fullscreen; autoplay" allowfullscreen loading="lazy"></iframe>
				</div>
			} else if v.ViewerAvailable {
				@stateAlert("Viewer unavailable", "The 3D viewer cannot play runs that ended early. You can still download the replay.", false)
			} else {
				@stateAlert("Viewer unavailable", "The 3D viewer is not bundled with this build. You can still download the replay.", false)
			}
```

  and the two other viewer uses (the "Open viewer" button and the embed-code field) check `v.ViewerAvailable && ViewerPlays(v.Score)` instead of `v.ViewerAvailable`;
- make the `default` case precise for attempts:

```templ
		default:
			if v.Score.Kind == model.KindAttempt {
				@stateAlert("No replay", PlatformName(ctx, v.Score.Platform)+" kept no replay for this attempt.", false)
			} else {
				@stateAlert("No replay", PlatformName(ctx, v.Score.Platform)+" has no replay for this score.", false)
			}
```

- [x] **Step 5: Titles and the embed gate**

`internal/web/public.go` — in `scorePage`, after computing `title`:

```go
	if sc.Kind == model.KindAttempt {
		title = fmt.Sprintf("%s · %s attempt by %s", views.SongTitle(sc), views.EndLabel(sc.EndType), views.PlayerName(sc))
	}
```

`internal/web/replay.go` — in `embedPage`, the unavailable condition becomes:

```go
	if err != nil || !h.viewer.Available() || sc.ReplayState != model.ReplayArchived || !views.ViewerPlays(sc) {
```

Run: `make generate && go test -race ./internal/web/...`
Expected: PASS.

- [ ] **Step 6: Check by hand whether ArcViewer plays a run that ended early**

ArcViewer is only served same-origin here, and BeatLeader's `otherreplays` host sends no CORS header, so serve a downloaded file locally with CORS:

```bash
mkdir -p /tmp/ssa-bsor && cd /tmp/ssa-bsor
# A quit and a fail (if any) from a top player whose history is public (verified 2026-10-10).
curl -s 'https://api.beatleader.xyz/player/76561199080950125/scoresstats?sortBy=date&order=desc&count=100' > page.json
python3 -c '
import json
d = json.load(open("page.json"))["data"]
for end, name in ((4, "quit"), (2, "fail"), (3, "restart")):
    u = next((x["replay"] for x in d if x["endType"] == end and "/otherreplays/" in (x.get("replay") or "")), None)
    print(name, u or "-")
'
curl -s -o quit.bsor '<the quit URL printed above>'
python3 -c '
import http.server as s
class H(s.SimpleHTTPRequestHandler):
    def end_headers(self):
        self.send_header("Access-Control-Allow-Origin", "*")
        super().end_headers()
s.ThreadingHTTPServer(("127.0.0.1", 8099), H).serve_forever()
' &
```

With `make viewer && make run` (or `make dev`) running, open `http://localhost:8080/viewer/?replayURL=http://127.0.0.1:8099/quit.bsor&noProxy=true`.

- **It loads and plays until the run stops** (the viewer may end abruptly): keep `AttemptViewer = true`.
- **It errors or never loads:** set `AttemptViewer = false` and re-run `go test ./internal/web/...`. The tests follow the constant.

Repeat with the fail and restart replays if the page had them. Stop the Python server (`kill %1`). Record the result (viewer build from `internal/viewer`, files tried, outcome) in the **Verification log**; it is the decision spec §6.1 defers to implementation.

- [x] **Step 7: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/web docs/superpowers/plans/2026-10-10-beatleader-attempts.md
git commit -m "feat(web): attempt pages, end-type badges and attempt counts"
```

---
## Task 8: End-to-end attempts test, docs, live check

The whole attempts path through the real app against a fake BeatLeader whose history starts private:

1. switching attempts on answers `private` with the hint;
2. the history goes public and **Check again** answers `public` with the remote total;
3. the worker archives the failed attempt from `otherreplays` and skips the PB clear;
4. the attempt page, its raw file and the `type` filter serve it.

Then the README and a manual run against the live API.

**Files:**
- Modify: `internal/app/beatleader_test.go` (`fakeBeatLeader` gains attempts; new test)
- Modify: `README.md`

**Interfaces:**
- Consumes: Plan 2 Task 11 `startApp`, `running.do`, `running.waitArchived`, `fakeScoreSaber`, `fakeBeatLeader`, `getBody`; Tasks 1–7.
- Produces: `fakeBeatLeader(t *testing.T, public *atomic.Bool) *httptest.Server` (the attempts history is public while `public` is true).

- [x] **Step 1: Give the fake BeatLeader an attempts history**

In `internal/app/beatleader_test.go`, change the signature to `func fakeBeatLeader(t *testing.T, public *atomic.Bool) *httptest.Server` (import `"sync/atomic"`), update the existing call in `TestBeatLeaderEndToEnd` to `fakeBeatLeader(t, new(atomic.Bool))`, and add, before `srv := httptest.NewServer(mux)`:

```go
	// Attempts: a fail with its own replay, and a clear that is the PB score
	// (its replay is the score's replays-storage file): the clear is skipped.
	mux.HandleFunc("GET /player/76561198038925092/scoresstats", func(w http.ResponseWriter, r *http.Request) {
		if !public.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		lb := `"leaderboardId":"abc71","leaderboard":{"id":"abc71","song":{"hash":"abc","name":"E2E Song","subName":"","author":"A","mapper":"M","coverImage":""},` +
			`"difficulty":{"value":9,"modeName":"Standard","difficultyName":"ExpertPlus","status":0,"stars":null,"maxScore":1100}}`
		fmt.Fprintf(w, `{"metadata":{"page":1,"itemsPerPage":100,"total":2},"data":[
			{"id":991,"endType":2,"time":42.5,"timepost":%d,"timeset":null,"baseScore":300,"modifiedScore":300,"accuracy":0.8,"pp":0,"rank":0,"modifiers":"","hmd":256,
			 "replay":"http://%s/otherreplays/991.bsor",%s},
			{"id":992,"endType":1,"time":120,"timepost":%d,"timeset":null,"baseScore":1050,"modifiedScore":1050,"accuracy":0.96,"pp":0,"rank":3,"modifiers":"","hmd":256,
			 "replay":"http://%s/replays-storage/888-76561198038925092-ExpertPlus-Standard-ABC.bsor",%s}]}`,
			set+60, r.Host, lb, set, r.Host, lb)
	})
	mux.HandleFunc("GET /otherreplays/991.bsor", func(w http.ResponseWriter, r *http.Request) {
		if !public.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(attemptBytes)
	})
```

with `var attemptBytes = []byte("BSOR attempt e2e payload")` next to `bsorBytes`.

- [x] **Step 2: Write the end-to-end test**

Append to `internal/app/beatleader_test.go`:

```go
func TestBeatLeaderAttemptsEndToEnd(t *testing.T) {
	public := new(atomic.Bool)
	r := startApp(t, app.Options{ScoreSaberURL: fakeScoreSaber(t).URL, BeatLeaderURL: fakeBeatLeader(t, public).URL})

	code, body := r.do(t, http.MethodPost, "/api/v1/players", `{"ref":"https://beatleader.com/u/76561198038925092"}`)
	if code != http.StatusCreated {
		t.Fatalf("add: %d %s", code, body)
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &p)
	feed := "/api/v1/players/" + p.ID + "/identities/beatleader/feeds/attempt"

	code, body = r.do(t, http.MethodPatch, feed, `{"enabled":true}`)
	if code != 200 || !strings.Contains(string(body), `"access":"private"`) ||
		!strings.Contains(string(body), `"title":"Attempt history is private on BeatLeader"`) {
		t.Fatalf("switch on while private: %d %s", code, body)
	}

	public.Store(true) // the player turned "Public history (auto-synced)" on
	code, body = r.do(t, http.MethodPost, feed+"/check", "")
	if code != 200 || !strings.Contains(string(body), `"access":"public"`) || !strings.Contains(string(body), `"remote_total":2`) {
		t.Fatalf("check again: %d %s", code, body)
	}

	r.waitArchived(t, "/api/v1/scores/beatleader/attempt/991")
	if status, got, err := getBody(r.Client, r.Base+"/r/bl/attempt/991.bsor"); err != nil || status != 200 || !bytes.Equal(got, attemptBytes) {
		t.Fatalf("attempt replay: %d %q %v", status, got, err)
	}
	if status, page, _ := getBody(r.Client, r.Base+"/s/bl/attempt/991"); status != 200 || !strings.Contains(string(page), "Failed") {
		t.Fatalf("attempt page: %d", status)
	}
	if status, _, _ := getBody(r.Client, r.Base+"/api/v1/scores/beatleader/attempt/992"); status != 404 {
		t.Fatalf("the PB clear is skipped, not stored: %d", status)
	}
	for q, want := range map[string]string{"": `"total":1`, "?type=fail": `"total":1`, "?type=all": `"total":2`} {
		_, list, _ := getBody(r.Client, r.Base+"/api/v1/players/"+p.ID+"/scores"+q)
		if !strings.Contains(string(list), want) {
			t.Errorf("scores%s = %s, want %s", q, list, want)
		}
	}
}
```

Run: `go test -race ./internal/app/ -v -run 'BeatLeader'`
Expected: PASS (both BeatLeader end-to-end tests). Default listing `total:1` is score 888 alone; `type=all` adds attempt 991.

- [x] **Step 3: README**

After the "Players and platforms" section (Plan 2), add:

```markdown
### BeatLeader attempts

BeatLeader also records attempts: failed, quit, restarted and practice runs, and
clears that did not beat the personal best, each with its own replay. They are
archived per account, on request: on **Manage**, open a player's BeatLeader
badge → **Archive attempts**.

- BeatLeader only shares attempts when the player's history is public. The
  player signs in on beatleader.com, opens **Settings → Scores**, turns on
  **Public history (auto-synced)**, then reloads the page and checks the switch
  stayed on (BeatLeader can show it on even when saving failed). Until then,
  **Manage** and **Sync** show these steps with a **Check again** button, and
  SSArchiver re-checks every 24 hours by itself.
- A history can be tens of thousands of runs and several GB of replays.
  Attempts are fetched after all score work, so they never delay score
  archiving.
- BeatLeader drops old attempt replays over time: the sooner attempts are
  switched on, the more can be saved.
- Attempts have their own pages (`/s/bl/attempt/<id>`), raw files
  (`/r/bl/attempt/<id>.bsor`) and embeds. The player page and the API list them
  with the `type` filter: `complete` (default: scores and cleared attempts),
  `fail`, `quit`, `restart`, `practice`, `all`.
```

and append to the **API** section:

```markdown
Players' `identities[].feeds[]` report `optional`, `access`
(`n/a`, `unknown`, `public`, `private`), `remote_total`, per-feed `counts`, and a
`hint` while access is private. Optional feeds are switched with
`PATCH /api/v1/players/{id}/identities/{platform}/feeds/{kind}` (`{"enabled":
true}`; switching on checks access at once) and re-checked with
`POST …/feeds/{kind}/check`. `GET /api/v1/players/{id}/scores?type=fail,quit`
lists attempts; scores carry `kind`, `end_type` and `end_time`.
```

- [x] **Step 4: Full verification**

Run: `make generate && git diff --exit-code -- '*_templ.go' internal/db/query internal/web/static/css/app.css && go test -race ./... && make lint`
Expected: no diff; all packages ok; `0 issues.`

Run: `go test -tags live -run Live ./internal/beatleader/ -v`
Expected: PASS; the log says whether the owner's attempt history was public.

- [ ] **Step 5: Manual check against live BeatLeader**

With `make run` and the instance owner's BeatLeader account tracked (Plan 2 Task 11):

1. **Manage** → BeatLeader badge → **Archive attempts** → the confirmation names "BeatLeader attempt" and the storage warning → confirm. With the history public, the toast says "Archiving attempts" and the menu shows "attempts: public".
2. **Sync**: an "BeatLeader · attempts" queue row appears and runs after the score rows. The owner's 27 attempts list quickly; most have no replay ("BeatLeader kept no replay for this attempt" on their pages).
3. If the owner can, switch **Public history (auto-synced)** off on beatleader.com, then **Check again** on **Manage**: the badge turns red and the full hint appears (title, four steps, settings link, note). Switch it back on (reload to check it stayed on), **Check again**: "Access granted", the hint disappears.
4. The player page with **Type → Fails** lists the failed runs. A public page never shows the hint.

Record what you checked in the Verification log.

- [x] **Step 6: Commit**

```bash
git add internal/app README.md docs/superpowers/plans/2026-10-10-beatleader-attempts.md
git commit -m "test: BeatLeader attempts end to end; document attempt archiving"
```
