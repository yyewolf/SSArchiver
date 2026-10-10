# BeatLeader Scores Implementation Plan (multi-platform, part 2 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **Update the Progress Tracking section (below) as you go — it is the hand-off contract between agents.**

**Goal:** Archive BeatLeader scores and replays next to ScoreSaber ones: a BeatLeader client and registry entry, linking/unlinking/merging platform accounts on one player, a merged player page with one row per map, per-platform public routes, and the `platform`/`min_score`/`max_score` filters.

**Architecture:** `internal/beatleader` mirrors `internal/scoresaber`: a typed client with sliding-window limiters, and an adapter that registers BeatLeader in the platform registry Plan 1 built. Everything else stays generic and is driven by the registry: the service gains account management (link, unlink, merge with aliases) and SQL-grouped map queries, `platform.Registry` builds every public URL, and the web/API layers expose the new operations. The fake third platform from Plan 1 (`testutil.FakePlatform`) is the test vehicle for every generic path; BeatLeader-specific behaviour is tested in its own package with fixtures trimmed from live responses.

**Tech Stack:** Go 1.27 · GORM + gorm gen · `github.com/glebarez/sqlite` (bundled SQLite 3.41.2) · templ + htmx · huma v2 · golangci-lint v2. No new dependencies.

**Spec:** [`docs/superpowers/specs/2026-10-10-beatleader-design.md`](../specs/2026-10-10-beatleader-design.md) — read §1–§3, §4.2–4.6, §5.2–5.5 and §6 before starting any task. Where the spec and this plan differ in a signature, the plan wins. Section numbers below ("spec §6.2") refer to it. [Plan 1](2026-10-10-platform-foundation.md) built the code this plan extends (registry, accounts, feeds, opaque IDs, per-platform storage); its File Map is the quickest orientation.

**Sibling plans:** Plan 1 (done) — platform foundation. Plan 3 ([`2026-10-10-beatleader-attempts.md`](2026-10-10-beatleader-attempts.md)) — BeatLeader attempts: attempts feed, access probe, access hint UI, `type` filter. It consumes the names this plan produces; record every rename in the Deviations log.

**What this plan deliberately does not do** (Plan 3):
- the BeatLeader attempts feed, `scoresstats`, the access probe tier, `ErrUnauthorized` handling in the worker;
- optional-feed switches (API `PATCH …/feeds/{kind}`, `…/check`), the access hint, the `type` filter;
- attempt-specific display (end-type badge, "ended at m:ss"). The `/s/{slug}/attempt/{id}`, `/r/…/attempt/…` and `/embed/…/attempt/…` routes **are** added here (they are generic lookups by kind), so Plan 3 only adds content.

## Global Constraints

Every task's requirements implicitly include all of these.

- Module `github.com/yyewolf/ssarchiver`, `go 1.27`. **No new dependencies; no version bumps.** Pinned: `gorm.io/gorm v1.31.2`, `gorm.io/gen v0.3.29`, `github.com/glebarez/sqlite v1.11.0`, `github.com/danielgtaylor/huma/v2 v2.39.1`, `github.com/a-h/templ v0.3.1070`, golangci-lint `v2.14.0`.
- **No schema change in this plan.** Every table and column it needs exists since Plan 1. Never call `Migrator().DropColumn`/`AlterColumn`/`DropTable` (they rebuild tables and cascade-delete every score under `foreign_keys(1)`).
- **`players.id` is opaque.** Platform account IDs come only from `player_platforms.external_id` (`service.WorkFeed.ExternalID`, `Identity.ExternalID`). Never build a platform URL or API call from a player ID.
- **No platform name in generic code.** `service`, `archiver`, `storage`, `web`, `api` and `views` never mention `beatleader`/`BeatLeader` (or `scoresaber` beyond the existing `model.PlatformScoreSaber` uses); they go through `*platform.Registry`. `internal/beatleader` is imported only by `internal/app` and its own tests.
- BeatLeader values (exact): registry name `beatleader` (`beatleader.Name`), slug `bl`, display name `BeatLeader`, priority `10`, replay extension `.bsor`, `PBOnly: true`. API base `https://api.beatleader.xyz`. Replay allowlist (the only URLs ever fetched for replays): `https://cdn.replays.beatleader.xyz/` (CDN limiter), `https://api.beatleader.xyz/replays-storage/` and `https://api.beatleader.xyz/otherreplays/` (API limiter). Limiters: `beatleader/api` 40 requests and `beatleader/cdn` 20 requests per 10 s sliding window.
- Public URL shapes (spec §6.1): legacy (ScoreSaber) scores keep `/s/{id}`, `/r/{id}.dat`, `/embed/{id}`, and **these resolve ScoreSaber rows only**. Every other row: `/s/{slug}/{externalID}`, `/s/{slug}/attempt/{externalID}`, `/r/{slug}/{externalID}{ext}`, `/r/{slug}/attempt/{externalID}{ext}`, `/embed/{slug}/…`. Account links `/p/{slug}/{externalID}`. All of them are built by `platform.Registry` (`PlayPath`, `ReplayPath`, `AccountPath`); views and the API never concatenate them by hand.
- Internal row IDs (`scores.id`, `leaderboards.id` ≥ `platform.InternalIDBase` for non-legacy rows) are never exposed in URLs or the API; the API reports `id: 0` for non-legacy rows.
- An archived replay is never re-downloaded, replaced or moved except by a merge (hard link, then DB move, then source directory removal — in that order).
- `api`, `web`, `views` and `archiver` never import `gorm.io/*` or `internal/db/query`.
- Generated code is committed: run `make generate` after changing any `.templ` file or Tailwind classes and commit `*_templ.go` and `internal/web/static/css/app.css`. No model change happens in this plan, so `internal/db/query/*` must not change.
- Every commit passes `go build ./...`, `go test ./...` and `make lint`. Conventional Commits (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`).
- Errors wrapped with `%w` and a package prefix (`fmt.Errorf("service: link: %w", err)`); `context.Context` first on every I/O function; logging via `log/slog` only. Tests never hit the network (the live smoke test is behind `-tags live`).
- UI text: sentence case, no trailing period on buttons and badges; toasts and form errors are full sentences (`sentence(err)` in `internal/web/admin.go`).

## Review Focus

Inputs/failure modes the spec implies that are easy to get wrong. Each has a pinned test in the owning task.

1. **Linking an account that is already tracked** (as its own player, or pasted as URL vs bare ID, or already linked to this very player) — expected: refused with a message naming the other player and suggesting a merge; nothing is created or moved. → Task 4 `TestLinkIdentityRefusals`, Task 9 `TestAdminLinkUnlinkIdentity`, Task 10 `TestIdentityEndpoints`.
2. **A crash or rerun in the middle of a merge** — expected: no archived replay ever loses its file; rerunning after the file-link step succeeds and keeps existing target files. → Task 5 `TestMergeRerunAfterFileLinkStep`, `TestMergeMovesEverything`.
3. **Hostile or surprising replay URLs in BeatLeader payloads** (other hosts, look-alike hosts, `..` paths, redirects off the allowlist, the `replays-storage` host) — expected: only allowlisted URLs are fetched, refused ones are stored without a replay and logged. → Task 1 `TestReplayAllowlist`, Task 2 `TestPlaysConversion`, Task 3 `TestRefusedReplaysAreLogged`.
4. **Old links after the change** — `/s/{ssID}`, `/r/{id}.dat`, `/embed/{id}` must keep resolving ScoreSaber rows and must not resolve a BeatLeader row through its internal ID; a merged-away `/p/{old}` must 301 to the survivor (query kept). → Task 7 `TestLegacyRoutesResolveScoreSaberOnly`, Task 8 `TestMergedAwayPlayerRedirects`.
5. **The same map played on two platforms** (ScoreSaber uppercase hash + `SoloStandard`, BeatLeader lowercase hash + `Standard`) on a large history — expected: one row per map, paginated over maps in SQL with exact totals under every filter, and a map is hidden only when none of its plays match. → Task 6 `TestListMapGroupsMergesPlatforms`, `TestListMapGroupsFiltersAndPaging`.

---

## Progress Tracking

**Rules for every agent working on this plan:**

1. Before starting a task: set its row to `🟡 in progress`, fill `Owner` and today's date in `Started`.
2. Tick each step checkbox (`- [x]`) in the task body as soon as it is done — not in batches.
3. When the task's final commit lands: set `✅ done`, put the short commit SHA in `Commit`, and add one line to **Verification log** with the exact commands run and their result.
4. If you deviate from the plan (renamed function, different library call, extra file), add an entry to **Deviations log** *and* fix any later task text that refers to the old name — in this plan **and in Plan 3**, which builds on these names. Later agents only read their own task.
5. If blocked: set `⛔ blocked`, explain in **Session hand-off**, stop.
6. Before ending a session: update **Session hand-off**. Commit the plan file together with your work (`docs: update plan progress`).

Status legend: `⬜ todo` · `🟡 in progress` · `✅ done` · `⛔ blocked`

| # | Task | Status | Owner | Started | Commit |
|---|------|--------|-------|---------|--------|
| 1 | `internal/beatleader` client: types, limiters, allowlisted replays | ✅ done | SDD controller | 2026-10-10 | dab88d1 |
| 2 | BeatLeader adapter + registry entry + app wiring | ✅ done | SDD controller | 2026-10-10 | 9e02e16 |
| 3 | Generic play semantics: PB supersede, kept archives, refused replays, profile via `Resolve` | ✅ done | SDD controller | 2026-10-10 | 62d9df9 |
| 4 | Account management: link, unlink, enable; per-account counts | ✅ done | SDD controller | 2026-10-10 | 4a63461 |
| 5 | Merge and aliases | ✅ done | SDD controller | 2026-10-10 | 77c6d9a + 3967457 |
| 6 | Score queries: new filters, map groups, lookup by platform ID | ✅ done | SDD controller | 2026-10-10 | e6529d1 |
| 7 | Registry-built links, per-platform public routes, registry-built CSP | ✅ done | SDD controller | 2026-10-10 | b093bb2 |
| 8 | Merged player page | ✅ done | SDD controller | 2026-10-10 | 7fefbf2 |
| 9 | Admin UI: accounts, link/unlink/merge; sync queue per feed; event filters | ✅ done | SDD controller | 2026-10-10 | 74dbb14 |
| 10 | JSON API: accounts, merge, filters, per-platform scores | ✅ done | SDD controller | 2026-10-10 | f13d54f |
| 11 | BeatLeader end-to-end test + docs | ✅ done | SDD controller | 2026-10-10 | 591a66c |

### Session hand-off

_Current task:_ —
_Next step:_ Task 11 Step 5 — the manual `make run` check against live BeatLeader (human visual pass; the automated suite, including `TestBeatLeaderEndToEnd` and the `-tags live` smoke test, is green).
_Half-done / uncommitted:_ —
_Notes for next agent:_ All 11 tasks implemented and reviewed 2026-10-10 via subagent-driven development; every task review approved, one task fix round (Task 5: merge keeps unlocatable replay files), and a final whole-branch review (ready to merge) whose five fixable findings landed in e554b48 (same-second PB supersede tie-break, `/p/{old}/map` follows merge aliases, nil-`Latest` row guard, `sync.templ` injectable clock, legacy-source merge test); two findings deferred with rulings — the `More()` under-count under score-bound filters (plan-mandated formula; fold into Plan 3's filter work) and `ReplayAllowed`'s `%2e%2e` hardening (host allowlist bounds it). Deviations are all lint/gofumpt-driven except the `chipWhere`→`bestWhere` rename (gosec G101) and the `#nosec G710` on the alias 301 (repo pattern). Plan 3 consumes these names unchanged except `chipWhere`→`bestWhere` (internal to Task 6's `groups.go`, not referenced by Plan 3).

### Verification log

| Date | Task | Command(s) | Result |
|------|------|------------|--------|
| 2026-10-10 | 1–11 | `go test -race ./...` (per task on touched packages, full suite before each commit) | all packages ok |
| 2026-10-10 | 1–11 | `make lint` | `0 issues.` every task |
| 2026-10-10 | 11 | `make generate && git diff --exit-code -- '*_templ.go' internal/db/query internal/web/static/css/app.css` | no diff |
| 2026-10-10 | 11 | `go test -tags live -run Live ./internal/beatleader/ -v` | PASS against live api.beatleader.xyz (owner's profile) |
| 2026-10-10 | 11 | `go test -race ./internal/app/ -run 'TestBeatLeaderEndToEnd|TestEndToEnd' -v` | PASS |
| 2026-10-10 | final | `go test -race ./...` + `make lint` at 37350be (final review) and e554b48 (fix wave) | all ok; `0 issues.` |

### Deviations log

| Date | Task | Deviation | Reason | Later tasks updated? |
|------|------|-----------|--------|----------------------|
| 2026-10-10 | 1 | gofumpt realigned the `TestDefaultAllowlist` map literal (whitespace only) | lint (`0 issues.` required) | n/a |
| 2026-10-10 | 2 | gofumpt split the 110-char `fakeAPI.Scores` one-liner (whitespace only) | lint | n/a |
| 2026-10-10 | 3 | gofumpt key realignment in `fakeplatform.go` (whitespace only) | lint | n/a |
| 2026-10-10 | 4 | gofumpt reflowed two `s.Log` composite literals in `identities.go` | lint | n/a |
| 2026-10-10 | 5 | gofumpt line-break reformat of the `s.Log` call in `merge.go` (whitespace only) | lint | n/a |
| 2026-10-10 | 6 | `chipWhere` renamed to `bestWhere` in `internal/service/groups.go` | gosec G101 flags "pW" inside the identifier | No later task references `chipWhere`; Plan 3 does not either |

---

## File Map

```
internal/beatleader/types.go            NEW  Player, ScorePage, Meta, Score, Leaderboard, Song, Difficulty, UnixTime
internal/beatleader/limiter.go          NEW  10 s sliding-window Limiter (api 40, cdn 20), server headers, injectable clock
internal/beatleader/client.go           NEW  Client: Player, Scores, Replay (allowlist + redirect guard), errors
internal/beatleader/platform.go         NEW  NewPlatform (registry entry), adapter, Plays conversion
internal/beatleader/hmd.go              NEW  HMDName (BeatLeader HMD codes → display names)
internal/beatleader/testdata/*.json     NEW  player.json, scores.json (trimmed live responses)
internal/beatleader/*_test.go           NEW  client, limiter, platform tests; live_test.go (-tags live)
internal/platform/platform.go               + Platform.PBOnly, PlayPage.Refused
internal/platform/links.go              NEW  PlayRef, Registry.PlayPath/ReplayPath/AccountPath/ImageHosts
internal/app/app.go                         register BeatLeader; Options.BeatLeaderURL
internal/service/service.go                 errors: generic ErrInvalidPlayerRef, account/merge errors, LinkedElsewhereError
internal/service/players.go                 per-account counts (by kind), AddPlayer via linkedElsewhere, createRequiredFeeds
internal/service/identities.go          NEW  LinkIdentity, UnlinkIdentity, SetIdentityEnabled
internal/service/merge.go               NEW  MergePlayers, ResolvePlayerID
internal/service/scores.go                  PB supersede, URLChanged; ScoreFilter.Platform/MinScore/MaxScore/MapKey; SQL filter builder
internal/service/groups.go              NEW  ListMapGroups, MapGroup, GetPlay
internal/service/events.go                  EventFilter.Platform/Feed
internal/storage/storage.go                 RemoveDir, Link
internal/archiver/poll.go                   profile via Resolve, refused/URL-changed events
internal/httpx/httpx.go                     DefaultCSP/SecurityHeaders take image hosts
internal/web/web.go                         routes; registry in request context; CSP from registry
internal/web/public.go                      merged player page, map fragment, alias 301, per-platform score pages
internal/web/replay.go                      per-platform raw replays and embeds
internal/web/admin.go                       account/merge handlers
internal/web/admin_sync.go                  queue per feed, event filters
internal/web/views/links.go             NEW  registry from context; row-based URL helpers; ViewerSrc
internal/web/views/{format,urls}.go         int64 URL helpers removed; PlayerScoresURL/MapPlaysURL
internal/web/views/{player,score,admin,sync}.templ   merged page, platform-aware score page, account menus, feed queue
internal/api/{api,dto,players,scores,identities}.go  coded problems, alias-aware paths, account + merge ops, filters, DTO fields
internal/testutil/fakeplatform.go           FakePlatform: PBOnly, Refused, named instances, second account
internal/testutil/service.go                NewMultiService, Archive, UpsertFake
README.md                                   BeatLeader, accounts, merging, API additions
```

---
## Task 1: `internal/beatleader` client — types, limiters, allowlisted replays

A typed client for the three BeatLeader calls this plan needs (spec §2.1, §5.3), with no application logic. It mirrors `internal/scoresaber/{client,limiter,types}.go`; read those first.

**Files:**
- Create: `internal/beatleader/types.go`, `internal/beatleader/limiter.go`, `internal/beatleader/client.go`
- Create: `internal/beatleader/testdata/player.json`, `internal/beatleader/testdata/scores.json`
- Test: `internal/beatleader/client_test.go`, `internal/beatleader/limiter_test.go`, `internal/beatleader/live_test.go`

**Interfaces:**
- Consumes: `platform.ErrNotFound`, `platform.ErrRateLimited`, `platform.ErrUnauthorized`, `platform.LimiterSnapshot`, `platform.WindowSnapshot` (Plan 1); `buildinfo.UserAgent()`.
- Produces (package `beatleader`):
  - `const DefaultBaseURL = "https://api.beatleader.xyz"`, `const ScoresPageSize = 100`
  - `type ReplayPrefixes struct{ CDN, Storage, Other string }`, `var DefaultReplayPrefixes`
  - `const APILimiterName = "beatleader/api"`, `const CDNLimiterName = "beatleader/cdn"`
  - `func NewAPILimiter() *Limiter`, `func NewCDNLimiter() *Limiter`; `(*Limiter).SetClock(func() time.Time)`, `Name() string`, `Ready(now time.Time) (bool, time.Time)`, `Wait(ctx) error`, `Observe(h http.Header, status int)`, `Snapshot() platform.LimiterSnapshot`
  - `func NewClient(api, cdn *Limiter, opts ...Option) *Client`; options `WithBaseURL(u string)`, `WithHTTPClient(*http.Client)`, `WithUserAgent(string)`
  - `(*Client).Player(ctx, id string) (Player, error)`, `Scores(ctx, playerID string, page int) (ScorePage, error)`, `Replay(ctx, url string) (io.ReadCloser, error)`, `ReplayAllowed(url string) bool`
  - errors `ErrNotFound`, `ErrRateLimited`, `ErrUnauthorized` (aliases of the `platform` sentinels), `ErrReplayURL`, `*StatusError`
  - types `Player{ID, Name, Avatar, Country}`, `ScorePage{Metadata Meta; Data []Score}`, `Meta{Page, ItemsPerPage, Total int}` + `(Meta).TotalPages() int`, `Score`, `Leaderboard`, `Song`, `Difficulty`, `UnixTime` + `(UnixTime).Time() time.Time`

- [ ] **Step 1: Add the fixtures**

These are the instance owner's own live responses (`GET /player/76561198038925092?stats=false` and `GET /player/76561198038925092/scores?sortBy=date&order=desc&page=1&count=100`, 2026-10-10), trimmed to the fields the client reads plus a few it must ignore. Note the second score's replay on `replays-storage`, the `null` stars and the string `timeset`.

`internal/beatleader/testdata/player.json`:

```json
{"id":"76561198038925092","name":"Yewolf","platform":"steam","avatar":"https://avatars.akamai.steamstatic.com/d8beea9cbba7bdfe53cbc618220c542d24c2d7cd_full.jpg","country":"FR","alias":null,"role":""}
```

`internal/beatleader/testdata/scores.json`:

```json
{
  "metadata": {"itemsPerPage": 100, "page": 1, "total": 3},
  "data": [
    {
      "id": 12164051, "baseScore": 825292, "modifiedScore": 825292, "accuracy": 0.797295, "pp": 181.86964,
      "rank": 1736, "modifiers": "", "badCuts": 13, "missedNotes": 16, "bombCuts": 0, "wallsHit": 0,
      "fullCombo": false, "maxCombo": 423, "hmd": 256, "controller": 0, "playerId": "76561198038925092",
      "timeset": "1706356788", "timepost": 1706356788, "leaderboardId": "1d3f5x71",
      "replay": "https://cdn.replays.beatleader.xyz/12164051-76561198038925092-Expert-Standard-57511EE48555E00E031BD3B1DF90BA7BE5712B56.bsor",
      "player": null,
      "leaderboard": {
        "id": "1d3f5x71",
        "song": {"id": "1d3f5", "hash": "57511ee48555e00e031bd3b1df90ba7be5712b56", "name": "Night Raid with a Dragon", "subName": "", "author": "Camellia", "mapper": "nolan121405", "coverImage": "https://eu.cdn.beatsaver.com/57511ee48555e00e031bd3b1df90ba7be5712b56.jpg"},
        "difficulty": {"id": 1, "value": 7, "mode": 1, "modeName": "Standard", "difficultyName": "Expert", "status": 3, "stars": 7.2064004, "maxScore": 1035115}
      }
    },
    {
      "id": 12163441, "baseScore": 609589, "modifiedScore": 609589, "accuracy": 0.850437, "pp": 0,
      "rank": 71, "modifiers": "", "badCuts": 5, "missedNotes": 4, "fullCombo": false, "maxCombo": 159, "hmd": 256,
      "timeset": "1706354762", "timepost": 1706354762, "leaderboardId": "1075671",
      "replay": "https://api.beatleader.xyz/replays-storage/12163441-76561198038925092-Expert-Standard-346AB7665240AD1C4DC4F3C946227FC738685520.bsor",
      "leaderboard": {
        "id": "1075671",
        "song": {"hash": "346ab7665240ad1c4dc4f3c946227fc738685520", "name": "MORE", "subName": "ft. Madison Beer, (G)I-DLE, Lexie Liu, Jaira Burns, Seraphine", "author": "KDA", "mapper": "Ben Records", "coverImage": "https://eu.cdn.beatsaver.com/346ab7665240ad1c4dc4f3c946227fc738685520.jpg"},
        "difficulty": {"value": 7, "modeName": "Standard", "difficultyName": "Expert", "status": 0, "stars": null, "maxScore": 716795}
      }
    },
    {
      "id": 12163193, "baseScore": 905030, "modifiedScore": 905030, "accuracy": 0.7819003, "pp": 0,
      "rank": 17, "modifiers": "DA,FS", "badCuts": 24, "missedNotes": 20, "fullCombo": false, "maxCombo": 179, "hmd": 9999,
      "timeset": "1706353905", "timepost": 1706353905, "leaderboardId": "113e791",
      "replay": null,
      "leaderboard": {
        "id": "113e791",
        "song": {"hash": "e2d0a2d90cce2daf0cc4a49f347b24ba368c07c7", "name": "Drum Go Dum", "subName": null, "author": "KDA", "mapper": "Joy", "coverImage": "https://eu.cdn.beatsaver.com/e2d0a2d90cce2daf0cc4a49f347b24ba368c07c7.jpg"},
        "difficulty": {"value": 9, "modeName": "Standard", "difficultyName": "ExpertPlus", "status": 2, "stars": 8.5, "maxScore": 1157475}
      }
    }
  ]
}
```

(The third item was edited for coverage: `modifiers`, an unknown `hmd` code, `replay: null`, `subName: null` and `status: 2`.)

- [ ] **Step 2: Write the failing client tests**

`internal/beatleader/client_test.go`:

```go
package beatleader_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/beatleader"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type env struct {
	c        *beatleader.Client
	srv      *httptest.Server
	api, cdn *beatleader.Limiter
	hits     atomic.Int64
}

func newClient(t *testing.T, h http.HandlerFunc) *env {
	t.Helper()
	e := &env{api: beatleader.NewAPILimiter(), cdn: beatleader.NewCDNLimiter()}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(e.srv.Close)
	e.c = beatleader.NewClient(e.api, e.cdn, beatleader.WithBaseURL(e.srv.URL), beatleader.WithUserAgent("test-agent"))
	return e
}

func TestPlayer(t *testing.T) {
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/76561198038925092" || r.URL.Query().Get("stats") != "false" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write(fixture(t, "player.json"))
	})
	p, err := e.c.Player(context.Background(), "76561198038925092")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "76561198038925092" || p.Name != "Yewolf" || p.Country != "FR" || !strings.HasPrefix(p.Avatar, "https://avatars.akamai.steamstatic.com/") {
		t.Fatalf("player = %+v", p)
	}
}

func TestScores(t *testing.T) {
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/player/42/scores" || q.Get("sortBy") != "date" || q.Get("order") != "desc" ||
			q.Get("page") != "2" || q.Get("count") != "100" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write(fixture(t, "scores.json"))
	})
	sp, err := e.c.Scores(context.Background(), "42", 2)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Metadata.Total != 3 || sp.Metadata.TotalPages() != 1 || len(sp.Data) != 3 {
		t.Fatalf("page = %+v", sp.Metadata)
	}
	s := sp.Data[0]
	if s.ID != 12164051 || s.BaseScore != 825292 || s.HMD != 256 || s.LeaderboardID != "1d3f5x71" ||
		!s.Timeset.Time().Equal(time.Date(2024, 1, 27, 11, 59, 48, 0, time.UTC)) {
		t.Fatalf("score = %+v", s)
	}
	lb := s.Leaderboard
	if lb.ID != "1d3f5x71" || lb.Song.Hash != "57511ee48555e00e031bd3b1df90ba7be5712b56" || lb.Difficulty.ModeName != "Standard" ||
		lb.Difficulty.Status != 3 || lb.Difficulty.Stars == nil || *lb.Difficulty.Stars != 7.2064004 {
		t.Fatalf("leaderboard = %+v", lb)
	}
	if sp.Data[1].Leaderboard.Difficulty.Stars != nil || sp.Data[2].Replay != "" || sp.Data[2].Leaderboard.Song.SubName != "" {
		t.Fatalf("nulls must decode to zero values: %+v", sp.Data[1:])
	}
}

func TestUnixTimeAcceptsNumbersStringsAndNull(t *testing.T) {
	for in, want := range map[string]int64{`"1706356788"`: 1706356788, `1706356788`: 1706356788, `null`: 0, `""`: 0} {
		var u beatleader.UnixTime
		if err := u.UnmarshalJSON([]byte(in)); err != nil || int64(u) != want {
			t.Errorf("%s → %d %v", in, u, err)
		}
	}
	var u beatleader.UnixTime
	if err := u.UnmarshalJSON([]byte(`"soon"`)); err == nil {
		t.Error("garbage must not decode")
	}
}

func TestStatusErrors(t *testing.T) {
	reset := time.Now().UTC().Add(7 * time.Second).Truncate(time.Second)
	status := map[string]int{"/player/a": 404, "/player/b": 401, "/player/c": 403, "/player/d": 429, "/player/e": 500}
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/player/d" {
			w.Header().Set("x-rate-limit-remaining", "0")
			w.Header().Set("x-rate-limit-reset", reset.Format(time.RFC3339Nano))
		}
		w.WriteHeader(status[r.URL.Path])
		_, _ = w.Write([]byte("nope"))
	})
	ctx := context.Background()
	if _, err := e.c.Player(ctx, "a"); !errors.Is(err, beatleader.ErrNotFound) {
		t.Errorf("404 → %v", err)
	}
	for _, id := range []string{"b", "c"} {
		if _, err := e.c.Player(ctx, id); !errors.Is(err, beatleader.ErrUnauthorized) {
			t.Errorf("%s → %v", id, err)
		}
	}
	if _, err := e.c.Player(ctx, "d"); !errors.Is(err, beatleader.ErrRateLimited) {
		t.Errorf("429 → %v", err)
	}
	if ok, at := e.api.Ready(time.Now()); ok || !at.Equal(reset) {
		t.Errorf("a 429 with an exhausted window must block until the reset: ok=%v at=%v want %v", ok, at, reset)
	}
	var se *beatleader.StatusError
	e2 := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if _, err := e2.c.Scores(ctx, "e", 1); !errors.As(err, &se) || se.StatusCode != 500 {
		t.Errorf("500 → %v", err)
	}
}

func TestReplayAllowlist(t *testing.T) {
	e := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cdn-replays/redirect-out.bsor":
			http.Redirect(w, r, "/elsewhere/x.bsor", http.StatusFound)
		case "/cdn-replays/redirect-in.bsor":
			http.Redirect(w, r, "/otherreplays/target.bsor", http.StatusFound)
		default:
			_, _ = w.Write([]byte("bsor:" + r.URL.Path))
		}
	})
	ctx := context.Background()
	read := func(u string) (string, error) {
		rc, err := e.c.Replay(ctx, u)
		if err != nil {
			return "", err
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		return string(b), err
	}

	for path, limiter := range map[string]*beatleader.Limiter{
		"/cdn-replays/1.bsor": e.cdn, "/replays-storage/2.bsor": e.api, "/otherreplays/3.bsor": e.api,
	} {
		before := limiter.Snapshot().Windows[0].Used
		if got, err := read(e.srv.URL + path); err != nil || got != "bsor:"+path {
			t.Errorf("%s: %q %v", path, got, err)
		}
		if limiter.Snapshot().Windows[0].Used != before+1 {
			t.Errorf("%s must consume the %s limiter", path, limiter.Name())
		}
	}

	hits := e.hits.Load()
	for _, u := range []string{
		e.srv.URL + "/elsewhere/x.bsor",
		e.srv.URL + "/cdn-replays/../player/1",
		"https://evil.example/cdn-replays/x.bsor",
		"https://cdn.replays.beatleader.xyz.evil.example/x.bsor",
		"",
	} {
		if _, err := read(u); !errors.Is(err, beatleader.ErrReplayURL) {
			t.Errorf("%q must be refused, got %v", u, err)
		}
	}
	if e.hits.Load() != hits {
		t.Fatal("refused URLs must never be requested")
	}

	if _, err := read(e.srv.URL + "/cdn-replays/redirect-out.bsor"); !errors.Is(err, beatleader.ErrReplayURL) {
		t.Errorf("a redirect off the allowlist must be refused, got %v", err)
	}
	if got, err := read(e.srv.URL + "/cdn-replays/redirect-in.bsor"); err != nil || got != "bsor:/otherreplays/target.bsor" {
		t.Errorf("a redirect inside the allowlist is followed: %q %v", got, err)
	}
}

func TestDefaultAllowlist(t *testing.T) {
	c := beatleader.NewClient(nil, nil)
	for u, want := range map[string]bool{
		"https://cdn.replays.beatleader.xyz/1-2-Expert-Standard-ABC.bsor":    true,
		"https://api.beatleader.xyz/replays-storage/1-2-Expert-Standard.bsor": true,
		"https://api.beatleader.xyz/otherreplays/34897106.bsor":              true,
		"https://api.beatleader.xyz/player/1":                                false,
		"http://cdn.replays.beatleader.xyz/1.bsor":                           false,
		"https://cdn.replays.beatleader.xyz.evil.example/1.bsor":             false,
	} {
		if c.ReplayAllowed(u) != want {
			t.Errorf("ReplayAllowed(%s) = %v", u, !want)
		}
	}
}
```

`internal/beatleader/limiter_test.go`:

```go
package beatleader_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/beatleader"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestLimiterWindow(t *testing.T) {
	l := beatleader.NewAPILimiter()
	now := t0
	l.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for range 40 {
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(100 * time.Millisecond)
	}
	if ok, at := l.Ready(now); ok || !at.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("41st request: ok=%v at=%v, want blocked until the first hit leaves the window", ok, at)
	}
	snap := l.Snapshot() // before the next Ready, which prunes the oldest hit
	if len(snap.Windows) != 1 || snap.Windows[0].Name != "short" || snap.Windows[0].Limit != 40 || snap.Windows[0].Used != 40 ||
		snap.Windows[0].ServerRemaining != -1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if ok, _ := l.Ready(t0.Add(10 * time.Second)); !ok {
		t.Fatal("ready once the oldest hit is 10s old")
	}
	if l.Name() != beatleader.APILimiterName || beatleader.NewCDNLimiter().Snapshot().Windows[0].Limit != 20 {
		t.Fatal("names and limits")
	}
}

func TestLimiterServerHeaders(t *testing.T) {
	l := beatleader.NewCDNLimiter()
	l.SetClock(func() time.Time { return t0 })
	h := http.Header{}
	h.Set("x-rate-limit-limit", "10s")
	h.Set("x-rate-limit-remaining", "12")
	h.Set("x-rate-limit-reset", "2026-10-10T12:00:04.6376402Z")
	l.Observe(h, 200)
	if ok, _ := l.Ready(t0); !ok {
		t.Fatal("remaining > 0 must not block")
	}
	if w := l.Snapshot().Windows[0]; w.ServerRemaining != 12 || !w.ServerResetAt.Equal(time.Date(2026, 10, 10, 12, 0, 4, 637640200, time.UTC)) {
		t.Fatalf("server window = %+v", w)
	}
	h.Set("x-rate-limit-remaining", "0")
	l.Observe(h, 200)
	if ok, at := l.Ready(t0); ok || !at.Equal(time.Date(2026, 10, 10, 12, 0, 4, 637640200, time.UTC)) {
		t.Fatalf("exhausted: ok=%v at=%v", ok, at)
	}

	l2 := beatleader.NewAPILimiter()
	l2.SetClock(func() time.Time { return t0 })
	l2.Observe(http.Header{}, http.StatusTooManyRequests)
	if ok, at := l2.Ready(t0); ok || !at.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("429 without headers waits one window: ok=%v at=%v", ok, at)
	}
	if l2.Snapshot().BlockedUntil.IsZero() {
		t.Fatal("snapshot must show the block")
	}
}

func TestLimiterWaitHonoursContext(t *testing.T) {
	l := beatleader.NewCDNLimiter()
	l.Observe(http.Header{}, http.StatusTooManyRequests) // blocked for 10s of real time
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("Wait must return the context error while blocked")
	}
	if l.Snapshot().Waiting {
		t.Fatal("Waiting must be cleared after a cancelled wait")
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/beatleader/`
Expected: FAIL — `package github.com/yyewolf/ssarchiver/internal/beatleader` has no non-test Go files / undefined: `beatleader.NewClient`.

- [ ] **Step 4: Implement the types**

`internal/beatleader/types.go`:

```go
// Package beatleader is a minimal client for BeatLeader's public API (spec
// §2.1, §5.3) with client-side rate limiters that mirror the server's 10 s
// window. It holds no application logic: the adapter in platform.go turns
// its payloads into neutral platform.Play values.
package beatleader

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Player struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Avatar  string `json:"avatar"`
	Country string `json:"country"`
}

type ScorePage struct {
	Metadata Meta    `json:"metadata"`
	Data     []Score `json:"data"`
}

type Meta struct {
	Page         int `json:"page"`
	ItemsPerPage int `json:"itemsPerPage"`
	Total        int `json:"total"`
}

// TotalPages is the number of pages of the listing.
func (m Meta) TotalPages() int {
	per := m.ItemsPerPage
	if per <= 0 {
		per = ScoresPageSize
	}
	return (m.Total + per - 1) / per
}

// Score is one entry of a scores listing (one per leaderboard: the current
// personal best).
type Score struct {
	ID            int64       `json:"id"`
	BaseScore     int64       `json:"baseScore"`
	ModifiedScore int64       `json:"modifiedScore"`
	Accuracy      float64     `json:"accuracy"`
	PP            float64     `json:"pp"`
	Rank          int         `json:"rank"`
	Modifiers     string      `json:"modifiers"`
	BadCuts       int         `json:"badCuts"`
	MissedNotes   int         `json:"missedNotes"`
	FullCombo     bool        `json:"fullCombo"`
	MaxCombo      int         `json:"maxCombo"`
	HMD           int         `json:"hmd"`
	Timeset       UnixTime    `json:"timeset"`
	Timepost      UnixTime    `json:"timepost"`
	LeaderboardID string      `json:"leaderboardId"`
	Replay        string      `json:"replay"` // null decodes to ""
	Leaderboard   Leaderboard `json:"leaderboard"`
}

type Leaderboard struct {
	ID         string     `json:"id"`
	Song       Song       `json:"song"`
	Difficulty Difficulty `json:"difficulty"`
}

type Song struct {
	Hash       string `json:"hash"` // lowercase
	Name       string `json:"name"`
	SubName    string `json:"subName"`
	Author     string `json:"author"`
	Mapper     string `json:"mapper"`
	CoverImage string `json:"coverImage"`
}

type Difficulty struct {
	Value          int      `json:"value"`    // 1..9, same scale as ScoreSaber
	ModeName       string   `json:"modeName"` // "Standard", "OneSaber", …
	DifficultyName string   `json:"difficultyName"`
	Status         int      `json:"status"` // unranked(0) nominated(1) qualified(2) ranked(3) …
	Stars          *float64 `json:"stars"`
	MaxScore       int64    `json:"maxScore"`
}

// UnixTime is a unix-seconds timestamp that BeatLeader sends as a number or as
// a numeric string (scores' timeset); null and "" decode to zero.
type UnixTime int64

func (u *UnixTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*u = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("beatleader: bad timestamp %s: %w", b, err)
	}
	*u = UnixTime(n)
	return nil
}

// Time is the timestamp in UTC (zero time.Time for zero).
func (u UnixTime) Time() time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(int64(u), 0).UTC()
}
```

- [ ] **Step 5: Implement the limiter**

`internal/beatleader/limiter.go`:

```go
package beatleader

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/platform"
)

// Limiter names (spec §5.3): one per host class.
const (
	APILimiterName = "beatleader/api"
	CDNLimiterName = "beatleader/cdn"
)

// window is BeatLeader's rate-limit window (x-rate-limit-limit: 10s).
const window = 10 * time.Second

// Limiter is a sliding-window client-side limiter for one BeatLeader host
// class. It also honours the server's x-rate-limit-remaining/-reset headers.
type Limiter struct {
	mu              sync.Mutex
	name            string
	limit           int
	now             func() time.Time
	hits            []time.Time
	serverRemaining int
	serverResetAt   time.Time
	blockedUntil    time.Time
	waitUntil       time.Time
}

// NewAPILimiter limits api.beatleader.xyz: 40 requests per 10 s, headroom
// under the 50 observed.
func NewAPILimiter() *Limiter { return newLimiter(APILimiterName, 40) }

// NewCDNLimiter limits cdn.replays.beatleader.xyz downloads: 20 per 10 s.
func NewCDNLimiter() *Limiter { return newLimiter(CDNLimiterName, 20) }

func newLimiter(name string, limit int) *Limiter {
	return &Limiter{name: name, limit: limit, now: time.Now, serverRemaining: -1}
}

// SetClock replaces the time source (tests).
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

func (l *Limiter) Name() string { return l.name }

// prune drops hits older than the window; l.mu must be held.
func (l *Limiter) prune(now time.Time) {
	i := 0
	for i < len(l.hits) && !l.hits[i].Add(window).After(now) {
		i++
	}
	l.hits = l.hits[i:]
}

// nextAllowed is when the next request may go out; l.mu must be held.
func (l *Limiter) nextAllowed(now time.Time) time.Time {
	l.prune(now)
	until := l.blockedUntil
	if len(l.hits) >= l.limit {
		if t := l.hits[0].Add(window); t.After(until) {
			until = t
		}
	}
	return until
}

// Ready reports whether Wait would return at once at now, and otherwise when it could.
func (l *Limiter) Ready(now time.Time) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := l.nextAllowed(now); until.After(now) {
		return false, until
	}
	return true, time.Time{}
}

// Wait blocks until a request may be sent, then records it.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.now()
		until := l.nextAllowed(now)
		if !until.After(now) {
			l.hits = append(l.hits, now)
			l.waitUntil = time.Time{}
			l.mu.Unlock()
			return nil
		}
		l.waitUntil = until
		l.mu.Unlock()

		timer := time.NewTimer(until.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			l.mu.Lock()
			l.waitUntil = time.Time{}
			l.mu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Observe records BeatLeader's rate-limit headers. An exhausted window or a
// 429 blocks until x-rate-limit-reset (Retry-After or one window when absent).
func (l *Limiter) Observe(h http.Header, status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var reset time.Time
	if v := h.Get("x-rate-limit-reset"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			reset = t.UTC()
			l.serverResetAt = reset
		}
	}
	if v := h.Get("x-rate-limit-remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			l.serverRemaining = n
			if n <= 0 && reset.After(l.blockedUntil) {
				l.blockedUntil = reset
			}
		}
	}
	if status == http.StatusTooManyRequests && !l.blockedUntil.After(now) {
		until := now.Add(window)
		if reset.After(now) {
			until = reset
		}
		if ra, err := strconv.Atoi(h.Get("Retry-After")); err == nil && ra > 0 {
			until = now.Add(time.Duration(ra) * time.Second)
		}
		l.blockedUntil = until
	}
}

// Snapshot returns the current usage for display.
func (l *Limiter) Snapshot() platform.LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	s := platform.LimiterSnapshot{
		Waiting: !l.waitUntil.IsZero(), WaitUntil: l.waitUntil,
		Windows: []platform.WindowSnapshot{{
			Name: "short", Limit: l.limit, Used: len(l.hits), Period: window,
			ServerRemaining: l.serverRemaining, ServerResetAt: l.serverResetAt,
		}},
	}
	if l.blockedUntil.After(now) {
		s.BlockedUntil = l.blockedUntil
	}
	return s
}
```

(The window is named `short` so the sync page's existing `windowLabel` shows "Last 10 seconds".)

- [ ] **Step 6: Implement the client**

`internal/beatleader/client.go`:

```go
package beatleader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

const (
	DefaultBaseURL = "https://api.beatleader.xyz"
	ScoresPageSize = 100
)

// ReplayPrefixes are the only URL prefixes replays are fetched from (spec §5.3).
type ReplayPrefixes struct {
	CDN     string // score replays on the CDN (CDN limiter)
	Storage string // score replays served by the API host (API limiter)
	Other   string // attempt replays served by the API host (API limiter)
}

var DefaultReplayPrefixes = ReplayPrefixes{
	CDN:     "https://cdn.replays.beatleader.xyz/",
	Storage: "https://api.beatleader.xyz/replays-storage/",
	Other:   "https://api.beatleader.xyz/otherreplays/",
}

var (
	ErrNotFound     = platform.ErrNotFound
	ErrRateLimited  = platform.ErrRateLimited
	ErrUnauthorized = platform.ErrUnauthorized
	ErrReplayURL    = errors.New("beatleader: replay URL not on the allowlist")
)

// StatusError is returned for unexpected non-2xx responses.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("beatleader: unexpected status %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL   string
	prefixes  ReplayPrefixes
	hc        *http.Client
	api, cdn  *Limiter
	userAgent string
}

type Option func(*Client)

// WithBaseURL points the client at another server. The replay prefixes move
// under it too ({u}/cdn-replays/, {u}/replays-storage/, {u}/otherreplays/) so
// one fake server can stand in for every BeatLeader host in tests.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = u
		c.prefixes = ReplayPrefixes{CDN: u + "/cdn-replays/", Storage: u + "/replays-storage/", Other: u + "/otherreplays/"}
	}
}

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// NewClient returns a client whose API calls go through api and CDN replay
// downloads through cdn (nil limiters: not rate limited).
func NewClient(api, cdn *Limiter, opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL, prefixes: DefaultReplayPrefixes,
		hc:  &http.Client{Timeout: 2 * time.Minute},
		api: api, cdn: cdn, userAgent: buildinfo.UserAgent(),
	}
	for _, o := range opts {
		o(c)
	}
	hc := *c.hc // never mutate the caller's client
	hc.CheckRedirect = c.checkRedirect
	c.hc = &hc
	return c
}

// checkRedirect only follows redirects that stay on the replay allowlist.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("beatleader: too many redirects")
	}
	if !c.ReplayAllowed(req.URL.String()) {
		return fmt.Errorf("%w: redirect to %s", ErrReplayURL, req.URL.Redacted())
	}
	return nil
}

// ReplayAllowed reports whether u is under one of the replay prefixes.
func (c *Client) ReplayAllowed(u string) bool {
	for _, p := range []string{c.prefixes.CDN, c.prefixes.Storage, c.prefixes.Other} {
		if p != "" && strings.HasPrefix(u, p) && !strings.Contains(u[len(p):], "..") {
			return true
		}
	}
	return false
}

// Player fetches a player's profile. BeatLeader maps alternate account IDs to
// the main one: store the returned ID, not the one asked for.
func (c *Client) Player(ctx context.Context, id string) (Player, error) {
	var p Player
	err := c.getJSON(ctx, "/player/"+url.PathEscape(id), url.Values{"stats": {"false"}}, &p)
	return p, err
}

// Scores fetches one page (newest first) of a player's scores: one per
// leaderboard, the current personal best. 404 for unknown players and for
// players without scores.
func (c *Client) Scores(ctx context.Context, playerID string, page int) (ScorePage, error) {
	var sp ScorePage
	q := url.Values{
		"sortBy": {"date"}, "order": {"desc"},
		"page": {strconv.Itoa(page)}, "count": {strconv.Itoa(ScoresPageSize)},
	}
	err := c.getJSON(ctx, "/player/"+url.PathEscape(playerID)+"/scores", q, &sp)
	return sp, err
}

// Replay streams a replay from an allowlisted URL. The caller closes the body.
func (c *Client) Replay(ctx context.Context, u string) (io.ReadCloser, error) {
	if !c.ReplayAllowed(u) {
		return nil, fmt.Errorf("%w: %q", ErrReplayURL, u)
	}
	l := c.api
	if strings.HasPrefix(u, c.prefixes.CDN) {
		l = c.cdn
	}
	resp, err := c.do(ctx, l, u)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) do(ctx context.Context, l *Limiter, u string) (*http.Response, error) {
	if l != nil {
		if err := l.Wait(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("beatleader: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	path := req.URL.Path
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("beatleader: GET %s: %w", path, err)
	}
	if l != nil {
		l.Observe(resp.Header, resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return resp, nil
	case http.StatusNotFound:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	case http.StatusUnauthorized, http.StatusForbidden:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, path)
	case http.StatusTooManyRequests:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, path)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, &StatusError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, c.api, c.baseURL+path+"?"+q.Encode())
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("beatleader: decode %s: %w", path, err)
	}
	return nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test -race ./internal/beatleader/`
Expected: PASS.

- [ ] **Step 8: Add the live smoke test**

`internal/beatleader/live_test.go`:

```go
//go:build live

package beatleader_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/beatleader"
)

// Run manually: go test -tags live -run Live ./internal/beatleader
func TestLiveBeatLeader(t *testing.T) {
	c := beatleader.NewClient(beatleader.NewAPILimiter(), beatleader.NewCDNLimiter())
	ctx := context.Background()
	const livePlayerID = "76561198038925092" // instance owner's profile
	p, err := c.Player(ctx, livePlayerID)
	if err != nil || p.ID != livePlayerID || p.Name == "" {
		t.Fatalf("player: %+v %v", p, err)
	}
	page, err := c.Scores(ctx, p.ID, 1)
	if err != nil || len(page.Data) == 0 {
		t.Fatalf("scores: %v", err)
	}
	s := page.Data[0]
	if s.Leaderboard.Song.Hash == "" || s.Timeset.Time().IsZero() || !c.ReplayAllowed(s.Replay) {
		t.Fatalf("first score: %+v", s)
	}
	rc, err := c.Replay(ctx, s.Replay)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	defer rc.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(rc, head); err != nil || !bytes.Equal(head, []byte{0x69, 0x3d, 0x2d, 0x44}) {
		t.Fatalf("not a BSOR file: % x %v", head, err)
	}
}
```

Run: `go vet -tags live ./internal/beatleader/` (compiles the live test without running it).
Expected: no output.

- [ ] **Step 9: Lint and commit**

Run: `make lint && go test ./...`
Expected: `0 issues.`; all packages ok.

```bash
git add internal/beatleader
git commit -m "feat(beatleader): add a typed BeatLeader client with rate limiters and a replay allowlist"
```

---
## Task 2: BeatLeader adapter + registry entry + app wiring

BeatLeader becomes a registered platform: its adapter turns score pages into neutral plays (spec §4.5–4.6), treats a scores 404 as the end of the listing (spec §5.2), and only offers allowlisted replay URLs. Two small neutral fields are added to `internal/platform` for it: `Platform.PBOnly` (used by Task 3) and `PlayPage.Refused` (logged by Task 3).

**Files:**
- Modify: `internal/platform/platform.go` (`Platform.PBOnly`, `PlayPage.Refused`)
- Create: `internal/beatleader/platform.go`, `internal/beatleader/hmd.go`
- Modify: `internal/app/app.go` (register BeatLeader, `Options.BeatLeaderURL`)
- Test: `internal/beatleader/platform_test.go`, `internal/app/app_test.go`

**Interfaces:**
- Consumes: Task 1 `beatleader.Client` methods, `Limiter`, `Score`, `ScorePage`, `ErrNotFound`; Plan 1 `platform.Platform`, `platform.Adapter`, `platform.MapKey`, `model.Kind*`/`End*`/`AccessNA`.
- Produces:
  - `platform.Platform.PBOnly bool` — the score feed lists current personal bests only
  - `platform.PlayPage.Refused int` — plays whose replay URL was not allowlisted (kept without a replay)
  - `beatleader.Name = "beatleader"`; `beatleader.API` interface (`Player`, `Scores`, `Replay`, `ReplayAllowed`)
  - `func beatleader.NewPlatform(api API, apiL, cdnL *Limiter) platform.Platform`
  - `func beatleader.Plays(items []Score, allowed func(string) bool) ([]platform.Play, int)`
  - `func beatleader.HMDName(code int) string`
  - `app.Options.BeatLeaderURL string` (tests point it at a fake server)

- [ ] **Step 1: Add the neutral fields**

`internal/platform/platform.go` — in `PlayPage`:

```go
// PlayPage is one page of a feed, newest first.
type PlayPage struct {
	Plays      []Play
	TotalPages int
	Refused    int // plays whose replay URL is not on the platform's allowlist; kept without a replay
}
```

and in `Platform`, after `Legacy`:

```go
	PBOnly      bool // the score feed lists current personal bests only: older rows lose personal_best (spec §4.6)
```

- [ ] **Step 2: Write the failing adapter tests**

`internal/beatleader/platform_test.go`:

```go
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
	player    beatleader.Player
	page      beatleader.ScorePage
	err       error
	replayURL string
}

func (f *fakeAPI) Player(context.Context, string) (beatleader.Player, error) { return f.player, f.err }

func (f *fakeAPI) Scores(context.Context, string, int) (beatleader.ScorePage, error) { return f.page, f.err }

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
	if _, err := a.FeedPage(ctx, model.KindAttempt, "1", 1); err == nil {
		t.Fatal("no attempts feed in this plan")
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
	if f, ok := bl.Feed(model.KindScore); !ok || f.Optional || len(bl.Feeds) != 1 {
		t.Fatalf("feeds = %+v", bl.Feeds)
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
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/beatleader/`
Expected: FAIL — undefined: `beatleader.Plays`, `beatleader.NewPlatform`, `beatleader.HMDName`, `beatleader.Name`.

- [ ] **Step 4: Add the HMD names**

`internal/beatleader/hmd.go` (codes from BeatLeader's swagger `HMD` enum, 2026-10-10):

```go
package beatleader

import "strconv"

var hmdNames = map[int]string{
	0: "Unknown", 1: "Rift", 2: "Vive", 4: "Vive Pro", 8: "WMR", 16: "Rift S", 32: "Quest",
	33: "Pico Neo 3", 34: "Pico Neo 2", 35: "Vive Pro 2", 36: "Vive Elite", 37: "Miramar",
	38: "Pimax 8K", 39: "Pimax 5K", 40: "Pimax Artisan", 41: "HP Reverb", 42: "Samsung WMR",
	43: "Qiyu Dream", 44: "Disco", 45: "Lenovo Explorer", 46: "Acer WMR", 47: "Vive Focus",
	48: "Arpara", 49: "Dell Visor", 50: "E3", 51: "Vive DVT", 52: "Glasses 2.0", 53: "Hedy",
	54: "Vaporeon", 55: "Huawei VR", 56: "Asus WMR", 57: "CloudXR", 58: "VRidge", 59: "Medion",
	60: "Pico Neo 4", 61: "Quest Pro", 62: "Pimax Crystal", 63: "E4", 64: "Valve Index",
	65: "Controllable", 66: "Bigscreen Beyond", 67: "Nolo Sonic", 68: "Hypereal", 69: "Varjo Aero",
	70: "PS VR2", 71: "MeganeX", 72: "Varjo XR-3", 73: "MeganeX Superlight", 74: "Somnium VR1",
	75: "Steam Frame", 128: "Vive Cosmos", 256: "Quest 2", 512: "Quest 3", 513: "Quest 3S",
}

// HMDName is the display name of a BeatLeader HMD code; unknown codes are
// kept as their number (spec §4.6).
func HMDName(code int) string {
	if n, ok := hmdNames[code]; ok {
		return n
	}
	return strconv.Itoa(code)
}
```

- [ ] **Step 5: Implement the adapter**

`internal/beatleader/platform.go`:

```go
package beatleader

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// Name is BeatLeader's registry name, stored in the database.
const Name = "beatleader"

// API is the part of the client the adapter uses (*Client implements it;
// tests pass fakes).
type API interface {
	Player(ctx context.Context, id string) (Player, error)
	Scores(ctx context.Context, playerID string, page int) (ScorePage, error)
	Replay(ctx context.Context, url string) (io.ReadCloser, error)
	ReplayAllowed(url string) bool
}

var (
	profileURLRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?beatleader\.(?:com|xyz)/u/([A-Za-z0-9_.-]{1,64})/?(?:[?#].*)?$`)
	idRe         = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// NewPlatform is BeatLeader's registry entry (spec §5.3). Nil limiters
// (tests) mean no client-side rate limiting.
func NewPlatform(api API, apiL, cdnL *Limiter) platform.Platform {
	return platform.Platform{
		Name: Name, Slug: "bl", DisplayName: "BeatLeader", Priority: 10, PBOnly: true, ReplayExt: ".bsor",
		ImageHosts: []string{
			"https://cdn.assets.beatleader.xyz", "https://cdn.beatsaver.com", "https://*.cdn.beatsaver.com",
			"https://avatars.akamai.steamstatic.com", "https://avatars.steamstatic.com",
		},
		ProfileURL: func(id string) string { return "https://beatleader.com/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := profileURLRe.FindStringSubmatch(strings.TrimSpace(in))
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: idRe.MatchString,
		Feeds:   []platform.FeedSpec{{Kind: model.KindScore}},
		Adapter: adapter{api: api, apiL: apiL, cdnL: cdnL},
	}
}

type adapter struct {
	api        API
	apiL, cdnL *Limiter
}

// Resolve looks a player up; the profile carries BeatLeader's canonical ID.
func (a adapter) Resolve(ctx context.Context, id string) (platform.Profile, error) {
	p, err := a.api.Player(ctx, id)
	if err != nil {
		return platform.Profile{}, err
	}
	return platform.Profile{ExternalID: p.ID, Name: p.Name, AvatarURL: p.Avatar, Country: p.Country}, nil
}

func (a adapter) FeedPage(ctx context.Context, kind, externalID string, page int) (platform.PlayPage, error) {
	if kind != model.KindScore {
		return platform.PlayPage{}, fmt.Errorf("beatleader: no %s feed", kind)
	}
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
}

func (adapter) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessNA, 0, nil
}

func (a adapter) Replay(ctx context.Context, ref platform.ReplayRef) (io.ReadCloser, error) {
	if ref.URL == "" {
		return nil, fmt.Errorf("beatleader: %s %s has no replay URL", ref.Kind, ref.ExternalID)
	}
	return a.api.Replay(ctx, ref.URL)
}

func (a adapter) Limiters() []platform.Limiter {
	var out []platform.Limiter
	for _, l := range []*Limiter{a.apiL, a.cdnL} {
		if l != nil {
			out = append(out, l)
		}
	}
	return out
}

// FeedLimiter: listings hit the API host.
func (a adapter) FeedLimiter(string) string { return a.apiName() }

// ReplayLimiter: most replay downloads hit the API host (replays-storage,
// otherreplays); the CDN limiter's Wait covers the rest (spec §5.3).
func (a adapter) ReplayLimiter(string) string { return a.apiName() }

func (a adapter) apiName() string {
	if a.apiL == nil {
		return ""
	}
	return a.apiL.Name()
}

// Plays converts a scores page into neutral plays (spec §4.5, §4.6). A replay
// whose URL is not allowlisted is dropped — the play is kept without one —
// and counted in the second result.
func Plays(items []Score, allowed func(string) bool) ([]platform.Play, int) {
	out := make([]platform.Play, 0, len(items))
	refused := 0
	for _, s := range items {
		pl := play(s)
		pl.Kind, pl.EndType, pl.PersonalBest = model.KindScore, model.EndClear, true
		if s.Replay != "" {
			if allowed(s.Replay) {
				pl.HasReplay, pl.ReplayURL = true, s.Replay
			} else {
				refused++
			}
		}
		out = append(out, pl)
	}
	return out, refused
}

// play maps the fields scores and attempts share.
func play(s Score) platform.Play {
	lb, d := s.Leaderboard, s.Leaderboard.Difficulty
	stars := 0.0
	if d.Stars != nil {
		stars = *d.Stars
	}
	return platform.Play{
		Leaderboard: platform.LeaderboardData{
			ExternalID: cmp.Or(lb.ID, s.LeaderboardID), SongHash: lb.Song.Hash, SongName: lb.Song.Name,
			SongSubName: lb.Song.SubName, SongAuthor: lb.Song.Author, Mapper: lb.Song.Mapper,
			Difficulty: d.Value, DifficultyRaw: d.DifficultyName, GameMode: d.ModeName, CoverURL: lb.Song.CoverImage,
			Status: status(d.Status), Stars: stars, MaxScore: d.MaxScore,
		},
		ExternalID: strconv.FormatInt(s.ID, 10), Rank: s.Rank, ModifiedScore: s.ModifiedScore,
		UnmodifiedScore: s.BaseScore, Accuracy: s.Accuracy, PP: s.PP, Mods: s.Modifiers, FullCombo: s.FullCombo,
		MissedNotes: s.MissedNotes, BadCuts: s.BadCuts, MaxCombo: s.MaxCombo, HMD: HMDName(s.HMD),
		SetAt: cmp.Or(s.Timeset, s.Timepost).Time(),
	}
}

// status maps BeatLeader's difficulty status onto the stored vocabulary.
func status(v int) string {
	switch v {
	case 3:
		return "RANKED"
	case 2:
		return "QUALIFIED"
	}
	return "UNRANKED"
}
```

- [ ] **Step 6: Run the package tests**

Run: `go test -race ./internal/beatleader/`
Expected: PASS.

- [ ] **Step 7: Write the failing app wiring test**

Append to `internal/app/app_test.go` (add `"slices"` to its imports):

```go
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
```

Run: `go test ./internal/app/ -run TestBeatLeaderRegistered`
Expected: FAIL — `BeatLeader not registered`.

- [ ] **Step 8: Register BeatLeader**

`internal/app/app.go` — add the import `"github.com/yyewolf/ssarchiver/internal/beatleader"`, extend `Options`:

```go
type Options struct {
	ScoreSaberURL string // tests point these at fake servers
	BeatLeaderURL string
}
```

and replace the registry construction in `New`:

```go
	limiter := scoresaber.NewLimiter(cfg.HourlyBudget)
	var copts []scoresaber.Option
	if opts.ScoreSaberURL != "" {
		copts = append(copts, scoresaber.WithBaseURL(opts.ScoreSaberURL))
	}
	blAPI, blCDN := beatleader.NewAPILimiter(), beatleader.NewCDNLimiter()
	var bopts []beatleader.Option
	if opts.BeatLeaderURL != "" {
		bopts = append(bopts, beatleader.WithBaseURL(opts.BeatLeaderURL))
	}
	reg, err := platform.NewRegistry(
		scoresaber.NewPlatform(scoresaber.NewClient(limiter, copts...), limiter),
		beatleader.NewPlatform(beatleader.NewClient(blAPI, blCDN, bopts...), blAPI, blCDN),
	)
```

- [ ] **Step 9: Run everything**

Run: `go test -race ./... && make lint`
Expected: all packages ok (`TestEndToEnd` and `TestUpgradeFromV1DataDir` are unaffected: no BeatLeader player exists, so no BeatLeader call is made); `0 issues.`

- [ ] **Step 10: Commit**

```bash
git add internal/platform/platform.go internal/beatleader internal/app
git commit -m "feat(beatleader): register BeatLeader as a platform"
```

---
## Task 3: Generic play semantics — PB supersede, kept archives, refused replays, profile via `Resolve`

Four platform-neutral rules from spec §4.6, §5.1 and §5.4, all exercised through the fake third platform:

1. On a platform with `PBOnly`, a new score row clears `personal_best` on the player's older score rows of the same leaderboard (BeatLeader lists only the current PB; the superseded row keeps its archived replay).
2. An archived replay's URL is never replaced; when the platform reports a different one, the upsert counts it and the worker logs a `warn`.
3. Plays whose replay URL was refused by the adapter (`PlayPage.Refused`) are logged as a `warn`.
4. On page 1 of a score feed the worker refreshes the display profile from the payload, or — when no play carries one (BeatLeader) — from `Adapter.Resolve`. A `Resolve` 404 disables that account like a listing 404 does.

**Files:**
- Modify: `internal/service/scores.go` (`UpsertResult.URLChanged`, `supersede`, `refreshPlay`)
- Modify: `internal/archiver/poll.go` (`profile`, `logPageNotes`)
- Modify: `internal/testutil/fakeplatform.go` (`PBOnly`, `Refused`, second account `def`)
- Modify: `internal/testutil/service.go` (`NewMultiService`, `UpsertFake`, `Row`, `Archive`)
- Test: `internal/service/scores_test.go`, `internal/archiver/semantics_test.go` (new)

**Interfaces:**
- Consumes: Task 2 `platform.Platform.PBOnly`, `platform.PlayPage.Refused`; Plan 1 `service.UpsertPlays`, `RefreshProfile`, `MarkIdentityError`, `testutil.FakePlatform`, `testutil.FakePlay`, `testutil.NewServiceWith`.
- Produces:
  - `service.UpsertResult{New, Known, NewReplays, URLChanged int}`
  - `testutil.FakePlatform.PBOnly bool`, `.Refused int`; account `"def"` ("Dee") in `NewFakePlatform().Profiles`
  - `func testutil.NewMultiService(t) (*service.Service, *testutil.FakePlatform, *testutil.Clock)` — ScoreSaber (fake resolver with `DefaultPlayers`) + `testplat`
  - `func testutil.UpsertFake(t, svc, playerID string, plays ...platform.Play) service.UpsertResult` (platform `testplat`)
  - `func testutil.Row(t, svc, playerID, externalID string) *model.Score` (a player's row by platform ID, any platform)
  - `func testutil.Archive(t, svc, sc *model.Score, body string)` (stores the file and marks the row archived)
  - worker events: `warn`/`replay` "N replays skipped: their URL is not on the {Platform} allowlist"; `warn`/`replay` "{Platform} now reports a different replay URL for N archived {scores}; the archived copies are kept"

- [ ] **Step 1: Extend the test helpers**

`internal/testutil/fakeplatform.go` — add two fields to `FakePlatform` (after `Limiter`):

```go
	PBOnly  bool // registry PBOnly; set before calling Platform()
	Refused int  // reported as PlayPage.Refused on every page
```

add the second account to `NewFakePlatform`'s `Profiles`:

```go
		Profiles: map[string]platform.Profile{
			"abc": {ExternalID: "abc", Name: "Tess", Country: "SE", AvatarURL: "https://img.tp.example/abc.png"},
			"def": {ExternalID: "def", Name: "Dee", Country: "NO", AvatarURL: "https://img.tp.example/def.png"},
		},
```

set `PBOnly: f.PBOnly,` in the `platform.Platform` literal of `Platform()`, and return the refused count from `FeedPage`:

```go
	return platform.PlayPage{Plays: out, TotalPages: total, Refused: f.Refused}, nil
```

Append to `internal/testutil/service.go` (imports: add `"strings"`, `"github.com/yyewolf/ssarchiver/internal/platform"` is already there):

```go
// NewMultiService is NewService with the fake third platform ("testplat",
// slug "tp") registered next to ScoreSaber.
func NewMultiService(t testing.TB) (*service.Service, *FakePlatform, *Clock) {
	t.Helper()
	fp := NewFakePlatform()
	svc, _, clk := NewServiceWith(t, scoresaber.NewPlatform(&Resolver{Players: DefaultPlayers()}, nil), fp.Platform())
	return svc, fp, clk
}

// UpsertFake stores testplat plays for a player.
func UpsertFake(t testing.TB, svc *service.Service, playerID string, plays ...platform.Play) service.UpsertResult {
	t.Helper()
	res, err := svc.UpsertPlays(context.Background(), playerID, "testplat", plays)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Row returns a player's row by its platform ID (any platform and kind).
func Row(t testing.TB, svc *service.Service, playerID, externalID string) *model.Score {
	t.Helper()
	list, err := svc.ListScores(context.Background(), service.ScoreFilter{PlayerID: playerID, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range list.Items {
		if sc.ExternalID == externalID {
			return sc
		}
	}
	t.Fatalf("player %s has no row %s", playerID, externalID)
	return nil
}

// Archive stores body as the row's replay file and marks the row archived.
func Archive(t testing.TB, svc *service.Service, sc *model.Score, body string) {
	t.Helper()
	size, sum, err := svc.PutReplay(sc, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkReplayArchived(context.Background(), sc.ID, size, sum); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Write the failing service tests**

Append to `internal/service/scores_test.go`:

```go
func TestUpsertPBOnlySupersedesOlderScores(t *testing.T) {
	fp := testutil.NewFakePlatform()
	fp.PBOnly = true
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(&testutil.Resolver{Players: testutil.DefaultPlayers()}, nil), fp.Platform())
	ctx := context.Background()
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	other := testutil.FakePlay(model.KindScore, "s2", "lb-b", testutil.T0.Add(-90*time.Minute), false)
	testutil.UpsertFake(t, svc, tess, testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-2*time.Hour), true), other)
	testutil.Archive(t, svc, testutil.Row(t, svc, tess, "s1"), "old pb")
	// The platform improved lb-a: its listing now shows s3 instead of s1.
	res := testutil.UpsertFake(t, svc, tess, testutil.FakePlay(model.KindScore, "s3", "lb-a", testutil.T0.Add(-time.Hour), true), other)
	if res.New != 1 || res.Known != 1 {
		t.Fatalf("result = %+v", res)
	}
	s1, s2, s3 := testutil.Row(t, svc, tess, "s1"), testutil.Row(t, svc, tess, "s2"), testutil.Row(t, svc, tess, "s3")
	if s1.PersonalBest || !s2.PersonalBest || !s3.PersonalBest {
		t.Fatalf("pb flags: s1=%v s2=%v s3=%v", s1.PersonalBest, s2.PersonalBest, s3.PersonalBest)
	}
	if s1.ReplayState != model.ReplayArchived {
		t.Fatal("the superseded score keeps its archived replay")
	}

	// A platform that is not PB-only keeps the flag it reports.
	alice := mustAdd(t, svc, "1001")
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0.Add(-2*time.Hour), true))
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 2, 501, testutil.T0.Add(-time.Hour), true))
	if sc, _ := svc.GetScore(ctx, 1); !sc.PersonalBest {
		t.Fatal("ScoreSaber rows must not be superseded")
	}
}

func TestUpsertKeepsArchivedReplayURL(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	tess := mustAdd(t, svc, "https://tp.example/u/abc")
	archived := testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0, true)
	pending := testutil.FakePlay(model.KindScore, "s2", "lb-b", testutil.T0, true)
	testutil.UpsertFake(t, svc, tess, archived, pending)
	testutil.Archive(t, svc, testutil.Row(t, svc, tess, "s1"), "first")

	archived.ReplayURL = "https://tp.example/replays/moved1.tpr"
	pending.ReplayURL = "https://tp.example/replays/moved2.tpr"
	res := testutil.UpsertFake(t, svc, tess, archived, pending)
	if res.Known != 2 || res.URLChanged != 1 {
		t.Fatalf("result = %+v", res)
	}
	if sc := testutil.Row(t, svc, tess, "s1"); *sc.ReplayURL != "https://tp.example/replays/s1.tpr" || sc.ReplayState != model.ReplayArchived {
		t.Fatalf("archived row must keep its URL: %+v", sc)
	}
	if sc := testutil.Row(t, svc, tess, "s2"); *sc.ReplayURL != "https://tp.example/replays/moved2.tpr" {
		t.Fatalf("a pending row follows the new URL: %v", *sc.ReplayURL)
	}
}
```

Run: `go test ./internal/service/ -run 'TestUpsertPBOnly|TestUpsertKeepsArchived'`
Expected: FAIL — `res.URLChanged undefined`, then (once it compiles) `pb flags: s1=true`.

- [ ] **Step 3: Implement the upsert rules**

`internal/service/scores.go`:

```go
type UpsertResult struct{ New, Known, NewReplays, URLChanged int }
```

In `UpsertPlays`, right after the `CreateInBatches(fresh, 100)` block (still inside the transaction):

```go
		if p.PBOnly {
			if err := supersede(ctx, tx, fresh); err != nil {
				return err
			}
		}
```

Add:

```go
// supersede clears personal_best on a player's older scores of the same
// leaderboard when a PB-only platform lists a new score there (spec §4.6).
func supersede(ctx context.Context, tx *query.Query, fresh []*model.Score) error {
	q := tx.Score
	for _, r := range fresh {
		if r.Kind != model.KindScore {
			continue
		}
		if _, err := q.WithContext(ctx).Where(
			q.PlayerID.Eq(r.PlayerID), q.Platform.Eq(r.Platform), q.Kind.Eq(model.KindScore),
			q.LeaderboardID.Eq(r.LeaderboardID), q.ID.Neq(r.ID), q.SetAt.Lt(r.SetAt), q.PersonalBest.Is(true),
		).Update(q.PersonalBest, false); err != nil {
			return fmt.Errorf("supersede scores: %w", err)
		}
	}
	return nil
}
```

In `refreshPlay`, replace the `replay_url` block with:

```go
	if pl.ReplayURL != "" && old.ReplayState != model.ReplayArchived {
		upd["replay_url"] = pl.ReplayURL
	}
	if pl.ReplayURL != "" && old.ReplayState == model.ReplayArchived && old.ReplayURL != nil && *old.ReplayURL != pl.ReplayURL {
		res.URLChanged++ // the archive is never replaced (spec §5.4); the worker logs it
	}
```

Run: `go test ./internal/service/`
Expected: PASS.

- [ ] **Step 4: Write the failing worker tests**

`internal/archiver/semantics_test.go`:

```go
package archiver_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// tpEnv is a worker over ScoreSaber (fake client) and the fake third platform.
type tpEnv struct {
	svc *service.Service
	fc  *fakeClient
	fp  *testutil.FakePlatform
	w   *archiver.Worker
	clk *testutil.Clock
}

func newTPEnv(t *testing.T) *tpEnv {
	t.Helper()
	fc, fp := newFake(), testutil.NewFakePlatform()
	svc, _, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil), fp.Platform())
	return &tpEnv{svc: svc, fc: fc, fp: fp, w: archiver.New(svc), clk: clk}
}

func (e *tpEnv) drain(t *testing.T) {
	t.Helper()
	for range 100 {
		did, err := e.w.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
	}
	t.Fatal("worker still busy after 100 steps")
}

func (e *tpEnv) events(t *testing.T, kind string) string {
	t.Helper()
	evs, _, err := e.svc.ListEvents(context.Background(), service.EventFilter{Kind: kind, PerPage: 200})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		b.WriteString(ev.Level + ": " + ev.Message + "\n")
	}
	return b.String()
}

func TestPollRefreshesProfileViaResolve(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.Profiles["abc"] = platform.Profile{ExternalID: "abc", Name: "Tess Renamed", Country: "SE"}
	e.fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), false))
	e.drain(t)
	if p, _ := e.svc.GetPlayer(context.Background(), tess); p.Name != "Tess Renamed" {
		t.Fatalf("listings without a profile refresh it through Resolve, name = %q", p.Name)
	}
}

func TestPollResolveNotFoundDisablesAccount(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	delete(e.fp.Profiles, "abc")
	e.drain(t)
	sum, err := e.svc.GetPlayerSummary(context.Background(), tess)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Enabled || sum.Identities[0].Enabled || !strings.Contains(sum.Identities[0].LastError, "not found on TestPlat") {
		t.Fatalf("a vanished player disables the account only: %+v", sum.Identities[0])
	}
}

func TestRefusedReplaysAreLogged(t *testing.T) {
	e := newTPEnv(t)
	testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	e.fp.Refused = 2
	e.fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), false))
	e.drain(t)
	if got := e.events(t, model.KindReplay); !strings.Contains(got, "warn: 2 replays skipped: their URL is not on the TestPlat allowlist") {
		t.Fatalf("events:\n%s", got)
	}
}

func TestChangedURLOfArchivedReplayIsLogged(t *testing.T) {
	e := newTPEnv(t)
	tess := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	pl := testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), true)
	e.fp.SetPlays(model.KindScore, "abc", pl)
	e.drain(t)
	sc := testutil.Row(t, e.svc, tess, "s1")
	if sc.ReplayState != model.ReplayArchived {
		t.Fatalf("state = %s", sc.ReplayState)
	}
	pl.ReplayURL = "https://tp.example/replays/elsewhere.tpr"
	e.fp.SetPlays(model.KindScore, "abc", pl)
	e.clk.Advance(2 * time.Hour)
	e.drain(t)
	if got := e.events(t, model.KindReplay); !strings.Contains(got, "warn: TestPlat now reports a different replay URL for 1 archived scores; the archived copies are kept") {
		t.Fatalf("events:\n%s", got)
	}
	path, _ := e.svc.ReplayPath(sc)
	if b, err := os.ReadFile(path); err != nil || string(b) != "tp-replay-s1" {
		t.Fatalf("archived file must be untouched: %q %v", b, err)
	}
}
```

Run: `go test ./internal/archiver/ -run 'TestPollRefreshes|TestPollResolve|TestRefused|TestChangedURL'`
Expected: FAIL — name not refreshed, account still enabled, no events.

- [ ] **Step 5: Implement the worker side**

`internal/archiver/poll.go` — in `poll`, resolve the platform once before the loop and replace the page-1 profile block; track the page notes:

```go
func (w *Worker) poll(ctx context.Context, wf *service.WorkFeed) error {
	w.setStatus(StateRunning, "Polling "+wf.PlayerName)
	k := wf.Key()
	p, err := w.platform(wf.Platform)
	if err != nil {
		return err
	}
	var newScores, newReplays, pagesRead, totalPages, refused, urlChanged int
	reachedEnd := false
	for page := 1; page <= MaxPollPages; page++ {
		pg, err := p.Adapter.FeedPage(ctx, wf.Feed, wf.ExternalID, page)
		if err != nil {
			return w.clientError(ctx, p, wf, err, true)
		}
		pagesRead, totalPages = page, pg.TotalPages
		if page == 1 && wf.Feed == model.KindScore {
			prof, err := w.profile(ctx, p, wf, pg)
			if err != nil {
				return w.clientError(ctx, p, wf, err, true)
			}
			if err := w.svc.RefreshProfile(ctx, wf.PlayerID, wf.Platform, prof); err != nil {
				return err
			}
		}
		res, err := w.svc.UpsertPlays(ctx, wf.PlayerID, wf.Platform, pg.Plays)
		if err != nil {
			return err
		}
		newScores += res.New
		newReplays += res.NewReplays
		refused += pg.Refused
		urlChanged += res.URLChanged
		if res.Known > 0 || len(pg.Plays) == 0 || page >= pg.TotalPages {
			reachedEnd = true
			break
		}
	}
	w.logPageNotes(ctx, p, wf, refused, urlChanged)
	// … the rest of poll (backfill-state switch, MarkFeedPolled, "%d new" event) is unchanged …
```

In `backfill`, keep the `UpsertPlays` result and log its notes:

```go
	res, err := w.svc.UpsertPlays(ctx, wf.PlayerID, wf.Platform, pg.Plays)
	if err != nil {
		return err
	}
	w.logPageNotes(ctx, p, wf, pg.Refused, res.URLChanged)
```

Add:

```go
// profile is the account's display profile for the page-1 refresh: the one a
// play carries, else Resolve — for platforms whose listings omit it, which is
// also how a vanished player is noticed there (spec §5.1, §5.2).
func (w *Worker) profile(ctx context.Context, p platform.Platform, wf *service.WorkFeed, pg platform.PlayPage) (platform.Profile, error) {
	for _, pl := range pg.Plays {
		if pl.Profile != nil {
			return *pl.Profile, nil
		}
	}
	return p.Adapter.Resolve(ctx, wf.ExternalID)
}

// logPageNotes reports refused replay URLs and archived replays whose URL the
// platform changed (spec §5.2, §5.4).
func (w *Worker) logPageNotes(ctx context.Context, p platform.Platform, wf *service.WorkFeed, refused, urlChanged int) {
	if refused > 0 {
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindReplay, fmt.Sprintf(
			"%d replays skipped: their URL is not on the %s allowlist", refused, p.DisplayName)))
	}
	if urlChanged > 0 {
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindReplay, fmt.Sprintf(
			"%s now reports a different replay URL for %d archived %s; the archived copies are kept", p.DisplayName, urlChanged, noun(wf.Feed))))
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/archiver/ ./internal/service/ ./internal/testutil/`
Expected: PASS — including the existing archiver tests (ScoreSaber plays always carry a profile, so ScoreSaber only calls `Resolve` for an account whose first page is empty).

- [ ] **Step 7: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/service/scores.go internal/service/scores_test.go internal/archiver internal/testutil
git commit -m "feat: supersede PBs on PB-only platforms, keep archived replay URLs, log refused replays"
```

---
## Task 4: Account management — link, unlink, enable; per-account counts

A player can now hold one account per platform (spec §4.3, §6.2): link one (resolved live; refused when the player already has that platform or the account is tracked as another player), unlink one (never the last; rows and optionally files go with it), and pause/resume one. Counts become per account and per row kind; the player's headline counts stay `kind=score` rows only. The opaque-ID guard from spec §9 lands here because linking is what lets a player's ID and account IDs diverge.

**Files:**
- Modify: `internal/service/service.go` (errors, `LinkedElsewhereError`)
- Modify: `internal/service/players.go` (`ResolvePlayer` error cause, `AddPlayer` via `linkedElsewhere`, `createRequiredFeeds`, counts by kind, `Identity.Counts`)
- Create: `internal/service/identities.go`
- Modify: `internal/storage/storage.go` (`RemoveDir`)
- Test: `internal/service/identities_test.go` (new), `internal/service/replays_test.go`, `internal/storage/storage_test.go`, `internal/archiver/semantics_test.go`, `internal/web/admin_test.go` (message text)

**Interfaces:**
- Consumes: Plan 1 `ResolvePlayer`, `PlayerByIdentity`, `newFeed`, `replayLoc`, `Busy`/`PlatformKind`; Task 3 `testutil.NewMultiService`, `UpsertFake`, `Row`, `Archive`.
- Produces:
  - `service.ErrInvalidPlayerRef` — message now `"invalid player reference"`; errors wrap it with the parser's reason, e.g. `"invalid player reference: paste a profile URL or a player ID"`
  - `service.ErrIdentityLinkedElsewhere`, `service.ErrPlatformAlreadyLinked`, `service.ErrLastIdentity`
  - `type service.LinkedElsewhereError struct{ Platform, ExternalID, PlayerID, PlayerName string }` — `errors.Is` matches both `ErrIdentityLinkedElsewhere` and `ErrPlayerExists`; `AddPlayer` returns it too
  - `(*Service).LinkIdentity(ctx, playerID, ref, platformName string) (*model.PlayerPlatform, error)`
  - `(*Service).UnlinkIdentity(ctx, playerID, platformName string, deleteFiles bool) error`
  - `(*Service).SetIdentityEnabled(ctx, playerID, platformName string, enabled bool) error`
  - `service.Identity.Counts map[string]Counts` (by row kind) and `(Identity).Scores() Counts`; `PlayerSummary.Counts` = `kind=score` rows of every platform
  - `(*storage.Store).RemoveDir(playerID, dir string) error`

- [ ] **Step 1: Write the failing tests**

`internal/service/identities_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestLinkIdentity(t *testing.T) {
	svc, _, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	<-svc.WakeC()
	clk.Advance(time.Hour)
	link, err := svc.LinkIdentity(ctx, alice, "https://tp.example/u/abc", "testplat")
	if err != nil {
		t.Fatal(err)
	}
	if link.ExternalID != "abc" || !link.Enabled || !link.LinkedAt.Equal(testutil.T0.Add(time.Hour)) {
		t.Fatalf("link = %+v", link)
	}
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if len(sum.Identities) != 2 || sum.Identities[0].Platform != model.PlatformScoreSaber || sum.Identities[1].Platform != "testplat" {
		t.Fatalf("identities = %+v", sum.Identities)
	}
	f := sum.Identities[1].Feed(model.KindScore)
	if !f.Enabled || f.BackfillState != model.BackfillPending || !f.StartedAt.Equal(testutil.T0.Add(time.Hour)) || f.Access != model.AccessNA {
		t.Fatalf("new account's feed = %+v", f)
	}
	if len(sum.Identities[1].Feeds) != 1 {
		t.Fatal("optional feeds are not created by linking")
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("linking must wake the worker")
	}
	if p, _ := svc.PlayerByIdentity(ctx, "testplat", "abc"); p == nil || p.ID != alice {
		t.Fatal("account lookup must find the player")
	}
}

func TestLinkIdentityRefusals(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	bob := mustAdd(t, svc, "1002")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkIdentity(ctx, alice, "def", "testplat"); !errors.Is(err, service.ErrPlatformAlreadyLinked) {
		t.Fatalf("second account on one platform: %v", err)
	}
	for _, ref := range []string{"abc", "https://tp.example/u/abc"} {
		_, err := svc.LinkIdentity(ctx, bob, ref, "testplat")
		var le *service.LinkedElsewhereError
		if !errors.As(err, &le) || le.PlayerID != alice || le.PlayerName != "Alice" || le.Platform != "TestPlat" ||
			!errors.Is(err, service.ErrIdentityLinkedElsewhere) {
			t.Fatalf("link %q to bob: %v", ref, err)
		}
	}
	if _, err := svc.AddPlayer(ctx, "https://tp.example/u/abc", ""); !errors.Is(err, service.ErrPlayerExists) || !errors.Is(err, service.ErrIdentityLinkedElsewhere) {
		t.Fatalf("adding an account linked as a secondary account: %v", err)
	}
	if _, err := svc.LinkIdentity(ctx, bob, "zzz", "testplat"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := svc.LinkIdentity(ctx, bob, "abc", ""); !errors.Is(err, service.ErrInvalidPlayerRef) {
		t.Fatalf("a link names its platform: %v", err)
	}
	if _, err := svc.LinkIdentity(ctx, "p99999999999", "def", "testplat"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown player: %v", err)
	}
	if sum, _ := svc.GetPlayerSummary(ctx, bob); len(sum.Identities) != 1 {
		t.Fatal("refusals must not create anything")
	}
}

func TestUnlinkIdentity(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true))
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0, false))
	t1 := testutil.Row(t, svc, alice, "t1")
	testutil.Archive(t, svc, t1, "tp bytes")
	tpPath, _ := svc.ReplayPath(t1)

	if err := svc.UnlinkIdentity(ctx, alice, "testplat", true); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.PlayerCounts(ctx, alice); c.Scores != 1 {
		t.Fatalf("only the ScoreSaber row must remain: %+v", c)
	}
	if feeds, _ := svc.Feeds(ctx, alice); len(feeds) != 1 || feeds[0].Platform != model.PlatformScoreSaber {
		t.Fatalf("feeds = %+v", feeds)
	}
	if _, err := os.Stat(tpPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("files of the unlinked platform must be deleted when asked")
	}
	if _, err := svc.PlayerByIdentity(ctx, "testplat", "abc"); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("the account must be free to link elsewhere")
	}
	if err := svc.UnlinkIdentity(ctx, alice, model.PlatformScoreSaber, false); !errors.Is(err, service.ErrLastIdentity) {
		t.Fatalf("last account: %v", err)
	}
	if err := svc.UnlinkIdentity(ctx, alice, "testplat", false); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestUnlinkLegacyKeepsFilesUnlessAsked(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true), testutil.Item("1001", 2, 502, testutil.T0, true))
	s1, _ := svc.GetScore(ctx, 1)
	s2, _ := svc.GetScore(ctx, 2)
	testutil.Archive(t, svc, s1, "one")
	testutil.Archive(t, svc, s2, "two")
	if err := svc.UnlinkIdentity(ctx, alice, model.PlatformScoreSaber, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().Open(storage.Loc{PlayerID: alice, RowID: 1, Ext: ".dat"}); err != nil {
		t.Fatal("files are kept unless asked")
	}
	if _, err := svc.GetScore(ctx, 1); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("rows of the unlinked account are removed")
	}
	// Relink and unlink again, deleting files: the legacy layout is removed file by file.
	if _, err := svc.LinkIdentity(ctx, alice, "1001", model.PlatformScoreSaber); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true))
	s1, _ = svc.GetScore(ctx, 1)
	testutil.Archive(t, svc, s1, "one again")
	if err := svc.UnlinkIdentity(ctx, alice, model.PlatformScoreSaber, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().Open(storage.Loc{PlayerID: alice, RowID: 1, Ext: ".dat"}); err == nil {
		t.Fatal("legacy file must be deleted when asked")
	}
}

func TestSetIdentityEnabled(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	_ = svc.MarkIdentityError(ctx, alice, "testplat", "player not found on TestPlat", true)
	if err := svc.SetIdentityEnabled(ctx, alice, "testplat", true); err != nil {
		t.Fatal(err)
	}
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if id := sum.Identities[1]; !id.Enabled || id.LastError != "" {
		t.Fatalf("resuming clears the error: %+v", id.PlayerPlatform)
	}
	if err := svc.SetIdentityEnabled(ctx, alice, "testplat", false); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.DueFeed(ctx, time.Minute, nil); f == nil || f.Platform != model.PlatformScoreSaber {
		t.Fatalf("a paused account is never due, the others are: %+v", f)
	}
	if err := svc.SetIdentityEnabled(ctx, alice, "nope", true); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestCountsPerAccountAndKind(t *testing.T) {
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true), testutil.Item("1001", 2, 502, testutil.T0, false))
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0, true),
		testutil.FakePlay(model.KindAttempt, "a1", "lb-a", testutil.T0, true))
	testutil.Archive(t, svc, testutil.Row(t, svc, alice, "t1"), "x")
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if sum.Counts.Scores != 4 || sum.Counts.Archived != 1 || sum.Counts.Pending != 2 {
		t.Fatalf("headline counts are score rows of every platform: %+v", sum.Counts)
	}
	ss, tp := sum.Identities[0], sum.Identities[1]
	if ss.Scores().Scores != 2 || tp.Scores().Scores != 2 || tp.Scores().Archived != 1 || tp.Counts[model.KindAttempt].Scores != 1 {
		t.Fatalf("per-account counts: ss=%+v tp=%+v", ss.Counts, tp.Counts)
	}
	all, _ := svc.ListPlayers(ctx, true)
	if all[0].Identities[1].Scores().Scores != 2 {
		t.Fatal("ListPlayers fills per-account counts too")
	}
}
```

Append to `internal/service/replays_test.go`:

```go
func TestLinkedAccountOlderPlaysAreBackfill(t *testing.T) {
	svc, _, clk := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	clk.Advance(time.Hour) // the account is linked at T0+1h
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "new", "lb-a", testutil.T0.Add(90*time.Minute), true),
		testutil.FakePlay(model.KindScore, "old", "lb-b", testutil.T0.Add(-time.Hour), true))
	sc, err := svc.NextReplay(ctx, service.TierNew, "", nil)
	if err != nil || sc == nil || sc.ExternalID != "new" {
		t.Fatalf("new tier = %+v %v", sc, err)
	}
	_ = svc.MarkReplayGone(ctx, sc.ID, "test")
	if sc, _ := svc.NextReplay(ctx, service.TierNew, "", nil); sc != nil {
		t.Fatalf("plays older than the link are backfill work, got %s", sc.ExternalID)
	}
	if sc, _ := svc.NextReplay(ctx, service.TierBackfill, "", nil); sc == nil || sc.ExternalID != "old" {
		t.Fatalf("backfill tier = %+v", sc)
	}
}
```

Append to `internal/storage/storage_test.go`:

```go
func TestRemoveDir(t *testing.T) {
	s, _ := newStore(t)
	legacy := storage.Loc{PlayerID: "p1", RowID: 1, Ext: ".dat"}
	other := storage.Loc{PlayerID: "p1", Dir: "testplat", RowID: 2, Ext: ".tpr"}
	for _, l := range []storage.Loc{legacy, other} {
		if _, _, err := s.Put(l, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveDir("p1", "testplat"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path(other)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("platform directory not removed")
	}
	if _, err := os.Stat(s.Path(legacy)); err != nil {
		t.Fatal("the legacy files of the player must stay")
	}
	for _, bad := range [][2]string{{"../x", "testplat"}, {"p1", ".."}, {"p1", ""}} {
		if err := s.RemoveDir(bad[0], bad[1]); !errors.Is(err, storage.ErrInvalidID) {
			t.Errorf("RemoveDir(%q, %q) = %v", bad[0], bad[1], err)
		}
	}
}
```

Append to `internal/archiver/semantics_test.go` (spec §9 "ID-is-opaque guard"):

```go
// TestPlayerIDIsNeverAnAccountID gives a player the legacy-looking ID "123"
// while its accounts are ScoreSaber 1001 and testplat abc: any code that
// still treats players.id as a platform ID calls the platform with "123".
func TestPlayerIDIsNeverAnAccountID(t *testing.T) {
	e := newTPEnv(t)
	ctx := context.Background()
	e.svc.SetIDGenerator(func() string { return "123" })
	id := testutil.AddPlayer(t, e.svc, "https://tp.example/u/abc")
	if id != "123" {
		t.Fatalf("id = %s", id)
	}
	if _, err := e.svc.LinkIdentity(ctx, id, "1001", model.PlatformScoreSaber); err != nil {
		t.Fatal(err)
	}
	e.fc.scores["1001"] = e.fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	e.fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), true))
	e.drain(t)
	calls := e.fc.calls()
	if len(calls) == 0 {
		t.Fatal("ScoreSaber account never polled")
	}
	for _, c := range append(calls, e.fp.Calls...) {
		if strings.Contains(c, "123") {
			t.Fatalf("a platform was called with the player ID: %v %v", calls, e.fp.Calls)
		}
	}
	if c, _ := e.svc.PlayerCounts(ctx, id); c.Archived != 3 {
		t.Fatalf("both accounts archived under the one player: %+v", c)
	}
}
```

In `internal/web/admin_test.go` `TestLookupPlayer`, the invalid-reference message changes:

```go
	contains(t, bad.Body.String(), "Invalid player reference: paste a profile URL or a player ID")
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/service/ ./internal/storage/ ./internal/archiver/ ./internal/web/`
Expected: FAIL — undefined: `LinkIdentity`, `UnlinkIdentity`, `SetIdentityEnabled`, `LinkedElsewhereError`, `ErrPlatformAlreadyLinked`, `ErrLastIdentity`, `Identity.Counts`, `Store.RemoveDir`.

- [ ] **Step 3: Add the errors**

`internal/service/service.go` — replace the `var (…)` error block:

```go
var (
	ErrNotFound                = errors.New("not found")
	ErrPlayerExists            = errors.New("player is already tracked")
	ErrInvalidPlayerRef        = errors.New("invalid player reference")
	ErrIdentityLinkedElsewhere = errors.New("account is tracked as another player")
	ErrPlatformAlreadyLinked   = errors.New("this player already has an account on that platform")
	ErrLastIdentity            = errors.New("a player keeps at least one account; delete the player instead")
)

// LinkedElsewhereError reports an account already tracked as another player
// (spec §6.2: the admin merges the two players instead). It matches both
// ErrIdentityLinkedElsewhere and ErrPlayerExists.
type LinkedElsewhereError struct {
	Platform   string // display name
	ExternalID string
	PlayerID   string
	PlayerName string
}

func (e *LinkedElsewhereError) Error() string {
	return fmt.Sprintf("this %s account is already tracked as %s; merge the two players to combine them", e.Platform, e.PlayerName)
}

func (e *LinkedElsewhereError) Unwrap() []error {
	return []error{ErrIdentityLinkedElsewhere, ErrPlayerExists}
}
```

- [ ] **Step 4: Rework `players.go`**

`ResolvePlayer` keeps the parser's reason (add `"strings"` to the imports):

```go
	p, id, err := s.reg.ParseRef(ref, platformName)
	if err != nil {
		// "invalid player reference: paste a profile URL or a player ID"
		return platform.Platform{}, platform.Profile{}, fmt.Errorf("%w%s", ErrInvalidPlayerRef, strings.TrimPrefix(err.Error(), platform.ErrInvalidRef.Error()))
	}
```

Add the shared helpers and use them in `AddPlayer`:

```go
// linkedElsewhere returns a *LinkedElsewhereError when the account is
// already linked to a player, nil when it is free.
func (s *Service) linkedElsewhere(ctx context.Context, plat platform.Platform, externalID string) error {
	p, err := s.PlayerByIdentity(ctx, plat.Name, externalID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return &LinkedElsewhereError{Platform: plat.DisplayName, ExternalID: externalID, PlayerID: p.ID, PlayerName: p.Name}
}

// createRequiredFeeds creates the always-on feeds of a new account.
func createRequiredFeeds(ctx context.Context, tx *query.Query, playerID string, plat platform.Platform, now time.Time) error {
	for _, f := range plat.RequiredFeeds() {
		if err := tx.SyncFeed.WithContext(ctx).Create(newFeed(playerID, plat.Name, f.Kind, now, model.AccessNA)); err != nil {
			return err
		}
	}
	return nil
}
```

In `AddPlayer`, replace the `PlayerByIdentity` pre-check with:

```go
	if err := s.linkedElsewhere(ctx, plat, prof.ExternalID); err != nil {
		return nil, err
	}
```

and the feed loop inside its transaction with `return createRequiredFeeds(ctx, tx, id, plat, now)` (after creating the `PlayerPlatform`). Keep the `db.IsDuplicate → ErrPlayerExists` mapping (a concurrent add). Add `"time"` to the imports.

Replace the counts code:

```go
// Identity is one linked platform account with its feeds and row counts.
type Identity struct {
	model.PlayerPlatform
	Feeds  []model.SyncFeed
	Counts map[string]Counts // by row kind (model.Kind*)
}

// Scores are the counts of the account's score rows.
func (i Identity) Scores() Counts { return i.Counts[model.KindScore] }
```

```go
func (c *Counts) add(state string, n int64) {
	c.Scores += n
	switch state {
	case model.ReplayArchived:
		c.Archived += n
	case model.ReplayPending:
		c.Pending += n
	case model.ReplayFailed:
		c.Failed += n
	case model.ReplayGone:
		c.Gone += n
	}
}

// playerCounts are one player's row counts.
type playerCounts struct {
	scores Counts                  // score rows of every platform: the headline counts
	byKind map[PlatformKind]Counts // per platform and row kind
}

func (s *Service) countsBy(ctx context.Context, playerID string) (map[string]playerCounts, error) {
	type row struct {
		PlayerID, Platform, Kind, ReplayState string
		N                                     int64
	}
	var rows []row
	tx := s.db.WithContext(ctx).Model(&model.Score{}).
		Select("player_id, platform, kind, replay_state, COUNT(*) AS n").Group("player_id, platform, kind, replay_state")
	if playerID != "" {
		tx = tx.Where("player_id = ?", playerID)
	}
	if err := tx.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("service: count scores: %w", err)
	}
	out := map[string]playerCounts{}
	for _, r := range rows {
		pc := out[r.PlayerID]
		if pc.byKind == nil {
			pc.byKind = map[PlatformKind]Counts{}
		}
		k := PlatformKind{r.Platform, r.Kind}
		c := pc.byKind[k]
		c.add(r.ReplayState, r.N)
		pc.byKind[k] = c
		if r.Kind == model.KindScore {
			pc.scores.add(r.ReplayState, r.N)
		}
		out[r.PlayerID] = pc
	}
	return out, nil
}

// withCounts fills each account's per-kind counts.
func withCounts(ids []Identity, pc playerCounts) []Identity {
	for i := range ids {
		ids[i].Counts = map[string]Counts{}
		for k, c := range pc.byKind {
			if k.Platform == ids[i].Platform {
				ids[i].Counts[k.Kind] = c
			}
		}
	}
	return ids
}
```

Use them: in `GetPlayerSummary` → `PlayerSummary{Player: *p, Counts: counts[id].scores, Identities: withCounts(ids[id], counts[id])}`; in `ListPlayers` → `PlayerSummary{Player: *p, Counts: counts[p.ID].scores, Identities: withCounts(ids[p.ID], counts[p.ID])}`; `PlayerCounts` returns `counts[id].scores`. Update the `Counts.Replays` comment to "the number of score rows the platform offered a replay for".

- [ ] **Step 5: Implement account management**

`internal/storage/storage.go`:

```go
// RemoveDir deletes one platform directory of a player ({root}/{player}/{dir}).
func (s *Store) RemoveDir(playerID, dir string) error {
	if !ValidPlayerID(playerID) || !dirRe.MatchString(dir) {
		return fmt.Errorf("%w: %q/%q", ErrInvalidID, playerID, dir)
	}
	if err := os.RemoveAll(filepath.Join(s.root, playerID, dir)); err != nil {
		return fmt.Errorf("storage: remove platform dir: %w", err)
	}
	return nil
}
```

`internal/service/identities.go`:

```go
package service

import (
	"context"
	"fmt"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

// LinkIdentity links a platform account to an existing player (spec §6.2).
// The account is resolved live. It is refused when the player already has an
// account on that platform, or when the account is tracked as another player
// (*LinkedElsewhereError). Its required feeds start now, so its older plays
// are backfill work.
func (s *Service) LinkIdentity(ctx context.Context, playerID, ref, platformName string) (*model.PlayerPlatform, error) {
	if platformName == "" {
		return nil, fmt.Errorf("%w: choose the account's platform", ErrInvalidPlayerRef)
	}
	if _, err := s.GetPlayer(ctx, playerID); err != nil {
		return nil, err
	}
	if err := s.checkNoAccountOn(ctx, playerID, platformName); err != nil {
		return nil, err
	}
	plat, prof, err := s.ResolvePlayer(ctx, ref, platformName)
	if err != nil {
		return nil, err
	}
	if err := s.linkedElsewhere(ctx, plat, prof.ExternalID); err != nil {
		return nil, err
	}
	now := s.Now()
	link := &model.PlayerPlatform{PlayerID: playerID, Platform: plat.Name, ExternalID: prof.ExternalID, Enabled: true, LinkedAt: now}
	err = s.q.Transaction(func(tx *query.Query) error {
		if err := tx.PlayerPlatform.WithContext(ctx).Create(link); err != nil {
			return err
		}
		return createRequiredFeeds(ctx, tx, playerID, plat, now)
	})
	if db.IsDuplicate(err) { // lost a race with another link or add
		if lerr := s.linkedElsewhere(ctx, plat, prof.ExternalID); lerr != nil {
			return nil, lerr
		}
		return nil, ErrPlatformAlreadyLinked
	}
	if err != nil {
		return nil, fmt.Errorf("service: link: %w", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(playerID), Platform: new(plat.Name),
		Message: "linked " + plat.DisplayName + " account " + prof.ExternalID})
	s.Wake()
	return link, nil
}

func (s *Service) checkNoAccountOn(ctx context.Context, playerID, platformName string) error {
	pp := s.q.PlayerPlatform
	n, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Count()
	if err != nil {
		return fmt.Errorf("service: link: %w", err)
	}
	if n > 0 {
		return ErrPlatformAlreadyLinked
	}
	return nil
}

// UnlinkIdentity removes one account of a player with its feeds and rows
// (spec §6.2), and its replay files when deleteFiles is set. A player keeps
// at least one account.
func (s *Service) UnlinkIdentity(ctx context.Context, playerID, platformName string, deleteFiles bool) error {
	ids, err := s.Identities(ctx, playerID)
	if err != nil {
		return err
	}
	found := false
	for _, id := range ids {
		found = found || id.Platform == platformName
	}
	if !found {
		return fmt.Errorf("%w: %s account of player %s", ErrNotFound, platformName, playerID)
	}
	if len(ids) == 1 {
		return ErrLastIdentity
	}
	q := s.q.Score
	var archived []*model.Score
	if deleteFiles {
		if archived, err = q.WithContext(ctx).Where(q.PlayerID.Eq(playerID), q.Platform.Eq(platformName),
			q.ReplayState.Eq(model.ReplayArchived)).Find(); err != nil {
			return fmt.Errorf("service: unlink: %w", err)
		}
	}
	err = s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.Score.WithContext(ctx).Where(tx.Score.PlayerID.Eq(playerID), tx.Score.Platform.Eq(platformName)).Delete(); err != nil {
			return err
		}
		// Feeds go with the account (composite foreign key, ON DELETE CASCADE).
		pp := tx.PlayerPlatform
		_, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Delete()
		return err
	})
	if err != nil {
		return fmt.Errorf("service: unlink: %w", err)
	}
	if deleteFiles {
		if err := s.removeFiles(playerID, platformName, archived); err != nil {
			return err
		}
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(playerID), Platform: new(platformName),
		Message: fmt.Sprintf("unlinked %s account (files deleted: %v)", s.displayName(platformName), deleteFiles)})
	return nil
}

// removeFiles deletes one platform's replay files of a player: its directory
// in the per-platform layout, file by file in the legacy one.
func (s *Service) removeFiles(playerID, platformName string, archived []*model.Score) error {
	if p, ok := s.reg.Get(platformName); ok && !p.Legacy {
		return s.store.RemoveDir(playerID, p.Name)
	}
	for _, sc := range archived {
		l, err := s.replayLoc(sc)
		if err != nil {
			continue
		}
		if err := s.store.Remove(l); err != nil {
			return err
		}
	}
	return nil
}

// SetIdentityEnabled pauses or resumes one account; resuming clears its error.
func (s *Service) SetIdentityEnabled(ctx context.Context, playerID, platformName string, enabled bool) error {
	pp := s.q.PlayerPlatform
	upd := map[string]any{"enabled": enabled}
	if enabled {
		upd["last_error"] = ""
	}
	info, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: set account enabled: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: %s account of player %s", ErrNotFound, platformName, playerID)
	}
	if enabled {
		s.Wake()
	}
	return nil
}

// displayName is a platform's display name (its name when unregistered).
func (s *Service) displayName(platformName string) string {
	if p, ok := s.reg.Get(platformName); ok {
		return p.DisplayName
	}
	return platformName
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/service/ ./internal/storage/ ./internal/archiver/ ./internal/web/ ./internal/api/`
Expected: PASS.

- [ ] **Step 7: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/service internal/storage internal/archiver/semantics_test.go internal/web/admin_test.go
git commit -m "feat(service): link, unlink and pause platform accounts; count rows per account"
```

---
## Task 5: Merge and aliases

`MergePlayers(source, into)` combines two players whose platforms are disjoint (spec §6.2). The order is chosen so that a crash at any point loses nothing:

1. hard-link every archived file of the source into the target's directory (copy when linking fails);
2. in one transaction: re-point accounts, feeds, scores, aliases and sync events, insert the alias `source → into`, delete the source player;
3. remove the source's replay directory.

`ResolvePlayerID` follows aliases so that old `/p/{id}` links and API paths keep working (wired in Tasks 8 and 10).

**Files:**
- Modify: `internal/storage/storage.go` (`Link`)
- Modify: `internal/service/service.go` (`ErrMergeConflict`, `ErrMergeSelf`)
- Create: `internal/service/merge.go`
- Modify: `internal/testutil/fakeplatform.go` (named instances)
- Test: `internal/service/merge_test.go` (new), `internal/storage/storage_test.go`

**Interfaces:**
- Consumes: Task 4 `PlayerSummary.Identities`, `displayName`, `platformOrder`; Plan 1 `replayLoc`, `storage.Loc`, `Store.Put`, `Store.RemovePlayer`.
- Produces:
  - `service.ErrMergeConflict`, `service.ErrMergeSelf`
  - `(*Service).MergePlayers(ctx, sourceID, intoID string) error`
  - `(*Service).ResolvePlayerID(ctx, id string) (current string, aliased bool, err error)` — `ErrNotFound` when neither a player nor an alias
  - `(*storage.Store).Link(from, to storage.Loc) error` — hard link, else atomic copy; an existing target is kept
  - `func testutil.NewFakePlatformAs(name, slug, displayName string) *testutil.FakePlatform` (`NewFakePlatform()` = `NewFakePlatformAs("testplat", "tp", "TestPlat")`); profile URLs `https://{slug}.example/u/{id}`

- [ ] **Step 1: Make the fake platform nameable**

`internal/testutil/fakeplatform.go` — replace the package-level `tpURLRe` and the constructor, and make `Platform()` and `FakeLimiter` use the instance's names:

```go
// FakePlatform is a scripted non-legacy platform for genericity tests (spec
// §9): a required score feed and an optional attempt feed that needs access,
// string IDs, replays fetched by URL. NewFakePlatform is "testplat" (slug
// "tp"); NewFakePlatformAs makes more of them.
type FakePlatform struct {
	mu          sync.Mutex
	Name, Slug  string
	DisplayName string
	urlRe       *regexp.Regexp
	PerPage     int
	Profiles    map[string]platform.Profile
	plays       map[string][]platform.Play // kind + "/" + account, newest first
	Replays     map[string][]byte          // replay URL → bytes
	Calls       []string                   // "kind:account:page"
	Limiter     *FakeLimiter
	PBOnly      bool // registry PBOnly; set before calling Platform()
	Refused     int  // reported as PlayPage.Refused on every page
}

func NewFakePlatform() *FakePlatform { return NewFakePlatformAs("testplat", "tp", "TestPlat") }

// NewFakePlatformAs is a fake platform with its own name, slug ([a-z]{2,8})
// and display name; its profile URLs are https://{slug}.example/u/{id}.
func NewFakePlatformAs(name, slug, displayName string) *FakePlatform {
	return &FakePlatform{
		Name: name, Slug: slug, DisplayName: displayName,
		urlRe:   regexp.MustCompile(`^https://` + slug + `\.example/u/([a-z0-9]{1,16})$`),
		PerPage: 2,
		Profiles: map[string]platform.Profile{
			"abc": {ExternalID: "abc", Name: "Tess", Country: "SE", AvatarURL: "https://img." + slug + ".example/abc.png"},
			"def": {ExternalID: "def", Name: "Dee", Country: "NO", AvatarURL: "https://img." + slug + ".example/def.png"},
		},
		plays:   map[string][]platform.Play{},
		Replays: map[string][]byte{},
		Limiter: &FakeLimiter{name: name},
	}
}

var tpIDRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Platform is the registry entry.
func (f *FakePlatform) Platform() platform.Platform {
	return platform.Platform{
		Name: f.Name, Slug: f.Slug, DisplayName: f.DisplayName, Priority: 50, ReplayExt: ".tpr", PBOnly: f.PBOnly,
		ImageHosts: []string{"https://img." + f.Slug + ".example"},
		ProfileURL: func(id string) string { return "https://" + f.Slug + ".example/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := f.urlRe.FindStringSubmatch(in)
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: tpIDRe.MatchString,
		Feeds: []platform.FeedSpec{
			{Kind: model.KindScore},
			{Kind: model.KindAttempt, Optional: true, NeedsAccess: true, AccessHint: &platform.Hint{
				Title: "History is private", Steps: []string{"Open settings", "Make history public"},
				LinkText: "Open settings", LinkURL: "https://" + f.Slug + ".example/settings",
			}},
		},
		Adapter: f,
	}
}
```

`FakeLimiter` gets a name:

```go
// FakeLimiter is ready unless blocked.
type FakeLimiter struct {
	mu    sync.Mutex
	name  string
	until time.Time
}

func (l *FakeLimiter) Name() string { return l.name }
```

and its error message in `Resolve` uses the instance name: `fmt.Errorf("%w: %s player %s", platform.ErrNotFound, f.Name, id)`. (Everything that used `"testplat"` keeps working: it is the default name.)

- [ ] **Step 2: Write the failing tests**

`internal/service/merge_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// mergeFixture is Alice (ScoreSaber, score 1 archived) and Tess (testplat:
// t1 archived, t2 pending, backfill cursor at page 3).
func mergeFixture(t *testing.T) (svc *service.Service, alice, tess string, t1 *model.Score) {
	t.Helper()
	svc, _, _ = testutil.NewMultiService(t)
	ctx := context.Background()
	alice = mustAdd(t, svc, "1001")
	tess = mustAdd(t, svc, "https://tp.example/u/abc")
	testutil.Upsert(t, svc, alice, testutil.Item("1001", 1, 501, testutil.T0, true))
	s1, _ := svc.GetScore(ctx, 1)
	testutil.Archive(t, svc, s1, "alice replay")
	testutil.UpsertFake(t, svc, tess,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0, true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0, true))
	t1 = testutil.Row(t, svc, tess, "t1")
	testutil.Archive(t, svc, t1, "tess replay")
	if err := svc.SetFeedBackfill(ctx, service.FeedKey{PlayerID: tess, Platform: "testplat", Kind: model.KindScore}, model.BackfillRunning, 3, 9); err != nil {
		t.Fatal(err)
	}
	return svc, alice, tess, t1
}

func readReplay(t *testing.T, svc *service.Service, sc *model.Score) string {
	t.Helper()
	f, err := svc.OpenReplay(sc)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

func TestMergeMovesEverything(t *testing.T) {
	svc, alice, tess, t1 := mergeFixture(t)
	ctx := context.Background()
	oldPath, _ := svc.ReplayPath(t1)

	if err := svc.MergePlayers(ctx, tess, alice); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.GetPlayer(ctx, tess); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("the source player must be gone")
	}
	if cur, aliased, err := svc.ResolvePlayerID(ctx, tess); err != nil || !aliased || cur != alice {
		t.Fatalf("alias = %s %v %v", cur, aliased, err)
	}
	if cur, aliased, _ := svc.ResolvePlayerID(ctx, alice); cur != alice || aliased {
		t.Fatal("a live player resolves to itself")
	}
	if _, _, err := svc.ResolvePlayerID(ctx, "nobody"); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("unknown IDs are not found")
	}
	sum, _ := svc.GetPlayerSummary(ctx, alice)
	if len(sum.Identities) != 2 || sum.Counts.Scores != 3 || sum.Counts.Archived != 2 || sum.Name != "Alice" {
		t.Fatalf("survivor = %+v %+v", sum.Player, sum.Counts)
	}
	if f := sum.Identities[1].Feed(model.KindScore); f.BackfillState != model.BackfillRunning || f.BackfillPage != 3 {
		t.Fatalf("the moved feed keeps its cursor: %+v", f)
	}
	moved := testutil.Row(t, svc, alice, "t1")
	if moved.ID != t1.ID || moved.ReplayState != model.ReplayArchived || readReplay(t, svc, moved) != "tess replay" {
		t.Fatalf("moved row = %+v", moved)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the source directory is removed last")
	}
	if p, _ := svc.PlayerByIdentity(ctx, "testplat", "abc"); p == nil || p.ID != alice {
		t.Fatal("the account now belongs to the survivor")
	}
	evs, _, _ := svc.ListEvents(ctx, service.EventFilter{PlayerID: alice, PerPage: 50})
	var msgs []string
	for _, e := range evs {
		msgs = append(msgs, e.Message)
	}
	if all := strings.Join(msgs, "\n"); !strings.Contains(all, "player added: Tess") || !strings.Contains(all, "merged Tess into Alice") {
		t.Fatalf("events follow the survivor:\n%s", all)
	}
	if res, err := svc.ReconcileStorage(ctx); err != nil || res.Requeued != 0 || res.Orphans != 0 {
		t.Fatalf("storage after merge: %+v %v", res, err)
	}
}

func TestMergeAdoptsPrimaryProfile(t *testing.T) {
	svc, alice, tess, _ := mergeFixture(t)
	ctx := context.Background()
	if err := svc.MergePlayers(ctx, alice, tess); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.GetPlayer(ctx, tess)
	if p.Name != "Alice" || p.Country != "FR" {
		t.Fatalf("the survivor shows its primary (ScoreSaber) profile: %+v", p)
	}
	sum, _ := svc.GetPlayerSummary(ctx, tess)
	if sum.Identities[0].Platform != model.PlatformScoreSaber {
		t.Fatal("ScoreSaber is the primary account")
	}
}

func TestMergeRefusals(t *testing.T) {
	svc, alice, tess, _ := mergeFixture(t)
	ctx := context.Background()
	bob := mustAdd(t, svc, "1002")
	if err := svc.MergePlayers(ctx, bob, alice); !errors.Is(err, service.ErrMergeConflict) {
		t.Fatalf("two ScoreSaber players: %v", err)
	}
	if err := svc.MergePlayers(ctx, alice, alice); !errors.Is(err, service.ErrMergeSelf) {
		t.Fatalf("self: %v", err)
	}
	if err := svc.MergePlayers(ctx, "nobody", alice); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown source: %v", err)
	}
	if err := svc.MergePlayers(ctx, tess, "nobody"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown target: %v", err)
	}
	for _, id := range []string{alice, bob, tess} {
		if _, err := svc.GetPlayer(ctx, id); err != nil {
			t.Fatalf("refusals change nothing: %s %v", id, err)
		}
	}
}

func TestMergeRepointsAliases(t *testing.T) {
	tp, zp := testutil.NewFakePlatform(), testutil.NewFakePlatformAs("zedplat", "zp", "ZedPlat")
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(&testutil.Resolver{Players: testutil.DefaultPlayers()}, nil), tp.Platform(), zp.Platform())
	ctx := context.Background()
	a := mustAdd(t, svc, "1001")
	x := mustAdd(t, svc, "https://tp.example/u/abc")
	z := mustAdd(t, svc, "https://zp.example/u/abc")
	if err := svc.MergePlayers(ctx, x, a); err != nil {
		t.Fatal(err)
	}
	if err := svc.MergePlayers(ctx, a, z); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{x, a} {
		if cur, aliased, err := svc.ResolvePlayerID(ctx, old); err != nil || !aliased || cur != z {
			t.Fatalf("%s resolves to %s (%v %v), want %s: aliases never chain", old, cur, aliased, err, z)
		}
	}
	if sum, _ := svc.GetPlayerSummary(ctx, z); len(sum.Identities) != 3 {
		t.Fatalf("identities = %d", len(sum.Identities))
	}
}

func TestMergeRerunAfterFileLinkStep(t *testing.T) {
	svc, alice, tess, t1 := mergeFixture(t)
	ctx := context.Background()
	// A previous merge crashed right after step 1: the file is already linked.
	from := storage.Loc{PlayerID: tess, Dir: "testplat", RowID: t1.ID, Ext: ".tpr"}
	to := storage.Loc{PlayerID: alice, Dir: "testplat", RowID: t1.ID, Ext: ".tpr"}
	if err := svc.Store().Link(from, to); err != nil {
		t.Fatal(err)
	}
	if res, _ := svc.ReconcileStorage(ctx); res.Orphans != 1 {
		t.Fatalf("the half-merged copy is an orphan until the rows move: %+v", res)
	}
	if err := svc.MergePlayers(ctx, tess, alice); err != nil {
		t.Fatal(err)
	}
	if got := readReplay(t, svc, testutil.Row(t, svc, alice, "t1")); got != "tess replay" {
		t.Fatalf("content = %q", got)
	}
	if res, _ := svc.ReconcileStorage(ctx); res.Orphans != 0 || res.Requeued != 0 {
		t.Fatalf("after the rerun: %+v", res)
	}
}
```

Append to `internal/storage/storage_test.go`:

```go
func TestLink(t *testing.T) {
	s, _ := newStore(t)
	from := storage.Loc{PlayerID: "p1", Dir: "testplat", RowID: 7, Ext: ".tpr"}
	to := storage.Loc{PlayerID: "p2", Dir: "testplat", RowID: 7, Ext: ".tpr"}
	if _, _, err := s.Put(from, strings.NewReader("bytes")); err != nil {
		t.Fatal(err)
	}
	if err := s.Link(from, to); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(s.Path(from))
	b, err := os.Stat(s.Path(to))
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("expected a hard link: %v", err)
	}
	kept := storage.Loc{PlayerID: "p3", RowID: 7, Ext: ".tpr"}
	if _, _, err := s.Put(kept, strings.NewReader("already there")); err != nil {
		t.Fatal(err)
	}
	if err := s.Link(from, kept); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(s.Path(kept)); string(got) != "already there" {
		t.Fatal("an existing target is kept")
	}
	if err := s.Link(storage.Loc{PlayerID: "p1", RowID: 99, Ext: ".tpr"}, storage.Loc{PlayerID: "p4", RowID: 99, Ext: ".tpr"}); err == nil {
		t.Fatal("a missing source is an error")
	}
	if err := s.Link(from, storage.Loc{PlayerID: "../x", RowID: 1, Ext: ".tpr"}); !errors.Is(err, storage.ErrInvalidID) {
		t.Fatalf("invalid target: %v", err)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/service/ ./internal/storage/`
Expected: FAIL — undefined: `MergePlayers`, `ResolvePlayerID`, `ErrMergeConflict`, `ErrMergeSelf`, `Store.Link`.

- [ ] **Step 4: Implement `Store.Link`**

`internal/storage/storage.go`:

```go
// Link makes the replay at from also available at to: a hard link, or an
// atomic, fsynced copy when the filesystem refuses links. An existing target
// is kept, so a merge rerun after a crash is harmless (spec §6.2).
func (s *Store) Link(from, to Loc) error {
	if !from.valid() || !to.valid() {
		return fmt.Errorf("%w: %+v → %+v", ErrInvalidID, from, to)
	}
	dst := s.Path(to)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.dir(to), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir %s: %w", ErrWrite, s.dir(to), err)
	}
	src := s.Path(from)
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("storage: link: %w", err)
	}
	defer func() { _ = in.Close() }()
	_, _, err = s.Put(to, in)
	return err
}
```

- [ ] **Step 5: Implement the merge**

`internal/service/service.go` — add to the error block:

```go
	ErrMergeConflict = errors.New("both players have an account on the same platform")
	ErrMergeSelf     = errors.New("a player cannot be merged into itself")
```

`internal/service/merge.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

// ResolvePlayerID follows merge aliases (spec §4.2): a live player resolves to
// itself, a merged-away ID to its survivor (aliased=true).
func (s *Service) ResolvePlayerID(ctx context.Context, id string) (string, bool, error) {
	if _, err := s.GetPlayer(ctx, id); err == nil {
		return id, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", false, err
	}
	a := s.q.PlayerAlias
	al, err := a.WithContext(ctx).Where(a.OldID.Eq(id)).First()
	if err != nil {
		return "", false, notFound(err, "player "+id)
	}
	return al.PlayerID, true, nil
}

// MergePlayers moves every account, feed, score and replay of source onto
// into, and leaves source as an alias of into (spec §6.2). Allowed only when
// the players have no platform in common, so nothing is ever dropped. Files
// are linked first, the database moves in one transaction, and the source
// directory goes last: a crash at any point leaves at worst orphan copies.
func (s *Service) MergePlayers(ctx context.Context, sourceID, intoID string) error {
	if sourceID == intoID {
		return ErrMergeSelf
	}
	src, err := s.GetPlayerSummary(ctx, sourceID)
	if err != nil {
		return err
	}
	dst, err := s.GetPlayerSummary(ctx, intoID)
	if err != nil {
		return err
	}
	for _, a := range src.Identities {
		for _, b := range dst.Identities {
			if a.Platform == b.Platform {
				return fmt.Errorf("%w: %s", ErrMergeConflict, s.displayName(a.Platform))
			}
		}
	}

	// 1. Files: make every archived replay reachable under the target first.
	q := s.q.Score
	archived, err := q.WithContext(ctx).Where(q.PlayerID.Eq(sourceID), q.ReplayState.Eq(model.ReplayArchived)).Find()
	if err != nil {
		return fmt.Errorf("service: merge: %w", err)
	}
	for _, sc := range archived {
		from, err := s.replayLoc(sc)
		if err != nil {
			continue // a platform that is no longer registered: its rows move, its files stay put
		}
		to := from
		to.PlayerID = intoID
		if err := s.store.Link(from, to); err != nil {
			return fmt.Errorf("service: merge: %w", err)
		}
	}

	// 2. Database.
	if err := s.q.Transaction(func(tx *query.Query) error { return s.mergeRows(ctx, tx, src, dst) }); err != nil {
		return fmt.Errorf("service: merge: %w", err)
	}

	// 3. The source directory now only holds duplicates.
	if err := s.store.RemovePlayer(sourceID); err != nil {
		slog.Warn("merge: source replay directory left behind", "player", sourceID, "err", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: new(intoID),
		Message: "merged " + src.Name + " into " + dst.Name})
	return nil
}

func (s *Service) mergeRows(ctx context.Context, tx *query.Query, src, dst PlayerSummary) error {
	from, into := src.ID, dst.ID
	// sync_feeds references player_platforms(player_id, platform) without
	// ON UPDATE: lift the feeds out, move the accounts, put the feeds back.
	f := tx.SyncFeed
	feeds, err := f.WithContext(ctx).Where(f.PlayerID.Eq(from)).Find()
	if err != nil {
		return err
	}
	if _, err := f.WithContext(ctx).Where(f.PlayerID.Eq(from)).Delete(); err != nil {
		return err
	}
	pp := tx.PlayerPlatform
	if _, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(from)).Update(pp.PlayerID, into); err != nil {
		return err
	}
	for _, fd := range feeds {
		fd.PlayerID = into
	}
	if len(feeds) > 0 {
		if err := f.WithContext(ctx).Create(feeds...); err != nil {
			return err
		}
	}
	sc := tx.Score
	if _, err := sc.WithContext(ctx).Where(sc.PlayerID.Eq(from)).Update(sc.PlayerID, into); err != nil {
		return err
	}
	al := tx.PlayerAlias
	if _, err := al.WithContext(ctx).Where(al.PlayerID.Eq(from)).Update(al.PlayerID, into); err != nil {
		return err
	}
	if err := al.WithContext(ctx).Create(&model.PlayerAlias{OldID: from, PlayerID: into}); err != nil {
		return err
	}
	ev := tx.SyncEvent
	if _, err := ev.WithContext(ctx).Where(ev.PlayerID.Eq(from)).Update(ev.PlayerID, into); err != nil {
		return err
	}
	if s.bestPriority(src) < s.bestPriority(dst) { // the survivor's primary account now comes from the source
		p := tx.Player
		if _, err := p.WithContext(ctx).Where(p.ID.Eq(into)).Updates(map[string]any{
			"name": src.Name, "avatar_url": src.AvatarURL, "country": src.Country,
		}); err != nil {
			return err
		}
	}
	_, err = tx.Player.WithContext(ctx).Where(tx.Player.ID.Eq(from)).Delete()
	return err
}

// bestPriority is the registry priority of the player's primary account.
func (s *Service) bestPriority(p PlayerSummary) int {
	best := int(^uint(0) >> 1)
	for _, id := range p.Identities {
		best = min(best, s.platformOrder(id.Platform))
	}
	return best
}
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./internal/service/ ./internal/storage/ ./internal/testutil/ ./internal/archiver/`
Expected: PASS.

- [ ] **Step 7: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/service internal/storage internal/testutil
git commit -m "feat(service): merge players with disjoint platforms and keep the old IDs as aliases"
```

---
## Task 6: Score queries — new filters, map groups, lookup by platform ID

One SQL filter builder now serves both the row listing (API) and the merged map view (web), so a filter can never mean two different things. `ListScores` is rebuilt on it; `ListMapGroups` groups matching plays by `map_key` in SQL and pages over maps (spec §6.1: nothing loads the whole history); `GetPlay` finds a row by `(platform, kind, external_id)` for the per-platform routes.

**Files:**
- Modify: `internal/service/scores.go` (`ScoreFilter` fields, `normalized`, `where`, `ListScores`, `loadScores`)
- Create: `internal/service/groups.go` (`MapGroup`, `MapGroupList`, `ListMapGroups`, `GetPlay`)
- Test: `internal/service/groups_test.go` (new)

**Interfaces:**
- Consumes: Task 4 `LinkIdentity`, `platformOrder`; Task 3 `testutil.NewMultiService`, `UpsertFake`, `Row`, `Archive`.
- Produces:
  - `service.ScoreFilter` gains `Platform string` (`""` = all), `MinScore, MaxScore *int64` (inclusive bounds on `modified_score`), `MapKey string`
  - `type service.MapGroup struct{ MapKey string; Latest *model.Score; Chips []*model.Score; Plays int64 }` + `(MapGroup).More() int64`
  - `type service.MapGroupList struct{ Groups []MapGroup; Total int64; Page, PerPage, Pages int }`
  - `(*Service).ListMapGroups(ctx, f ScoreFilter) (MapGroupList, error)` — `f.PlayerID` required
  - `(*Service).GetPlay(ctx, platformName, kind, externalID string) (*model.Score, error)` — preloads leaderboard and player

- [ ] **Step 1: Write the failing tests**

`internal/service/groups_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

const map501 = "hash501/Standard/9" // ScoreSaber leaderboard 501 (HASH501, SoloStandard, Expert+)

// crossFixture: Alice with ScoreSaber and testplat accounts.
//
//	map 501   SS 1 (PB, archived, T0+3m), SS 4 (not PB, T0-1h), TP t1 (950k, T0+4m)
//	map 502   SS 2 (T0+2m)        map 503   SS 3 (no replay, T0+1m)
//	map solo  TP t2 (900, T0+30s) map 504   SS 5 (not PB, T0-2h)
func crossFixture(t *testing.T) (*service.Service, string) {
	t.Helper()
	svc, _, _ := testutil.NewMultiService(t)
	ctx := context.Background()
	alice := mustAdd(t, svc, "1001")
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	s4 := testutil.Item("1001", 4, 501, testutil.T0.Add(-time.Hour), true)
	s4.Score.PersonalBest = false
	s5 := testutil.Item("1001", 5, 504, testutil.T0.Add(-2*time.Hour), true)
	s5.Score.PersonalBest = false
	testutil.Upsert(t, svc, alice,
		testutil.Item("1001", 1, 501, testutil.T0.Add(3*time.Minute), true),
		testutil.Item("1001", 2, 502, testutil.T0.Add(2*time.Minute), true),
		testutil.Item("1001", 3, 503, testutil.T0.Add(time.Minute), false), s4, s5)
	s1, _ := svc.GetScore(ctx, 1)
	testutil.Archive(t, svc, s1, "x")
	t1 := testutil.FakePlay(model.KindScore, "t1", "lb-x501", testutil.T0.Add(4*time.Minute), true)
	t1.Leaderboard.SongHash, t1.Leaderboard.GameMode, t1.Leaderboard.Difficulty = "hash501", "Standard", 9
	t1.ModifiedScore = 950_000
	testutil.UpsertFake(t, svc, alice, t1, testutil.FakePlay(model.KindScore, "t2", "lb-solo", testutil.T0.Add(30*time.Second), false))
	return svc, alice
}

func TestListMapGroupsMergesPlatforms(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	list, err := svc.ListMapGroups(ctx, service.ScoreFilter{PlayerID: alice})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 5 || len(list.Groups) != 5 || list.Pages != 1 {
		t.Fatalf("list = total %d, %d groups, %d pages", list.Total, len(list.Groups), list.Pages)
	}
	var order []string
	for _, g := range list.Groups {
		order = append(order, g.Latest.ExternalID)
	}
	if want := []string{"t1", "2", "3", "t2", "5"}; !equal(order, want) {
		t.Fatalf("maps by newest play = %v, want %v", order, want)
	}
	g := list.Groups[0]
	if g.MapKey != map501 || g.Plays != 3 || len(g.Chips) != 2 || g.More() != 1 {
		t.Fatalf("map 501 = %+v", g)
	}
	if g.Chips[0].Platform != model.PlatformScoreSaber || g.Chips[0].ExternalID != "1" || g.Chips[1].ExternalID != "t1" {
		t.Fatalf("one chip per platform, primary first: %s/%s, %s/%s", g.Chips[0].Platform, g.Chips[0].ExternalID, g.Chips[1].Platform, g.Chips[1].ExternalID)
	}
	if g.Latest.Leaderboard == nil || g.Chips[0].Leaderboard == nil {
		t.Fatal("rows come with their leaderboard")
	}
	if last := list.Groups[4]; len(last.Chips) != 0 || last.Latest.ExternalID != "5" || last.More() != 1 {
		t.Fatalf("a map whose only play is not a PB is still listed: %+v", last)
	}
	plays, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: alice, MapKey: map501})
	if err != nil || plays.Total != 3 || plays.Items[0].ExternalID != "t1" || plays.Items[1].ExternalID != "1" || plays.Items[2].ExternalID != "4" {
		t.Fatalf("every play of one map, newest first: %+v %v", plays, err)
	}
	if _, err := svc.ListMapGroups(ctx, service.ScoreFilter{}); err == nil {
		t.Fatal("the merged view is per player")
	}
}

func TestListMapGroupsFiltersAndPaging(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	groups := func(f service.ScoreFilter) service.MapGroupList {
		t.Helper()
		f.PlayerID = alice
		l, err := svc.ListMapGroups(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := groups(service.ScoreFilter{PerPage: 3, Page: 2}); l.Total != 5 || l.Pages != 2 || len(l.Groups) != 2 || l.Groups[0].Latest.ExternalID != "t2" {
		t.Fatalf("page 2 = %+v", l)
	}
	tp := groups(service.ScoreFilter{Platform: "testplat"})
	if tp.Total != 2 || len(tp.Groups[0].Chips) != 1 || tp.Groups[0].Chips[0].ExternalID != "t1" {
		t.Fatalf("platform filter = %+v", tp)
	}
	if l := groups(service.ScoreFilter{MinScore: new(int64(960_000))}); l.Total != 4 {
		t.Fatalf("min_score: total %d", l.Total)
	}
	if l := groups(service.ScoreFilter{MaxScore: new(int64(950_000))}); l.Total != 2 {
		t.Fatalf("max_score: total %d", l.Total)
	}
	if l := groups(service.ScoreFilter{Search: "Song 502"}); l.Total != 1 {
		t.Fatalf("search: total %d", l.Total)
	}
	if l := groups(service.ScoreFilter{State: service.FilterArchived}); l.Total != 1 || l.Groups[0].MapKey != map501 {
		t.Fatalf("state: %+v", l)
	}
	if l := groups(service.ScoreFilter{Platform: model.PlatformScoreSaber, MaxScore: new(int64(950_000))}); l.Total != 0 || len(l.Groups) != 0 || l.Pages != 1 {
		t.Fatalf("combined filters: %+v", l)
	}
}

func TestListScoresPlatformAndScoreBounds(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	count := func(f service.ScoreFilter) int64 {
		t.Helper()
		f.PlayerID = alice
		l, err := svc.ListScores(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return l.Total
	}
	for name, c := range map[string]struct {
		f    service.ScoreFilter
		want int64
	}{
		"all":       {service.ScoreFilter{}, 7},
		"platform":  {service.ScoreFilter{Platform: "testplat"}, 2},
		"min":       {service.ScoreFilter{MinScore: new(int64(960_000))}, 5},
		"max":       {service.ScoreFilter{MaxScore: new(int64(900))}, 1},
		"between":   {service.ScoreFilter{MinScore: new(int64(900)), MaxScore: new(int64(950_000))}, 2},
		"combined":  {service.ScoreFilter{Platform: model.PlatformScoreSaber, MaxScore: new(int64(950_000))}, 0},
		"with map":  {service.ScoreFilter{MapKey: map501, Platform: model.PlatformScoreSaber}, 2},
	} {
		if got := count(c.f); got != c.want {
			t.Errorf("%s: total %d, want %d", name, got, c.want)
		}
	}
}

func TestGetPlay(t *testing.T) {
	svc, alice := crossFixture(t)
	ctx := context.Background()
	sc, err := svc.GetPlay(ctx, "testplat", model.KindScore, "t1")
	if err != nil || sc.PlayerID != alice || sc.Leaderboard == nil || sc.Player == nil || sc.Player.Name != "Alice" {
		t.Fatalf("GetPlay = %+v %v", sc, err)
	}
	if sc, err := svc.GetPlay(ctx, model.PlatformScoreSaber, model.KindScore, "1"); err != nil || sc.ID != 1 {
		t.Fatalf("legacy row = %+v %v", sc, err)
	}
	for _, c := range [][3]string{{"testplat", model.KindAttempt, "t1"}, {"testplat", model.KindScore, "1"}, {"nope", model.KindScore, "t1"}} {
		if _, err := svc.GetPlay(ctx, c[0], c[1], c[2]); !errors.Is(err, service.ErrNotFound) {
			t.Errorf("GetPlay%v = %v", c, err)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/service/ -run 'MapGroups|PlatformAndScoreBounds|GetPlay'`
Expected: FAIL — undefined: `ListMapGroups`, `GetPlay`, `ScoreFilter.Platform`, `MinScore`, `MaxScore`, `MapKey`.

- [ ] **Step 3: Rebuild `ListScores` on a shared filter**

`internal/service/scores.go` — replace `ScoreFilter` and `ListScores` (keep `likeStripper`, `ScoreList`, `GetScore`), add `"slices"` to the imports:

```go
type ScoreFilter struct {
	PlayerID   string
	Search     string
	RankedOnly bool
	State      string // "", FilterWithReplay, FilterArchived
	Platform   string // "" = every platform
	MinScore   *int64 // inclusive bounds on modified_score
	MaxScore   *int64
	MapKey     string // one map (the merged page's "more plays")
	Page       int
	PerPage    int
}

func (f ScoreFilter) normalized() ScoreFilter {
	if f.PerPage <= 0 {
		f.PerPage = 50
	}
	f.PerPage = min(f.PerPage, 100)
	f.Page = max(f.Page, 1)
	return f
}

// playsFrom joins every play to its leaderboard; conditions use s and lb.
const playsFrom = " FROM scores s JOIN leaderboards lb ON lb.id = s.leaderboard_id WHERE "

// where is the per-play condition shared by the row listing and the merged
// map view: a filter always means the same thing in both.
func (f ScoreFilter) where() (string, []any) {
	conds := []string{"1 = 1"}
	var args []any
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if f.PlayerID != "" {
		add("s.player_id = ?", f.PlayerID)
	}
	if term := likeStripper.Replace(strings.TrimSpace(f.Search)); term != "" {
		p := "%" + term + "%"
		add("(lb.song_name LIKE ? OR lb.song_author LIKE ? OR lb.mapper LIKE ?)", p, p, p)
	}
	if f.RankedOnly {
		add("lb.status = ?", "RANKED")
	}
	switch f.State {
	case FilterWithReplay:
		add("s.has_replay")
	case FilterArchived:
		add("s.replay_state = ?", model.ReplayArchived)
	}
	if f.Platform != "" {
		add("s.platform = ?", f.Platform)
	}
	if f.MinScore != nil {
		add("s.modified_score >= ?", *f.MinScore)
	}
	if f.MaxScore != nil {
		add("s.modified_score <= ?", *f.MaxScore)
	}
	if f.MapKey != "" {
		add("lb.map_key = ?", f.MapKey)
	}
	return strings.Join(conds, " AND "), args
}

func (s *Service) ListScores(ctx context.Context, f ScoreFilter) (ScoreList, error) {
	f = f.normalized()
	where, args := f.where()
	gdb := s.db.WithContext(ctx)
	var total int64
	if err := gdb.Raw("SELECT COUNT(*)"+playsFrom+where, args...).Scan(&total).Error; err != nil {
		return ScoreList{}, fmt.Errorf("service: count scores: %w", err)
	}
	var ids []int64
	if err := gdb.Raw("SELECT s.id"+playsFrom+where+" ORDER BY s.set_at DESC, s.id DESC LIMIT ? OFFSET ?",
		slices.Concat(args, []any{f.PerPage, (f.Page - 1) * f.PerPage})...).Scan(&ids).Error; err != nil {
		return ScoreList{}, fmt.Errorf("service: list scores: %w", err)
	}
	items, err := s.loadScores(ctx, ids)
	if err != nil {
		return ScoreList{}, err
	}
	pages := int((total + int64(f.PerPage) - 1) / int64(f.PerPage))
	return ScoreList{Items: items, Total: total, Page: f.Page, PerPage: f.PerPage, Pages: max(pages, 1)}, nil
}

// loadScores loads rows with their leaderboard, in the order of ids
// (duplicates allowed).
func (s *Service) loadScores(ctx context.Context, ids []int64) ([]*model.Score, error) {
	if len(ids) == 0 {
		return []*model.Score{}, nil
	}
	q := s.q.Score
	rows, err := q.WithContext(ctx).Preload(q.Leaderboard).Where(q.ID.In(ids...)).Find()
	if err != nil {
		return nil, fmt.Errorf("service: load scores: %w", err)
	}
	byID := make(map[int64]*model.Score, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]*model.Score, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}
```

The `query` import of `scores.go` is still used by `UpsertPlays`; `gorm.io/gorm/clause` too.

- [ ] **Step 4: Implement the map groups and `GetPlay`**

`internal/service/groups.go`:

```go
package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// MapGroup is one row of the merged player page (spec §6.1): the plays of one
// map (map_key) that match the filter.
type MapGroup struct {
	MapKey string
	Latest *model.Score   // the newest matching play; shows the map
	Chips  []*model.Score // each platform's current personal best, primary platform first
	Plays  int64          // matching plays of this map
}

// More is how many matching plays the chips do not show.
func (g MapGroup) More() int64 { return max(g.Plays-int64(len(g.Chips)), 0) }

type MapGroupList struct {
	Groups  []MapGroup
	Total   int64 // matching maps
	Page    int
	PerPage int
	Pages   int
}

// ListMapGroups pages over a player's maps, newest play first. Grouping,
// ordering and paging happen in SQL; a map is listed when any of its plays
// matches f. Chips are each platform's current personal best on that map
// (restricted to f.Platform when set), whatever the other filters.
func (s *Service) ListMapGroups(ctx context.Context, f ScoreFilter) (MapGroupList, error) {
	if f.PlayerID == "" {
		return MapGroupList{}, errors.New("service: map groups need a player")
	}
	f = f.normalized()
	where, args := f.where()
	gdb := s.db.WithContext(ctx)
	out := MapGroupList{Page: f.Page, PerPage: f.PerPage}
	if err := gdb.Raw("SELECT COUNT(DISTINCT lb.map_key)"+playsFrom+where, args...).Scan(&out.Total).Error; err != nil {
		return out, fmt.Errorf("service: count maps: %w", err)
	}
	out.Pages = max(int((out.Total+int64(f.PerPage)-1)/int64(f.PerPage)), 1)

	type groupRow struct {
		MapKey string
		Plays  int64
	}
	var rows []groupRow
	if err := gdb.Raw("SELECT lb.map_key AS map_key, COUNT(*) AS plays"+playsFrom+where+
		" GROUP BY lb.map_key ORDER BY MAX(s.set_at) DESC, lb.map_key LIMIT ? OFFSET ?",
		slices.Concat(args, []any{f.PerPage, (f.Page - 1) * f.PerPage})...).Scan(&rows).Error; err != nil {
		return out, fmt.Errorf("service: list maps: %w", err)
	}
	if len(rows) == 0 {
		return out, nil
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.MapKey
	}

	var latestIDs []int64
	if err := gdb.Raw("SELECT id FROM (SELECT s.id AS id, ROW_NUMBER() OVER "+
		"(PARTITION BY lb.map_key ORDER BY s.set_at DESC, s.id DESC) AS rn"+playsFrom+where+" AND lb.map_key IN ?) WHERE rn = 1",
		slices.Concat(args, []any{keys})...).Scan(&latestIDs).Error; err != nil {
		return out, fmt.Errorf("service: newest plays: %w", err)
	}
	chipWhere := "s.player_id = ? AND s.kind = ? AND s.personal_best AND lb.map_key IN ?"
	chipArgs := []any{f.PlayerID, model.KindScore, keys}
	if f.Platform != "" {
		chipWhere += " AND s.platform = ?"
		chipArgs = append(chipArgs, f.Platform)
	}
	var chipIDs []int64
	if err := gdb.Raw("SELECT id FROM (SELECT s.id AS id, ROW_NUMBER() OVER "+
		"(PARTITION BY lb.map_key, s.platform ORDER BY s.modified_score DESC, s.set_at DESC, s.id DESC) AS rn"+
		playsFrom+chipWhere+") WHERE rn = 1", chipArgs...).Scan(&chipIDs).Error; err != nil {
		return out, fmt.Errorf("service: personal bests: %w", err)
	}

	loaded, err := s.loadScores(ctx, slices.Concat(latestIDs, chipIDs))
	if err != nil {
		return out, err
	}
	byID := make(map[int64]*model.Score, len(loaded))
	for _, sc := range loaded {
		byID[sc.ID] = sc
	}
	latest := map[string]*model.Score{}
	for _, id := range latestIDs {
		if sc := byID[id]; sc != nil && sc.Leaderboard != nil {
			latest[sc.Leaderboard.MapKey] = sc
		}
	}
	chips := map[string][]*model.Score{}
	for _, id := range chipIDs {
		if sc := byID[id]; sc != nil && sc.Leaderboard != nil {
			chips[sc.Leaderboard.MapKey] = append(chips[sc.Leaderboard.MapKey], sc)
		}
	}
	for _, r := range rows {
		c := chips[r.MapKey]
		slices.SortFunc(c, func(a, b *model.Score) int {
			return cmp.Or(cmp.Compare(s.platformOrder(a.Platform), s.platformOrder(b.Platform)), strings.Compare(a.Platform, b.Platform))
		})
		out.Groups = append(out.Groups, MapGroup{MapKey: r.MapKey, Latest: latest[r.MapKey], Chips: c, Plays: r.Plays})
	}
	return out, nil
}

// GetPlay finds a row by its platform, kind and platform ID (spec §6.1).
func (s *Service) GetPlay(ctx context.Context, platformName, kind, externalID string) (*model.Score, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).
		Where(q.Platform.Eq(platformName), q.Kind.Eq(kind), q.ExternalID.Eq(externalID)).First()
	if err != nil {
		return nil, notFound(err, platformName+" "+kind+" "+externalID)
	}
	return sc, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/service/ ./internal/web/ ./internal/api/ ./internal/archiver/`
Expected: PASS — the rebuilt `ListScores` keeps every existing filter test green (`TestListScoresFiltersAndPaging`, web player-page filters, API listing).

- [ ] **Step 6: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/service
git commit -m "feat(service): platform and score filters, SQL-grouped map listing, lookup by platform ID"
```

---
## Task 7: Registry-built links, per-platform public routes, registry-built CSP

Every public URL is now built by `platform.Registry` from a row's `(platform, kind, external_id)` (spec §6.1): legacy (ScoreSaber) scores keep `/s/{id}`, `/r/{id}.dat`, `/embed/{id}`, and every other row gets `/{s,r,embed}/{slug}/[attempt/]{externalID}`. The bare legacy routes resolve ScoreSaber rows only, so a BeatLeader row's internal ID can never shadow an old link. Views read the registry from the request context, which the web middleware fills. The CSP `img-src` comes from the registry's image hosts.

**Files:**
- Create: `internal/platform/links.go`, `internal/web/views/links.go`, `internal/web/plays.go`
- Modify: `internal/web/views/format.go` (drop the int64 URL helpers), `internal/web/views/urls.go` (`ViewerSrc` moves to `links.go`)
- Modify: `internal/web/views/{player,score,sync}.templ` (row-based helpers, platform wording)
- Modify: `internal/web/web.go` (routes, `withPlatforms`, CSP), `internal/web/public.go` (`score`, `platformScore`, `scorePage`), `internal/web/replay.go` (`platformReplay`, `serveReplay`, `platformEmbed`, `embedPage`)
- Modify: `internal/httpx/httpx.go` (`DefaultCSP`, `SecurityHeaders` take image hosts)
- Test: `internal/platform/links_test.go` (new), `internal/web/views/urls_test.go`, `internal/web/views/format_test.go`, `internal/web/env_test.go`, `internal/web/platform_routes_test.go` (new), `internal/httpx/httpx_test.go`

**Interfaces:**
- Consumes: Task 6 `(*Service).GetPlay`; Task 3 `testutil.UpsertFake`, `Row`, `Archive`; Plan 1 `Registry.Get/BySlug/Legacy`.
- Produces:
  - `type platform.PlayRef struct{ Platform, Kind, ExternalID string }`
  - `(*platform.Registry).PlayPath(prefix string, ref PlayRef) string` (`prefix` = `"/s"`, `"/embed"`, `"/r"`), `ReplayPath(ref PlayRef) string`, `AccountPath(platformName, externalID string) string`, `ImageHosts() []string`
  - `views.WithPlatforms(ctx, *platform.Registry) context.Context`, `views.Platforms(ctx) *platform.Registry`
  - `views.ScoreURL(ctx, *model.Score) string`, `views.ReplayPath(ctx, s)`, `views.EmbedPath(ctx, s)`, `views.ReplayExt(ctx, s)`, `views.AccountPath(ctx, platformName, externalID)`, `views.ProfileURL(ctx, platformName, externalID)`, `views.PlatformName(ctx, platformName)` (display name)
  - `views.ViewerSrc(ctx, base string, s *model.Score, autoplay, loop, hideUI bool, st service.Settings) string`
  - `httpx.DefaultCSP(nonce string, imgHosts []string) string`, `httpx.SecurityHeaders(imgHosts ...string) func(http.Handler) http.Handler`
  - web routes `GET /s/{slug}/{externalID}`, `GET /s/{slug}/attempt/{externalID}`, `GET|OPTIONS /r/{slug}/{file}`, `GET|OPTIONS /r/{slug}/attempt/{file}`, `GET /embed/{slug}/{externalID}`, `GET /embed/{slug}/attempt/{externalID}`
  - web test helpers `newEnvWith(t, *testutil.FakePlatform)`, `(*testEnv).seedTP() string` (Tess: `t1` archived, `t2` pending, attempt `a1` archived)

- [ ] **Step 1: Write the failing tests**

`internal/platform/links_test.go` (reuses `plat` from `registry_test.go`):

```go
package platform_test

import (
	"slices"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

func TestLinks(t *testing.T) {
	a, b := plat("alpha", "aa", true, 0, "a.example"), plat("beta", "bb", false, 10, "b.example")
	a.ImageHosts = []string{"https://img.a.example", "https://shared.example"}
	b.ImageHosts = []string{"https://shared.example", "https://img.b.example"}
	r, err := platform.NewRegistry(b, a)
	if err != nil {
		t.Fatal(err)
	}
	legacy := platform.PlayRef{Platform: "alpha", Kind: model.KindScore, ExternalID: "42"}
	score := platform.PlayRef{Platform: "beta", Kind: model.KindScore, ExternalID: "x1"}
	attempt := platform.PlayRef{Platform: "beta", Kind: model.KindAttempt, ExternalID: "a 9"}
	for got, want := range map[string]string{
		r.PlayPath("/s", legacy):     "/s/42",
		r.PlayPath("/s", score):      "/s/bb/x1",
		r.PlayPath("/s", attempt):    "/s/bb/attempt/a%209",
		r.PlayPath("/embed", score):  "/embed/bb/x1",
		r.ReplayPath(legacy):         "/r/42.bin",
		r.ReplayPath(score):          "/r/bb/x1.bin",
		r.ReplayPath(attempt):        "/r/bb/attempt/a%209.bin",
		r.AccountPath("alpha", "42"): "/p/aa/42",
		r.AccountPath("beta", "a/b"): "/p/bb/a%2Fb",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	unknown := platform.PlayRef{Platform: "nope", Kind: model.KindScore, ExternalID: "1"}
	if r.PlayPath("/s", unknown) != "" || r.ReplayPath(unknown) != "" || r.AccountPath("nope", "1") != "" {
		t.Error("unknown platforms have no URL")
	}
	if got := r.ImageHosts(); !slices.Equal(got, []string{"https://img.a.example", "https://shared.example", "https://img.b.example"}) {
		t.Errorf("ImageHosts = %v (registry order, deduplicated)", got)
	}
}
```

Replace `internal/web/views/urls_test.go` `TestURLs` and `TestViewerSrcHeadsetOverride` with registry-based versions, and add `TestRowLinks` (imports: add `"context"`, `"github.com/yyewolf/ssarchiver/internal/platform"`, `"github.com/yyewolf/ssarchiver/internal/scoresaber"`, `"github.com/yyewolf/ssarchiver/internal/testutil"`):

```go
func testCtx(t *testing.T) context.Context {
	t.Helper()
	reg, err := platform.NewRegistry(scoresaber.NewPlatform(nil, nil), testutil.NewFakePlatform().Platform())
	if err != nil {
		t.Fatal(err)
	}
	return WithPlatforms(context.Background(), reg)
}

func ssRow(id string) *model.Score {
	return &model.Score{Platform: model.PlatformScoreSaber, Kind: model.KindScore, ExternalID: id}
}

func tpRow(kind, id string) *model.Score {
	return &model.Score{Platform: "testplat", Kind: kind, ExternalID: id}
}

func TestURLs(t *testing.T) {
	ctx := testCtx(t)
	f := service.ScoreFilter{Search: "ghost rule", State: service.FilterArchived, RankedOnly: true}
	if got := PlayerScoresURL("1001", f, 3); got != "/p/1001?page=3&q=ghost+rule&ranked=1&state=archived" {
		t.Errorf("PlayerScoresURL = %s", got)
	}
	if got := PlayerScoresURL("1001", service.ScoreFilter{}, 1); got != "/p/1001" {
		t.Errorf("PlayerScoresURL(empty) = %s", got)
	}
	src := ViewerSrc(ctx, "https://r.example.com", ssRow("42"), true, false, true, service.DefaultSettings)
	if src != "/viewer/?autoPlay=true&noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat&uiOff=true" {
		t.Errorf("ViewerSrc = %s", src)
	}
	if got := ViewerSrc(ctx, "https://r.example.com", tpRow(model.KindScore, "t1"), false, false, false, service.DefaultSettings); got != "/viewer/?noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2Ftp%2Ft1.tpr" {
		t.Errorf("ViewerSrc(testplat) = %s", got)
	}
	if got := EmbedSnippet("https://r.example.com/embed/42"); got != `<iframe src="https://r.example.com/embed/42" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>` {
		t.Errorf("EmbedSnippet = %s", got)
	}
}

func TestViewerSrcHeadsetOverride(t *testing.T) {
	ctx := testCtx(t)
	st := service.DefaultSettings
	st.ViewerShowHeadset = true
	st.ViewerHeadsetColor = "#Ff0080"
	st.ViewerHeadsetAlpha = 0.4
	src := ViewerSrc(ctx, "https://r.example.com", ssRow("42"), false, false, false, st)
	want := "/viewer/?noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat" +
		"&settingsOverride=%7B%22Bools%22%3A%7B%22showheadset%22%3Atrue%7D%2C%22Ints%22%3A%7B%7D%2C%22Floats%22%3A%7B%22headsetalpha%22%3A0.4%2C%22headsetcolor.b%22%3A0.502%2C%22headsetcolor.g%22%3A0%2C%22headsetcolor.r%22%3A1%7D%7D"
	if src != want {
		t.Errorf("ViewerSrc = %s, want %s", src, want)
	}
	if got := ViewerSrc(ctx, "https://r.example.com", ssRow("42"), false, false, false, service.DefaultSettings); got != "/viewer/?noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat" {
		t.Errorf("ViewerSrc(default) = %s", got)
	}
}

func TestRowLinks(t *testing.T) {
	ctx := testCtx(t)
	for got, want := range map[string]string{
		ScoreURL(ctx, ssRow("42")):                           "/s/42",
		ReplayPath(ctx, ssRow("42")):                         "/r/42.dat",
		EmbedPath(ctx, ssRow("42")):                          "/embed/42",
		ScoreURL(ctx, tpRow(model.KindScore, "t1")):          "/s/tp/t1",
		ScoreURL(ctx, tpRow(model.KindAttempt, "a1")):        "/s/tp/attempt/a1",
		ReplayPath(ctx, tpRow(model.KindAttempt, "a1")):      "/r/tp/attempt/a1.tpr",
		EmbedPath(ctx, tpRow(model.KindScore, "t1")):         "/embed/tp/t1",
		ReplayExt(ctx, tpRow(model.KindScore, "t1")):         ".tpr",
		AccountPath(ctx, model.PlatformScoreSaber, "1001"):   "/p/ss/1001",
		ProfileURL(ctx, "testplat", "abc"):                   "https://tp.example/u/abc",
		PlatformName(ctx, "testplat"):                        "TestPlat",
		PlatformName(ctx, "gone"):                            "gone",
		ScoreURL(context.Background(), ssRow("42")):          "",
		PlatformName(context.Background(), "testplat"):       "testplat",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
```

In `internal/web/views/format_test.go`, delete the three entries `ScoreURL(42)`, `ReplayPath(42)` and `EmbedPath(42)`.

`internal/web/env_test.go` — let the env register the fake platform, and add a seed for it (imports: add `"github.com/yyewolf/ssarchiver/internal/platform"`):

```go
type testEnv struct {
	t      *testing.T
	svc    *service.Service
	clk    *testutil.Clock
	status *statusStub
	fp     *testutil.FakePlatform // nil unless built with newEnvWith
	h      http.Handler
}

func newEnv(t *testing.T) *testEnv { return newEnvWith(t, nil) }

// newEnvWith also registers the fake third platform when fp is not nil.
func newEnvWith(t *testing.T, fp *testutil.FakePlatform) *testEnv {
	t.Helper()
	plats := []platform.Platform{scoresaber.NewPlatform(&testutil.Resolver{Players: testutil.DefaultPlayers()}, nil)}
	if fp != nil {
		plats = append(plats, fp.Platform())
	}
	svc, _, clk := testutil.NewServiceWith(t, plats...)
	vh := viewer.NewHandler(fstest.MapFS{"index.html.gz": {Data: testutil.Gzip("<html>viewer</html>")}}, "test")
	st := &statusStub{st: archiver.Status{State: archiver.StateIdle, Since: testutil.T0}}
	h := web.New(web.Deps{Service: svc, Status: st, Viewer: vh, Config: config.Config{HourlyBudget: 300, BaseURL: "https://replays.example.com"}})
	mux := http.NewServeMux()
	h.Routes(mux)
	return &testEnv{t: t, svc: svc, clk: clk, status: st, fp: fp, h: h.Middleware(mux)}
}

// seedTP tracks Tess (testplat account abc): score t1 archived, score t2
// pending, attempt a1 archived. It returns her player ID.
func (e *testEnv) seedTP() string {
	e.t.Helper()
	tess := testutil.AddPlayer(e.t, e.svc, "https://tp.example/u/abc")
	testutil.UpsertFake(e.t, e.svc, tess,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0.Add(2*time.Minute), true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0.Add(time.Minute), true),
		testutil.FakePlay(model.KindAttempt, "a1", "lb-a", testutil.T0, true))
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, tess, "t1"), "TP replay bytes")
	testutil.Archive(e.t, e.svc, testutil.Row(e.t, e.svc, tess, "a1"), "TP attempt bytes")
	return tess
}
```

`internal/web/platform_routes_test.go`:

```go
package web_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestPlatformRoutes(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	e.seedTP()

	page := e.do(http.MethodGet, "/s/tp/t1", nil)
	if page.Code != 200 {
		t.Fatalf("score page = %d", page.Code)
	}
	contains(t, page.Body.String(), "Song lb-a", `src="/embed/tp/t1"`, `href="/r/tp/t1.tpr"`, "Download .tpr",
		"replays.example.com/embed/tp/t1", `property="og:url" content="https://replays.example.com/s/tp/t1"`)
	contains(t, e.do(http.MethodGet, "/s/tp/t2", nil).Body.String(), "queued for archiving")
	attempt := e.do(http.MethodGet, "/s/tp/attempt/a1", nil)
	if attempt.Code != 200 {
		t.Fatalf("attempt page = %d", attempt.Code)
	}
	contains(t, attempt.Body.String(), `src="/embed/tp/attempt/a1"`, `href="/r/tp/attempt/a1.tpr"`)

	raw := e.do(http.MethodGet, "/r/tp/t1.tpr", nil)
	h := raw.Header()
	if raw.Code != 200 || raw.Body.String() != "TP replay bytes" || h.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(h.Get("Cache-Control"), "immutable") || len(h.Get("ETag")) != 66 ||
		h.Get("Content-Disposition") != `attachment; filename="t1.tpr"` {
		t.Fatalf("raw = %d %q %v", raw.Code, raw.Body.String(), h)
	}
	if part := e.do(http.MethodGet, "/r/tp/t1.tpr", nil, withHeader("Range", "bytes=0-1")); part.Code != http.StatusPartialContent || part.Body.String() != "TP" {
		t.Fatalf("range = %d %q", part.Code, part.Body.String())
	}
	if got := e.do(http.MethodGet, "/r/tp/attempt/a1.tpr", nil).Body.String(); got != "TP attempt bytes" {
		t.Fatalf("attempt replay = %q", got)
	}
	if pre := e.do(http.MethodOptions, "/r/tp/attempt/a1.tpr", nil); pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight = %d", pre.Code)
	}

	embed := e.do(http.MethodGet, "/embed/tp/t1", nil)
	if embed.Code != 200 || embed.Header().Get("Content-Security-Policy") != httpx.EmbedCSP {
		t.Fatalf("embed = %d %q", embed.Code, embed.Header().Get("Content-Security-Policy"))
	}
	contains(t, embed.Body.String(), "replayURL=https%3A%2F%2Freplays.example.com%2Fr%2Ftp%2Ft1.tpr")

	for _, p := range []string{
		"/s/zz/t1", "/s/tp/nope", "/s/tp/attempt/t1", "/r/zz/t1.tpr", "/r/tp/t1.dat", "/r/tp/t2.tpr", "/r/tp/.tpr",
		"/embed/tp/t2", "/embed/zz/t1",
	} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
}

// TestLegacyRoutesResolveScoreSaberOnly: the bare routes of the original
// version must keep resolving ScoreSaber scores, and only them (spec §6.1).
func TestLegacyRoutesResolveScoreSaberOnly(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	e.seed()
	tess := e.seedTP()
	internal := strconv.FormatInt(testutil.Row(t, e.svc, tess, "t1").ID, 10)
	for _, p := range []string{"/s/" + internal, "/r/" + internal + ".dat", "/embed/" + internal} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d: a non-legacy row must not resolve through its internal ID", p, rec.Code)
		}
	}
	for _, p := range []string{"/s/1", "/embed/1"} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != 200 {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
	if got := e.do(http.MethodGet, "/r/1.dat", nil).Body.String(); got != replayBody {
		t.Errorf("/r/1.dat = %q", got)
	}
}

func TestCSPIncludesPlatformImageHosts(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	csp := e.do(http.MethodGet, "/", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: https://cdn.scoresaber.com https://img.tp.example;") {
		t.Fatalf("csp = %q", csp)
	}
}
```

`internal/httpx/httpx_test.go` — construct the middleware with hosts: replace both `httpx.SecurityHeaders(http.HandlerFunc(…))` calls with `httpx.SecurityHeaders("https://cdn.scoresaber.com")(http.HandlerFunc(…))`, and in `TestSecurityHeadersNonce` also assert:

```go
	if !strings.Contains(csp, "img-src 'self' data: https://cdn.scoresaber.com;") {
		t.Fatalf("img-src must list the given hosts: %q", csp)
	}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/platform/ ./internal/httpx/ ./internal/web/...`
Expected: FAIL — undefined: `platform.PlayRef`, `views.WithPlatforms`, `views.ScoreURL` arity, `httpx.SecurityHeaders` arity; `/s/tp/t1` 404.

- [ ] **Step 3: Registry links**

`internal/platform/links.go`:

```go
package platform

import (
	"net/url"
	"slices"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// PlayRef locates one stored play for public URLs (spec §6.1).
type PlayRef struct{ Platform, Kind, ExternalID string }

// PlayPath is a play's public path under prefix ("/s" score page, "/embed"
// viewer, "/r" raw file without extension): /s/{id} for the legacy
// platform's scores, else /s/{slug}/{id} or /s/{slug}/attempt/{id}.
// It is "" for an unknown platform.
func (r *Registry) PlayPath(prefix string, ref PlayRef) string {
	p, ok := r.Get(ref.Platform)
	if !ok {
		return ""
	}
	id := url.PathEscape(ref.ExternalID)
	switch {
	case p.Legacy && ref.Kind == model.KindScore:
		return prefix + "/" + id
	case ref.Kind == model.KindAttempt:
		return prefix + "/" + p.Slug + "/attempt/" + id
	}
	return prefix + "/" + p.Slug + "/" + id
}

// ReplayPath is the raw replay file's path: /r/{id}.dat for legacy scores,
// else /r/{slug}/[attempt/]{id}{ext}.
func (r *Registry) ReplayPath(ref PlayRef) string {
	p, ok := r.Get(ref.Platform)
	if !ok {
		return ""
	}
	return r.PlayPath("/r", ref) + p.ReplayExt
}

// AccountPath is the readable link to a tracked account, /p/{slug}/{id};
// it survives merges (spec §6.1).
func (r *Registry) AccountPath(platformName, externalID string) string {
	p, ok := r.Get(platformName)
	if !ok {
		return ""
	}
	return "/p/" + p.Slug + "/" + url.PathEscape(externalID)
}

// ImageHosts are every platform's image hosts (registry order, deduplicated),
// for the CSP img-src.
func (r *Registry) ImageHosts() []string {
	var out []string
	for _, p := range r.list {
		for _, h := range p.ImageHosts {
			if !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}
```

- [ ] **Step 4: View helpers**

Delete `ScoreURL`, `ReplayPath` and `EmbedPath` (the int64 versions) from `internal/web/views/format.go`, and `ViewerSrc` from `internal/web/views/urls.go` (its body moves below; drop `"encoding/json"` and `"math"` from `urls.go` if unused).

`internal/web/views/links.go`:

```go
package views

import (
	"context"
	"encoding/json"
	"math"
	"net/url"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type platformsKey struct{}

// WithPlatforms stores the registry for components that build platform
// URLs; the web middleware does it for every request.
func WithPlatforms(ctx context.Context, reg *platform.Registry) context.Context {
	return context.WithValue(ctx, platformsKey{}, reg)
}

// Platforms is the request's registry (nil outside a request).
func Platforms(ctx context.Context) *platform.Registry {
	reg, _ := ctx.Value(platformsKey{}).(*platform.Registry)
	return reg
}

func playRef(s *model.Score) platform.PlayRef {
	return platform.PlayRef{Platform: s.Platform, Kind: s.Kind, ExternalID: s.ExternalID}
}

// ScoreURL is a row's public page ("" without a registry).
func ScoreURL(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.PlayPath("/s", playRef(s))
	}
	return ""
}

// ReplayPath is a row's raw replay file.
func ReplayPath(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.ReplayPath(playRef(s))
	}
	return ""
}

// EmbedPath is a row's embeddable viewer page.
func EmbedPath(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.PlayPath("/embed", playRef(s))
	}
	return ""
}

// ReplayExt is the replay file extension of a row's platform (".dat", ".bsor").
func ReplayExt(ctx context.Context, s *model.Score) string {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(s.Platform); ok {
			return p.ReplayExt
		}
	}
	return ""
}

// AccountPath is the shareable /p/{slug}/{externalID} link of an account.
func AccountPath(ctx context.Context, platformName, externalID string) string {
	if reg := Platforms(ctx); reg != nil {
		return reg.AccountPath(platformName, externalID)
	}
	return ""
}

// ProfileURL is an account's profile on its platform.
func ProfileURL(ctx context.Context, platformName, externalID string) string {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(platformName); ok {
			return p.ProfileURL(externalID)
		}
	}
	return ""
}

// PlatformName is a platform's display name (its stored name when unknown).
func PlatformName(ctx context.Context, platformName string) string {
	if reg := Platforms(ctx); reg != nil {
		if p, ok := reg.Get(platformName); ok {
			return p.DisplayName
		}
	}
	return platformName
}

// ViewerSrc is the same-origin ArcViewer URL that loads one archived replay.
// When the instance enables headset rendering, ArcViewer's settingsOverride
// parameter applies the admin's choices over each visitor's own viewer
// settings (the visitor can still veto them inside the viewer UI).
func ViewerSrc(ctx context.Context, base string, s *model.Score, autoplay, loop, hideUI bool, st service.Settings) string {
	v := url.Values{}
	v.Set("replayURL", base+ReplayPath(ctx, s))
	v.Set("noProxy", "true")
	if autoplay {
		v.Set("autoPlay", "true")
	}
	if loop {
		v.Set("loop", "true")
	}
	if hideUI {
		v.Set("uiOff", "true")
	}
	if st.ViewerShowHeadset {
		r, g, b, ok := service.ParseHexColor(st.ViewerHeadsetColor)
		if !ok {
			r, g, b = 0.529, 0.529, 0.529
		}
		round := func(f float64) float64 { return math.Round(f*1000) / 1000 }
		override := struct {
			Bools  map[string]bool    `json:"Bools"`
			Ints   map[string]int     `json:"Ints"`
			Floats map[string]float64 `json:"Floats"`
		}{
			// All three dictionaries must be present (empty is fine):
			// Newtonsoft leaves omitted ones null and ArcViewer dereferences
			// them when overrides are active.
			Bools: map[string]bool{"showheadset": true},
			Ints:  map[string]int{},
			Floats: map[string]float64{
				"headsetalpha":   round(st.ViewerHeadsetAlpha),
				"headsetcolor.r": round(r),
				"headsetcolor.g": round(g),
				"headsetcolor.b": round(b),
			},
		}
		if data, err := json.Marshal(override); err == nil {
			v.Set("settingsOverride", string(data))
		}
	}
	return "/viewer/?" + v.Encode()
}
```

- [ ] **Step 5: Templates**

templ components receive the request context as `ctx`. Update the call sites:

- `player.templ` `scoreRow`: `href={ templ.SafeURL(ScoreURL(ctx, s)) }` (Task 8 rewrites this page).
- `sync.templ` `FailedTable`: `href={ templ.SafeURL(ScoreURL(ctx, s)) }`, and the second line shows the platform's ID: `{ PlayerName(s) } · { PlatformName(ctx, s.Platform) } #{ s.ExternalID }` (the retry button keeps posting the internal `s.ID`).
- `score.templ` `replaySection`:

```templ
templ replaySection(v ScoreView) {
	switch v.Score.ReplayState {
		case model.ReplayArchived:
			if v.ViewerAvailable {
				<div class="aspect-video w-full overflow-hidden rounded-lg border bg-muted">
					<iframe src={ EmbedPath(ctx, v.Score) } title="Replay viewer" class="size-full border-0" allow="fullscreen; autoplay" allowfullscreen loading="lazy"></iframe>
				</div>
			} else {
				@stateAlert("Viewer unavailable", "The 3D viewer is not bundled with this build. You can still download the replay.", false)
			}
			<div class="flex flex-wrap gap-2">
				@button.Button(button.Props{Href: ReplayPath(ctx, v.Score), Attributes: templ.Attributes{"download": ""}}) {
					@icon.Download()
					{ "Download " + ReplayExt(ctx, v.Score) }
				}
				if v.ViewerAvailable {
					@button.Button(button.Props{Variant: button.VariantOutline, Href: EmbedPath(ctx, v.Score), Target: "_blank"}) {
						@icon.Play()
						Open viewer
					}
				}
			</div>
			if v.ViewerAvailable {
				@field.Field() {
					@field.Label(field.LabelProps{For: "embed-code"}) {
						Embed on your site
					}
					@textarea.Textarea(textarea.Props{ID: "embed-code", Value: v.EmbedCode, ReadOnly: true, Rows: 3, Class: "font-mono text-xs"})
					<div>
						@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Attributes: templ.Attributes{"data-copy": "#embed-code"}}) {
							@icon.Copy()
							<span data-copy-label>Copy embed code</span>
						}
					</div>
					@field.Description() {
						Append ?autoplay=1, ?loop=1 or ?ui=0 to the iframe URL to change how it starts.
					}
				}
			}
			<p class="font-mono text-xs break-all text-muted-foreground">{ HumanBytes(v.Score.ReplaySize) } · sha256 { v.Score.ReplaySHA256 }</p>
		case model.ReplayPending:
			@stateAlert("Replay queued", "This replay is queued for archiving and will be available shortly.", false)
		case model.ReplayFailed:
			@stateAlert("Archiving failed", "Archiving failed after several attempts: "+v.Score.LastError, true)
		case model.ReplayGone:
			@stateAlert("Replay pruned", PlatformName(ctx, v.Score.Platform)+" pruned this replay before it could be archived.", false)
		default:
			@stateAlert("No replay", PlatformName(ctx, v.Score.Platform)+" has no replay for this score.", false)
	}
}
```

In `ScorePage`, add a platform badge first in the badge row: `@badge.Badge(badge.Props{Variant: badge.VariantOutline}) { { PlatformName(ctx, v.Score.Platform) } }`.

Run: `make generate`
Expected: `*_templ.go` regenerated, no errors.

- [ ] **Step 6: Middleware, CSP and routes**

`internal/httpx/httpx.go`:

```go
// DefaultCSP is the policy for every UI page. imgHosts are the platforms'
// image hosts (avatars, covers), from the registry.
func DefaultCSP(nonce string, imgHosts []string) string {
	img := "'self' data:"
	if len(imgHosts) > 0 {
		img += " " + strings.Join(imgHosts, " ")
	}
	return "default-src 'self'; script-src 'self' 'nonce-" + nonce + "'; style-src 'self' 'unsafe-inline'; " +
		"img-src " + img + "; frame-src 'self'; connect-src 'self'; " +
		"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

// SecurityHeaders sets baseline headers and a per-request CSP nonce, which
// templ components read via templ.GetNonce. Handlers may override the CSP.
func SecurityHeaders(imgHosts ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := make([]byte, 16)
			_, _ = rand.Read(raw)
			nonce := base64.RawStdEncoding.EncodeToString(raw)
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			if strings.HasPrefix(r.URL.Path, "/api/docs") {
				h.Set("Content-Security-Policy", DocsCSP)
			} else {
				h.Set("Content-Security-Policy", DefaultCSP(nonce, imgHosts))
			}
			next.ServeHTTP(w, r.WithContext(templ.WithNonce(r.Context(), nonce)))
		})
	}
}
```

`internal/web/web.go` — in `Middleware`, replace `hd = httpx.SecurityHeaders(hd)` and add the registry:

```go
	hd := cop.Handler(next)
	hd = h.requireSetup(hd)
	hd = h.loadSession(hd)
	hd = h.withPlatforms(hd)
	hd = httpx.BaseURL(h.cfg.BaseURL, h.cfg.TrustProxy)(hd)
	hd = httpx.SecurityHeaders(h.svc.Platforms().ImageHosts()...)(hd)
```

```go
// withPlatforms gives every component the registry (views.ScoreURL & co).
func (h *Handler) withPlatforms(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(views.WithPlatforms(r.Context(), h.svc.Platforms())))
	})
}
```

and register the per-platform routes after the legacy ones:

```go
	mux.HandleFunc("GET /s/{id}", h.score)
	mux.HandleFunc("GET /s/{slug}/{externalID}", h.platformScore(model.KindScore))
	mux.HandleFunc("GET /s/{slug}/attempt/{externalID}", h.platformScore(model.KindAttempt))
	mux.HandleFunc("GET /r/{file}", h.replayFile)
	mux.HandleFunc("GET /r/{slug}/{file}", h.platformReplay(model.KindScore))
	mux.HandleFunc("GET /r/{slug}/attempt/{file}", h.platformReplay(model.KindAttempt))
	mux.HandleFunc("OPTIONS /r/{file}", h.replayPreflight)
	mux.HandleFunc("OPTIONS /r/{slug}/{file}", h.replayPreflight)
	mux.HandleFunc("OPTIONS /r/{slug}/attempt/{file}", h.replayPreflight)
	mux.HandleFunc("GET /embed/{id}", h.embed)
	mux.HandleFunc("GET /embed/{slug}/{externalID}", h.platformEmbed(model.KindScore))
	mux.HandleFunc("GET /embed/{slug}/attempt/{externalID}", h.platformEmbed(model.KindAttempt))
```

(add the `internal/model` import to `web.go`).

- [ ] **Step 7: Lookups and handlers**

`internal/web/plays.go`:

```go
package web

import (
	"net/http"
	"strconv"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// legacyPlay resolves the ID of a bare route (/s/{id}, /r/{id}.dat,
// /embed/{id}): scores of the legacy platform only, so no other platform's
// internal ID can shadow an old link (spec §6.1).
func (h *Handler) legacyPlay(r *http.Request, idStr string) (*model.Score, error) {
	id, ok := parseID(idStr)
	lp, legacy := h.svc.Platforms().Legacy()
	if !ok || !legacy {
		return nil, service.ErrNotFound
	}
	return h.svc.GetPlay(r.Context(), lp.Name, model.KindScore, strconv.FormatInt(id, 10))
}

// slugPlay resolves /…/{slug}/[attempt/]{externalID}; unknown slugs are not found.
func (h *Handler) slugPlay(r *http.Request, kind, externalID string) (*model.Score, error) {
	p, ok := h.svc.Platforms().BySlug(r.PathValue("slug"))
	if !ok {
		return nil, service.ErrNotFound
	}
	return h.svc.GetPlay(r.Context(), p.Name, kind, externalID)
}
```

`internal/web/public.go` — replace `score`:

```go
// score serves the legacy /s/{id} (ScoreSaber scores).
func (h *Handler) score(w http.ResponseWriter, r *http.Request) {
	sc, err := h.legacyPlay(r, r.PathValue("id"))
	h.scorePage(w, r, sc, err)
}

// platformScore serves /s/{slug}/{externalID} and /s/{slug}/attempt/{externalID}.
func (h *Handler) platformScore(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := h.slugPlay(r, kind, r.PathValue("externalID"))
		h.scorePage(w, r, sc, err)
	}
}

func (h *Handler) scorePage(w http.ResponseWriter, r *http.Request, sc *model.Score, err error) {
	if isNotFound(err) {
		h.notFound(w, r, "No such score.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	base := httpx.BaseURLFrom(ctx)
	v := views.ScoreView{
		Score: sc, ViewerAvailable: h.viewer.Available(), Now: h.svc.Now(),
		ReplayURL: base + views.ReplayPath(ctx, sc), EmbedURL: base + views.EmbedPath(ctx, sc),
	}
	v.EmbedCode = views.EmbedSnippet(v.EmbedURL)
	title := fmt.Sprintf("%s by %s", views.SongTitle(sc), views.PlayerName(sc))
	p := h.page(r, title)
	p.OG = &views.OpenGraph{Title: title, Description: views.ScoreSummary(sc), Image: views.CoverURL(sc), URL: base + views.ScoreURL(ctx, sc)}
	render(w, r, http.StatusOK, views.ScorePage(p, v))
}
```

(add the `internal/model` import).

`internal/web/replay.go` — replace `replayFile` and `embed`, add the platform variants:

```go
// replayFile serves the legacy /r/{id}.dat.
func (h *Handler) replayFile(w http.ResponseWriter, r *http.Request) {
	setCORS(w.Header())
	idStr, ok := strings.CutSuffix(r.PathValue("file"), ".dat")
	if !ok {
		http.NotFound(w, r)
		return
	}
	sc, err := h.legacyPlay(r, idStr)
	h.serveReplay(w, r, sc, err)
}

// platformReplay serves /r/{slug}/{externalID}{ext} and /r/{slug}/attempt/….
func (h *Handler) platformReplay(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w.Header())
		p, ok := h.svc.Platforms().BySlug(r.PathValue("slug"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		id, ok := strings.CutSuffix(r.PathValue("file"), p.ReplayExt)
		if !ok || id == "" {
			http.NotFound(w, r)
			return
		}
		sc, err := h.svc.GetPlay(r.Context(), p.Name, kind, id)
		h.serveReplay(w, r, sc, err)
	}
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// serveReplay streams an archived replay: CORS *, Range, ETag (sha256),
// immutable caching, download as {externalID}{ext}.
func (h *Handler) serveReplay(w http.ResponseWriter, r *http.Request, sc *model.Score, err error) {
	if err != nil || sc.ReplayState != model.ReplayArchived {
		http.NotFound(w, r)
		return
	}
	f, err := h.svc.OpenReplay(sc)
	if err != nil {
		slog.Error("archived replay file missing", "score", sc.ID, "err", err)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	name := unsafeFileChars.ReplaceAllString(sc.ExternalID+views.ReplayExt(r.Context(), sc), "_")
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	hd.Set("ETag", `"`+sc.ReplaySHA256+`"`)
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// embed serves the legacy /embed/{id}.
func (h *Handler) embed(w http.ResponseWriter, r *http.Request) {
	sc, err := h.legacyPlay(r, r.PathValue("id"))
	h.embedPage(w, r, sc, err)
}

// platformEmbed serves /embed/{slug}/{externalID} and /embed/{slug}/attempt/….
func (h *Handler) platformEmbed(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := h.slugPlay(r, kind, r.PathValue("externalID"))
		h.embedPage(w, r, sc, err)
	}
}

// embedPage is the iframe-able wrapper around the same-origin viewer.
func (h *Handler) embedPage(w http.ResponseWriter, r *http.Request, sc *model.Score, err error) {
	w.Header().Set("Content-Security-Policy", httpx.EmbedCSP)
	if err != nil || !h.viewer.Available() || sc.ReplayState != model.ReplayArchived {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	q := r.URL.Query()
	st, serr := h.svc.Settings(r.Context())
	if serr != nil {
		st = service.DefaultSettings
	}
	src := views.ViewerSrc(r.Context(), httpx.BaseURLFrom(r.Context()), sc, q.Get("autoplay") == "1", q.Get("loop") == "1", q.Get("ui") == "0", st)
	render(w, r, http.StatusOK, views.Embed(views.SongTitle(sc)+" · "+views.PlayerName(sc), src))
}
```

(imports of `replay.go`: add `"regexp"`; `strconv` is no longer needed there.)

- [ ] **Step 8: Run the tests**

Run: `go test -race ./internal/platform/ ./internal/httpx/ ./internal/web/... ./internal/app/`
Expected: PASS — including the unchanged legacy tests (`TestReplayDownload` keeps `filename="1.dat"`, `TestScorePageArchived` keeps `href="/r/1.dat"` and "Download .dat").

- [ ] **Step 9: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/platform internal/httpx internal/web
git commit -m "feat(web): registry-built links, per-platform score/replay/embed routes and CSP"
```

---
## Task 8: Merged player page

`/p/{id}` becomes the merged view of spec §6.1: one row per map, one chip per platform with its current PB (score %, raw score, rank, replay state, link), and an "N more plays" button that loads every play of that map from `/p/{id}/map?key=…`. Filters gain platform and score bounds. The header lists every account with its share link (`/p/{slug}/{externalID}`) and profile link. A merged-away ID answers **301** to the survivor, keeping the query.

**Files:**
- Modify: `internal/web/public.go` (`player`, `playerMap`, `scoreFilter`, `scoreBound`, `accountPlatforms`)
- Modify: `internal/web/web.go` (route `GET /p/{id}/map`)
- Modify: `internal/web/views/player.templ` (rewritten), `internal/web/views/urls.go` (`scoreQuery`, `PlayerScoresURL`, `MapPlaysURL`)
- Test: `internal/web/public_test.go`, `internal/web/views/urls_test.go`

**Interfaces:**
- Consumes: Task 5 `ResolvePlayerID`, `MergePlayers`; Task 6 `ListMapGroups`, `MapGroup`, `ListScores` with `MapKey`; Task 7 `views.ScoreURL/AccountPath/ProfileURL/PlatformName`, `newEnvWith`, `seedTP`.
- Produces:
  - `views.PlayerView{Player *model.Player; Summary service.PlayerSummary; Groups service.MapGroupList; Filter service.ScoreFilter; Platforms []platform.Platform; Now time.Time}`
  - `views.MapPlaysView{Plays service.ScoreList; Now time.Time}`, components `views.MapPlays`, `views.PlayChip(*model.Score)`
  - `views.MapPlaysURL(playerID string, f service.ScoreFilter, mapKey string) string`; `PlayerScoresURL` also encodes `platform`, `min_score`, `max_score`
  - route `GET /p/{id}/map?key={map_key}&…filters` (htmx fragment); query params `platform`, `min_score`, `max_score` on `/p/{id}`

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/views/urls_test.go`:

```go
func TestMapPlaysURL(t *testing.T) {
	f := service.ScoreFilter{Platform: "testplat", MinScore: new(int64(5)), MaxScore: new(int64(900000))}
	if got := MapPlaysURL("p1", f, "hash501/Standard/9"); got != "/p/p1/map?key=hash501%2FStandard%2F9&max_score=900000&min_score=5&platform=testplat" {
		t.Errorf("MapPlaysURL = %s", got)
	}
	if got := PlayerScoresURL("p1", f, 2); got != "/p/p1?max_score=900000&min_score=5&page=2&platform=testplat" {
		t.Errorf("PlayerScoresURL = %s", got)
	}
}
```

Append to `internal/web/public_test.go` (imports: add `"context"`, `"time"`, `"github.com/yyewolf/ssarchiver/internal/model"`, `"github.com/yyewolf/ssarchiver/internal/testutil"`):

```go
// crossSeed is seed() plus Alice's testplat account: t1 on the same map as
// ScoreSaber score 1 (map 501), and t0, an older non-PB play of that map.
func (e *testEnv) crossSeed() string {
	e.t.Helper()
	a := e.seed()
	if _, err := e.svc.LinkIdentity(context.Background(), a, "abc", "testplat"); err != nil {
		e.t.Fatal(err)
	}
	t1 := testutil.FakePlay(model.KindScore, "t1", "lb-x501", testutil.T0.Add(4*time.Minute), true)
	t1.Leaderboard.SongHash, t1.Leaderboard.GameMode, t1.Leaderboard.Difficulty = "hash501", "Standard", 9
	t1.ModifiedScore = 950_000
	t0 := testutil.FakePlay(model.KindScore, "t0", "lb-x501", testutil.T0.Add(-time.Hour), false)
	t0.Leaderboard, t0.PersonalBest = t1.Leaderboard, false
	testutil.UpsertFake(e.t, e.svc, a, t1, t0)
	return a
}

func TestMergedPlayerPage(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.crossSeed()
	body := e.do(http.MethodGet, "/p/"+a, nil).Body.String()
	contains(t, body, `href="/s/1"`, `href="/s/tp/t1"`, "TestPlat", "ScoreSaber", "950,000",
		`href="/p/ss/1001"`, `href="/p/tp/abc"`, `href="https://tp.example/u/abc"`, "TestPlat profile",
		"1 more play", `hx-get="/p/`+a+`/map?key=hash501%2FStandard%2F9"`, `hx-target="#plays-0"`,
		"ScoreSaber &amp; TestPlat replays", `name="platform"`, "All platforms", `name="min_score"`, `name="max_score"`)
	if strings.Count(body, `href="/s/tp/t0"`) != 0 {
		t.Fatal("non-PB plays are behind 'more plays', not chips")
	}
}

func TestPlayerPageMapFragment(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.crossSeed()
	frag := e.do(http.MethodGet, "/p/"+a+"/map?key=hash501%2FStandard%2F9", nil, htmx("plays-0"))
	body := frag.Body.String()
	if frag.Code != 200 || strings.Contains(body, "<html") {
		t.Fatalf("fragment = %d\n%s", frag.Code, body)
	}
	contains(t, body, `href="/s/1"`, `href="/s/tp/t1"`, `href="/s/tp/t0"`, "superseded")
	tpOnly := e.do(http.MethodGet, "/p/"+a+"/map?key=hash501%2FStandard%2F9&platform=testplat", nil, htmx("plays-0")).Body.String()
	if strings.Contains(tpOnly, `href="/s/1"`) {
		t.Fatal("the fragment applies the page's filters")
	}
	for _, p := range []string{"/p/" + a + "/map", "/p/nobody/map?key=x"} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
}

func TestPlayerPagePlatformAndScoreFilters(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.crossSeed()
	get := func(q string) string {
		return e.do(http.MethodGet, "/p/"+a+"?"+q, nil, htmx("scores")).Body.String()
	}
	tp := get("platform=testplat")
	if !strings.Contains(tp, `href="/s/tp/t1"`) || strings.Contains(tp, `href="/s/1"`) || strings.Contains(tp, "Song 502") {
		t.Fatalf("platform filter:\n%s", tp)
	}
	if high := get("min_score=960000"); !strings.Contains(high, "Song 502") {
		t.Fatal("min_score keeps the ScoreSaber maps")
	}
	low := get("max_score=950000")
	if strings.Contains(low, "Song 502") || !strings.Contains(low, `href="/s/tp/t1"`) {
		t.Fatalf("max_score:\n%s", low)
	}
	if bad := get("platform=nope&min_score=abc"); !strings.Contains(bad, "Song 502") {
		t.Fatal("unknown values are ignored, not errors")
	}
}

func TestSinglePlatformPageHasNoPlatformFilter(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.seed()
	if body := e.do(http.MethodGet, "/p/"+a, nil).Body.String(); strings.Contains(body, `name="platform"`) {
		t.Fatal("the platform select only appears for players with several accounts")
	}
}

func TestMergedAwayPlayerRedirects(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	e.setup()
	a := e.seed()
	tess := e.seedTP()
	if err := e.svc.MergePlayers(context.Background(), tess, a); err != nil {
		t.Fatal(err)
	}
	res := e.do(http.MethodGet, "/p/"+tess+"?page=2&q=x", nil)
	if res.Code != http.StatusMovedPermanently || res.Header().Get("Location") != "/p/"+a+"?page=2&q=x" {
		t.Fatalf("alias = %d %q", res.Code, res.Header().Get("Location"))
	}
	if loc := e.do(http.MethodGet, "/p/tp/abc", nil).Header().Get("Location"); loc != "/p/"+a {
		t.Fatalf("account links follow the merge: %q", loc)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/web/...`
Expected: FAIL — undefined: `MapPlaysURL`; no chips, no `/p/{id}/map` route, no 301.

- [ ] **Step 3: URL helpers**

`internal/web/views/urls.go` — replace `PlayerScoresURL`:

```go
// scoreQuery encodes the player page's filters (page excluded).
func scoreQuery(f service.ScoreFilter) url.Values {
	q := url.Values{}
	if f.Search != "" {
		q.Set("q", f.Search)
	}
	if f.State != "" {
		q.Set("state", f.State)
	}
	if f.RankedOnly {
		q.Set("ranked", "1")
	}
	if f.Platform != "" {
		q.Set("platform", f.Platform)
	}
	if f.MinScore != nil {
		q.Set("min_score", strconv.FormatInt(*f.MinScore, 10))
	}
	if f.MaxScore != nil {
		q.Set("max_score", strconv.FormatInt(*f.MaxScore, 10))
	}
	return q
}

func PlayerScoresURL(playerID string, f service.ScoreFilter, page int) string {
	q := scoreQuery(f)
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	u := "/p/" + url.PathEscape(playerID)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// MapPlaysURL is the fragment with every play of one map, filters applied.
func MapPlaysURL(playerID string, f service.ScoreFilter, mapKey string) string {
	q := scoreQuery(f)
	q.Set("key", mapKey)
	return "/p/" + url.PathEscape(playerID) + "/map?" + q.Encode()
}
```

- [ ] **Step 4: Rewrite the player page**

`internal/web/views/player.templ` (complete file):

```templ
package views

import (
	"fmt"
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/avatar"
	"github.com/yyewolf/ssarchiver/internal/web/components/badge"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/empty"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
	"github.com/yyewolf/ssarchiver/internal/web/components/input"
	"github.com/yyewolf/ssarchiver/internal/web/components/nativeselect"
	"github.com/yyewolf/ssarchiver/internal/web/components/progress"
	"github.com/yyewolf/ssarchiver/internal/web/components/table"
)

type PlayerView struct {
	Player    *model.Player
	Summary   service.PlayerSummary
	Groups    service.MapGroupList
	Filter    service.ScoreFilter
	Platforms []platform.Platform // the player's platforms, primary first
	Now       time.Time
}

// listing are the accounts whose score feed is still walking its history.
func (v PlayerView) listing() []service.Identity {
	var out []service.Identity
	for _, id := range v.Summary.Identities {
		if f := id.Feed(model.KindScore); f.BackfillState != "" && f.BackfillState != model.BackfillDone {
			out = append(out, id)
		}
	}
	return out
}

func boundValue(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

// extraPlays is how many plays a map row does not show.
func extraPlays(g service.MapGroup) int64 {
	if len(g.Chips) == 0 {
		return max(g.Plays-1, 0) // the newest play stands in for the missing chips
	}
	return g.More()
}

func morePlaysLabel(n int64) string {
	if n == 1 {
		return "1 more play"
	}
	return Number(n) + " more plays"
}

templ PlayerPage(p Page, v PlayerView) {
	@Layout(p) {
		<div class="mb-6 flex flex-wrap items-center gap-4">
			@avatar.Avatar(avatar.Props{Class: "size-14"}) {
				@avatar.Image(avatar.ImageProps{Src: v.Player.AvatarURL, Alt: v.Player.Name})
				@avatar.Fallback() {
					{ Initials(v.Player.Name) }
				}
			}
			<div class="min-w-0 flex-1">
				<h1 class="truncate text-2xl font-semibold tracking-tight">{ v.Player.Name }</h1>
				<p class="text-sm text-muted-foreground">{ v.Player.Country }</p>
			</div>
			<dl class="flex gap-8 text-sm">
				@stat("Scores", Number(v.Summary.Counts.Scores))
				@stat("Archived", Number(v.Summary.Counts.Archived))
				@stat("Pending", Number(v.Summary.Counts.Pending))
			</dl>
		</div>
		@accounts(v)
		if len(v.listing()) > 0 || v.Summary.Counts.Pending > 0 {
			@archiveProgress(v)
		}
		@scoreFilters(v)
		@ScoreTable(v)
	}
}

templ stat(label, value string) {
	<div class="flex flex-col">
		<dt class="text-muted-foreground">{ label }</dt>
		<dd class="text-lg font-semibold tabular-nums">{ value }</dd>
	</div>
}

templ accounts(v PlayerView) {
	<ul class="mb-6 flex flex-wrap gap-2 text-sm">
		for _, id := range v.Summary.Identities {
			<li class="flex items-center gap-2 rounded-lg border px-3 py-1.5">
				@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
					{ PlatformName(ctx, id.Platform) }
				}
				<span class="tabular-nums text-muted-foreground">{ Number(id.Scores().Archived) } / { Number(id.Scores().Replays()) } replays</span>
				<a class="underline-offset-4 hover:underline" href={ templ.SafeURL(ProfileURL(ctx, id.Platform, id.ExternalID)) } target="_blank" rel="noopener">{ PlatformName(ctx, id.Platform) } profile</a>
				<a
					class="text-muted-foreground hover:text-foreground"
					href={ templ.SafeURL(AccountPath(ctx, id.Platform, id.ExternalID)) }
					title="Shareable link to this account"
					aria-label={ "Shareable " + PlatformName(ctx, id.Platform) + " link" }
				>
					@icon.Link2(icon.Props{Size: 14})
				</a>
			</li>
		}
	</ul>
}

templ archiveProgress(v PlayerView) {
	<div class="mb-6 flex flex-col gap-2 rounded-lg border p-4">
		@progress.Progress(progress.Props{Value: int(v.Summary.Counts.Archived), Max: max(int(v.Summary.Counts.Replays()), 1)}) {
			@progress.Label() {
				Archiving replays
			}
		}
		<p class="text-xs text-muted-foreground">
			{ Number(v.Summary.Counts.Archived) } of { Number(v.Summary.Counts.Replays()) } replays archived
			for _, id := range v.listing() {
				{{ f := id.Feed(model.KindScore) }}
				· still listing older { PlatformName(ctx, id.Platform) } scores (page { strconv.Itoa(f.BackfillPage) } of { strconv.Itoa(max(f.BackfillTotalPages, f.BackfillPage)) })
			}
		</p>
	</div>
}

templ scoreFilters(v PlayerView) {
	<form
		id="score-filters"
		action={ templ.SafeURL("/p/" + v.Player.ID) }
		method="get"
		class="mb-4 flex flex-wrap items-center gap-2"
		hx-get={ "/p/" + v.Player.ID }
		hx-target="#scores"
		hx-swap="outerHTML"
		hx-push-url="true"
		hx-trigger="input changed delay:300ms from:#score-search, change"
	>
		<div class="relative w-full sm:w-72">
			<span class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-muted-foreground">
				@icon.Search(icon.Props{Size: 16})
			</span>
			@input.Input(input.Props{ID: "score-search", Name: "q", Type: "search", Value: v.Filter.Search, Placeholder: "Search song, artist or mapper", Class: "pl-8"})
		</div>
		if len(v.Platforms) > 1 {
			@nativeselect.NativeSelect(nativeselect.Props{Name: "platform", Attributes: templ.Attributes{"aria-label": "Platform filter"}}) {
				@nativeselect.Option(nativeselect.OptionProps{Value: "", Selected: v.Filter.Platform == ""}) {
					All platforms
				}
				for _, pf := range v.Platforms {
					@nativeselect.Option(nativeselect.OptionProps{Value: pf.Name, Selected: v.Filter.Platform == pf.Name}) {
						{ pf.DisplayName }
					}
				}
			}
		}
		@nativeselect.NativeSelect(nativeselect.Props{Name: "state", Attributes: templ.Attributes{"aria-label": "Replay filter"}}) {
			@nativeselect.Option(nativeselect.OptionProps{Value: "", Selected: v.Filter.State == ""}) {
				All scores
			}
			@nativeselect.Option(nativeselect.OptionProps{Value: service.FilterWithReplay, Selected: v.Filter.State == service.FilterWithReplay}) {
				With replay
			}
			@nativeselect.Option(nativeselect.OptionProps{Value: service.FilterArchived, Selected: v.Filter.State == service.FilterArchived}) {
				Archived
			}
		}
		@nativeselect.NativeSelect(nativeselect.Props{Name: "ranked", Attributes: templ.Attributes{"aria-label": "Map filter"}}) {
			@nativeselect.Option(nativeselect.OptionProps{Value: "", Selected: !v.Filter.RankedOnly}) {
				All maps
			}
			@nativeselect.Option(nativeselect.OptionProps{Value: "1", Selected: v.Filter.RankedOnly}) {
				Ranked only
			}
		}
		@input.Input(input.Props{Name: "min_score", Type: "number", Value: boundValue(v.Filter.MinScore), Placeholder: "Min score", Class: "w-32", Attributes: templ.Attributes{"min": "0", "aria-label": "Minimum score"}})
		@input.Input(input.Props{Name: "max_score", Type: "number", Value: boundValue(v.Filter.MaxScore), Placeholder: "Max score", Class: "w-32", Attributes: templ.Attributes{"min": "0", "aria-label": "Maximum score"}})
		<noscript>
			@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantOutline}) {
				Apply
			}
		</noscript>
	</form>
}

templ ScoreTable(v PlayerView) {
	<div id="scores">
		if len(v.Groups.Groups) == 0 {
			@empty.Empty(empty.Props{Class: "border border-dashed"}) {
				@empty.Header() {
					@empty.Title() {
						No scores match
					}
					@empty.Description() {
						Try another search or filter.
					}
				}
			}
		} else {
			<div class="rounded-lg border">
				@table.Table() {
					@table.Header() {
						@table.Row() {
							@table.Head() {
								Map
							}
							@table.Head(table.HeadProps{Class: "hidden md:table-cell"}) {
								Difficulty
							}
							@table.Head() {
								Best per platform
							}
							@table.Head(table.HeadProps{Class: "hidden lg:table-cell"}) {
								Last played
							}
						}
					}
					@table.Body() {
						for i, g := range v.Groups.Groups {
							@groupRow(v, i, g)
						}
					}
				}
			</div>
			@pagination(v)
		}
	</div>
}

templ groupRow(v PlayerView, i int, g service.MapGroup) {
	@table.Row() {
		@table.Cell() {
			<div class="flex items-center gap-3">
				if cover := CoverURL(g.Latest); cover != "" {
					<img src={ cover } alt="" loading="lazy" class="size-10 shrink-0 rounded-md border object-cover"/>
				}
				<span class="min-w-0">
					<span class="block max-w-64 truncate font-medium">{ SongTitle(g.Latest) }</span>
					<span class="block max-w-64 truncate text-xs text-muted-foreground">{ SongAuthor(g.Latest) } · { Mapper(g.Latest) }</span>
				</span>
			</div>
			<div id={ "plays-" + strconv.Itoa(i) } class="empty:hidden"></div>
		}
		@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
			@DifficultyBadge(g.Latest)
		}
		@table.Cell() {
			<div class="flex flex-wrap items-center gap-2">
				for _, c := range g.Chips {
					@PlayChip(c)
				}
				if len(g.Chips) == 0 {
					@PlayChip(g.Latest)
				}
				if n := extraPlays(g); n > 0 {
					@button.Button(button.Props{Variant: button.VariantGhost, Size: button.SizeSm, Attributes: templ.Attributes{
						"hx-get": MapPlaysURL(v.Player.ID, v.Filter, g.MapKey), "hx-target": "#plays-" + strconv.Itoa(i), "hx-swap": "innerHTML",
					}}) {
						{ morePlaysLabel(n) }
					}
				}
			</div>
		}
		@table.Cell(table.CellProps{Class: "hidden text-muted-foreground lg:table-cell"}) {
			{ TimeAgo(g.Latest.SetAt, v.Now) }
		}
	}
}

// PlayChip is one play: platform, accuracy, raw score, rank and replay state.
templ PlayChip(s *model.Score) {
	<a href={ templ.SafeURL(ScoreURL(ctx, s)) } class="inline-flex items-center gap-2 rounded-md border px-2 py-1 text-xs underline-offset-4 hover:underline">
		<span class="font-medium">{ PlatformName(ctx, s.Platform) }</span>
		<span class="tabular-nums">{ Percent(s.Accuracy) }</span>
		<span class="tabular-nums text-muted-foreground">{ Number(s.ModifiedScore) }</span>
		<span class="tabular-nums text-muted-foreground">#{ strconv.Itoa(s.Rank) }</span>
		@ReplayBadge(s.ReplayState)
	</a>
}

type MapPlaysView struct {
	Plays service.ScoreList
	Now   time.Time
}

// MapPlays is the "more plays" fragment: every play of one map, newest first.
templ MapPlays(v MapPlaysView) {
	<ul class="mt-2 flex flex-col gap-1">
		for _, s := range v.Plays.Items {
			<li class="flex flex-wrap items-center gap-2 text-xs">
				@PlayChip(s)
				<span class="text-muted-foreground">{ TimeAgo(s.SetAt, v.Now) }</span>
				if s.Kind == model.KindScore && !s.PersonalBest {
					<span class="text-muted-foreground">superseded</span>
				}
			</li>
		}
	</ul>
	if v.Plays.Total > int64(len(v.Plays.Items)) {
		<p class="mt-1 text-xs text-muted-foreground">Showing the newest { Number(len(v.Plays.Items)) } of { Number(v.Plays.Total) } plays.</p>
	}
}

templ DifficultyBadge(s *model.Score) {
	if s.Leaderboard != nil {
		@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
			{ DifficultyName(s.Leaderboard.Difficulty) }
		}
	}
}

templ ReplayBadge(state string) {
	switch state {
		case model.ReplayArchived:
			@badge.Badge() {
				Archived
			}
		case model.ReplayPending:
			@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
				Pending
			}
		case model.ReplayFailed:
			@badge.Badge(badge.Props{Variant: badge.VariantDestructive}) {
				Failed
			}
		case model.ReplayGone:
			@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
				Pruned
			}
		default:
			<span class="text-muted-foreground">—</span>
	}
}

templ pagination(v PlayerView) {
	if v.Groups.Pages > 1 {
		<nav class="mt-4 flex items-center justify-between text-sm" aria-label="Pagination">
			<span class="text-muted-foreground">Page { strconv.Itoa(v.Groups.Page) } of { strconv.Itoa(v.Groups.Pages) } · { fmt.Sprintf("%s maps", Number(v.Groups.Total)) }</span>
			<div class="flex gap-2">
				@pageButton(v, v.Groups.Page-1, "Previous", v.Groups.Page <= 1)
				@pageButton(v, v.Groups.Page+1, "Next", v.Groups.Page >= v.Groups.Pages)
			</div>
		</nav>
	}
}

templ pageButton(v PlayerView, page int, label string, disabled bool) {
	if disabled {
		@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Disabled: true}) {
			{ label }
		}
	} else {
		{{ u := PlayerScoresURL(v.Player.ID, v.Filter, page) }}
		@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Href: u, Attributes: templ.Attributes{"hx-get": u, "hx-target": "#scores", "hx-swap": "outerHTML", "hx-push-url": "true"}}) {
			{ label }
		}
	}
}
```

- [ ] **Step 5: Handlers and route**

`internal/web/web.go` — add after `GET /p/{slug}/{externalID}`: `mux.HandleFunc("GET /p/{id}/map", h.playerMap)` (the literal `map` segment is more specific than `{externalID}`, so the mux prefers it; spec §6.1 forbids a `map` slug).

`internal/web/public.go` — replace `player` and add the helpers (imports: add `"strings"`, `"github.com/yyewolf/ssarchiver/internal/platform"`):

```go
func (h *Handler) player(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, aliased, err := h.svc.ResolvePlayerID(ctx, r.PathValue("id"))
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if aliased { // merged away: the survivor's page (spec §6.1)
		target := "/p/" + url.PathEscape(id)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	sum, err := h.svc.GetPlayerSummary(ctx, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	f := scoreFilter(r.URL.Query(), id, h.svc.Platforms())
	groups, err := h.svc.ListMapGroups(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plats := h.accountPlatforms(sum)
	v := views.PlayerView{Player: &sum.Player, Summary: sum, Groups: groups, Filter: f, Platforms: plats, Now: h.svc.Now()}
	if isHTMX(r) && r.Header.Get("HX-Target") == "scores" {
		render(w, r, http.StatusOK, views.ScoreTable(v))
		return
	}
	names := make([]string, 0, len(plats))
	for _, p := range plats {
		names = append(names, p.DisplayName)
	}
	p := h.page(r, sum.Name)
	p.OG = &views.OpenGraph{
		Title:       sum.Name + " · " + strings.Join(names, " & ") + " replays",
		Description: fmt.Sprintf("%s archived replays", views.Number(sum.Counts.Archived)),
		Image:       sum.AvatarURL,
		URL:         httpx.BaseURLFrom(ctx) + "/p/" + id,
	}
	render(w, r, http.StatusOK, views.PlayerPage(p, v))
}

// playerMap is the htmx fragment with every play of one map (spec §6.1).
func (h *Handler) playerMap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	q := r.URL.Query()
	if _, err := h.svc.GetPlayer(ctx, id); err != nil || q.Get("key") == "" {
		if err != nil && !isNotFound(err) {
			h.serverError(w, r, err)
			return
		}
		h.notFound(w, r, "No such map.")
		return
	}
	f := scoreFilter(q, id, h.svc.Platforms())
	f.MapKey, f.Page, f.PerPage = q.Get("key"), 1, 100
	list, err := h.svc.ListScores(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.MapPlays(views.MapPlaysView{Plays: list, Now: h.svc.Now()}))
}

// scoreFilter reads the player page's filters; unknown values are ignored.
func scoreFilter(q url.Values, playerID string, reg *platform.Registry) service.ScoreFilter {
	page, _ := strconv.Atoi(q.Get("page"))
	f := service.ScoreFilter{PlayerID: playerID, Search: q.Get("q"), RankedOnly: q.Get("ranked") == "1", Page: page, PerPage: 50}
	if s := q.Get("state"); s == service.FilterWithReplay || s == service.FilterArchived {
		f.State = s
	}
	if p := q.Get("platform"); p != "" {
		if _, ok := reg.Get(p); ok {
			f.Platform = p
		}
	}
	f.MinScore, f.MaxScore = scoreBound(q.Get("min_score")), scoreBound(q.Get("max_score"))
	return f
}

func scoreBound(s string) *int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

// accountPlatforms are the registry entries of the player's accounts, primary first.
func (h *Handler) accountPlatforms(sum service.PlayerSummary) []platform.Platform {
	var out []platform.Platform
	for _, id := range sum.Identities {
		if p, ok := h.svc.Platforms().Get(id.Platform); ok {
			out = append(out, p)
		}
	}
	return out
}
```

Run: `make generate && go test -race ./internal/web/...`
Expected: PASS — including the existing `TestPlayerPage`, `TestPlayerPageProfileLinkUsesAccountID` and `TestPlayerPageHTMXPartialAndFilters` (chips link to `/s/1`, the replay badges still read "Archived"/"Pending", the empty state still says "No scores match").

- [ ] **Step 6: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.` (The page is checked by eye with real BeatLeader data in Task 11.)

```bash
git add internal/web
git commit -m "feat(web): merged player page with one row per map, per-platform chips and account links"
```

---
## Task 9: Admin UI — accounts, link/unlink/merge; sync queue per feed; event filters

**Manage** gets what spec §6.3 asks for, built from native elements (the component set has no dropdown or dialog; `<details>` menus and `hx-confirm` cover it):

- the add form gains a platform select ("Detect from URL" by default; bare IDs go to the legacy platform);
- each row shows one badge per account (secondary = OK, outline = paused, destructive + warning icon = error). Clicking a badge opens a small menu: the account ID, its error, **Pause/Resume this account**, and **Unlink** (with a "files" checkbox) when the player has more than one account;
- a **Link** badge opens a form (platform select limited to unlinked platforms, profile URL or ID);
- a **Merge** action lists only players with no platform in common (or explains why none qualifies), confirms, and re-renders the table.

**Sync** lists one queue row per feed (platform, feed, next poll, history page X/Y, counts) and the event log gains platform and feed filters plus an account column.

**Files:**
- Modify: `internal/web/admin.go` (`adminPlayersView`, `renderRow`, `linkIdentity`, `setIdentityEnabled`, `unlinkIdentity`, `mergePlayer`)
- Modify: `internal/web/web.go` (four admin routes)
- Modify: `internal/web/admin_sync.go` (`syncView` per feed, `eventsView` filters)
- Modify: `internal/web/views/admin.templ` (rewritten), `internal/web/views/sync.templ` (`QueueRow`, `queueCard`, `eventFilters`, `EventsTable`, `EventsURL`, `limiterTitle` for the budget cards)
- Modify: `internal/service/events.go` (`EventFilter.Platform`, `.Feed`)
- Test: `internal/web/admin_accounts_test.go` (new), `internal/web/admin_sync_test.go`, `internal/service/events_test.go`

**Interfaces:**
- Consumes: Task 4 `LinkIdentity`, `UnlinkIdentity`, `SetIdentityEnabled`, errors, `Identity.Counts`; Task 5 `MergePlayers`, `ErrMergeConflict`, `ErrMergeSelf`; Task 7 `views.PlatformName`, `newEnvWith`, `seedTP`; Task 8 `crossSeed`.
- Produces:
  - routes `POST /admin/players/{id}/identities` (form `platform`, `ref`), `POST /admin/players/{id}/identities/{platform}/enabled` (`enabled`), `POST /admin/players/{id}/identities/{platform}/delete` (`delete_files`), `POST /admin/players/{id}/merge` (`into`)
  - `views.AdminPlayersView{Players; Platforms []platform.Platform; Now}` + methods `Unlinked(pl) []platform.Platform`, `MergeTargets(pl) []service.PlayerSummary`; `views.AdminPlayerRow(pl, v)`, `views.PlayerRowToast(pl, v, title)`, `views.PlayersMerged(v, title)`
  - `views.QueueRow{Player; Identity service.Identity; Feed model.SyncFeed; Counts service.Counts; NextPoll; ETA}`; `views.EventsView.Platforms`
  - `service.EventFilter{Level, Kind, PlayerID, Platform, Feed string; Page, PerPage int}`

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/events_test.go`:

```go
func TestListEventsByPlatformAndFeed(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Platform: new("testplat"), Feed: new(model.KindScore), Message: "tp score"})
	svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Platform: new("testplat"), Feed: new(model.KindAttempt), Message: "tp attempt"})
	svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, Message: "global"})
	tp, total, _ := svc.ListEvents(ctx, service.EventFilter{Platform: "testplat"})
	if total != 2 || tp[0].Message != "tp attempt" {
		t.Fatalf("platform filter = %v", tp)
	}
	att, total, _ := svc.ListEvents(ctx, service.EventFilter{Feed: model.KindAttempt})
	if total != 1 || att[0].Message != "tp attempt" {
		t.Fatalf("feed filter = %v", att)
	}
}
```

`internal/web/admin_accounts_test.go`:

```go
package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestAdminLinkUnlinkIdentity(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	a := e.seed()
	ctx := context.Background()
	row := "player-" + a

	link := e.do(http.MethodPost, "/admin/players/"+a+"/identities", url.Values{"platform": {"testplat"}, "ref": {"https://tp.example/u/abc"}}, withCookie(c), htmx(row))
	contains(t, link.Body.String(), `id="player-`+a+`"`, "TestPlat", "Account linked")
	if sum, _ := e.svc.GetPlayerSummary(ctx, a); len(sum.Identities) != 2 {
		t.Fatalf("identities = %d", len(sum.Identities))
	}

	again := e.do(http.MethodPost, "/admin/players/"+a+"/identities", url.Values{"platform": {"testplat"}, "ref": {"def"}}, withCookie(c), htmx(row))
	if again.Header().Get("HX-Reswap") != "none" {
		t.Fatal("a refused link must not swap the row")
	}
	contains(t, again.Body.String(), "Could not link account", "already has an account on that platform")

	bob := testutil.AddPlayer(t, e.svc, "1002")
	elsewhere := e.do(http.MethodPost, "/admin/players/"+bob+"/identities", url.Values{"platform": {"testplat"}, "ref": {"abc"}}, withCookie(c), htmx("player-"+bob))
	contains(t, elsewhere.Body.String(), "Already tracked", "already tracked as Alice", "merge the two players")

	pause := e.do(http.MethodPost, "/admin/players/"+a+"/identities/testplat/enabled", url.Values{"enabled": {"false"}}, withCookie(c), htmx(row))
	contains(t, pause.Body.String(), "Account paused", "Resume this account")
	if sum, _ := e.svc.GetPlayerSummary(ctx, a); sum.Identities[1].Enabled {
		t.Fatal("account not paused")
	}

	unlink := e.do(http.MethodPost, "/admin/players/"+a+"/identities/testplat/delete", url.Values{"delete_files": {"on"}}, withCookie(c), htmx(row))
	contains(t, unlink.Body.String(), "Account unlinked")
	if sum, _ := e.svc.GetPlayerSummary(ctx, a); len(sum.Identities) != 1 {
		t.Fatal("account not unlinked")
	}
	last := e.do(http.MethodPost, "/admin/players/"+a+"/identities/scoresaber/delete", url.Values{}, withCookie(c), htmx(row))
	contains(t, last.Body.String(), "Could not unlink account", "keeps at least one account")
	missing := e.do(http.MethodPost, "/admin/players/"+a+"/identities/testplat/enabled", url.Values{"enabled": {"true"}}, withCookie(c), htmx(row))
	contains(t, missing.Body.String(), "Account not found")
}

func TestAdminMergePlayers(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	a := e.seed()
	tess := e.seedTP()
	bob := testutil.AddPlayer(t, e.svc, "1002")

	page := e.do(http.MethodGet, "/admin", nil, withCookie(c)).Body.String()
	contains(t, page, "Detect from URL", `value="testplat"`, `hx-post="/admin/players/`+tess+`/merge"`,
		`hx-post="/admin/players/`+a+`/identities"`, "Link account")
	// Tess can go into Alice or Bob; Alice and Bob share ScoreSaber, so Alice's only target is Tess.
	aliceRow := rowOf(t, page, a)
	if !strings.Contains(aliceRow, `value="`+tess+`"`) || strings.Contains(aliceRow, `<option value="`+bob+`"`) {
		t.Fatalf("Alice's merge targets:\n%s", aliceRow)
	}

	conflict := e.do(http.MethodPost, "/admin/players/"+bob+"/merge", url.Values{"into": {a}}, withCookie(c), htmx("admin-players"))
	contains(t, conflict.Body.String(), "Could not merge", "same platform")

	ok := e.do(http.MethodPost, "/admin/players/"+tess+"/merge", url.Values{"into": {a}}, withCookie(c), htmx("admin-players"))
	body := ok.Body.String()
	contains(t, body, `id="admin-players"`, "Merged Tess into Alice")
	if strings.Contains(body, `id="player-`+tess+`"`) {
		t.Fatal("the merged-away row must disappear")
	}
	if _, err := e.svc.GetPlayer(context.Background(), tess); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("merge not applied")
	}
}

func TestLookupPlayerOnPlatform(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	bare := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"abc"}, "platform": {"testplat"}}, withCookie(c), htmx("lookup-result"))
	contains(t, bare.Body.String(), "Tess", `value="testplat"`, "Track player")
	byURL := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"https://tp.example/u/abc"}}, withCookie(c), htmx("lookup-result"))
	contains(t, byURL.Body.String(), "Tess", `value="testplat"`)
	add := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"abc"}, "platform": {"testplat"}}, withCookie(c), htmx("admin-players"))
	contains(t, add.Body.String(), "Now tracking Tess")
}

// rowOf returns the <tr> of a player in the admin table.
func rowOf(t *testing.T, page, playerID string) string {
	t.Helper()
	start := strings.Index(page, `id="player-`+playerID+`"`)
	if start < 0 {
		t.Fatalf("no row for %s", playerID)
	}
	end := strings.Index(page[start:], "</tr>")
	return page[start : start+end]
}
```

Append to `internal/web/admin_sync_test.go`:

```go
func TestSyncQueuePerFeed(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	a := e.crossSeed()
	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, body, "Per-account progress", "ScoreSaber", "TestPlat")
	if n := strings.Count(body, `hx-post="/admin/players/`+a+`/poll"`); n != 2 {
		t.Fatalf("one queue row per feed: %d", n)
	}
}

// TestBudgetCardPerLimiter: a platform with several limiters (BeatLeader's API
// and CDN) gets one titled card per limiter.
func TestBudgetCardPerLimiter(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	win := func(limit, used int) platform.LimiterSnapshot {
		return platform.LimiterSnapshot{Windows: []platform.WindowSnapshot{{Name: "short", Limit: limit, Used: used, ServerRemaining: -1}}}
	}
	e.status.st = archiver.Status{State: archiver.StateIdle, Since: testutil.T0, Limiters: []archiver.LimiterStatus{
		{Platform: "TestPlat", Name: "testplat/api", Snapshot: win(40, 2)},
		{Platform: "TestPlat", Name: "testplat/cdn", Snapshot: win(20, 5)},
	}}
	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, body, "TestPlat API budget", "TestPlat CDN budget", "Last 10 seconds", "2 / 40", "5 / 20")
}

func TestSyncEventsFilterByPlatform(t *testing.T) {
	e := newEnvWith(t, testutil.NewFakePlatform())
	c := e.login()
	ctx := context.Background()
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Platform: new("testplat"), Feed: new(model.KindScore), Message: "tp polled"})
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Platform: new(model.PlatformScoreSaber), Feed: new(model.KindScore), Message: "ss polled"})
	tp := e.do(http.MethodGet, "/admin/sync/events?platform=testplat", nil, withCookie(c), htmx("sync-events")).Body.String()
	if !strings.Contains(tp, "tp polled") || strings.Contains(tp, "ss polled") {
		t.Fatalf("platform filter:\n%s", tp)
	}
	contains(t, tp, "TestPlat")
	if att := e.do(http.MethodGet, "/admin/sync/events?feed=attempt", nil, withCookie(c), htmx("sync-events")).Body.String(); strings.Contains(att, "polled") {
		t.Fatal("feed filter")
	}
	contains(t, e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String(), `name="platform"`, "All platforms", `name="feed"`, "All feeds")
}
```

(`admin_sync_test.go` needs the `"github.com/yyewolf/ssarchiver/internal/platform"` import for `TestBudgetCardPerLimiter`.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/service/ -run ByPlatformAndFeed && go test ./internal/web/`
Expected: FAIL — `EventFilter.Platform` undefined; admin routes 404 / 405; "Detect from URL" missing.

- [ ] **Step 3: Service event filters**

`internal/service/events.go`:

```go
type EventFilter struct {
	Level, Kind, PlayerID, Platform, Feed string
	Page, PerPage                         int
}
```

and in `ListEvents`, after the player filter:

```go
	if f.Platform != "" {
		do = do.Where(e.Platform.Eq(f.Platform))
	}
	if f.Feed != "" {
		do = do.Where(e.Feed.Eq(f.Feed))
	}
```

- [ ] **Step 4: Rewrite the admin template**

`internal/web/views/admin.templ` (complete file):

```templ
package views

import (
	"strconv"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/avatar"
	"github.com/yyewolf/ssarchiver/internal/web/components/badge"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/card"
	"github.com/yyewolf/ssarchiver/internal/web/components/empty"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
	"github.com/yyewolf/ssarchiver/internal/web/components/input"
	"github.com/yyewolf/ssarchiver/internal/web/components/nativeselect"
	"github.com/yyewolf/ssarchiver/internal/web/components/table"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
)

type AdminPlayersView struct {
	Players   []service.PlayerSummary
	Platforms []platform.Platform // every registered platform, by priority
	Now       time.Time
}

// Unlinked are the registered platforms pl has no account on.
func (v AdminPlayersView) Unlinked(pl service.PlayerSummary) []platform.Platform {
	var out []platform.Platform
	for _, p := range v.Platforms {
		if !hasPlatform(pl, p.Name) {
			out = append(out, p)
		}
	}
	return out
}

// MergeTargets are the players pl can be merged into: no platform in common
// (spec §6.2).
func (v AdminPlayersView) MergeTargets(pl service.PlayerSummary) []service.PlayerSummary {
	var out []service.PlayerSummary
	for _, o := range v.Players {
		if o.ID == pl.ID {
			continue
		}
		disjoint := true
		for _, id := range pl.Identities {
			disjoint = disjoint && !hasPlatform(o, id.Platform)
		}
		if disjoint {
			out = append(out, o)
		}
	}
	return out
}

func hasPlatform(pl service.PlayerSummary, name string) bool {
	for _, id := range pl.Identities {
		if id.Platform == name {
			return true
		}
	}
	return false
}

// addHelp explains the add form: URLs pick their platform, bare IDs go to the legacy one.
func addHelp(ps []platform.Platform) string {
	names := make([]string, 0, len(ps))
	legacy := ""
	for _, p := range ps {
		names = append(names, p.DisplayName)
		if p.Legacy {
			legacy = p.DisplayName
		}
	}
	help := "Paste a profile URL (" + strings.Join(names, ", ") + ") or a player ID."
	if legacy != "" {
		help += " Bare IDs are " + legacy + " IDs unless you pick another platform."
	}
	return help
}

func identityVariant(id service.Identity) badge.Variant {
	switch {
	case id.LastError != "":
		return badge.VariantDestructive
	case !id.Enabled:
		return badge.VariantOutline
	}
	return badge.VariantSecondary
}

func identityURL(playerID, platformName string) string {
	return "/admin/players/" + playerID + "/identities/" + platformName
}

func lastPoll(f model.SyncFeed, now time.Time) string {
	if f.LastPolledAt == nil {
		return "never"
	}
	return TimeAgo(*f.LastPolledAt, now)
}

func backfillLabel(f model.SyncFeed) string {
	switch f.BackfillState {
	case model.BackfillDone:
		return "Complete"
	case model.BackfillRunning:
		return "Page " + strconv.Itoa(f.BackfillPage) + " of " + strconv.Itoa(max(f.BackfillTotalPages, f.BackfillPage))
	}
	return "Waiting"
}

// Menus open inline inside their table cell: the table scrolls horizontally
// (overflow-x-auto), which would clip an absolutely positioned popover.
const (
	menuClass    = "mt-1 flex w-72 flex-col gap-2 rounded-md border bg-popover p-3 text-left text-xs text-popover-foreground shadow-sm"
	summaryClass = "flex cursor-pointer list-none items-center [&::-webkit-details-marker]:hidden"
)

templ AdminPlayersPage(p Page, v AdminPlayersView) {
	@Layout(p) {
		<div class="mb-8 flex flex-col gap-1">
			<h1 class="text-2xl font-semibold tracking-tight">Manage players</h1>
			<p class="text-sm text-muted-foreground">Tracked players are polled regularly and their full replay history is archived.</p>
		</div>
		@card.Card(card.Props{Class: "mb-8"}) {
			@card.Header() {
				@card.Title() {
					Add a player
				}
				@card.Description() {
					{ addHelp(v.Platforms) }
				}
			}
			@card.Content() {
				<form hx-post="/admin/players/lookup" hx-target="#lookup-result" hx-swap="innerHTML" class="flex flex-col gap-2 sm:flex-row">
					@input.Input(input.Props{Name: "ref", Placeholder: "Profile URL or player ID", Required: true, Class: "sm:flex-1", Attributes: templ.Attributes{"aria-label": "Profile URL or player ID"}})
					@nativeselect.NativeSelect(nativeselect.Props{Name: "platform", Attributes: templ.Attributes{"aria-label": "Platform"}}) {
						@nativeselect.Option(nativeselect.OptionProps{Value: "", Selected: true}) {
							Detect from URL
						}
						for _, pf := range v.Platforms {
							@nativeselect.Option(nativeselect.OptionProps{Value: pf.Name}) {
								{ pf.DisplayName }
							}
						}
					}
					@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantSecondary}) {
						@icon.Search()
						Look up
					}
				</form>
				<div id="lookup-result" class="mt-4 empty:hidden"></div>
			}
		}
		@AdminPlayersTable(v)
	}
}

templ PlayerPreview(platformName string, prof platform.Profile, tracked bool) {
	<div class="flex items-center gap-3 rounded-lg border p-3">
		@avatar.Avatar() {
			@avatar.Image(avatar.ImageProps{Src: prof.AvatarURL, Alt: prof.Name})
			@avatar.Fallback() {
				{ Initials(prof.Name) }
			}
		}
		<div class="min-w-0 flex-1">
			<p class="truncate font-medium">{ prof.Name }</p>
			<p class="font-mono text-xs text-muted-foreground">{ PlatformName(ctx, platformName) } · { prof.ExternalID } · { prof.Country }</p>
		</div>
		if tracked {
			@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
				Already tracked
			}
		} else {
			<form hx-post="/admin/players" hx-target="#admin-players" hx-swap="outerHTML">
				<input type="hidden" name="ref" value={ prof.ExternalID }/>
				<input type="hidden" name="platform" value={ platformName }/>
				@button.Button(button.Props{Type: button.TypeSubmit, Size: button.SizeSm}) {
					@icon.Plus()
					Track player
				}
			</form>
		}
	</div>
}

templ AdminPlayersTable(v AdminPlayersView) {
	<div id="admin-players">
		if len(v.Players) == 0 {
			@empty.Empty(empty.Props{Class: "border border-dashed"}) {
				@empty.Header() {
					@empty.Title() {
						No tracked players
					}
					@empty.Description() {
						Look a player up above to start archiving their replays.
					}
				}
			}
		} else {
			<div class="rounded-lg border">
				@table.Table() {
					@table.Header() {
						@table.Row() {
							@table.Head() {
								Player
							}
							@table.Head() {
								Accounts
							}
							@table.Head() {
								Status
							}
							@table.Head(table.HeadProps{Class: "hidden md:table-cell"}) {
								History
							}
							@table.Head(table.HeadProps{Class: "text-right"}) {
								Replays
							}
							@table.Head(table.HeadProps{Class: "hidden md:table-cell"}) {
								Last poll
							}
							@table.Head(table.HeadProps{Class: "text-right"}) {
								<span class="sr-only">Actions</span>
							}
						}
					}
					@table.Body() {
						for _, pl := range v.Players {
							@AdminPlayerRow(pl, v)
						}
					}
				}
			</div>
		}
	</div>
}

templ AdminPlayerRow(pl service.PlayerSummary, v AdminPlayersView) {
	@table.Row(table.RowProps{ID: "player-" + pl.ID}) {
		@table.Cell() {
			<a href={ templ.SafeURL("/p/" + pl.ID) } class="flex items-center gap-2 underline-offset-4 hover:underline">
				@avatar.Avatar(avatar.Props{Class: "size-8"}) {
					@avatar.Image(avatar.ImageProps{Src: pl.AvatarURL, Alt: pl.Name})
					@avatar.Fallback() {
						{ Initials(pl.Name) }
					}
				}
				<span class="font-medium">{ pl.Name }</span>
			</a>
		}
		@table.Cell() {
			<div class="flex flex-wrap items-center gap-1">
				for _, id := range pl.Identities {
					@identityMenu(pl, id)
				}
				if un := v.Unlinked(pl); len(un) > 0 {
					@linkMenu(pl, un)
				}
			</div>
		}
		@table.Cell() {
			if pl.Enabled {
				@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
					Enabled
				}
			} else {
				@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
					Disabled
				}
			}
			if pl.Error() != "" {
				<p class="mt-1 max-w-56 truncate text-xs text-destructive" title={ pl.Error() }>{ pl.Error() }</p>
			}
		}
		@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
			{ backfillLabel(pl.Sync()) }
		}
		@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
			{ Number(pl.Counts.Archived) } / { Number(pl.Counts.Replays()) }
		}
		@table.Cell(table.CellProps{Class: "hidden text-muted-foreground md:table-cell"}) {
			{ lastPoll(pl.Sync(), v.Now) }
		}
		@table.Cell(table.CellProps{Class: "text-right"}) {
			<div class="flex items-center justify-end gap-1">
				@button.Button(button.Props{Variant: button.VariantGhost, Size: button.SizeIconSm, Attributes: templ.Attributes{"hx-post": "/admin/players/" + pl.ID + "/poll", "hx-swap": "none", "aria-label": "Poll now", "title": "Poll now"}}) {
					@icon.RefreshCw()
				}
				<form hx-post={ "/admin/players/" + pl.ID + "/enabled" } hx-target={ "#player-" + pl.ID } hx-swap="outerHTML">
					<input type="hidden" name="enabled" value={ strconv.FormatBool(!pl.Enabled) }/>
					@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantGhost, Size: button.SizeIconSm, Attributes: templ.Attributes{"aria-label": "Toggle tracking", "title": "Pause or resume tracking"}}) {
						if pl.Enabled {
							@icon.Pause()
						} else {
							@icon.Play()
						}
					}
				</form>
				@mergeMenu(pl, v.MergeTargets(pl))
				<form
					hx-post={ "/admin/players/" + pl.ID + "/delete" }
					hx-target={ "#player-" + pl.ID }
					hx-swap="outerHTML"
					hx-confirm={ "Stop tracking " + pl.Name + "? Their scores are removed from the archive." }
					class="flex items-center gap-1"
				>
					<label class="flex items-center gap-1 text-xs text-muted-foreground" title="Also delete replay files from disk">
						<input type="checkbox" name="delete_files" class="size-3.5 accent-primary"/>
						files
					</label>
					@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantGhost, Size: button.SizeIconSm, Class: "text-destructive", Attributes: templ.Attributes{"aria-label": "Delete player", "title": "Delete player"}}) {
						@icon.Trash2()
					}
				</form>
			</div>
		}
	}
}

templ identityMenu(pl service.PlayerSummary, id service.Identity) {
	<details>
		<summary class={ summaryClass } title={ PlatformName(ctx, id.Platform) + " account " + id.ExternalID }>
			@badge.Badge(badge.Props{Variant: identityVariant(id)}) {
				{ PlatformName(ctx, id.Platform) }
				if id.LastError != "" {
					@icon.TriangleAlert(icon.Props{Size: 12})
				}
			}
		</summary>
		<div class={ menuClass }>
			<p class="font-mono break-all text-muted-foreground">{ id.ExternalID }</p>
			if id.LastError != "" {
				<p class="text-destructive">{ id.LastError }</p>
			}
			<form hx-post={ identityURL(pl.ID, id.Platform) + "/enabled" } hx-target={ "#player-" + pl.ID } hx-swap="outerHTML">
				<input type="hidden" name="enabled" value={ strconv.FormatBool(!id.Enabled) }/>
				@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantOutline, Size: button.SizeSm, Class: "w-full"}) {
					if id.Enabled {
						Pause this account
					} else {
						Resume this account
					}
				}
			</form>
			if len(pl.Identities) > 1 {
				<form
					hx-post={ identityURL(pl.ID, id.Platform) + "/delete" }
					hx-target={ "#player-" + pl.ID }
					hx-swap="outerHTML"
					hx-confirm={ "Unlink this " + PlatformName(ctx, id.Platform) + " account from " + pl.Name + "? Its scores are removed from the archive." }
					class="flex items-center justify-between gap-2"
				>
					<label class="flex items-center gap-1 text-muted-foreground" title="Also delete this account's replay files">
						<input type="checkbox" name="delete_files" class="size-3.5 accent-primary"/>
						files
					</label>
					@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantGhost, Size: button.SizeSm, Class: "text-destructive"}) {
						@icon.Unlink()
						Unlink
					}
				</form>
			}
		</div>
	</details>
}

templ linkMenu(pl service.PlayerSummary, plats []platform.Platform) {
	<details>
		<summary class={ summaryClass } title="Link another platform account">
			@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
				@icon.Plus(icon.Props{Size: 12})
				Link
			}
		</summary>
		<form hx-post={ "/admin/players/" + pl.ID + "/identities" } hx-target={ "#player-" + pl.ID } hx-swap="outerHTML" class={ menuClass }>
			@nativeselect.NativeSelect(nativeselect.Props{Name: "platform", Attributes: templ.Attributes{"aria-label": "Platform"}}) {
				for _, pf := range plats {
					@nativeselect.Option(nativeselect.OptionProps{Value: pf.Name}) {
						{ pf.DisplayName }
					}
				}
			}
			@input.Input(input.Props{Name: "ref", Placeholder: "Profile URL or player ID", Required: true, Attributes: templ.Attributes{"aria-label": "Account to link"}})
			@button.Button(button.Props{Type: button.TypeSubmit, Size: button.SizeSm}) {
				@icon.Link()
				Link account
			}
		</form>
	</details>
}

templ mergeMenu(pl service.PlayerSummary, targets []service.PlayerSummary) {
	<details>
		<summary class={ summaryClass + " size-8 justify-center rounded-md hover:bg-accent" } title="Merge into another player" aria-label="Merge into another player">
			@icon.Merge()
		</summary>
		<div class={ menuClass }>
			if len(targets) == 0 {
				<p class="text-muted-foreground">No player to merge into: merging needs two players with no platform in common.</p>
			} else {
				<form
					hx-post={ "/admin/players/" + pl.ID + "/merge" }
					hx-target="#admin-players"
					hx-swap="outerHTML"
					hx-confirm={ "Merge " + pl.Name + " into the selected player? Every account, score and replay moves, and " + pl.Name + "'s page will redirect there." }
					class="flex flex-col gap-2"
				>
					<p>Merge { pl.Name } into:</p>
					@nativeselect.NativeSelect(nativeselect.Props{Name: "into", Attributes: templ.Attributes{"aria-label": "Merge into"}}) {
						for _, t := range targets {
							@nativeselect.Option(nativeselect.OptionProps{Value: t.ID}) {
								{ t.Name }
							}
						}
					}
					@button.Button(button.Props{Type: button.TypeSubmit, Size: button.SizeSm}) {
						Merge
					}
				</form>
			}
		</div>
	</details>
}

templ PlayerAdded(v AdminPlayersView, name string) {
	@AdminPlayersTable(v)
	<div id="lookup-result" hx-swap-oob="innerHTML"></div>
	@ToastOOB(toast.TypeSuccess, "Now tracking "+name, "Their history is being archived in the background.")
}

templ PlayerRowToast(pl service.PlayerSummary, v AdminPlayersView, title string) {
	@AdminPlayerRow(pl, v)
	@ToastOOB(toast.TypeSuccess, title, pl.Name)
}

templ PlayersMerged(v AdminPlayersView, title string) {
	@AdminPlayersTable(v)
	@ToastOOB(toast.TypeSuccess, title, "")
}
```

- [ ] **Step 5: Admin handlers and routes**

`internal/web/admin.go`:

```go
func (h *Handler) adminPlayersView(r *http.Request) (views.AdminPlayersView, error) {
	players, err := h.svc.ListPlayers(r.Context(), true)
	return views.AdminPlayersView{Players: players, Platforms: h.svc.Platforms().All(), Now: h.svc.Now()}, err
}

// renderRow re-renders one player's row with a success toast.
func (h *Handler) renderRow(w http.ResponseWriter, r *http.Request, id, title string) {
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
	render(w, r, http.StatusOK, views.PlayerRowToast(pl, v, title))
}
```

In `setPlayerEnabled`, replace everything after the `SetPlayerEnabled` error handling with:

```go
	title := "Tracking resumed"
	if !enabled {
		title = "Tracking paused"
	}
	h.renderRow(w, r, id, title)
```

Add the account and merge handlers:

```go
func (h *Handler) linkIdentity(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	_, err := h.svc.LinkIdentity(r.Context(), id, r.PostFormValue("ref"), r.PostFormValue("platform"))
	switch {
	case errors.Is(err, service.ErrIdentityLinkedElsewhere):
		h.toastOnly(w, r, toast.TypeWarning, "Already tracked", sentence(err))
		return
	case errors.Is(err, service.ErrPlatformAlreadyLinked), errors.Is(err, service.ErrInvalidPlayerRef), isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Could not link account", sentence(err))
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.renderRow(w, r, id, "Account linked")
}

func (h *Handler) setIdentityEnabled(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	enabled := r.PostFormValue("enabled") == "true"
	if err := h.svc.SetIdentityEnabled(r.Context(), id, r.PathValue("platform"), enabled); err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Account not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	title := "Account resumed"
	if !enabled {
		title = "Account paused"
	}
	h.renderRow(w, r, id, title)
}

func (h *Handler) unlinkIdentity(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	err := h.svc.UnlinkIdentity(r.Context(), id, r.PathValue("platform"), r.PostFormValue("delete_files") == "on")
	switch {
	case errors.Is(err, service.ErrLastIdentity):
		h.toastOnly(w, r, toast.TypeError, "Could not unlink account", sentence(err))
		return
	case isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Account not found", "")
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.renderRow(w, r, id, "Account unlinked")
}

func (h *Handler) mergePlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	ctx := r.Context()
	src, serr := h.svc.GetPlayer(ctx, r.PathValue("id"))
	dst, derr := h.svc.GetPlayer(ctx, r.PostFormValue("into"))
	if serr != nil || derr != nil {
		h.toastOnly(w, r, toast.TypeError, "Could not merge", "Player not found.")
		return
	}
	err := h.svc.MergePlayers(ctx, src.ID, dst.ID)
	switch {
	case errors.Is(err, service.ErrMergeConflict), errors.Is(err, service.ErrMergeSelf), isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Could not merge", sentence(err))
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.PlayersMerged(v, "Merged "+src.Name+" into "+dst.Name))
}
```

`internal/web/web.go` — after `POST /admin/players/{id}/delete`:

```go
	mux.HandleFunc("POST /admin/players/{id}/identities", h.requireAdmin(h.linkIdentity))
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/enabled", h.requireAdmin(h.setIdentityEnabled))
	mux.HandleFunc("POST /admin/players/{id}/identities/{platform}/delete", h.requireAdmin(h.unlinkIdentity))
	mux.HandleFunc("POST /admin/players/{id}/merge", h.requireAdmin(h.mergePlayer))
```

- [ ] **Step 6: Sync page per feed and event filters**

`internal/web/views/sync.templ` — replace `QueueRow`, `EventsView` and `EventsURL`, add the helpers (imports: add `"context"`, `"net/url"`, `"github.com/yyewolf/ssarchiver/internal/platform"`):

```go
type QueueRow struct {
	Player   service.PlayerSummary
	Identity service.Identity
	Feed     model.SyncFeed
	Counts   service.Counts // this feed's rows
	NextPoll time.Time
	ETA      time.Duration
}

// active reports whether the worker runs this feed.
func (q QueueRow) active() bool { return q.Player.Enabled && q.Identity.Enabled && q.Feed.Enabled }

// feedLabel names a queue row: "BeatLeader", or "BeatLeader · attempts" for other feeds.
func feedLabel(ctx context.Context, q QueueRow) string {
	name := PlatformName(ctx, q.Identity.Platform)
	if q.Feed.Feed != model.KindScore {
		name += " · " + q.Feed.Feed + "s"
	}
	return name
}

type EventsView struct {
	Events    []*model.SyncEvent
	Total     int64
	Filter    service.EventFilter
	Players   []service.PlayerSummary
	Platforms []platform.Platform
	Now       time.Time
}

// eventAccount names the platform account an event is about ("—" for global events).
func eventAccount(ctx context.Context, e *model.SyncEvent) string {
	if e.Platform == nil {
		return "—"
	}
	s := PlatformName(ctx, *e.Platform)
	if e.Feed != nil && *e.Feed != model.KindScore {
		s += " · " + *e.Feed + "s"
	}
	return s
}

func EventsURL(f service.EventFilter, page int) string {
	q := url.Values{"level": {f.Level}, "kind": {f.Kind}, "player": {f.PlayerID}, "platform": {f.Platform}, "feed": {f.Feed}}
	q.Set("page", strconv.Itoa(page))
	return "/admin/sync/events?" + q.Encode()
}
```

Replace the `queueCard` table (header and rows):

```templ
templ queueCard(v SyncView) {
	@card.Card() {
		@card.Header() {
			@card.Title() {
				Queue
			}
			@card.Description() {
				Per-account progress. Estimates assume the current budget and poll interval.
			}
		}
		@card.Content() {
			if len(v.Queues) == 0 {
				<p class="text-sm text-muted-foreground">No tracked players.</p>
			} else {
				@table.Table() {
					@table.Header() {
						@table.Row() {
							@table.Head() {
								Player
							}
							@table.Head() {
								Account
							}
							@table.Head() {
								Next poll
							}
							@table.Head(table.HeadProps{Class: "hidden md:table-cell"}) {
								History
							}
							@table.Head(table.HeadProps{Class: "text-right"}) {
								Archived
							}
							@table.Head(table.HeadProps{Class: "text-right"}) {
								Pending
							}
							@table.Head(table.HeadProps{Class: "hidden text-right sm:table-cell"}) {
								Failed
							}
							@table.Head(table.HeadProps{Class: "hidden text-right sm:table-cell"}) {
								Pruned
							}
							@table.Head(table.HeadProps{Class: "hidden lg:table-cell"}) {
								ETA
							}
							@table.Head(table.HeadProps{Class: "text-right"}) {
								<span class="sr-only">Actions</span>
							}
						}
					}
					@table.Body() {
						for _, q := range v.Queues {
							@table.Row() {
								@table.Cell(table.CellProps{Class: "font-medium"}) {
									{ q.Player.Name }
									if !q.Player.Enabled {
										<span class="ml-1 text-xs text-muted-foreground">(disabled)</span>
									}
								}
								@table.Cell() {
									{ feedLabel(ctx, q) }
									if q.Player.Enabled && !q.Identity.Enabled {
										<span class="ml-1 text-xs text-muted-foreground">(paused)</span>
									}
								}
								@table.Cell(table.CellProps{Class: "text-muted-foreground"}) {
									if q.active() {
										{ Until(q.NextPoll, v.Now) }
									} else {
										—
									}
								}
								@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
									{ backfillLabel(q.Feed) }
								}
								@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
									{ Number(q.Counts.Archived) }
								}
								@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
									{ Number(q.Counts.Pending) }
								}
								@table.Cell(table.CellProps{Class: "hidden text-right tabular-nums sm:table-cell"}) {
									{ Number(q.Counts.Failed) }
								}
								@table.Cell(table.CellProps{Class: "hidden text-right tabular-nums sm:table-cell"}) {
									{ Number(q.Counts.Gone) }
								}
								@table.Cell(table.CellProps{Class: "hidden text-muted-foreground lg:table-cell"}) {
									if q.ETA > 0 {
										{ HumanDuration(q.ETA) }
									} else {
										—
									}
								}
								@table.Cell(table.CellProps{Class: "text-right"}) {
									@button.Button(button.Props{Variant: button.VariantGhost, Size: button.SizeIconSm, Attributes: templ.Attributes{"hx-post": "/admin/players/" + q.Player.ID + "/poll", "hx-swap": "none", "aria-label": "Poll now", "title": "Poll now"}}) {
										@icon.RefreshCw()
									}
								}
							}
						}
					}
				}
			}
		}
	}
}
```

Title the budget cards per limiter. Add to the Go section of `sync.templ` (import `"strings"`):

```go
// limiterTitle names a budget card: the platform, plus the host class when a
// platform has several limiters ("beatleader/api" → "BeatLeader API").
func limiterTitle(l archiver.LimiterStatus) string {
	if _, sub, ok := strings.Cut(l.Name, "/"); ok {
		return l.Platform + " " + strings.ToUpper(sub)
	}
	return l.Platform
}
```

and in `budgetCard` use it for the title and description: `{ limiterTitle(l) } budget` and `Requests sent by this instance in each { limiterTitle(l) } rate-limit window.` (the "reports N remaining" line keeps `l.Platform`). `TestSyncPage` still sees "ScoreSaber budget": its limiter is named `scoresaber`.

In `eventFilters`, after the kind select, add:

```templ
		@nativeselect.NativeSelect(nativeselect.Props{Name: "platform", Attributes: templ.Attributes{"aria-label": "Platform"}}) {
			@nativeselect.Option(nativeselect.OptionProps{Value: "", Selected: ev.Filter.Platform == ""}) {
				All platforms
			}
			for _, pf := range ev.Platforms {
				@nativeselect.Option(nativeselect.OptionProps{Value: pf.Name, Selected: ev.Filter.Platform == pf.Name}) {
					{ pf.DisplayName }
				}
			}
		}
		@nativeselect.NativeSelect(nativeselect.Props{Name: "feed", Attributes: templ.Attributes{"aria-label": "Feed"}}) {
			@nativeselect.Option(nativeselect.OptionProps{Value: "", Selected: ev.Filter.Feed == ""}) {
				All feeds
			}
			for _, o := range []string{model.KindScore, model.KindAttempt} {
				@nativeselect.Option(nativeselect.OptionProps{Value: o, Selected: ev.Filter.Feed == o}) {
					{ o + "s" }
				}
			}
		}
```

In `EventsTable`, add an `Account` head after `Kind` (`table.HeadProps{Class: "hidden md:table-cell"}`) and the matching cell `@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) { { eventAccount(ctx, e) } }`.

`internal/web/admin_sync.go` — `syncView` builds one row per feed:

```go
func (h *Handler) syncView(ctx context.Context) (views.SyncView, error) {
	st, err := h.svc.Settings(ctx)
	if err != nil {
		return views.SyncView{}, err
	}
	players, err := h.svc.ListPlayers(ctx, true)
	if err != nil {
		return views.SyncView{}, err
	}
	now := h.svc.Now()
	var enabled, withPending int
	var pending, failed int64
	for _, p := range players {
		if p.Enabled {
			enabled++
		}
		for _, id := range p.Identities {
			for _, c := range id.Counts {
				pending += c.Pending
				failed += c.Failed
			}
			for _, f := range id.Feeds {
				if p.Enabled && id.Enabled && f.Enabled && id.Counts[f.Feed].Pending > 0 {
					withPending++
				}
			}
		}
	}
	rate := service.ReplayRatePerHour(h.cfg.HourlyBudget, enabled, st.PollInterval)
	var rows []views.QueueRow
	for _, p := range players {
		for _, id := range p.Identities {
			for _, f := range id.Feeds {
				q := views.QueueRow{Player: p, Identity: id, Feed: f, Counts: id.Counts[f.Feed], NextPoll: now}
				if f.LastPolledAt != nil {
					q.NextPoll = f.LastPolledAt.Add(st.PollInterval)
				}
				if p.Enabled && id.Enabled && f.Enabled && withPending > 0 {
					q.ETA = service.ETA(q.Counts.Pending, rate/float64(withPending))
				}
				rows = append(rows, q)
			}
		}
	}
	return views.SyncView{
		Status: h.status.Status(), Paused: st.WorkerPaused, Queues: rows,
		PendingTotal: pending, FailedTotal: failed, RatePerHour: rate, ETA: service.ETA(pending, rate), Now: now,
	}, nil
}
```

`eventsView` reads the two new filters and passes the registry:

```go
	f := service.EventFilter{
		Level: q.Get("level"), Kind: q.Get("kind"), PlayerID: q.Get("player"),
		Platform: q.Get("platform"), Feed: q.Get("feed"), Page: max(page, 1), PerPage: 50,
	}
	…
	return views.EventsView{Events: events, Total: total, Filter: f, Players: players, Platforms: h.svc.Platforms().All(), Now: h.svc.Now()}, err
```

- [ ] **Step 7: Run the tests and look at the pages**

Run: `make generate && go test -race ./internal/service/ ./internal/web/...`
Expected: PASS (including `TestAddPlayer`, `TestPlayerRowActions`, `TestSyncPage`, `TestSyncFailedPager`).

Then run `make dev`, add two players on **Manage**, and check by eye: each account badge opens its menu inside the cell and closes again; the **Link** form links an account; the merge icon lists the other player and its confirmation reads correctly. Fix layout problems before committing.

- [ ] **Step 8: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/service/events.go internal/service/events_test.go internal/web
git commit -m "feat(web): manage platform accounts, merge players, per-feed sync queue and event filters"
```

---
## Task 10: JSON API — accounts, merge, filters, per-platform scores

The admin API of spec §6.2 for everything Tasks 4–6 built, plus the score DTO fields and filters. Conflicts carry a machine-readable `code` (`identity_linked_elsewhere` with the other `player_id`, `platform_already_linked`, `last_identity`, `merge_platform_conflict`). Player path IDs follow merge aliases. `{platform}` values are checked against the registry at request time (huma tags are static, so the OpenAPI document lists the names in the operation descriptions instead of an enum — spec §6.2).

**Files:**
- Modify: `internal/api/api.go` (`Problem`, `problem`, `mapErr`, `playerID`, `platformName`, `platformList`, register identities)
- Modify: `internal/api/dto.go` (`Identity.Counts`, `Score`/`Leaderboard` fields, `scoreDTO(base, reg, s)`, `replayCounts`)
- Modify: `internal/api/players.go` (alias-aware handlers)
- Modify: `internal/api/scores.go` (filters, legacy-only `get-score`, `get-play`, `get-attempt`)
- Create: `internal/api/identities.go`
- Test: `internal/api/api_test.go`, `internal/api/identities_test.go` (new)

**Interfaces:**
- Consumes: Task 4 `LinkIdentity`, `UnlinkIdentity`, `SetIdentityEnabled`, `LinkedElsewhereError`, errors, `Identity.Scores()`; Task 5 `MergePlayers`, `ResolvePlayerID`, merge errors; Task 6 `ScoreFilter.Platform/MinScore/MaxScore`, `GetPlay`; Task 7 `Registry.PlayPath/ReplayPath`.
- Produces (all under `/api/v1`):
  - `POST /players/{id}/identities` (`link-identity`, body `{platform, ref}`) → player; `PATCH /players/{id}/identities/{platform}` (`update-identity`, body `{enabled}`) → player; `DELETE /players/{id}/identities/{platform}?delete_files=` (`unlink-identity`) → 204; `POST /players/{source}/merge` (`merge-player`, body `{into}`) → the survivor
  - `GET /scores/{platform}/{externalID}` (`get-play`), `GET /scores/{platform}/attempt/{externalID}` (`get-attempt`); `GET /scores/{id}` resolves ScoreSaber scores only
  - `GET /players/{id}/scores` gains `platform` (`all` or a name), `min_score`, `max_score` (digit strings)
  - DTO: `Identity.counts`; `Score.platform`, `kind`, `end_type`, `end_time`, `external_id`; `Leaderboard.external_id`; `Score.id`/`Leaderboard.id` are `0` for non-legacy rows
  - `type api.Problem struct{ huma.ErrorModel; Code, PlayerID string }`

- [ ] **Step 1: Write the failing tests**

In `internal/api/api_test.go`, split the handler construction so tests can pick the platforms:

```go
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
```

Extend `TestAdminRequiresSession`'s table with:

```go
		{http.MethodPost, "/api/v1/players/1001/identities"},
		{http.MethodPatch, "/api/v1/players/1001/identities/scoresaber"},
		{http.MethodDelete, "/api/v1/players/1001/identities/scoresaber"},
		{http.MethodPost, "/api/v1/players/1001/merge"},
```

and `TestOpenAPIDocument`'s list with `"link-identity"`, `"merge-player"`, `"get-play"`, `"min_score"`.

In `TestPublicReads`, after the `replay` checks, pin the new score fields:

```go
	s1 := items[0].(map[string]any)
	if s1["id"].(float64) != 1 || s1["platform"] != "scoresaber" || s1["kind"] != "score" || s1["end_type"] != "clear" ||
		s1["external_id"] != "1" || s1["leaderboard"].(map[string]any)["external_id"] != "501" {
		t.Fatalf("score fields = %v", s1)
	}
	if c := ids[0].(map[string]any)["counts"].(map[string]any); c["archived"].(float64) != 1 || c["scores"].(float64) != 2 {
		t.Fatalf("account counts = %v", c)
	}
```

`internal/api/identities_test.go`:

```go
package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestIdentityEndpoints(t *testing.T) {
	svc, h := newMultiAPI(t, true)
	alice := seed(t, svc)
	base := "/api/v1/players/" + alice + "/identities"

	code, p, _ := call(t, h, http.MethodPost, base, map[string]any{"platform": "testplat", "ref": "https://tp.example/u/abc"})
	ids, _ := p["identities"].([]any)
	if code != 200 || len(ids) != 2 || ids[1].(map[string]any)["platform"] != "testplat" || ids[1].(map[string]any)["id"] != "abc" {
		t.Fatalf("link = %d %v", code, p)
	}
	if code, body, _ := call(t, h, http.MethodPost, base, map[string]any{"platform": "testplat", "ref": "def"}); code != 409 || body["code"] != "platform_already_linked" {
		t.Fatalf("second account on a platform = %d %v", code, body)
	}
	bob := testutil.AddPlayer(t, svc, "1002")
	code, body, _ := call(t, h, http.MethodPost, "/api/v1/players/"+bob+"/identities", map[string]any{"platform": "testplat", "ref": "abc"})
	if code != 409 || body["code"] != "identity_linked_elsewhere" || body["player_id"] != alice {
		t.Fatalf("linked elsewhere = %d %v", code, body)
	}
	if code, _, _ := call(t, h, http.MethodPost, base, map[string]any{"platform": "nope", "ref": "abc"}); code != 422 {
		t.Fatalf("unknown platform = %d", code)
	}

	code, p, _ = call(t, h, http.MethodPatch, base+"/testplat", map[string]any{"enabled": false})
	if code != 200 || p["identities"].([]any)[1].(map[string]any)["enabled"] != false {
		t.Fatalf("pause = %d %v", code, p)
	}
	if code, _, _ := call(t, h, http.MethodPatch, base+"/nope", map[string]any{"enabled": true}); code != 422 {
		t.Fatalf("unknown platform = %d", code)
	}
	if code, _, _ := call(t, h, http.MethodDelete, base+"/testplat?delete_files=true", nil); code != 204 {
		t.Fatalf("unlink = %d", code)
	}
	if code, body, _ := call(t, h, http.MethodDelete, base+"/scoresaber", nil); code != 409 || body["code"] != "last_identity" {
		t.Fatalf("last account = %d %v", code, body)
	}
	if code, body, _ := call(t, h, http.MethodPost, "/api/v1/players", map[string]any{"ref": "1001"}); code != 409 || body["code"] != "identity_linked_elsewhere" {
		t.Fatalf("adding a tracked account = %d %v", code, body)
	}
}

func TestMergeEndpoint(t *testing.T) {
	svc, h := newMultiAPI(t, true)
	alice := seed(t, svc)
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	code, p, _ := call(t, h, http.MethodPost, "/api/v1/players/"+tess+"/merge", map[string]any{"into": alice})
	if code != 200 || p["id"] != alice || len(p["identities"].([]any)) != 2 {
		t.Fatalf("merge = %d %v", code, p)
	}
	if code, p, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess, nil); code != 200 || p["id"] != alice {
		t.Fatalf("a merged-away ID resolves to the survivor: %d %v", code, p)
	}
	if code, _, _ := call(t, h, http.MethodGet, "/api/v1/players/"+tess+"/scores", nil); code != 200 {
		t.Fatalf("alias scores = %d", code)
	}
	bob := testutil.AddPlayer(t, svc, "1002")
	if code, body, _ := call(t, h, http.MethodPost, "/api/v1/players/"+bob+"/merge", map[string]any{"into": alice}); code != 409 || body["code"] != "merge_platform_conflict" {
		t.Fatalf("conflict = %d %v", code, body)
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players/"+alice+"/merge", map[string]any{"into": alice}); code != 422 {
		t.Fatalf("self merge = %d", code)
	}
}

func TestPlatformScores(t *testing.T) {
	svc, h := newMultiAPI(t, false)
	alice := seed(t, svc)
	ctx := context.Background()
	if _, err := svc.LinkIdentity(ctx, alice, "abc", "testplat"); err != nil {
		t.Fatal(err)
	}
	testutil.UpsertFake(t, svc, alice,
		testutil.FakePlay(model.KindScore, "t1", "lb-a", testutil.T0.Add(5*time.Minute), true),
		testutil.FakePlay(model.KindScore, "t2", "lb-b", testutil.T0.Add(4*time.Minute), false))
	testutil.Archive(t, svc, testutil.Row(t, svc, alice, "t1"), "tp")

	code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/"+alice+"/scores?platform=testplat", nil)
	items, _ := page["items"].([]any)
	if code != 200 || page["total"].(float64) != 2 {
		t.Fatalf("platform filter = %d %v", code, page)
	}
	t1 := items[0].(map[string]any)
	r := t1["replay"].(map[string]any)
	if t1["id"].(float64) != 0 || t1["platform"] != "testplat" || t1["external_id"] != "t1" ||
		t1["url"] != "https://replays.example.com/s/tp/t1" || r["download_url"] != "https://replays.example.com/r/tp/t1.tpr" ||
		r["embed_url"] != "https://replays.example.com/embed/tp/t1" ||
		t1["leaderboard"].(map[string]any)["id"].(float64) != 0 || t1["leaderboard"].(map[string]any)["external_id"] != "lb-a" {
		t.Fatalf("non-legacy score = %v", t1)
	}
	for q, want := range map[string]float64{"platform=all": 4, "min_score=960000": 2, "max_score=900": 2, "min_score=901&max_score=999999": 0} {
		code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/"+alice+"/scores?"+q, nil)
		if code != 200 || page["total"].(float64) != want {
			t.Errorf("%s = %d total %v, want %v", q, code, page["total"], want)
		}
	}
	for q, want := range map[string]int{"max_score=abc": 422, "min_score=-1": 422, "platform=nope": 422} {
		if code, _, _ := call(t, h, http.MethodGet, "/api/v1/players/"+alice+"/scores?"+q, nil); code != want {
			t.Errorf("%s = %d, want %d", q, code, want)
		}
	}

	if code, s, _ := call(t, h, http.MethodGet, "/api/v1/scores/testplat/t1", nil); code != 200 || s["external_id"] != "t1" {
		t.Fatalf("get-play = %d %v", code, s)
	}
	internal := testutil.Row(t, svc, alice, "t1").ID
	for _, p := range []string{"/api/v1/scores/testplat/attempt/t1", "/api/v1/scores/nope/t1", "/api/v1/scores/" + strconv.FormatInt(internal, 10)} {
		if code, _, _ := call(t, h, http.MethodGet, p, nil); code != 404 {
			t.Errorf("%s = %d, want 404", p, code)
		}
	}
}
```

(imports of `identities_test.go`: also `"strconv"` and `"time"`.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/api/`
Expected: FAIL — 404/405 for the new routes, no `code` in bodies, missing score fields.

- [ ] **Step 3: Problems, alias resolution, platform checks**

`internal/api/api.go` (imports: add `"context"`, `"strconv"`, `"strings"`, `"github.com/yyewolf/ssarchiver/internal/platform"`):

```go
// Problem is an RFC 9457 problem with a machine-readable code (spec §6.2).
type Problem struct {
	huma.ErrorModel
	Code     string `json:"code,omitempty" doc:"Machine-readable reason"`
	PlayerID string `json:"player_id,omitempty" doc:"The other player (identity_linked_elsewhere)"`
}

func problem(status int, code, msg string) *Problem {
	return &Problem{ErrorModel: huma.ErrorModel{Status: status, Title: http.StatusText(status), Detail: msg}, Code: code}
}

func mapErr(err error) error {
	var le *service.LinkedElsewhereError
	switch {
	case errors.As(err, &le):
		p := problem(http.StatusConflict, "identity_linked_elsewhere", err.Error())
		p.PlayerID = le.PlayerID
		return p
	case errors.Is(err, service.ErrPlatformAlreadyLinked):
		return problem(http.StatusConflict, "platform_already_linked", err.Error())
	case errors.Is(err, service.ErrLastIdentity):
		return problem(http.StatusConflict, "last_identity", err.Error())
	case errors.Is(err, service.ErrMergeConflict):
		return problem(http.StatusConflict, "merge_platform_conflict", err.Error())
	case errors.Is(err, service.ErrMergeSelf), errors.Is(err, service.ErrInvalidPlayerRef):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, service.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, service.ErrPlayerExists):
		return huma.Error409Conflict(err.Error())
	}
	slog.Error("api request failed", "err", err)
	return huma.Error500InternalServerError("internal error")
}

// playerID resolves a path ID, following merge aliases (spec §6.2).
func (a *API) playerID(ctx context.Context, id string) (string, error) {
	cur, _, err := a.svc.ResolvePlayerID(ctx, id)
	if err != nil {
		return "", mapErr(err)
	}
	return cur, nil
}

// platformName checks a platform name against the registry.
func (a *API) platformName(name string) (platform.Platform, error) {
	p, ok := a.svc.Platforms().Get(name)
	if !ok {
		return platform.Platform{}, huma.Error422UnprocessableEntity("unknown platform " + strconv.Quote(name) + "; registered: " + a.platformList())
	}
	return p, nil
}

// platformList names the registered platforms, for errors and descriptions.
func (a *API) platformList() string {
	var names []string
	for _, p := range a.svc.Platforms().All() {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}
```

In `Register`, add `a.registerIdentities()` after `a.registerPlayers()`, and change `cfg.Info.Description` to `"Archived Beat Saber replays (ScoreSaber, BeatLeader). Read endpoints are public; write endpoints need the admin session cookie."`.

- [ ] **Step 4: DTOs**

`internal/api/dto.go`:

```go
type ReplayCounts struct {
	Scores   int64 `json:"scores" doc:"Stored scores"`
	Archived int64 `json:"archived"`
	Pending  int64 `json:"pending"`
	Failed   int64 `json:"failed"`
	Gone     int64 `json:"gone" doc:"Pruned by the platform before they could be archived"`
}

func replayCounts(c service.Counts) ReplayCounts {
	return ReplayCounts{Scores: c.Scores, Archived: c.Archived, Pending: c.Pending, Failed: c.Failed, Gone: c.Gone}
}
```

`Identity` gains `Counts ReplayCounts `json:"counts" doc:"This account's scores"``; `Leaderboard` gains `ExternalID string `json:"external_id" doc:"The platform's leaderboard ID"`` and its `ID` gets `doc:"ScoreSaber leaderboard ID; 0 on other platforms"`. `Score`:

```go
type Score struct {
	ID          int64        `json:"id" doc:"ScoreSaber score ID; 0 on other platforms (use platform + external_id)"`
	Platform    string       `json:"platform" example:"beatleader"`
	Kind        string       `json:"kind" enum:"score,attempt"`
	EndType     string       `json:"end_type" enum:"clear,fail,restart,quit,practice,unknown"`
	EndTime     *float64     `json:"end_time,omitempty" doc:"Seconds into the song when an attempt ended"`
	ExternalID  string       `json:"external_id" doc:"The platform's score or attempt ID"`
	PlayerID    string       `json:"player_id"`
	Leaderboard *Leaderboard `json:"leaderboard,omitempty"`
	Rank        int          `json:"rank"`
	Score       int64        `json:"score"`
	Accuracy    float64      `json:"accuracy" doc:"0..1"`
	PP          float64      `json:"pp"`
	Mods        []string     `json:"mods"`
	FullCombo   bool         `json:"full_combo"`
	MissedNotes int          `json:"missed_notes"`
	BadCuts     int          `json:"bad_cuts"`
	MaxCombo    int          `json:"max_combo"`
	HMD         string       `json:"hmd"`
	SetAt       time.Time    `json:"set_at"`
	Replay      Replay       `json:"replay"`
	URL         string       `json:"url"`
}
```

In `playerDTO`, use `Replays: replayCounts(p.Counts)` and set `Counts: replayCounts(id.Scores())` on each identity. Replace `scoreDTO`:

```go
func scoreDTO(base string, reg *platform.Registry, s *model.Score) Score {
	p, _ := reg.Get(s.Platform)
	ref := platform.PlayRef{Platform: s.Platform, Kind: s.Kind, ExternalID: s.ExternalID}
	mods := []string{}
	if s.Mods != "" {
		mods = strings.Split(s.Mods, ",")
	}
	out := Score{
		Platform: s.Platform, Kind: s.Kind, EndType: s.EndType, EndTime: s.EndTime, ExternalID: s.ExternalID,
		PlayerID: s.PlayerID, Rank: s.Rank, Score: s.ModifiedScore, Accuracy: s.Accuracy, PP: s.PP,
		Mods: mods, FullCombo: s.FullCombo, MissedNotes: s.MissedNotes, BadCuts: s.BadCuts, MaxCombo: s.MaxCombo,
		HMD: s.HMD, SetAt: s.SetAt, Replay: Replay{State: s.ReplayState}, URL: base + reg.PlayPath("/s", ref),
	}
	if p.Legacy {
		out.ID = s.ID // internal IDs of other platforms are never exposed
	}
	if lb := s.Leaderboard; lb != nil {
		out.Leaderboard = &Leaderboard{
			ExternalID: lb.ExternalID, SongHash: lb.SongHash, SongName: lb.SongName, SongSubName: lb.SongSubName,
			SongAuthor: lb.SongAuthor, Mapper: lb.Mapper, Difficulty: difficultyName(lb.Difficulty), DifficultyRaw: lb.DifficultyRaw,
			GameMode: lb.GameMode, CoverURL: lb.CoverURL, Status: lb.Status, Stars: lb.Stars,
		}
		if p.Legacy {
			out.Leaderboard.ID = lb.ID
		}
	}
	if s.ReplayState == model.ReplayArchived {
		out.Replay.Size, out.Replay.SHA256, out.Replay.ArchivedAt = s.ReplaySize, s.ReplaySHA256, s.ArchivedAt
		out.Replay.DownloadURL = base + reg.ReplayPath(ref)
		out.Replay.EmbedURL = base + reg.PlayPath("/embed", ref)
	}
	return out
}
```

(`strconv` is no longer used in `dto.go`.)

- [ ] **Step 5: Alias-aware player handlers**

`internal/api/players.go` — every handler taking `{id}` resolves it first. `summary` stays as is; the handlers become, for example:

```go
	}, func(ctx context.Context, in *PlayerPath) (*PlayerOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		p, err := a.summary(ctx, id)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: p}, nil
	})
```

Apply the same `id, err := a.playerID(ctx, in.ID)` prologue to `update-player`, `delete-player` and `poll-player`, and use `id` instead of `in.ID` below it. Update the `AddPlayerInput.Ref` doc to `"Profile URL (the platform is detected) or player ID"` and `Player.ID`'s example to `"k7m2q9x4c1ab"`.

- [ ] **Step 6: Score endpoints**

`internal/api/scores.go`:

```go
type ListScoresInput struct {
	PlayerPath
	Page     int    `query:"page" minimum:"1" default:"1"`
	PerPage  int    `query:"per_page" minimum:"1" maximum:"100" default:"50"`
	Search   string `query:"search" maxLength:"64" doc:"Matches song name, artist or mapper"`
	State    string `query:"state" enum:"replay,archived" doc:"replay: the platform offered a replay; archived: stored here"`
	Ranked   bool   `query:"ranked" doc:"Only ranked maps"`
	Platform string `query:"platform" maxLength:"32" doc:"all (default) or one platform name"`
	MinScore string `query:"min_score" pattern:"^[0-9]{1,12}$" doc:"Inclusive lower bound on the score"`
	MaxScore string `query:"max_score" pattern:"^[0-9]{1,12}$" doc:"Inclusive upper bound on the score"`
}

type PlayPath struct {
	Platform   string `path:"platform" maxLength:"32" example:"beatleader"`
	ExternalID string `path:"externalID" maxLength:"64" example:"12164051"`
}

// bound parses an optional digit-string bound (validated by the pattern).
func bound(s string) *int64 {
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func (a *API) registerScores() {
	huma.Register(a.api, huma.Operation{
		OperationID: "list-player-scores", Method: http.MethodGet, Path: "/api/v1/players/{id}/scores",
		Summary: "List a player's scores, newest first", Tags: []string{"Scores"},
		Description: "Platforms: " + a.platformList() + ".",
	}, func(ctx context.Context, in *ListScoresInput) (*ListScoresOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		f := service.ScoreFilter{
			PlayerID: id, Search: in.Search, RankedOnly: in.Ranked, State: in.State, Page: in.Page, PerPage: in.PerPage,
			MinScore: bound(in.MinScore), MaxScore: bound(in.MaxScore),
		}
		if in.Platform != "" && in.Platform != "all" {
			if _, err := a.platformName(in.Platform); err != nil {
				return nil, err
			}
			f.Platform = in.Platform
		}
		list, err := a.svc.ListScores(ctx, f)
		if err != nil {
			return nil, mapErr(err)
		}
		base, reg := httpx.BaseURLFrom(ctx), a.svc.Platforms()
		out := &ListScoresOutput{Body: ScorePage{Items: make([]Score, 0, len(list.Items)), Total: list.Total, Page: list.Page, PerPage: list.PerPage, Pages: list.Pages}}
		for _, s := range list.Items {
			out.Body.Items = append(out.Body.Items, scoreDTO(base, reg, s))
		}
		return out, nil
	})

	huma.Register(a.api, huma.Operation{
		OperationID: "get-score", Method: http.MethodGet, Path: "/api/v1/scores/{id}",
		Summary: "Get a ScoreSaber score and its replay status", Tags: []string{"Scores"},
	}, func(ctx context.Context, in *ScorePath) (*ScoreOutput, error) {
		lp, ok := a.svc.Platforms().Legacy()
		if !ok {
			return nil, huma.Error404NotFound("no such score")
		}
		return a.play(ctx, lp.Name, model.KindScore, strconv.FormatInt(in.ID, 10))
	})

	for _, c := range []struct{ id, path, summary, kind string }{
		{"get-play", "/api/v1/scores/{platform}/{externalID}", "Get a score by its platform ID", model.KindScore},
		{"get-attempt", "/api/v1/scores/{platform}/attempt/{externalID}", "Get an attempt by its platform ID", model.KindAttempt},
	} {
		huma.Register(a.api, huma.Operation{
			OperationID: c.id, Method: http.MethodGet, Path: c.path, Summary: c.summary, Tags: []string{"Scores"},
			Description: "Platforms: " + a.platformList() + ".",
		}, func(ctx context.Context, in *PlayPath) (*ScoreOutput, error) {
			return a.play(ctx, in.Platform, c.kind, in.ExternalID)
		})
	}
}

func (a *API) play(ctx context.Context, platformName, kind, externalID string) (*ScoreOutput, error) {
	s, err := a.svc.GetPlay(ctx, platformName, kind, externalID)
	if err != nil {
		return nil, mapErr(err)
	}
	return &ScoreOutput{Body: scoreDTO(httpx.BaseURLFrom(ctx), a.svc.Platforms(), s)}, nil
}
```

(imports: add `"strconv"`, `"github.com/yyewolf/ssarchiver/internal/model"`.)

- [ ] **Step 7: Account and merge endpoints**

`internal/api/identities.go`:

```go
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type IdentityPath struct {
	PlayerPath
	Platform string `path:"platform" maxLength:"32" example:"beatleader"`
}

type LinkIdentityInput struct {
	PlayerPath
	Body struct {
		Platform string `json:"platform" minLength:"1" maxLength:"32" example:"beatleader"`
		Ref      string `json:"ref" minLength:"1" maxLength:"200" doc:"Profile URL or player ID on that platform"`
	}
}

type UpdateIdentityInput struct {
	IdentityPath
	Body struct {
		Enabled *bool `json:"enabled,omitempty" doc:"Pause or resume this account"`
	}
}

type UnlinkIdentityInput struct {
	IdentityPath
	DeleteFiles bool `query:"delete_files" doc:"Also delete this account's archived replay files"`
}

type MergeInput struct {
	Source string `path:"source" pattern:"^[a-z0-9-]{1,40}$" doc:"The player merged away"`
	Body   struct {
		Into string `json:"into" pattern:"^[a-z0-9-]{1,40}$" doc:"The player that receives every account, score and replay"`
	}
}

func (a *API) registerIdentities() {
	plats := "Platforms: " + a.platformList() + "."

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "link-identity", Method: http.MethodPost, Path: "/api/v1/players/{id}/identities",
		Summary: "Link a platform account to a player", Tags: []string{"Players"},
		Description: plats + " 409 `identity_linked_elsewhere` (with `player_id`) when the account is tracked as another player — merge them instead; 409 `platform_already_linked` when this player already has an account there.",
	}), func(ctx context.Context, in *LinkIdentityInput) (*PlayerOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if _, err := a.platformName(in.Body.Platform); err != nil {
			return nil, err
		}
		if _, err := a.svc.LinkIdentity(ctx, id, in.Body.Ref, in.Body.Platform); err != nil {
			return nil, mapErr(err)
		}
		return a.playerOutput(ctx, id)
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "update-identity", Method: http.MethodPatch, Path: "/api/v1/players/{id}/identities/{platform}",
		Summary: "Pause or resume one platform account", Tags: []string{"Players"}, Description: plats,
	}), func(ctx context.Context, in *UpdateIdentityInput) (*PlayerOutput, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if _, err := a.platformName(in.Platform); err != nil {
			return nil, err
		}
		if in.Body.Enabled != nil {
			if err := a.svc.SetIdentityEnabled(ctx, id, in.Platform, *in.Body.Enabled); err != nil {
				return nil, mapErr(err)
			}
		}
		return a.playerOutput(ctx, id)
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "unlink-identity", Method: http.MethodDelete, Path: "/api/v1/players/{id}/identities/{platform}",
		DefaultStatus: http.StatusNoContent, Summary: "Unlink a platform account and remove its scores", Tags: []string{"Players"},
		Description: plats + " 409 `last_identity`: a player keeps at least one account (delete the player instead).",
	}), func(ctx context.Context, in *UnlinkIdentityInput) (*struct{}, error) {
		id, err := a.playerID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if _, err := a.platformName(in.Platform); err != nil {
			return nil, err
		}
		if err := a.svc.UnlinkIdentity(ctx, id, in.Platform, in.DeleteFiles); err != nil {
			return nil, mapErr(err)
		}
		return nil, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "merge-player", Method: http.MethodPost, Path: "/api/v1/players/{source}/merge",
		Summary: "Merge a player into another one", Tags: []string{"Players"},
		Description: "Moves every account, score and replay of the source into `into`; the source ID keeps resolving to it. " +
			"409 `merge_platform_conflict` when both players have an account on the same platform.",
	}), func(ctx context.Context, in *MergeInput) (*PlayerOutput, error) {
		src, err := a.playerID(ctx, in.Source)
		if err != nil {
			return nil, err
		}
		into, err := a.playerID(ctx, in.Body.Into)
		if err != nil {
			return nil, err
		}
		if err := a.svc.MergePlayers(ctx, src, into); err != nil {
			return nil, mapErr(err)
		}
		return a.playerOutput(ctx, into)
	})
}

func (a *API) playerOutput(ctx context.Context, id string) (*PlayerOutput, error) {
	dto, err := a.summary(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &PlayerOutput{Body: dto}, nil
}
```

- [ ] **Step 8: Run the tests**

Run: `go test -race ./internal/api/ ./internal/app/`
Expected: PASS. If `body["code"]` is missing in a 409 response, huma wrapped the error instead of writing it whole: check that `Problem` (a pointer) is what `mapErr` returns, so `errors.As(err, &huma.StatusError)` finds it with its extra fields.

- [ ] **Step 9: Lint and commit**

Run: `go test ./... && make lint`
Expected: all ok; `0 issues.`

```bash
git add internal/api
git commit -m "feat(api): link, unlink and merge endpoints, per-platform scores and filters"
```

---
## Task 11: BeatLeader end-to-end test + docs

The whole path through the real app with fake ScoreSaber and BeatLeader servers: add a player from a BeatLeader URL, link their ScoreSaber account, let the worker archive both platforms' replays (the BeatLeader one from `replays-storage`), and read everything back through the public routes, the account link, the merged page and the API. Then the README, and one manual run against live BeatLeader.

**Files:**
- Modify: `internal/app/app_test.go` (shared `startApp` helper; `TestEndToEnd` uses it)
- Create: `internal/app/beatleader_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: everything above; `app.Options{ScoreSaberURL, BeatLeaderURL}` (Task 2), `beatleader.WithBaseURL` replay prefixes (`{u}/replays-storage/` etc., Task 1).
- Produces: `startApp(t, app.Options) running` test helper (`Base`, `Client`, `Cookie`, `Stop() error`, `do`, `waitArchived`).

- [ ] **Step 1: Extract the app test harness**

Replace `internal/app/app_test.go` with (it keeps `fakeScoreSaber`, `getBody`, `TestEndToEnd`'s assertions and `TestBeatLeaderRegistered` from Task 2):

```go
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
```

Run: `go test -race ./internal/app/`
Expected: PASS (the refactor changes no assertion).

- [ ] **Step 2: Write the BeatLeader end-to-end test**

`internal/app/beatleader_test.go`:

```go
package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/app"
)

var bsorBytes = []byte("BSOR e2e payload")

// fakeBeatLeader serves one player with one score whose replay sits under
// replays-storage (beatleader.WithBaseURL maps the replay hosts onto it).
// The map is the one fakeScoreSaber's score 777 is on: hash ABC, Expert+.
func fakeBeatLeader(t *testing.T) *httptest.Server {
	t.Helper()
	set := time.Now().UTC().Add(time.Hour).Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /player/76561198038925092", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"76561198038925092","name":"Yewolf","avatar":"https://cdn.assets.beatleader.xyz/a.png","country":"FR"}`))
	})
	mux.HandleFunc("GET /player/76561198038925092/scores", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"metadata":{"page":1,"itemsPerPage":100,"total":1},"data":[{"id":888,"baseScore":1050,"modifiedScore":1050,
			"accuracy":0.96,"pp":0,"rank":3,"modifiers":"","hmd":256,"timeset":"%d","timepost":%d,"leaderboardId":"abc71",
			"replay":"http://%s/replays-storage/888-76561198038925092-ExpertPlus-Standard-ABC.bsor",
			"leaderboard":{"id":"abc71","song":{"hash":"abc","name":"E2E Song","subName":"","author":"A","mapper":"M","coverImage":""},
			"difficulty":{"value":9,"modeName":"Standard","difficultyName":"ExpertPlus","status":0,"stars":null,"maxScore":1100}}}]}`, set, set, r.Host)
	})
	mux.HandleFunc("GET /replays-storage/888-76561198038925092-ExpertPlus-Standard-ABC.bsor", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bsorBytes)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBeatLeaderEndToEnd(t *testing.T) {
	r := startApp(t, app.Options{ScoreSaberURL: fakeScoreSaber(t).URL, BeatLeaderURL: fakeBeatLeader(t).URL})

	code, body := r.do(t, http.MethodPost, "/api/v1/players", `{"ref":"https://beatleader.com/u/76561198038925092"}`)
	if code != http.StatusCreated {
		t.Fatalf("add from a BeatLeader URL: %d %s", code, body)
	}
	var p struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Identities []struct {
			Platform string `json:"platform"`
		} `json:"identities"`
	}
	_ = json.Unmarshal(body, &p)
	if p.Name != "Yewolf" || len(p.Identities) != 1 || p.Identities[0].Platform != "beatleader" {
		t.Fatalf("player = %+v", p)
	}
	code, body = r.do(t, http.MethodPost, "/api/v1/players/"+p.ID+"/identities", `{"platform":"scoresaber","ref":"1001"}`)
	if code != http.StatusOK || !strings.Contains(string(body), `"platform":"scoresaber"`) {
		t.Fatalf("link ScoreSaber: %d %s", code, body)
	}

	r.waitArchived(t, "/api/v1/scores/beatleader/888")
	r.waitArchived(t, "/api/v1/scores/777")

	for path, want := range map[string][]byte{"/r/bl/888.bsor": bsorBytes, "/r/777.dat": replayBytes} {
		status, got, err := getBody(r.Client, r.Base+path)
		if err != nil || status != 200 || !bytes.Equal(got, want) {
			t.Fatalf("GET %s: %d %q %v", path, status, got, err)
		}
	}
	status, _, _ := getBody(r.Client, r.Base+"/s/bl/888")
	if status != 200 {
		t.Fatalf("score page = %d", status)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, r.Base+"/p/bl/76561198038925092", nil)
	res, err := r.Client.Do(req)
	if err != nil || res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != "/p/"+p.ID {
		t.Fatalf("account link: %v %v", res, err)
	}
	res.Body.Close()

	_, page, _ := getBody(r.Client, r.Base+"/p/"+p.ID)
	html := string(page)
	if strings.Count(html, "E2E Song") != 1 || !strings.Contains(html, `href="/s/777"`) || !strings.Contains(html, `href="/s/bl/888"`) {
		t.Fatalf("one row for the map played on both platforms, with both chips:\n%s", html)
	}

	code, body = r.do(t, http.MethodGet, "/api/v1/sync", "")
	if code != 200 || !strings.Contains(string(body), `"name":"beatleader/api"`) {
		t.Fatalf("sync status: %d %s", code, body)
	}
}
```

Run: `go test -race ./internal/app/ -run TestBeatLeaderEndToEnd -v`
Expected: PASS. If a replay is never archived, temporarily set `LogLevel: "debug"` in `startApp` and read the sync events: a "replays skipped" warning means the fake's replay URL is not under `{base}/replays-storage/`.

- [ ] **Step 3: README**

`README.md`:

1. Replace the opening paragraph:

```markdown
Self-hosted archive for Beat Saber replays from ScoreSaber and BeatLeader.
Track players, keep every replay the platforms still offer (ScoreSaber prunes
them), and serve them back as pages, raw downloads and an embeddable 3D viewer
(ArcViewer).
```

2. After the "Upgrading to multi-platform" subsection, add:

```markdown
## Players and platforms

A player is one person with up to one account per platform (ScoreSaber,
BeatLeader). On **Manage**:

- **Add a player** from a profile URL (`https://scoresaber.com/u/…`,
  `https://beatleader.com/u/…`) or a player ID. Bare IDs are ScoreSaber IDs
  unless you pick another platform.
- **Link** another platform account to a player from its row; each badge is one
  account (click it to pause, resume or unlink it, optionally deleting its
  replay files). A player keeps at least one account.
- An account that is already tracked as another player cannot be linked: use
  **Merge** instead. Merging needs two players with no platform in common; every
  account, score and replay moves, and the old player URL redirects.

Every account has a readable link that survives merges: `/p/ss/{scoresaberID}`,
`/p/bl/{beatleaderID}`. The player page shows one row per map with each
platform's best score; the other plays of a map are one click away.

BeatLeader is paced client-side at 40 API requests and 20 replay-CDN requests per
10 seconds (its documented limit is 50); these limits are not configurable.
`SSA_HOURLY_BUDGET` only applies to ScoreSaber.
```

3. In **Embedding**, replace the last sentence:

```markdown
Options: `?autoplay=1`, `?loop=1`, `?ui=0`. Raw files are at
`/r/<score id>.dat` (ScoreSaber) and `/r/bl/<score id>.bsor` (BeatLeader) with
CORS enabled; BeatLeader embeds are at `/embed/bl/<score id>`.
```

4. In **API**, append:

```markdown
Scores carry `platform`, `kind`, `end_type`, `external_id` (the platform's ID)
and per-platform `url`/`download_url`/`embed_url`; `id` is the ScoreSaber score
ID and `0` on other platforms. `GET /api/v1/players/{id}/scores` filters by
`platform`, `min_score` and `max_score`; `GET /api/v1/scores/{platform}/{id}`
fetches one score by its platform ID. Accounts are managed with
`POST /api/v1/players/{id}/identities`, `PATCH`/`DELETE
/api/v1/players/{id}/identities/{platform}` and `POST
/api/v1/players/{id}/merge`; conflicts answer 409 with a `code`
(`identity_linked_elsewhere`, `platform_already_linked`, `last_identity`,
`merge_platform_conflict`). A merged-away player ID keeps resolving to the
survivor.
```

5. In the Configuration table, change the `SSA_HOURLY_BUDGET` meaning to `Max ScoreSaber requests per hour (1–360)` (unchanged wording if it already says so) and, under **Adding a platform**, add: `internal/beatleader` is the second, complete example (client, limiter, adapter, fixtures).

- [ ] **Step 4: Full verification**

Run: `make generate && git diff --exit-code -- '*_templ.go' internal/db/query internal/web/static/css/app.css` (generated code is in sync), then `go test -race ./... && make lint`.
Expected: no diff; all packages ok; `0 issues.`

Run: `go test -tags live -run Live ./internal/beatleader/ -v`
Expected: PASS against the live API (the owner's profile). Network flakiness is not a reason to change code; rerun.

- [ ] **Step 5: Manual check against live BeatLeader**

Run `make run` (data in `./data`), create the admin, then on **Manage**:

1. Paste `https://beatleader.com/u/76561198038925092` → the preview says BeatLeader · Yewolf → **Track player**.
2. On the new row, **Link** → ScoreSaber → `76561198038925092` → the row shows two account badges.
3. Open **Sync**: two queue rows (ScoreSaber, BeatLeader), a "BeatLeader API budget" and "BeatLeader CDN budget" card; events show polls for both platforms and no "replays skipped" warning.
4. When replays are archived, open the player page: maps played on both platforms show one row with two chips; "N more plays" expands; the share links `/p/ss/…` and `/p/bl/…` redirect to the page.
5. Open a BeatLeader score page (`/s/bl/…`): the viewer plays the `.bsor`, **Download .bsor** downloads it, and the embed code points at `/embed/bl/…`.

Record what you checked (and any fix) in the Verification log.

- [ ] **Step 6: Commit**

```bash
git add internal/app README.md docs/superpowers/plans/2026-10-10-beatleader-scores.md
git commit -m "test: BeatLeader end to end through the app; document platforms, accounts and merging"
```
