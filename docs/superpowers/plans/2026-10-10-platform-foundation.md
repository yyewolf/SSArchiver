# Platform Foundation Implementation Plan (multi-platform, part 1 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **Update the Progress Tracking section (below) as you go — it is the hand-off contract between agents.**

**Goal:** Move SSArchiver onto the platform-generic data model of the multi-platform spec: linked accounts (`player_platforms`), per-account sync cursors (`sync_feeds`), opaque player IDs and a platform registry. ScoreSaber stays the only registered platform, and existing installs upgrade in place without losing a row.

**Architecture:** A new `internal/platform` package defines the neutral types (`Play`, `Profile`, `Adapter`, `Registry`) every platform adapter produces. `internal/scoresaber` gains an adapter and registers as the legacy platform. `internal/service` stores plays from any platform and hands the worker "feeds" instead of players. `internal/archiver` drives feeds through the registry's adapters. `db.Migrate` upgrades v1 databases with a backed-up, count-verified, single-transaction migration whose SQL was tested against a seeded v1 copy.

**Tech Stack:** Go 1.27 · GORM + gorm gen · `github.com/glebarez/sqlite` (bundled SQLite 3.41.2) · templ · huma v2 · golangci-lint v2. No new dependencies.

**Spec:** [`docs/superpowers/specs/2026-10-10-beatleader-design.md`](../specs/2026-10-10-beatleader-design.md) — read §3, §4, §5.1–5.2, §5.5 and §7 before starting any task. Where the spec's §4.1 sketch and this plan differ in a signature, the plan wins (the spec was aligned to it on 2026-10-10). Section numbers below ("spec §7") refer to it. The original design ([2026-10-08 spec](../specs/2026-10-08-ssarchiver-design.md)) and plan ([2026-10-08 plan](2026-10-08-ssarchiver.md)) describe the code this plan changes.

**Sibling plans (written after this one lands):** Plan 2 — BeatLeader scores (client, linking/unlinking/merge, aliases, merged player page, per-platform routes, `platform`/`min_score`/`max_score` filters, registry-built CSP). Plan 3 — BeatLeader attempts (attempts feed, access probe UI + hint, `type` filter).

**What this plan deliberately does not do** (it belongs to Plans 2/3):
- anything BeatLeader-specific;
- link, unlink, merge, `player_aliases` writes and alias redirects, the per-account enable switch, and per-account counts in the DTO;
- per-platform score, replay and embed routes; merged map grouping and the share link on the player page; the new score-list filters; CSP changes; the event-log platform/feed filters;
- clearing the PB flag on PB-only platforms, and the warning when an archived replay's URL changes (spec §4.6, §5.4);
- the access-probe tier, the optional-feed API/UI and the access hint (the tables, `FeedSpec` fields and the fake platform's optional feed are in place).

## Global Constraints

Every task's requirements implicitly include all of these.

- Module `github.com/yyewolf/ssarchiver`, `go 1.27`. **No new dependencies; no version bumps.** Pinned: `gorm.io/gorm v1.31.2`, `gorm.io/gen v0.3.29`, `github.com/glebarez/sqlite v1.11.0`, `github.com/danielgtaylor/huma/v2 v2.39.1`, `github.com/a-h/templ v0.3.1070`, golangci-lint `v2.14.0`.
- **Never call `Migrator().DropColumn`, `Migrator().AlterColumn`, `Migrator().DropTable` or anything that rebuilds a table.** The pinned driver rebuilds tables with `DROP TABLE`, which under `foreign_keys(1)` cascades and deletes every score (reproduced: 4 scores → 0, `foreign_key_check` empty). Drop columns only with raw `ALTER TABLE … DROP COLUMN`.
- **Never use `gorm:"default:..."` on `bool` columns.** New `NOT NULL` *string* columns on *existing* tables use `gorm:"not null;default:''"` (needed for `ADD COLUMN`); columns of new tables carry no defaults — set every field explicitly.
- **No unique or composite index on new columns in model tags** — they are created by hand in `db.Migrate` (`indexDDL`) after the legacy backfill.
- **`sync_feeds` is created by raw DDL** (`syncFeedsDDL` in `internal/db/db.go`); `model.SyncFeed` declares **no** GORM relation (GORM put the composite FK on the wrong table when tried).
- **`players.id` is opaque.** No code may use it as a platform account ID; account IDs come only from `player_platforms.external_id` (`service.WorkFeed.ExternalID`). New IDs come from `platform.NewPlayerID()`.
- Stored values (exact strings, constants in `internal/model`): platform `scoresaber` (`model.PlatformScoreSaber`, slug `ss`); kinds `score`/`attempt`; end types `clear`/`fail`/`restart`/`quit`/`practice`/`unknown`; feed access `n/a`/`unknown`/`public`/`private`. A feed's key (`sync_feeds.feed`) is the row kind it produces.
- Internal row IDs for non-legacy platforms start at `platform.InternalIDBase = 1 << 62`.
- Timestamps are stored as GORM writes them (RFC 3339 text, e.g. `2026-10-08T10:00:00.123456789Z`) and compared in UTC via `service.Now()`.
- `api`, `web` and `archiver` never import `gorm.io/*` or `internal/db/query`.
- Generated code is committed: run `make generate` after changing `internal/model` or any `.templ` file and commit `internal/db/query/*` / `*_templ.go`.
- Every commit passes `go build ./...`, `go test ./...` and `make lint`. Conventional Commits (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`).
- Errors wrapped with `%w` and a package prefix (`fmt.Errorf("service: due feed: %w", err)`); `context.Context` first on every I/O function; logging via `log/slog` only.

## Review Focus

Inputs/failure modes the spec implies that are easy to get wrong. Each has a pinned test in the owning task.

1. **Upgrading a populated v1 database must lose nothing** — a table rebuild during the column drop silently deletes every score. Expected: identical row counts, every player gets an account row and a score feed carrying its old cursor, and any mismatch aborts startup with the backup path. → Task 2 `TestMigrateLegacyFixture`, Task 4 `TestMigrateDropsLegacyColumnsKeepingRows`, `TestVerifyMigrationRejectsCountChange`.
2. **Restarting during or after an upgrade** (crash mid-migration, `ssarchiver migrate` run twice, container restart loop) — expected: the second run is a no-op and never re-takes the backup over the pre-upgrade one. → Task 2 `TestMigrateIsIdempotent`, Task 4 `TestMigrateSecondRunAfterDrop`.
3. **A player whose ID is not their ScoreSaber ID** (every new player, and legacy players later relinked) — any leftover `p.ID`-as-account-ID call polls the wrong account. Expected: the worker always calls the platform with the account ID. → Task 5 `TestWorkerPollsAccountIDNotPlayerID`, `TestLegacyPlayerWithDifferentAccountID`.
4. **The same ScoreSaber profile pasted twice** (URL vs bare ID, `?page=2` suffix) — with opaque IDs the primary key no longer catches duplicates. Expected: the second add is refused with "already tracked" and the lookup preview says "Already tracked". → Task 5 `TestAddPlayerTwiceIsRejected`, `TestLookupPlayer` (its existing "Already tracked" assertions, plus the URL variant Task 5 adds).
5. **One platform rate-limited must not stall the others** — the single worker goroutine blocking inside a limiter stops every platform. Expected: work for the busy platform is skipped until its limiter is ready, other platforms keep going, and idle sleep wakes at the readiness time. → Task 8 `TestBusyPlatformIsSkipped`, `TestIdleWakesAtLimiterReadiness`.

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
| 1 | `internal/platform`: neutral types, registry, IDs, map key | ✅ done | kilo (SDD) | 2026-10-10 | f0f9065 |
| 2 | Additive schema + v1 migration (backup, backfill, indexes, verify) | ✅ done | kilo (SDD) | 2026-10-10 | 4951a48 |
| 3 | Feeds become the sync source of truth (service, worker, UI, API) | ✅ done | kilo (SDD) | 2026-10-10 | 84c5f32 |
| 4 | Drop the legacy `players` sync columns | ✅ done | kilo (SDD) | 2026-10-10 | f301824 |
| 5 | Opaque player IDs | ✅ done | kilo (SDD) | 2026-10-10 | 4e1f43d |
| 6 | Registry wiring: ScoreSaber adapter, neutral upsert, worker via adapters | ✅ done | kilo (SDD) | 2026-10-10 | 8ce3201 |
| 7 | Per-platform replay storage + genericity test (fake third platform) | ✅ done | kilo (SDD) | 2026-10-10 | 29cd9a9 |
| 8 | Limiter readiness, multi-limiter status, account links | ✅ done | kilo (SDD) | 2026-10-10 | 5d51237 |
| 9 | Upgrade end-to-end test + docs | ✅ done | kilo (SDD) | 2026-10-10 | 957e052 |

### Session hand-off

_Current task:_ COMPLETE — all 9 tasks implemented, each task-reviewed, final whole-branch review passed (1 Important fixed in d8a1b49, re-review clean).
_Next step:_ superpowers:finishing-a-development-branch (integration choice belongs to the human partner).
_Half-done / uncommitted:_ —
_Notes for next agent:_ Full execution ledger (rulings, per-task reviews, deferred-minor triage — all 17 minors triaged "ship" by the final review) lives in `.superpowers/sdd/2026-10-10-platform-foundation/progress.md` (git-ignored; the git history is the durable record). Re-open in Plan 2: ResolvePlayer's discarded ParseRef cause (#11), per-platform OG wording beyond primary identity, NextPollAt SQL-side busy filtering if feed counts grow, storage API contexts. A user-requested `Ptr`→`new(expr)` modernization (832f2c7) landed after Task 9. The migration SQL in Task 2/4 was executed against a seeded v1 copy under `foreign_keys=ON` while writing this plan (counts 3/4/2 preserved, FK check empty, cursors carried over, `map_key` normalized). The GORM model set in Task 2 was run through `AutoMigrate` twice on a v1 copy: first run emitted exactly the expected DDL, second run emitted nothing but the no-op `CREATE TABLE IF NOT EXISTS sync_feeds`.

### Deviations log

| Date | Task | Deviation | Reason | Later tasks updated? |
|------|------|-----------|--------|----------------------|
| 2026-10-10 | all | Per-step `- [ ]` checkboxes in task bodies were not ticked during execution | Progress Tracking table, Verification log and Session hand-off were maintained per task instead (they carry the same information with dates and SHAs); mass-ticking 100+ boxes post-hoc adds diff noise, not information | n/a |

### Verification log

| Date | Task | Command(s) | Result |
|------|------|------------|--------|
| 2026-10-10 | 1 | `go test -race ./internal/platform/... ./internal/model/...`; `make lint && go test ./...` | platform ok; 0 issues.; all packages ok |
| 2026-10-10 | 2 | `go test -race ./internal/db/...`; `go test -race ./...`; `make lint` | db ok; all packages ok; 0 issues. |
| 2026-10-10 | 3 | `go test -race ./...`; `make lint`; Step 13 leftover-reader grep | all packages ok; 0 issues.; grep empty |
| 2026-10-10 | 4 | `go test -race ./internal/db/...`; `go test -race ./...`; `make lint` | db ok (columns dropped, rows kept); all packages ok; 0 issues. |
| 2026-10-10 | 5 | `go test -race ./...`; `make lint` | all 14 packages ok; 0 issues. |
| 2026-10-10 | 6 | `go test -race ./...`; `make lint` | all 14 packages ok; 0 issues. |
| 2026-10-10 | 7 | `go test -race ./...`; `make lint` | all 14 packages ok; 0 issues.; TestThirdPlatformEndToEnd green |
| 2026-10-10 | 8 | `go test -race -count=1 ./...`; `make lint` | all 14 packages ok; 0 issues. |
| 2026-10-10 | 9 | `go test -race ./internal/app/ -run TestUpgradeFromV1DataDir -v`; `make generate && git diff --exit-code -- ':!docs' && go test -race ./... && make lint` | PASS; no drift; all ok; 0 issues. |

---

## File Map

```
internal/platform/platform.go          NEW  neutral types: Profile, LeaderboardData, Play, PlayPage, ReplayRef, Hint, FeedSpec,
                                            Platform, Adapter, Limiter, snapshots; error sentinels
internal/platform/registry.go          NEW  Registry: validation, lookup, ordering, ParseRef
internal/platform/ids.go               NEW  NewPlayerID, ValidPlayerID, InternalIDBase
internal/platform/mapkey.go            NEW  MapKey (cross-platform map grouping key)
internal/model/model.go                     + constants, PlayerPlatform, SyncFeed, PlayerAlias, new columns; Player slimmed (Task 4)
internal/db/db.go                           Migrate: backup, staged AutoMigrate, sync_feeds DDL, legacy backfill/drop, indexes, verify
internal/db/testdata/v1.sql            NEW  seeded v1 database (exact v1 DDL)
internal/db/migrate_test.go            NEW  migration tests; export_test.go exposes verifyMigration
internal/db/query/*                         REGENERATED (make generate)
internal/service/feeds.go              NEW  WorkFeed, FeedKey, Busy, DueFeed, NextPollAt, NextBackfillFeed, feed mutators
internal/service/backfill.go           DELETED (replaced by feeds.go)
internal/service/players.go                 Identity/PlayerSummary read models, AddPlayer via registry, opaque IDs, PlayerByIdentity
internal/service/scores.go                  UpsertPlays (neutral) + internal ID allocation
internal/service/replays.go                 replay tiers via feeds, Busy filtering, PutReplay/OpenReplay
internal/service/reconcile.go               per-platform storage layout
internal/service/service.go                 New(gdb, store, *platform.Registry); Platforms()
internal/scoresaber/platform.go        NEW  NewPlatform (legacy registry entry) + adapter + Plays conversion
internal/scoresaber/{client,limiter}.go     error sentinels alias platform's; Limiter.Name/Ready; snapshot type aliases
internal/storage/storage.go                 Loc-based API, per-platform directories, opaque player IDs
internal/archiver/{worker,poll,download}.go feeds + adapters + readiness; Status.Limiters
internal/web/{admin,admin_sync,public,replay,web}.go, views/{admin,player,sync}.templ   read summaries; account-link route
internal/api/{dto,players,sync,scores}.go   identities[] DTO, compat fields, by-account lookup, limiters[]
internal/app/app.go                         build the registry
internal/testutil/{fakes,service}.go        fake ScoreSaber API, FakePlatform (third platform), NewServiceWith, AddPlayer
README.md                                   upgrade + "Adding a platform" notes
```

---

## Task 1: `internal/platform` — neutral types, registry, IDs, map key

Pure package, no I/O, no DB. Everything later tasks share is defined here.

**Files:**
- Modify: `internal/model/model.go` (constants only)
- Create: `internal/platform/platform.go`, `internal/platform/registry.go`, `internal/platform/ids.go`, `internal/platform/mapkey.go`
- Test: `internal/platform/registry_test.go`, `internal/platform/ids_test.go`, `internal/platform/mapkey_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (exact names later tasks use):
  - `model.PlatformScoreSaber`, `model.KindScore`, `model.KindAttempt`, `model.EndClear` … `model.EndUnknown`, `model.AccessNA`, `model.AccessUnknown`, `model.AccessPublic`, `model.AccessPrivate`
  - `platform.ErrNotFound`, `platform.ErrRateLimited`, `platform.ErrUnauthorized`, `platform.ErrInvalidRef`
  - types `platform.Profile`, `LeaderboardData`, `Play`, `PlayPage`, `ReplayRef`, `Hint`, `FeedSpec`, `Platform`, `Adapter`, `Limiter`, `WindowSnapshot`, `LimiterSnapshot`
  - `platform.NewRegistry(ps ...Platform) (*Registry, error)`, `(*Registry).All() []Platform`, `Get(name) (Platform, bool)`, `BySlug(slug) (Platform, bool)`, `Legacy() (Platform, bool)`, `Priority(name) int`, `ParseRef(input, platformName string) (Platform, string, error)`
  - `(Platform).Feed(kind) (FeedSpec, bool)`, `(Platform).RequiredFeeds() []FeedSpec`
  - `platform.NewPlayerID() string`, `platform.ValidPlayerID(id) bool`, `platform.InternalIDBase`
  - `platform.MapKey(songHash, gameMode string, difficulty int) string`

- [ ] **Step 1: Add the constants to `internal/model/model.go`**

Insert after the existing `KindWorker` const block (before `type Player struct`):

```go
// PlatformScoreSaber is the legacy platform's registry name (spec §4.1). It is
// the only platform name code outside its own package needs (the migration).
const PlatformScoreSaber = "scoresaber"

// Row kinds and end types (spec §4.4). A feed's key in sync_feeds is the row
// kind it produces.
const (
	KindScore   = "score"   // a leaderboard submission
	KindAttempt = "attempt" // any other recorded run

	EndClear    = "clear"
	EndFail     = "fail"
	EndRestart  = "restart"
	EndQuit     = "quit"
	EndPractice = "practice"
	EndUnknown  = "unknown"
)

// Feed access states (spec §4.3).
const (
	AccessNA      = "n/a"     // the feed needs no access probe
	AccessUnknown = "unknown" // not probed yet
	AccessPublic  = "public"
	AccessPrivate = "private"
)
```

- [ ] **Step 2: Write the failing tests**

`internal/platform/mapkey_test.go`:

```go
package platform_test

import (
	"testing"

	"github.com/yyewolf/ssarchiver/internal/platform"
)

func TestMapKey(t *testing.T) {
	for _, c := range []struct {
		hash, mode string
		diff       int
		want       string
	}{
		// ScoreSaber: uppercase hash, "Solo" prefix.
		{"4640065298E79DC3D61A15695AEB7FED95B42B30", "SoloStandard", 9, "4640065298e79dc3d61a15695aeb7fed95b42b30/Standard/9"},
		// BeatLeader: lowercase hash, bare mode — must give the same key.
		{"4640065298e79dc3d61a15695aeb7fed95b42b30", "Standard", 9, "4640065298e79dc3d61a15695aeb7fed95b42b30/Standard/9"},
		{"ABC", "SoloOneSaber", 7, "abc/OneSaber/7"},
		{"abc", "OneSaber", 7, "abc/OneSaber/7"},
		{"abc", "Solo90Degree", 5, "abc/90Degree/5"},
		{"abc", "Lawless", 1, "abc/Lawless/1"},
	} {
		if got := platform.MapKey(c.hash, c.mode, c.diff); got != c.want {
			t.Errorf("MapKey(%q, %q, %d) = %q, want %q", c.hash, c.mode, c.diff, got, c.want)
		}
	}
}
```

`internal/platform/ids_test.go`:

```go
package platform_test

import (
	"regexp"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/platform"
)

func TestNewPlayerID(t *testing.T) {
	re := regexp.MustCompile(`^[a-hjkmnp-tv-z][0-9a-hjkmnp-tv-z]{11}$`)
	seen := map[string]bool{}
	for range 2000 {
		id := platform.NewPlayerID()
		if !re.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
		if !platform.ValidPlayerID(id) {
			t.Fatalf("ValidPlayerID(%q) = false", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValidPlayerID(t *testing.T) {
	for id, want := range map[string]bool{
		"76561198038925092": true, // legacy ScoreSaber-derived IDs stay valid
		"k7m2q9x4c1ab":      true,
		"a-b":               true,
		"":                  false,
		"../etc":            false,
		"A1":                false,
		"a/b":               false,
		"x.dat":             false,
		"0123456789012345678901234567890123456789x": false, // 41 chars
	} {
		if got := platform.ValidPlayerID(id); got != want {
			t.Errorf("ValidPlayerID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestInternalIDBase(t *testing.T) {
	if platform.InternalIDBase != 1<<62 {
		t.Fatalf("InternalIDBase = %d", platform.InternalIDBase)
	}
}
```

`internal/platform/registry_test.go`:

```go
package platform_test

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

type stubAdapter struct{}

func (stubAdapter) Resolve(context.Context, string) (platform.Profile, error) {
	return platform.Profile{}, nil
}

func (stubAdapter) FeedPage(context.Context, string, string, int) (platform.PlayPage, error) {
	return platform.PlayPage{}, nil
}

func (stubAdapter) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessNA, 0, nil
}

func (stubAdapter) Replay(context.Context, platform.ReplayRef) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (stubAdapter) Limiters() []platform.Limiter { return nil }
func (stubAdapter) FeedLimiter(string) string    { return "" }
func (stubAdapter) ReplayLimiter(string) string  { return "" }

var digits = regexp.MustCompile(`^[0-9]{1,32}$`)

func plat(name, slug string, legacy bool, prio int, host string) platform.Platform {
	re := regexp.MustCompile(`^https://` + regexp.QuoteMeta(host) + `/u/([0-9]+)$`)
	return platform.Platform{
		Name: name, Slug: slug, DisplayName: strings.ToUpper(name), Priority: prio, Legacy: legacy, ReplayExt: ".bin",
		ProfileURL: func(id string) string { return "https://" + host + "/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := re.FindStringSubmatch(in)
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: digits.MatchString,
		Feeds:   []platform.FeedSpec{{Kind: model.KindScore}},
		Adapter: stubAdapter{},
	}
}

func TestRegistryOrderAndLookup(t *testing.T) {
	r, err := platform.NewRegistry(plat("zeta", "zz", false, 10, "z.example"), plat("alpha", "aa", true, 0, "a.example"), plat("beta", "bb", false, 10, "b.example"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range r.All() {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "alpha,beta,zeta" {
		t.Fatalf("order = %v (priority, then name)", names)
	}
	if p, ok := r.BySlug("bb"); !ok || p.Name != "beta" {
		t.Fatalf("BySlug = %+v %v", p, ok)
	}
	if p, ok := r.Legacy(); !ok || p.Name != "alpha" {
		t.Fatalf("Legacy = %+v %v", p, ok)
	}
	if r.Priority("alpha") >= r.Priority("zeta") || r.Priority("nope") <= r.Priority("zeta") {
		t.Fatal("Priority must order known platforms and put unknown ones last")
	}
}

func TestRegistryRejectsInvalid(t *testing.T) {
	good := plat("alpha", "aa", true, 0, "a.example")
	for name, mutate := range map[string]func(p *platform.Platform){
		"bad name":            func(p *platform.Platform) { p.Name = "Alpha" },
		"bad slug":            func(p *platform.Platform) { p.Slug = "a1" },
		"slug map":            func(p *platform.Platform) { p.Slug = "map" },
		"no display name":     func(p *platform.Platform) { p.DisplayName = "" },
		"bad ext":             func(p *platform.Platform) { p.ReplayExt = "dat" },
		"no adapter":          func(p *platform.Platform) { p.Adapter = nil },
		"no score feed":       func(p *platform.Platform) { p.Feeds = []platform.FeedSpec{{Kind: model.KindAttempt, Optional: true}} },
		"optional score feed": func(p *platform.Platform) { p.Feeds = []platform.FeedSpec{{Kind: model.KindScore, Optional: true}} },
		"unknown kind":        func(p *platform.Platform) { p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: "bogus", Optional: true}) },
		"duplicate kind":      func(p *platform.Platform) { p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: model.KindScore}) },
		"required needs access": func(p *platform.Platform) {
			p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: model.KindAttempt, NeedsAccess: true})
		},
	} {
		p := good
		p.Feeds = append([]platform.FeedSpec(nil), good.Feeds...)
		mutate(&p)
		if _, err := platform.NewRegistry(p); err == nil {
			t.Errorf("%s: NewRegistry accepted %+v", name, p)
		}
	}
	if _, err := platform.NewRegistry(good, plat("alpha", "bb", false, 1, "b.example")); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := platform.NewRegistry(good, plat("beta", "aa", false, 1, "b.example")); err == nil {
		t.Error("duplicate slug accepted")
	}
	if _, err := platform.NewRegistry(good, plat("beta", "bb", true, 1, "b.example")); err == nil {
		t.Error("two legacy platforms accepted")
	}
	if _, err := platform.NewRegistry(); err == nil {
		t.Error("empty registry accepted")
	}
}

func TestParseRef(t *testing.T) {
	r, err := platform.NewRegistry(plat("alpha", "aa", true, 0, "a.example"), plat("beta", "bb", false, 10, "b.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		in, hint, wantPlat, wantID string
		ok                         bool
	}{
		{"https://a.example/u/42", "", "alpha", "42", true},
		{"https://b.example/u/42", "", "beta", "42", true}, // URLs pick their platform
		{"  42 ", "", "alpha", "42", true},                 // bare IDs go to the legacy platform
		{"42", "beta", "beta", "42", true},                 // unless a platform is chosen
		{"https://b.example/u/7", "beta", "beta", "7", true},
		{"https://a.example/u/7", "beta", "", "", false}, // URL of another platform than the chosen one
		{"nope", "", "", "", false},
		{"42", "gamma", "", "", false},
	} {
		p, id, err := r.ParseRef(c.in, c.hint)
		if !c.ok {
			if !errors.Is(err, platform.ErrInvalidRef) {
				t.Errorf("ParseRef(%q, %q) err = %v, want ErrInvalidRef", c.in, c.hint, err)
			}
			continue
		}
		if err != nil || p.Name != c.wantPlat || id != c.wantID {
			t.Errorf("ParseRef(%q, %q) = %s %q %v", c.in, c.hint, p.Name, id, err)
		}
	}
}

func TestFeedLookup(t *testing.T) {
	p := plat("alpha", "aa", true, 0, "a.example")
	p.Feeds = append(p.Feeds, platform.FeedSpec{Kind: model.KindAttempt, Optional: true, NeedsAccess: true})
	if _, err := platform.NewRegistry(p); err != nil {
		t.Fatal(err)
	}
	if f, ok := p.Feed(model.KindAttempt); !ok || !f.Optional {
		t.Fatalf("Feed(attempt) = %+v %v", f, ok)
	}
	if req := p.RequiredFeeds(); len(req) != 1 || req[0].Kind != model.KindScore {
		t.Fatalf("RequiredFeeds = %+v", req)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/platform/...`
Expected: FAIL — `no non-test Go files` / `undefined: platform.MapKey`.

- [ ] **Step 4: Implement `internal/platform/platform.go`**

```go
// Package platform describes the leaderboard platforms SSArchiver archives
// from (spec §4.1): the neutral types every platform adapter produces, the
// registry, and helpers shared by all platforms. It knows no concrete
// platform; those live in their own packages and are registered at startup.
package platform

import (
	"context"
	"errors"
	"io"
	"time"
)

// Errors adapters wrap so callers can classify failures without knowing the platform.
var (
	ErrNotFound     = errors.New("not found")
	ErrRateLimited  = errors.New("rate limited")
	ErrUnauthorized = errors.New("unauthorized")
	ErrInvalidRef   = errors.New("invalid player reference")
)

// Profile is a player's display identity on one platform.
type Profile struct {
	ExternalID string
	Name       string
	AvatarURL  string
	Country    string
}

// LeaderboardData is one map difficulty as a platform reports it.
type LeaderboardData struct {
	ExternalID    string
	SongHash      string
	SongName      string
	SongSubName   string
	SongAuthor    string
	Mapper        string
	Difficulty    int // 1..9, same scale on every platform
	DifficultyRaw string
	GameMode      string
	CoverURL      string
	Status        string // RANKED, QUALIFIED, LOVED, UNRANKED
	Stars         float64
	MaxScore      int64
}

// Play is one normalized score or attempt (spec §5.2).
type Play struct {
	Leaderboard     LeaderboardData
	Kind            string // model.KindScore | model.KindAttempt
	EndType         string // model.End*
	ExternalID      string
	EndTime         *float64 // seconds into the song when the run ended (attempts)
	Rank            int
	ModifiedScore   int64
	UnmodifiedScore int64
	Accuracy        float64 // 0..1
	PP              float64
	Mods            string // comma separated
	FullCombo       bool
	MissedNotes     int
	BadCuts         int
	MaxCombo        int
	HMD             string
	PersonalBest    bool
	SetAt           time.Time
	HasReplay       bool
	ReplayURL       string   // empty when the platform downloads replays by score ID
	Profile         *Profile // the player's profile when the payload carries one
}

// PlayPage is one page of a feed, newest first.
type PlayPage struct {
	Plays      []Play
	TotalPages int
}

// ReplayRef identifies the replay of a stored row.
type ReplayRef struct {
	Kind       string
	ExternalID string
	URL        string
}

// Hint tells the admin what a player must do to grant access to a feed (spec §6.3).
type Hint struct {
	Title    string
	Steps    []string
	LinkText string
	LinkURL  string
}

// FeedSpec declares one feed of a platform. Its Kind is also its key in sync_feeds.
type FeedSpec struct {
	Kind        string // model.KindScore | model.KindAttempt
	Optional    bool   // false: created with the account; true: admin opt-in
	NeedsAccess bool   // access must be probed before use (optional feeds only)
	AccessHint  *Hint  // shown while access is private
}

// Platform is one registered leaderboard platform.
type Platform struct {
	Name        string // stored in DB columns, e.g. "scoresaber"
	Slug        string // URL prefix, e.g. "ss"
	DisplayName string
	Priority    int  // lower wins for the player's display identity
	Legacy      bool // ScoreSaber only: bare IDs, legacy routes and storage layout
	ReplayExt   string
	ImageHosts  []string
	ProfileURL  func(externalID string) string
	ParseURL    func(input string) (externalID string, ok bool)
	ValidID     func(input string) bool
	Feeds       []FeedSpec
	Adapter     Adapter
}

// Feed returns the platform's feed of the given kind.
func (p Platform) Feed(kind string) (FeedSpec, bool) {
	for _, f := range p.Feeds {
		if f.Kind == kind {
			return f, true
		}
	}
	return FeedSpec{}, false
}

// RequiredFeeds are the feeds created with every account of this platform.
func (p Platform) RequiredFeeds() []FeedSpec {
	var out []FeedSpec
	for _, f := range p.Feeds {
		if !f.Optional {
			out = append(out, f)
		}
	}
	return out
}

// Adapter is everything the service and worker need from one platform.
// Implementations wrap ErrNotFound, ErrRateLimited and ErrUnauthorized.
type Adapter interface {
	// Resolve fetches a player's profile; ErrNotFound when the player does not exist.
	Resolve(ctx context.Context, externalID string) (Profile, error)
	// FeedPage returns one page (1-based, newest first) of a feed.
	FeedPage(ctx context.Context, kind, externalID string, page int) (PlayPage, error)
	// ProbeAccess returns model.AccessPublic or model.AccessPrivate for feeds
	// with NeedsAccess, plus the remote item total; model.AccessNA otherwise.
	ProbeAccess(ctx context.Context, kind, externalID string) (access string, total int64, err error)
	// Replay streams one replay; ErrNotFound when it is gone.
	Replay(ctx context.Context, ref ReplayRef) (io.ReadCloser, error)
	// Limiters lists the platform's client-side rate limiters.
	Limiters() []Limiter
	// FeedLimiter and ReplayLimiter name the limiter a listing or a replay
	// download of the given kind consumes ("" = not rate limited).
	FeedLimiter(kind string) string
	ReplayLimiter(kind string) string
}

// Limiter is a named client-side rate limiter.
type Limiter interface {
	Name() string
	// Ready reports whether a request may be sent at now, and if not, from when.
	Ready(now time.Time) (bool, time.Time)
	Snapshot() LimiterSnapshot
}

// WindowSnapshot is one rate-limit window's usage.
type WindowSnapshot struct {
	Name            string
	Limit           int
	Used            int
	Period          time.Duration
	ServerRemaining int // -1 when unknown
	ServerResetAt   time.Time
}

// LimiterSnapshot is a limiter's state for display.
type LimiterSnapshot struct {
	Windows      []WindowSnapshot
	BlockedUntil time.Time // zero when not blocked by server feedback
	Waiting      bool
	WaitUntil    time.Time
}
```

- [ ] **Step 5: Implement `internal/platform/registry.go`**

```go
package platform

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)
	slugRe = regexp.MustCompile(`^[a-z]{2,8}$`)
)

// Registry is the set of platforms this instance archives from.
type Registry struct {
	list   []Platform // by Priority, then Name
	byName map[string]Platform
	bySlug map[string]Platform
}

// NewRegistry validates and indexes the platforms.
func NewRegistry(ps ...Platform) (*Registry, error) {
	if len(ps) == 0 {
		return nil, errors.New("platform: empty registry")
	}
	r := &Registry{byName: map[string]Platform{}, bySlug: map[string]Platform{}}
	legacy := 0
	for _, p := range ps {
		if err := validate(p); err != nil {
			return nil, err
		}
		if _, dup := r.byName[p.Name]; dup {
			return nil, fmt.Errorf("platform: duplicate name %q", p.Name)
		}
		if _, dup := r.bySlug[p.Slug]; dup {
			return nil, fmt.Errorf("platform: duplicate slug %q", p.Slug)
		}
		if p.Legacy {
			legacy++
		}
		r.byName[p.Name], r.bySlug[p.Slug] = p, p
		r.list = append(r.list, p)
	}
	if legacy > 1 {
		return nil, errors.New("platform: more than one legacy platform")
	}
	slices.SortStableFunc(r.list, func(a, b Platform) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), strings.Compare(a.Name, b.Name))
	})
	return r, nil
}

func validate(p Platform) error {
	switch {
	case !nameRe.MatchString(p.Name):
		return fmt.Errorf("platform: invalid name %q", p.Name)
	case !slugRe.MatchString(p.Slug) || p.Slug == "map":
		return fmt.Errorf("platform %s: invalid slug %q", p.Name, p.Slug)
	case p.DisplayName == "":
		return fmt.Errorf("platform %s: missing display name", p.Name)
	case len(p.ReplayExt) < 2 || !strings.HasPrefix(p.ReplayExt, "."):
		return fmt.Errorf("platform %s: invalid replay extension %q", p.Name, p.ReplayExt)
	case p.Adapter == nil || p.ProfileURL == nil || p.ParseURL == nil || p.ValidID == nil:
		return fmt.Errorf("platform %s: missing adapter or ref functions", p.Name)
	}
	seen := map[string]bool{}
	for _, f := range p.Feeds {
		if f.Kind != model.KindScore && f.Kind != model.KindAttempt {
			return fmt.Errorf("platform %s: unknown feed kind %q", p.Name, f.Kind)
		}
		if seen[f.Kind] {
			return fmt.Errorf("platform %s: duplicate feed %q", p.Name, f.Kind)
		}
		seen[f.Kind] = true
		if f.NeedsAccess && !f.Optional {
			return fmt.Errorf("platform %s: feed %q needs access but is not optional", p.Name, f.Kind)
		}
	}
	if f, ok := p.Feed(model.KindScore); !ok || f.Optional {
		return fmt.Errorf("platform %s: needs a required %q feed", p.Name, model.KindScore)
	}
	return nil
}

// All returns the platforms by Priority, then name.
func (r *Registry) All() []Platform { return slices.Clone(r.list) }

func (r *Registry) Get(name string) (Platform, bool) {
	p, ok := r.byName[name]
	return p, ok
}

func (r *Registry) BySlug(slug string) (Platform, bool) {
	p, ok := r.bySlug[slug]
	return p, ok
}

// Legacy returns the legacy platform (ScoreSaber), if registered.
func (r *Registry) Legacy() (Platform, bool) {
	for _, p := range r.list {
		if p.Legacy {
			return p, true
		}
	}
	return Platform{}, false
}

// Priority orders platforms for display; unknown names sort last.
func (r *Registry) Priority(name string) int {
	if p, ok := r.byName[name]; ok {
		return p.Priority
	}
	return math.MaxInt
}

// ParseRef resolves an admin's input to a platform and account ID. With
// platformName empty, profile URLs pick their platform and bare IDs go to
// the legacy platform.
func (r *Registry) ParseRef(input, platformName string) (Platform, string, error) {
	in := strings.TrimSpace(input)
	if platformName != "" {
		p, ok := r.Get(platformName)
		if !ok {
			return Platform{}, "", fmt.Errorf("%w: unknown platform %q", ErrInvalidRef, platformName)
		}
		if id, ok := p.ParseURL(in); ok {
			return p, id, nil
		}
		if p.ValidID(in) {
			return p, in, nil
		}
		return Platform{}, "", fmt.Errorf("%w: not a %s player ID or profile URL", ErrInvalidRef, p.DisplayName)
	}
	for _, p := range r.list {
		if id, ok := p.ParseURL(in); ok {
			return p, id, nil
		}
	}
	if p, ok := r.Legacy(); ok && p.ValidID(in) {
		return p, in, nil
	}
	return Platform{}, "", fmt.Errorf("%w: paste a profile URL or a player ID", ErrInvalidRef)
}
```

- [ ] **Step 6: Implement `internal/platform/ids.go` and `internal/platform/mapkey.go`**

`ids.go`:

```go
package platform

import (
	"crypto/rand"
	"regexp"
)

// InternalIDBase is the first internal row ID of leaderboards and scores of
// non-legacy platforms (spec §4.5). Legacy (ScoreSaber) rows keep the
// platform's own numeric IDs, which are far below it.
const InternalIDBase int64 = 1 << 62

// idAlphabet is lowercase base32 without i, l, o and u.
const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// NewPlayerID returns a random opaque player ID (spec §4.2): 12 characters,
// the first one a letter so it never looks like a legacy all-digit ID.
func NewPlayerID() string {
	var b [12]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error (it panics on failure)
	out := make([]byte, len(b))
	out[0] = idAlphabet[10+int(b[0])%22]
	for i := 1; i < len(b); i++ {
		out[i] = idAlphabet[int(b[i])%32]
	}
	return string(out)
}

var playerIDRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// ValidPlayerID reports whether id is a well-formed (path-safe) player ID:
// legacy all-digit IDs and opaque IDs alike.
func ValidPlayerID(id string) bool { return playerIDRe.MatchString(id) }
```

`mapkey.go`:

```go
package platform

import (
	"strconv"
	"strings"
)

// MapKey is the cross-platform grouping key of a map difficulty (spec §4.5):
// lowercase song hash, game mode without ScoreSaber's "Solo" prefix, and the
// 1..9 difficulty. db.Migrate applies the same rule in SQL to legacy rows.
func MapKey(songHash, gameMode string, difficulty int) string {
	return strings.ToLower(songHash) + "/" + strings.TrimPrefix(gameMode, "Solo") + "/" + strconv.Itoa(difficulty)
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test -race ./internal/platform/... ./internal/model/...`
Expected: `ok` for `internal/platform`.

- [ ] **Step 8: Lint and full test run**

Run: `make lint && go test ./...`
Expected: `0 issues.`, all packages `ok`.

- [ ] **Step 9: Commit**

```bash
git add internal/model/model.go internal/platform
git commit -m "feat(platform): add neutral platform types, registry, opaque IDs and map key"
```

---

## Task 2: Additive schema + v1 migration (backup, backfill, indexes, verify)

Adds the new tables and columns and moves every v1 row onto the platform model, **without** dropping anything yet (Task 4 drops the six legacy `players` columns once nothing reads them). The `players` model keeps its legacy fields in this task, so the service keeps working; `AddPlayer` and the ScoreSaber upsert start filling the new tables/columns too.

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/db/db.go`
- Create: `internal/db/testdata/v1.sql`, `internal/db/migrate_test.go`, `internal/db/export_test.go`
- Regenerate: `internal/db/query/*` (`make generate`)
- Modify: `internal/service/players.go` (`AddPlayer`, new `Identities`, `Feeds`), `internal/service/scores.go` (`leaderboardFrom`, `scoreFrom`)
- Create: `internal/service/feeds.go` (only `newFeed` in this task)
- Test: `internal/service/players_test.go`, `internal/service/scores_test.go`

**Interfaces:**
- Consumes: Task 1 constants, `platform.MapKey`.
- Produces:
  - models `model.PlayerPlatform`, `model.SyncFeed`, `model.PlayerAlias`; new fields `Leaderboard.{Platform,ExternalID,MapKey}`, `Score.{Platform,Kind,EndType,EndTime,ExternalID,ReplayURL}`, `SyncEvent.{Platform,Feed}`
  - gen query fields `query.Query.PlayerPlatform`, `.SyncFeed`, `.PlayerAlias`
  - `db.BackupName = "ssarchiver.pre-platforms.db"`
  - `service.(*Service).Identities(ctx, playerID) ([]*model.PlayerPlatform, error)`, `Feeds(ctx, playerID) ([]*model.SyncFeed, error)`
  - unexported `service.newFeed(playerID, platformName, kind string, now time.Time, access string) *model.SyncFeed`

- [ ] **Step 1: Add the models**

In `internal/model/model.go` add to `Leaderboard` (after `MaxScore`):

```go
	Platform   string `gorm:"not null;default:''"` // unique with ExternalID: index created by db.Migrate
	ExternalID string `gorm:"not null;default:''"` // the platform's leaderboard ID
	MapKey     string `gorm:"not null;default:''"` // platform.MapKey; index created by db.Migrate
```

Add to `Score` (after `LastError`):

```go
	Platform   string `gorm:"not null;default:''"` // unique with Kind+ExternalID: index created by db.Migrate
	Kind       string `gorm:"not null;default:''"` // KindScore | KindAttempt
	EndType    string `gorm:"not null;default:''"` // End*
	EndTime    *float64
	ExternalID string `gorm:"not null;default:''"` // the platform's score/attempt ID
	ReplayURL  *string
```

Add to `SyncEvent` (after `ScoreID`):

```go
	Platform *string
	Feed     *string
```

Add after `type Player struct {…}`:

```go
// PlayerPlatform is one linked platform account of a player (spec §4.3).
type PlayerPlatform struct {
	PlayerID   string    `gorm:"primaryKey"`
	Player     *Player   `gorm:"constraint:OnDelete:CASCADE"`
	Platform   string    `gorm:"primaryKey"`
	ExternalID string    `gorm:"not null"` // unique with Platform: index created by db.Migrate
	Enabled    bool      `gorm:"not null"`
	LinkedAt   time.Time `gorm:"not null"`
	LastError  string    `gorm:"not null"`
}

// SyncFeed is the sync cursor of one feed of one platform account (spec
// §4.3). Its table is created by db.Migrate with raw DDL because of the
// composite foreign key to player_platforms: declare no relation here.
type SyncFeed struct {
	PlayerID           string    `gorm:"primaryKey"`
	Platform           string    `gorm:"primaryKey"`
	Feed               string    `gorm:"primaryKey"` // the row kind it produces: KindScore | KindAttempt
	Enabled            bool      `gorm:"not null"`
	StartedAt          time.Time `gorm:"not null"` // plays set at or after it are "new"
	Access             string    `gorm:"not null"` // Access*
	AccessCheckedAt    *time.Time
	RemoteTotal        int64  `gorm:"not null"`
	BackfillState      string `gorm:"not null"`
	BackfillPage       int    `gorm:"not null"` // next page to fetch, 1-based
	BackfillTotalPages int    `gorm:"not null"`
	BackfillRetryAt    *time.Time
	LastPolledAt       *time.Time
	LastError          string `gorm:"not null"`
}

// PlayerAlias maps a merged-away player ID to the surviving player (spec §4.2).
type PlayerAlias struct {
	OldID    string  `gorm:"primaryKey"`
	PlayerID string  `gorm:"not null;index"`
	Player   *Player `gorm:"constraint:OnDelete:CASCADE"`
}
```

Replace `All()`:

```go
// All returns every model, in migration order. db.Migrate creates sync_feeds
// by hand before AutoMigrate sees SyncFeed.
func All() []any {
	return []any{
		&Player{}, &PlayerPlatform{}, &SyncFeed{}, &PlayerAlias{}, &Leaderboard{}, &Score{},
		&User{}, &Session{}, &Setting{}, &SyncEvent{},
	}
}
```

- [ ] **Step 2: Regenerate gorm gen code**

Run: `make generate`
Expected: `internal/db/query/player_platforms.gen.go`, `sync_feeds.gen.go`, `player_aliases.gen.go` created; `go build ./...` ok.

- [ ] **Step 3: Write the v1 fixture `internal/db/testdata/v1.sql`**

This is an exact `.dump` of a v1 database (schema as created by the 2026-10-08 models), with timestamps in the format GORM writes. One statement per line, each ending with `;`.

```sql
-- SSArchiver v1 database, schema exactly as created by the 2026-10-08 models,
-- seeded with one row per case the platform migration must carry over:
-- a polled player with a finished backfill, a never-polled player, and a
-- disabled player with a running backfill, a retry time and an error;
-- scores in every replay state; a user, a session, a setting and an event.
CREATE TABLE `players` (`id` text,`name` text NOT NULL,`avatar_url` text NOT NULL,`country` text NOT NULL,`enabled` numeric NOT NULL,`added_at` datetime NOT NULL,`last_polled_at` datetime,`last_error` text NOT NULL,`backfill_state` text NOT NULL,`backfill_page` integer NOT NULL,`backfill_total_pages` integer NOT NULL,`backfill_retry_at` datetime,PRIMARY KEY (`id`));
INSERT INTO players VALUES('76561198038925092','Yewolf','https://cdn.scoresaber.com/avatars/a.jpg','FR',1,'2026-10-08T10:00:00Z','2026-10-10T09:00:00Z','','done',12,11,NULL);
INSERT INTO players VALUES('2169974796454690','Never polled','https://cdn.scoresaber.com/avatars/b.jpg','US',1,'2026-10-09T10:00:00Z',NULL,'','pending',1,0,NULL);
INSERT INTO players VALUES('111','Gone','https://cdn.scoresaber.com/avatars/c.jpg','DE',0,'2026-10-09T11:00:00Z','2026-10-09T12:00:00Z','player not found on ScoreSaber; tracking disabled','running',7,40,'2026-10-09T12:05:00Z');
CREATE TABLE `leaderboards` (`id` integer,`song_hash` text NOT NULL,`song_name` text NOT NULL,`song_sub_name` text NOT NULL,`song_author` text NOT NULL,`mapper` text NOT NULL,`difficulty` integer NOT NULL,`difficulty_raw` text NOT NULL,`game_mode` text NOT NULL,`cover_url` text NOT NULL,`status` text NOT NULL,`stars` real NOT NULL,`max_score` integer NOT NULL,PRIMARY KEY (`id`));
INSERT INTO leaderboards VALUES(1001,'4640065298E79DC3D61A15695AEB7FED95B42B30','Song A','','Art','Map',9,'_ExpertPlus_SoloStandard','SoloStandard','https://cdn.scoresaber.com/covers/a.png','RANKED',10.5,1000);
INSERT INTO leaderboards VALUES(1002,'4850C7BC85D89F832A96C6036347773E3858CED7','Song B','','Art','Map',7,'_Expert_SoloOneSaber','SoloOneSaber','https://cdn.scoresaber.com/covers/b.png','UNRANKED',0.0,900);
CREATE TABLE `scores` (`id` integer,`player_id` text NOT NULL,`leaderboard_id` integer NOT NULL,`rank` integer NOT NULL,`modified_score` integer NOT NULL,`unmodified_score` integer NOT NULL,`accuracy` real NOT NULL,`pp` real NOT NULL,`mods` text NOT NULL,`full_combo` numeric NOT NULL,`missed_notes` integer NOT NULL,`bad_cuts` integer NOT NULL,`max_combo` integer NOT NULL,`hmd` text NOT NULL,`personal_best` numeric NOT NULL,`set_at` datetime NOT NULL,`has_replay` numeric NOT NULL,`replay_state` text NOT NULL,`replay_size` integer NOT NULL,`replay_sha256` text NOT NULL,`archived_at` datetime,`attempts` integer NOT NULL,`next_attempt_at` datetime,`last_error` text NOT NULL,PRIMARY KEY (`id`),CONSTRAINT `fk_scores_player` FOREIGN KEY (`player_id`) REFERENCES `players`(`id`) ON DELETE CASCADE,CONSTRAINT `fk_scores_leaderboard` FOREIGN KEY (`leaderboard_id`) REFERENCES `leaderboards`(`id`) ON DELETE RESTRICT);
INSERT INTO scores VALUES(5001,'76561198038925092',1001,3,900,900,0.9,300.0,'',1,0,0,500,'Quest 3',1,'2026-10-09T10:00:00Z',1,'archived',2000000,'abc','2026-10-09T10:05:00Z',0,NULL,'');
INSERT INTO scores VALUES(5002,'76561198038925092',1001,9,800,800,0.8,250.0,'',0,2,1,200,'Quest 3',0,'2026-10-08T10:00:00Z',1,'pending',0,'',NULL,1,'2026-10-10T10:00:00Z','timeout');
INSERT INTO scores VALUES(5003,'2169974796454690',1002,1,850,850,0.85,0.0,'GN',1,0,0,400,'Index',1,'2026-10-07T10:00:00Z',0,'none',0,'',NULL,0,NULL,'');
INSERT INTO scores VALUES(5004,'111',1002,4,700,700,0.7,0.0,'',0,5,3,100,'Index',1,'2026-10-06T10:00:00Z',1,'gone',0,'',NULL,0,NULL,'replay no longer available');
CREATE TABLE `users` (`id` integer PRIMARY KEY AUTOINCREMENT,`username` text NOT NULL,`password_hash` text NOT NULL,`created_at` datetime NOT NULL);
INSERT INTO users VALUES(1,'admin','$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA','2026-10-08T09:00:00Z');
CREATE TABLE `sessions` (`token_hash` text,`user_id` integer NOT NULL,`expires_at` datetime NOT NULL,`created_at` datetime NOT NULL,PRIMARY KEY (`token_hash`),CONSTRAINT `fk_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE CASCADE);
INSERT INTO sessions VALUES('0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0',1,'2026-11-07T09:00:00Z','2026-10-08T09:00:00Z');
CREATE TABLE `settings` (`key` text,`value` text NOT NULL,PRIMARY KEY (`key`));
INSERT INTO settings VALUES('poll_interval','10m0s');
CREATE TABLE `sync_events` (`id` integer PRIMARY KEY AUTOINCREMENT,`at` datetime NOT NULL,`level` text NOT NULL,`kind` text NOT NULL,`player_id` text,`score_id` integer,`message` text NOT NULL);
INSERT INTO sync_events VALUES(1,'2026-10-10T09:00:00Z','info','replay','76561198038925092',5001,'archived replay (1.9 MB)');
CREATE INDEX `idx_players_backfill_state` ON `players`(`backfill_state`);
CREATE INDEX `idx_leaderboards_song_name` ON `leaderboards`(`song_name`);
CREATE INDEX `idx_leaderboards_song_hash` ON `leaderboards`(`song_hash`);
CREATE INDEX `idx_scores_state_set` ON `scores`(`replay_state`,`set_at` desc);
CREATE INDEX `idx_scores_leaderboard_id` ON `scores`(`leaderboard_id`);
CREATE INDEX `idx_scores_player_set` ON `scores`(`player_id`,`set_at` desc);
CREATE UNIQUE INDEX `idx_users_username` ON `users`(`username`);
CREATE INDEX `idx_sessions_expires_at` ON `sessions`(`expires_at`);
CREATE INDEX `idx_sessions_user_id` ON `sessions`(`user_id`);
CREATE INDEX `idx_sync_events_player_id` ON `sync_events`(`player_id`);
CREATE INDEX `idx_sync_events_kind` ON `sync_events`(`kind`);
CREATE INDEX `idx_sync_events_level` ON `sync_events`(`level`);
CREATE INDEX `idx_sync_events_at` ON `sync_events`(`at`);
```

- [ ] **Step 4: Write the failing migration tests**

`internal/db/export_test.go`:

```go
package db

// Exposed to db_test for the verification guard test.
type RowCounts = rowCounts

var VerifyMigration = verifyMigration
```

`internal/db/migrate_test.go`:

```go
package db_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// openFixture loads testdata/v1.sql into a fresh file database and returns
// it opened (not migrated) together with its path.
func openFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssarchiver.db")
	gdb, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	raw, err := os.ReadFile("testdata/v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if _, err := sqlDB.Exec(line); err != nil {
			t.Fatalf("fixture: %v\n%s", err, line)
		}
	}
	return gdb, path
}

func count(t *testing.T, gdb *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := gdb.Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrateLegacyFixture(t *testing.T) {
	gdb, path := openFixture(t)
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int64{"players": 3, "scores": 4, "leaderboards": 2, "users": 1, "sessions": 1, "settings": 1, "sync_events": 1, "player_platforms": 3, "sync_feeds": 3} {
		if got := count(t, gdb, table); got != want {
			t.Errorf("%s: %d rows, want %d", table, got, want)
		}
	}
	ctx := context.Background()
	q := query.Use(gdb)

	pp, err := q.PlayerPlatform.WithContext(ctx).Where(q.PlayerPlatform.PlayerID.Eq("111")).First()
	if err != nil {
		t.Fatal(err)
	}
	if pp.Platform != model.PlatformScoreSaber || pp.ExternalID != "111" || !pp.Enabled || pp.LastError != "" ||
		!pp.LinkedAt.Equal(mustTime(t, "2026-10-09T11:00:00Z")) {
		t.Errorf("identity = %+v", pp)
	}

	f, err := q.SyncFeed.WithContext(ctx).Where(q.SyncFeed.PlayerID.Eq("111")).First()
	if err != nil {
		t.Fatal(err)
	}
	if f.Platform != model.PlatformScoreSaber || f.Feed != model.KindScore || !f.Enabled || f.Access != model.AccessNA ||
		f.BackfillState != model.BackfillRunning || f.BackfillPage != 7 || f.BackfillTotalPages != 40 ||
		f.BackfillRetryAt == nil || !f.BackfillRetryAt.Equal(mustTime(t, "2026-10-09T12:05:00Z")) ||
		f.LastPolledAt == nil || !f.LastPolledAt.Equal(mustTime(t, "2026-10-09T12:00:00Z")) ||
		f.LastError != "player not found on ScoreSaber; tracking disabled" ||
		!f.StartedAt.Equal(mustTime(t, "2026-10-09T11:00:00Z")) {
		t.Errorf("feed = %+v", f)
	}
	never, err := q.SyncFeed.WithContext(ctx).Where(q.SyncFeed.PlayerID.Eq("2169974796454690")).First()
	if err != nil || never.LastPolledAt != nil || never.BackfillState != model.BackfillPending || never.BackfillPage != 1 {
		t.Errorf("never-polled feed = %+v %v", never, err)
	}

	lbs, err := q.Leaderboard.WithContext(ctx).Order(q.Leaderboard.ID).Find()
	if err != nil {
		t.Fatal(err)
	}
	if lbs[0].Platform != model.PlatformScoreSaber || lbs[0].ExternalID != "1001" ||
		lbs[0].MapKey != "4640065298e79dc3d61a15695aeb7fed95b42b30/Standard/9" ||
		lbs[1].MapKey != "4850c7bc85d89f832a96c6036347773e3858ced7/OneSaber/7" {
		t.Errorf("leaderboards = %+v %+v", lbs[0], lbs[1])
	}

	scores, err := q.Score.WithContext(ctx).Order(q.Score.ID).Find()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scores {
		if s.Platform != model.PlatformScoreSaber || s.Kind != model.KindScore || s.EndType != model.EndClear || s.ExternalID == "" || s.ReplayURL != nil {
			t.Errorf("score %d = platform %q kind %q end %q ext %q", s.ID, s.Platform, s.Kind, s.EndType, s.ExternalID)
		}
	}
	if scores[0].ExternalID != "5001" || scores[0].ReplayState != model.ReplayArchived || scores[0].ReplaySHA256 != "abc" {
		t.Errorf("archived score changed: %+v", scores[0])
	}

	for _, idx := range []string{"idx_player_platforms_external", "idx_leaderboards_external", "idx_leaderboards_map_key", "idx_scores_external", "idx_scores_player_kind_set", "idx_sync_events_platform"} {
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", idx).Scan(&n)
		if n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}

	backup := filepath.Join(filepath.Dir(path), db.BackupName)
	bdb, err := db.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(bdb) }()
	if got := count(t, bdb, "scores"); got != 4 {
		t.Errorf("backup has %d scores, want 4", got)
	}
	var hasPP int64
	bdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'player_platforms'").Scan(&hasPP)
	if hasPP != 0 {
		t.Error("backup must be taken before the schema changes")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	gdb, path := openFixture(t)
	for i := range 3 {
		if err := db.Migrate(gdb); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if got := count(t, gdb, "player_platforms"); got != 3 {
		t.Errorf("player_platforms = %d after reruns", got)
	}
	if got := count(t, gdb, "sync_feeds"); got != 3 {
		t.Errorf("sync_feeds = %d after reruns", got)
	}
	if got := count(t, gdb, "scores"); got != 4 {
		t.Errorf("scores = %d after reruns", got)
	}
	// The backup is the pre-upgrade state and is never overwritten.
	bdb, err := db.Open(filepath.Join(filepath.Dir(path), db.BackupName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(bdb) }()
	var hasPP int64
	bdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'player_platforms'").Scan(&hasPP)
	if hasPP != 0 {
		t.Error("backup was overwritten by a later run")
	}
}

func TestMigrateFreshDatabase(t *testing.T) {
	gdb := testutil.OpenDB(t)
	var ddl string
	gdb.Raw("SELECT sql FROM sqlite_master WHERE name = 'sync_feeds'").Scan(&ddl)
	if !strings.Contains(ddl, "REFERENCES `player_platforms`(`player_id`,`platform`) ON DELETE CASCADE") {
		t.Fatalf("sync_feeds must carry the composite FK, got %s", ddl)
	}
	gdb.Raw("SELECT sql FROM sqlite_master WHERE name = 'player_platforms'").Scan(&ddl)
	if strings.Contains(ddl, "sync_feeds") {
		t.Fatalf("player_platforms must not reference sync_feeds: %s", ddl)
	}
	ctx := context.Background()
	q := query.Use(gdb)
	now := time.Now().UTC()
	for _, id := range []string{"a1", "a2"} {
		if err := q.Player.WithContext(ctx).Create(&model.Player{ID: id, Name: id, Enabled: true, AddedAt: now, BackfillState: model.BackfillPending, BackfillPage: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{PlayerID: "a1", Platform: model.PlatformScoreSaber, ExternalID: "42", Enabled: true, LinkedAt: now}); err != nil {
		t.Fatal(err)
	}
	err := q.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{PlayerID: "a2", Platform: model.PlatformScoreSaber, ExternalID: "42", Enabled: true, LinkedAt: now})
	if !db.IsDuplicate(err) {
		t.Fatalf("one account on two players must violate the unique index, got %v", err)
	}
	if err := q.SyncFeed.WithContext(ctx).Create(&model.SyncFeed{PlayerID: "a1", Platform: model.PlatformScoreSaber, Feed: model.KindScore, Enabled: true, StartedAt: now, Access: model.AccessNA, BackfillState: model.BackfillPending, BackfillPage: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Player.WithContext(ctx).Where(q.Player.ID.Eq("a1")).Delete(); err != nil {
		t.Fatal(err)
	}
	if got := count(t, gdb, "sync_feeds"); got != 0 {
		t.Fatalf("deleting a player must cascade to its feeds, %d left", got)
	}
}

func TestVerifyMigrationRejectsCountChange(t *testing.T) {
	gdb := testutil.OpenDB(t)
	err := db.VerifyMigration(gdb, db.RowCounts{Players: 99})
	if err == nil || !strings.Contains(err.Error(), "row counts changed") {
		t.Fatalf("err = %v", err)
	}
	if err := db.VerifyMigration(gdb, db.RowCounts{}); err != nil {
		t.Fatalf("empty database must verify, got %v", err)
	}
}
```

- [ ] **Step 5: Run the tests to verify they fail**

Run: `go test ./internal/db/...`
Expected: FAIL — `undefined: db.BackupName`, `undefined: verifyMigration`.

- [ ] **Step 6: Implement the migration in `internal/db/db.go`**

Add imports `os`, `path/filepath` (keep the existing ones). Replace `Migrate` with:

```go
// BackupName is the copy of the database db.Migrate takes, next to it, before
// upgrading a v1 database (spec §7 step 0). It is never overwritten.
const BackupName = "ssarchiver.pre-platforms.db"

// syncFeedsDDL creates sync_feeds by hand: GORM cannot express its composite
// foreign key to player_platforms (it emits the constraint on the wrong table).
const syncFeedsDDL = "CREATE TABLE IF NOT EXISTS `sync_feeds` (" +
	"`player_id` text NOT NULL,`platform` text NOT NULL,`feed` text NOT NULL," +
	"`enabled` numeric NOT NULL,`started_at` datetime NOT NULL,`access` text NOT NULL," +
	"`access_checked_at` datetime,`remote_total` integer NOT NULL," +
	"`backfill_state` text NOT NULL,`backfill_page` integer NOT NULL,`backfill_total_pages` integer NOT NULL," +
	"`backfill_retry_at` datetime,`last_polled_at` datetime,`last_error` text NOT NULL," +
	"PRIMARY KEY (`player_id`,`platform`,`feed`)," +
	"CONSTRAINT `fk_sync_feeds_identity` FOREIGN KEY (`player_id`,`platform`) " +
	"REFERENCES `player_platforms`(`player_id`,`platform`) ON DELETE CASCADE)"

// indexDDL creates the indexes on new columns. They run after the legacy
// backfill so unique indexes never see the empty column defaults.
var indexDDL = []string{
	"CREATE UNIQUE INDEX IF NOT EXISTS `idx_player_platforms_external` ON `player_platforms`(`platform`,`external_id`)",
	"CREATE UNIQUE INDEX IF NOT EXISTS `idx_leaderboards_external` ON `leaderboards`(`platform`,`external_id`)",
	"CREATE INDEX IF NOT EXISTS `idx_leaderboards_map_key` ON `leaderboards`(`map_key`)",
	"CREATE UNIQUE INDEX IF NOT EXISTS `idx_scores_external` ON `scores`(`platform`,`kind`,`external_id`)",
	"CREATE INDEX IF NOT EXISTS `idx_scores_player_kind_set` ON `scores`(`player_id`,`kind`,`set_at` desc)",
	"CREATE INDEX IF NOT EXISTS `idx_sync_events_platform` ON `sync_events`(`platform`)",
}

// Migrate creates/updates the schema and upgrades v1 databases onto the
// platform model (spec §7).
func Migrate(gdb *gorm.DB) error {
	legacy, err := hasColumn(gdb, "players", "backfill_state")
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	var before rowCounts
	if legacy {
		if err := backup(gdb); err != nil {
			return fmt.Errorf("db: migrate: backup: %w", err)
		}
		if before, err = countRows(gdb); err != nil {
			return fmt.Errorf("db: migrate: %w", err)
		}
	}
	if err := gdb.AutoMigrate(&model.Player{}, &model.PlayerPlatform{}); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	if err := gdb.Exec(syncFeedsDDL).Error; err != nil {
		return fmt.Errorf("db: migrate: sync_feeds: %w", err)
	}
	if err := gdb.AutoMigrate(model.All()...); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		if legacy {
			if err := backfillLegacy(tx); err != nil {
				return err
			}
		}
		for _, ddl := range indexDDL {
			if err := tx.Exec(ddl).Error; err != nil {
				return fmt.Errorf("index: %w", err)
			}
		}
		if legacy {
			return verifyMigration(tx, before)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("db: migrate: %w (the database before this upgrade was saved as %s next to it)", err, BackupName)
	}
	return nil
}

func hasColumn(gdb *gorm.DB, table, column string) (bool, error) {
	var n int64
	err := gdb.Raw("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&n).Error
	return n > 0, err
}

// backup copies the database next to itself unless a backup already exists
// (the first one is the pre-upgrade state; reruns must not replace it).
func backup(gdb *gorm.DB) error {
	var file string
	if err := gdb.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file).Error; err != nil {
		return err
	}
	if file == "" {
		return nil // in-memory database
	}
	dst := filepath.Join(filepath.Dir(file), BackupName)
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return gdb.Exec("VACUUM INTO ?", dst).Error
}

// backfillLegacy moves v1 rows onto the platform model (spec §7 step 2.1).
// Every statement is guarded, so rerunning it changes nothing.
func backfillLegacy(tx *gorm.DB) error {
	ss := model.PlatformScoreSaber
	steps := []struct {
		sql  string
		args []any
	}{
		{"UPDATE leaderboards SET platform = ?, external_id = CAST(id AS TEXT) WHERE platform = ''", []any{ss}},
		{"UPDATE leaderboards SET map_key = lower(song_hash) || '/' || " +
			"CASE WHEN substr(game_mode, 1, 4) = 'Solo' THEN substr(game_mode, 5) ELSE game_mode END || " +
			"'/' || difficulty WHERE map_key = ''", nil},
		{"UPDATE scores SET platform = ?, kind = ?, end_type = ?, external_id = CAST(id AS TEXT) WHERE platform = ''",
			[]any{ss, model.KindScore, model.EndClear}},
		{"INSERT INTO player_platforms (player_id, platform, external_id, enabled, linked_at, last_error) " +
			"SELECT p.id, ?, p.id, 1, p.added_at, '' FROM players p " +
			"WHERE NOT EXISTS (SELECT 1 FROM player_platforms pp WHERE pp.player_id = p.id AND pp.platform = ?)",
			[]any{ss, ss}},
		{"INSERT INTO sync_feeds (player_id, platform, feed, enabled, started_at, access, remote_total, " +
			"backfill_state, backfill_page, backfill_total_pages, backfill_retry_at, last_polled_at, last_error) " +
			"SELECT p.id, ?, ?, 1, p.added_at, ?, 0, p.backfill_state, p.backfill_page, p.backfill_total_pages, " +
			"p.backfill_retry_at, p.last_polled_at, p.last_error FROM players p " +
			"WHERE NOT EXISTS (SELECT 1 FROM sync_feeds f WHERE f.player_id = p.id AND f.platform = ? AND f.feed = ?)",
			[]any{ss, model.KindScore, model.AccessNA, ss, model.KindScore}},
	}
	for _, s := range steps {
		if err := tx.Exec(s.sql, s.args...).Error; err != nil {
			return fmt.Errorf("legacy backfill: %w", err)
		}
	}
	return nil
}

type rowCounts struct{ Players, Scores, Leaderboards int64 }

func countRows(gdb *gorm.DB) (rowCounts, error) {
	var c rowCounts
	err := gdb.Raw("SELECT (SELECT COUNT(*) FROM players) AS players, (SELECT COUNT(*) FROM scores) AS scores, " +
		"(SELECT COUNT(*) FROM leaderboards) AS leaderboards").Scan(&c).Error
	return c, err
}

// verifyMigration fails the upgrade transaction unless nothing was lost
// (spec §7 step 2.4). foreign_key_check alone would not notice deleted rows.
func verifyMigration(tx *gorm.DB, before rowCounts) error {
	after, err := countRows(tx)
	if err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("row counts changed: before %+v, after %+v", before, after)
	}
	var orphans int64
	if err := tx.Raw("SELECT COUNT(*) FROM players p WHERE "+
		"NOT EXISTS (SELECT 1 FROM player_platforms pp WHERE pp.player_id = p.id) OR "+
		"NOT EXISTS (SELECT 1 FROM sync_feeds f WHERE f.player_id = p.id AND f.feed = ?)", model.KindScore).
		Scan(&orphans).Error; err != nil {
		return err
	}
	if orphans > 0 {
		return fmt.Errorf("%d players have no platform account or score feed", orphans)
	}
	var violations []map[string]any
	if err := tx.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("foreign key check failed: %v", violations)
	}
	return nil
}
```

- [ ] **Step 7: Run the migration tests**

Run: `go test -race ./internal/db/...`
Expected: `ok`. (`TestPragmas` and the existing cascade tests must still pass.)

- [ ] **Step 8: Write the failing service tests**

Append to `internal/service/players_test.go`:

```go
func TestAddPlayerCreatesAccountAndScoreFeed(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	p, err := svc.AddPlayer(ctx, "1001")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Identities(ctx, p.ID)
	if err != nil || len(ids) != 1 {
		t.Fatalf("identities = %v %v", ids, err)
	}
	if ids[0].Platform != model.PlatformScoreSaber || ids[0].ExternalID != "1001" || !ids[0].Enabled || !ids[0].LinkedAt.Equal(testutil.T0) {
		t.Fatalf("identity = %+v", ids[0])
	}
	feeds, err := svc.Feeds(ctx, p.ID)
	if err != nil || len(feeds) != 1 {
		t.Fatalf("feeds = %v %v", feeds, err)
	}
	f := feeds[0]
	if f.Feed != model.KindScore || !f.Enabled || f.Access != model.AccessNA || f.BackfillState != model.BackfillPending ||
		f.BackfillPage != 1 || f.LastPolledAt != nil || !f.StartedAt.Equal(testutil.T0) {
		t.Fatalf("feed = %+v", f)
	}
}
```

Append to `internal/service/scores_test.go`:

```go
func TestUpsertSetsPlatformColumns(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	upsert(t, svc, "1001", clk, []scoreItem{{id: 7, hasReplay: true}})
	sc, err := svc.GetScore(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Platform != model.PlatformScoreSaber || sc.Kind != model.KindScore || sc.EndType != model.EndClear || sc.ExternalID != "7" || sc.ReplayURL != nil {
		t.Fatalf("score = %+v", sc)
	}
	lb := sc.Leaderboard
	if lb.Platform != model.PlatformScoreSaber || lb.ExternalID != "1007" || lb.MapKey != "hash1007/Standard/9" {
		t.Fatalf("leaderboard = %+v", lb)
	}
}
```

(Add `"github.com/yyewolf/ssarchiver/internal/model"` to the imports of both files if missing.)

- [ ] **Step 9: Run them to verify they fail**

Run: `go test ./internal/service/ -run 'TestAddPlayerCreatesAccountAndScoreFeed|TestUpsertSetsPlatformColumns'`
Expected: FAIL — `svc.Identities undefined`; the upsert test fails on `sc.Platform`.

- [ ] **Step 10: Implement**

Create `internal/service/feeds.go`:

```go
package service

import (
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// newFeed is a fresh feed cursor: pending backfill from page 1, never polled.
func newFeed(playerID, platformName, kind string, now time.Time, access string) *model.SyncFeed {
	return &model.SyncFeed{
		PlayerID: playerID, Platform: platformName, Feed: kind, Enabled: true, StartedAt: now, Access: access,
		BackfillState: model.BackfillPending, BackfillPage: 1,
	}
}
```

In `internal/service/players.go`, replace the body of `AddPlayer` after `ResolvePlayer` with a transaction that also creates the account and its score feed (add imports `"github.com/yyewolf/ssarchiver/internal/db/query"`):

```go
func (s *Service) AddPlayer(ctx context.Context, input string) (*model.Player, error) {
	sp, err := s.ResolvePlayer(ctx, input)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	p := &model.Player{
		ID: sp.ID, Name: sp.Name, AvatarURL: sp.Avatar, Country: sp.Country,
		Enabled: true, AddedAt: now, BackfillState: model.BackfillPending, BackfillPage: 1,
	}
	err = s.q.Transaction(func(tx *query.Query) error {
		if err := tx.Player.WithContext(ctx).Create(p); err != nil {
			return err
		}
		if err := tx.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{
			PlayerID: p.ID, Platform: model.PlatformScoreSaber, ExternalID: sp.ID, Enabled: true, LinkedAt: now,
		}); err != nil {
			return err
		}
		return tx.SyncFeed.WithContext(ctx).Create(newFeed(p.ID, model.PlatformScoreSaber, model.KindScore, now, model.AccessNA))
	})
	if err != nil {
		if db.IsDuplicate(err) {
			return nil, ErrPlayerExists
		}
		return nil, fmt.Errorf("service: add player: %w", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: Ptr(p.ID), Message: "player added: " + p.Name})
	s.Wake()
	return p, nil
}

// Identities lists a player's linked platform accounts.
func (s *Service) Identities(ctx context.Context, playerID string) ([]*model.PlayerPlatform, error) {
	pp := s.q.PlayerPlatform
	out, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID)).Order(pp.Platform).Find()
	if err != nil {
		return nil, fmt.Errorf("service: identities: %w", err)
	}
	return out, nil
}

// Feeds lists a player's sync feeds.
func (s *Service) Feeds(ctx context.Context, playerID string) ([]*model.SyncFeed, error) {
	f := s.q.SyncFeed
	out, err := f.WithContext(ctx).Where(f.PlayerID.Eq(playerID)).Order(f.Platform, f.Feed).Find()
	if err != nil {
		return nil, fmt.Errorf("service: feeds: %w", err)
	}
	return out, nil
}
```

In `internal/service/scores.go` (add imports `"strconv"` and `"github.com/yyewolf/ssarchiver/internal/platform"`), extend the two converters:

```go
func leaderboardFrom(lb scoresaber.Leaderboard) *model.Leaderboard {
	return &model.Leaderboard{
		ID: lb.ID, SongHash: lb.Map.Hash, SongName: lb.Map.SongName, SongSubName: lb.Map.SongSubName,
		SongAuthor: lb.Map.SongAuthorName, Mapper: lb.Map.LevelAuthorName,
		Difficulty: lb.Difficulty.Difficulty, DifficultyRaw: lb.Difficulty.RawDifficulty, GameMode: lb.Difficulty.GameMode,
		CoverURL: lb.Map.CoverURL, Status: lb.Realm.LeaderboardStatus, Stars: lb.Realm.Stars, MaxScore: lb.MaxScore,
		Platform: model.PlatformScoreSaber, ExternalID: strconv.FormatInt(lb.ID, 10),
		MapKey: platform.MapKey(lb.Map.Hash, lb.Difficulty.GameMode, lb.Difficulty.Difficulty),
	}
}

func scoreFrom(playerID string, it scoresaber.ScoreItem) *model.Score {
	sc := it.Score
	state := model.ReplayNone
	if sc.HasReplay {
		state = model.ReplayPending
	}
	return &model.Score{
		ID: sc.ID, PlayerID: playerID, LeaderboardID: it.Leaderboard.ID, Rank: sc.Rank,
		ModifiedScore: sc.ModifiedScore, UnmodifiedScore: sc.UnmodifiedScore, Accuracy: sc.Accuracy, PP: sc.PP,
		Mods: strings.Join(sc.Mods, ","), FullCombo: sc.FullCombo, MissedNotes: sc.MissedNotes, BadCuts: sc.BadCuts,
		MaxCombo: sc.MaxCombo, HMD: sc.Device.HMD, PersonalBest: sc.PersonalBest, SetAt: sc.CreatedAt.UTC(),
		HasReplay: sc.HasReplay, ReplayState: state,
		Platform: model.PlatformScoreSaber, Kind: model.KindScore, EndType: model.EndClear, ExternalID: strconv.FormatInt(sc.ID, 10),
	}
}
```

- [ ] **Step 11: Run all tests, then lint**

Run: `go test -race ./... && make lint`
Expected: all `ok`, `0 issues.` If a pre-existing test creates `model.Player` rows directly (e.g. `internal/db/db_test.go` `seed`), it keeps compiling — the legacy fields still exist in this task.

- [ ] **Step 12: Commit**

```bash
git add internal/model internal/db internal/service
git commit -m "feat(db): add platform accounts, sync feeds and the v1 migration"
```

---
## Task 3: Feeds become the sync source of truth (service, worker, UI, API)

The worker stops reading/writing the six `players` sync columns and works on `sync_feeds` rows instead; every reader (web, API) switches to a `PlayerSummary` built from accounts and feeds. Still ScoreSaber-only, still the existing `archiver.Client`; account IDs equal player IDs for now, but the worker already calls the platform with `WorkFeed.ExternalID`. After this task nothing reads the legacy columns, so Task 4 can drop them.

**Behaviour change (intended, spec §4.3/§5.2):** a ScoreSaber 404 now disables the *account* (`player_platforms.enabled=false`, error on the account) instead of the player. "Enable" on the player re-enables its accounts and clears their errors (the per-account switch arrives in Plan 2).

**Files:**
- Modify: `internal/service/feeds.go` (grows), `internal/service/players.go`, `internal/service/replays.go`
- Delete: `internal/service/backfill.go` (its remaining helpers move to `players.go`/`feeds.go`)
- Modify: `internal/archiver/worker.go`, `internal/archiver/poll.go`, `internal/archiver/download.go`
- Modify: `internal/web/admin.go`, `internal/web/admin_sync.go`, `internal/web/public.go`, `internal/web/views/admin.templ`, `internal/web/views/player.templ`, `internal/web/views/sync.templ` (+ regenerated `*_templ.go`)
- Modify: `internal/api/dto.go`, `internal/api/players.go`
- Modify: `internal/testutil/service.go`
- Test: `internal/service/feeds_test.go` (new; replaces `internal/service/backfill_test.go`, which is deleted), `internal/service/replays_test.go`, `internal/service/players_test.go`, `internal/archiver/poll_test.go`, `internal/archiver/download_test.go`, `internal/web/admin_test.go`, `internal/api/api_test.go`

**Interfaces:**
- Consumes: Task 2 models and `newFeed`.
- Produces:
  - `service.BackfillRetryDelay` (unchanged value 5m, now in `feeds.go`)
  - `type service.FeedKey struct{ PlayerID, Platform, Kind string }` + `String()`
  - `type service.PlatformKind struct{ Platform, Kind string }`, `type service.Busy map[PlatformKind]bool`
  - `type service.WorkFeed struct{ model.SyncFeed; ExternalID, PlayerName string }` + `Key() FeedKey`
  - `(*Service).DueFeed(ctx, interval time.Duration, busy Busy) (*WorkFeed, error)`
  - `(*Service).NextPollAt(ctx, interval) (time.Time, bool, error)` (now over feeds)
  - `(*Service).NextBackfillFeed(ctx, last string, scores bool, busy Busy) (*WorkFeed, error)`
  - `(*Service).MarkFeedPolled(ctx, FeedKey) error`, `MarkFeedError(ctx, FeedKey, msg) error`, `SetFeedBackfill(ctx, FeedKey, state string, nextPage, totalPages int) error`, `DeferFeedBackfill(ctx, FeedKey, until time.Time, msg string) error`
  - `(*Service).MarkIdentityError(ctx, playerID, platformName, msg string, disable bool) error`
  - `(*Service).RequestPoll(ctx, playerID) error` (now clears every feed's `last_polled_at`)
  - `service.ReplayTier` values `TierNew`, `TierBackfill`, `TierBackfillOther`; `(*Service).NextReplay(ctx, tier, lastPlayer string, busy Busy) (*model.Score, error)`
  - `type service.Identity struct{ model.PlayerPlatform; Feeds []model.SyncFeed }` + `Feed(kind) model.SyncFeed`
  - `service.PlayerSummary` gains `Identities []Identity` and methods `Sync() model.SyncFeed`, `Error() string`
  - `(*Service).GetPlayerSummary(ctx, id) (PlayerSummary, error)`
  - `testutil.ScoreFeedKey(playerID) service.FeedKey`, `testutil.ScoreFeed(t, svc, playerID) model.SyncFeed`
  - `archiver.(*Worker).busy() service.Busy` (returns nil until Task 8)
  - API DTO types `api.Identity`, `api.Feed`; `api.Player.Identities`
- Removed: `DuePlayer`, `MarkPolled`, `MarkPlayerError`, `NextBackfillPlayer`, `SetBackfill`, `DeferBackfill`.

- [ ] **Step 1: Add the test helpers to `internal/testutil/service.go`**

```go
// ScoreFeedKey is the key of a ScoreSaber account's score feed.
func ScoreFeedKey(playerID string) service.FeedKey {
	return service.FeedKey{PlayerID: playerID, Platform: model.PlatformScoreSaber, Kind: model.KindScore}
}

// ScoreFeed returns a player's headline score feed (PlayerSummary.Sync).
func ScoreFeed(t testing.TB, svc *service.Service, playerID string) model.SyncFeed {
	t.Helper()
	sum, err := svc.GetPlayerSummary(context.Background(), playerID)
	if err != nil {
		t.Fatal(err)
	}
	return sum.Sync()
}
```

(Add imports `context` and `github.com/yyewolf/ssarchiver/internal/model`.)

- [ ] **Step 2: Write the failing service tests**

Delete `internal/service/backfill_test.go`. Create `internal/service/feeds_test.go`:

```go
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestDueFeed(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	f, err := svc.DueFeed(ctx, 10*time.Minute, nil)
	if err != nil || f == nil {
		t.Fatalf("never-polled feed must be due: %v %v", f, err)
	}
	if f.ExternalID != "1001" || f.PlayerName != "Alice" || f.Feed != model.KindScore || f.Platform != model.PlatformScoreSaber {
		t.Fatalf("work feed = %+v", f)
	}
	_ = svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey("1001"))
	clk.Advance(time.Minute)
	_ = svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey("1002"))
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, nil); f != nil {
		t.Fatalf("nothing should be due, got %+v", f.Key())
	}
	clk.Advance(10 * time.Minute)
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, nil); f == nil || f.PlayerID != "1001" {
		t.Fatalf("oldest poll first, got %+v", f)
	}
	busy := service.Busy{{Platform: model.PlatformScoreSaber, Kind: model.KindScore}: true}
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, busy); f != nil {
		t.Fatalf("busy platforms are skipped, got %+v", f.Key())
	}
	_ = svc.SetPlayerEnabled(ctx, "1001", false)
	if f, _ := svc.DueFeed(ctx, 10*time.Minute, nil); f == nil || f.PlayerID != "1002" {
		t.Fatalf("disabled players are never due, got %+v", f)
	}
	at, ok, err := svc.NextPollAt(ctx, 10*time.Minute)
	if err != nil || !ok || !at.Equal(testutil.T0.Add(11*time.Minute)) {
		t.Fatalf("NextPollAt = %v %v %v", at, ok, err)
	}
}

func TestMarkIdentityErrorDisablesOnlyTheAccount(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	if err := svc.MarkIdentityError(ctx, "1001", model.PlatformScoreSaber, "player not found on ScoreSaber", true); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.GetPlayerSummary(ctx, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Enabled || sum.Identities[0].Enabled || sum.Error() != "player not found on ScoreSaber" {
		t.Fatalf("summary = %+v", sum)
	}
	if f, _ := svc.DueFeed(ctx, time.Minute, nil); f != nil {
		t.Fatal("feeds of a disabled account must not be due")
	}
	if err := svc.SetPlayerEnabled(ctx, "1001", true); err != nil {
		t.Fatal(err)
	}
	sum, _ = svc.GetPlayerSummary(ctx, "1001")
	if !sum.Identities[0].Enabled || sum.Error() != "" {
		t.Fatalf("enabling the player must re-enable its accounts: %+v", sum.Identities[0])
	}
}

func TestUpdatePlayerProfileKeepsEmptyFields(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	_ = svc.UpdatePlayerProfile(ctx, "1001", scoresaber.Player{Name: "Alice2", Avatar: "a.jpg"})
	p, _ := svc.GetPlayer(ctx, "1001")
	if p.Name != "Alice2" || p.AvatarURL != "a.jpg" || p.Country != "FR" {
		t.Fatalf("profile update = %+v (empty fields must not overwrite)", p)
	}
}

func TestFeedErrorsClearOnPoll(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	k := testutil.ScoreFeedKey("1001")
	_ = svc.MarkFeedError(ctx, k, "502")
	if got := testutil.ScoreFeed(t, svc, "1001").LastError; got != "502" {
		t.Fatalf("last_error = %q", got)
	}
	_ = svc.MarkFeedPolled(ctx, k)
	if f := testutil.ScoreFeed(t, svc, "1001"); f.LastError != "" || f.LastPolledAt == nil {
		t.Fatalf("MarkFeedPolled must set last_polled_at and clear last_error: %+v", f)
	}
}

func TestNextBackfillFeed(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	k1, k2 := testutil.ScoreFeedKey("1001"), testutil.ScoreFeedKey("1002")
	if f, _ := svc.NextBackfillFeed(ctx, "", true, nil); f != nil {
		t.Fatal("feeds must be polled once before backfilling")
	}
	_ = svc.MarkFeedPolled(ctx, k1)
	_ = svc.MarkFeedPolled(ctx, k2)
	if f, _ := svc.NextBackfillFeed(ctx, "", true, nil); f == nil || f.PlayerID != "1001" {
		t.Fatalf("got %+v", f)
	}
	if f, _ := svc.NextBackfillFeed(ctx, k1.String(), true, nil); f == nil || f.PlayerID != "1002" {
		t.Fatalf("round robin: got %+v", f)
	}
	if f, _ := svc.NextBackfillFeed(ctx, "", false, nil); f != nil {
		t.Fatalf("no non-score feeds exist, got %+v", f.Key())
	}
	if err := svc.SetFeedBackfill(ctx, k2, model.BackfillDone, 85, 84); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.NextBackfillFeed(ctx, k1.String(), true, nil); f == nil || f.PlayerID != "1001" {
		t.Fatalf("done feeds are skipped: got %+v", f)
	}
	if err := svc.DeferFeedBackfill(ctx, k1, clk.Now().Add(5*time.Minute), "502"); err != nil {
		t.Fatal(err)
	}
	if f, _ := svc.NextBackfillFeed(ctx, "", true, nil); f != nil {
		t.Fatalf("deferred feed returned early: %+v", f.Key())
	}
	clk.Advance(5 * time.Minute)
	f, _ := svc.NextBackfillFeed(ctx, "", true, nil)
	if f == nil || f.PlayerID != "1001" || f.LastError != "502" {
		t.Fatalf("deferred feed after delay: %+v", f)
	}
	_ = svc.SetFeedBackfill(ctx, k1, model.BackfillRunning, 3, 84)
	got := testutil.ScoreFeed(t, svc, "1001")
	if got.BackfillPage != 3 || got.BackfillTotalPages != 84 || got.BackfillRetryAt != nil {
		t.Fatalf("SetFeedBackfill = %+v", got)
	}
	if err := svc.SetFeedBackfill(ctx, testutil.ScoreFeedKey("nobody"), model.BackfillDone, 1, 1); err == nil {
		t.Fatal("unknown feeds must report ErrNotFound")
	}
}

func TestPlayerSummary(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	_ = svc.SetFeedBackfill(ctx, testutil.ScoreFeedKey("1001"), model.BackfillRunning, 4, 9)
	sum, err := svc.GetPlayerSummary(ctx, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Identities) != 1 || sum.Identities[0].ExternalID != "1001" || len(sum.Identities[0].Feeds) != 1 {
		t.Fatalf("identities = %+v", sum.Identities)
	}
	if s := sum.Sync(); s.BackfillState != model.BackfillRunning || s.BackfillPage != 4 || s.BackfillTotalPages != 9 {
		t.Fatalf("Sync() = %+v", s)
	}
	list, err := svc.ListPlayers(ctx, true)
	if err != nil || len(list) != 1 || list[0].Sync().BackfillPage != 4 {
		t.Fatalf("ListPlayers = %+v %v", list, err)
	}
	if _, err := svc.GetPlayerSummary(ctx, "nobody"); err == nil {
		t.Fatal("unknown player must be ErrNotFound")
	}
}
```

In `internal/service/replays_test.go`, change the helper call to the new signature and add a busy test:

```go
	s, err := svc.NextReplay(context.Background(), tier, last, nil)
```

```go
func TestNextReplaySkipsBusyPlatform(t *testing.T) {
	svc, _ := seedQueue(t)
	ctx := context.Background()
	busy := service.Busy{{Platform: model.PlatformScoreSaber, Kind: model.KindScore}: true}
	if s, err := svc.NextReplay(ctx, service.TierNew, "", busy); err != nil || s != nil {
		t.Fatalf("busy platform must be skipped, got %+v %v", s, err)
	}
	if err := svc.MarkIdentityError(ctx, "1001", model.PlatformScoreSaber, "gone", true); err != nil {
		t.Fatal(err)
	}
	if got := nextID(t, svc, service.TierNew, ""); got != 4 {
		t.Fatalf("replays of a disabled account must be skipped, got %d", got)
	}
}
```

In `internal/service/players_test.go`, `TestRequestPollWakesAndClearsLastPolled`: replace `svc.MarkPolled(ctx, "1001")` with `svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey("1001"))` and the `GetPlayer`/`p.LastPolledAt` check with:

```go
	if f := testutil.ScoreFeed(t, svc, "1001"); f.LastPolledAt != nil {
		t.Fatal("RequestPoll must clear last_polled_at")
	}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/service/...`
Expected: FAIL to compile — `svc.DueFeed undefined`, `svc.GetPlayerSummary undefined`, `service.Busy undefined`.

- [ ] **Step 4: Implement the feed functions**

Replace `internal/service/feeds.go` with:

```go
package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// BackfillRetryDelay is how long a feed waits after a failed backfill page.
const BackfillRetryDelay = 5 * time.Minute

// FeedKey names one feed of one platform account.
type FeedKey struct{ PlayerID, Platform, Kind string }

func (k FeedKey) String() string { return k.PlayerID + "|" + k.Platform + "|" + k.Kind }

// PlatformKind is a (platform, row kind) pair.
type PlatformKind struct{ Platform, Kind string }

// Busy lists the (platform, kind) pairs the worker must skip for now because
// the limiter they would use is not ready (spec §5.1). nil skips nothing.
type Busy map[PlatformKind]bool

func (b Busy) has(platformName, kind string) bool { return b[PlatformKind{platformName, kind}] }

// WorkFeed is a feed the worker can act on, with what it needs to call the platform.
type WorkFeed struct {
	model.SyncFeed
	ExternalID string // the account ID on the platform — never the player ID
	PlayerName string
}

func (f *WorkFeed) Key() FeedKey { return FeedKey{f.PlayerID, f.Platform, f.Feed} }

// newFeed is a fresh feed cursor: pending backfill from page 1, never polled.
func newFeed(playerID, platformName, kind string, now time.Time, access string) *model.SyncFeed {
	return &model.SyncFeed{
		PlayerID: playerID, Platform: platformName, Feed: kind, Enabled: true, StartedAt: now, Access: access,
		BackfillState: model.BackfillPending, BackfillPage: 1,
	}
}

// workFeedSQL selects the enabled, accessible feeds of enabled accounts of
// enabled players (spec §5.1).
const workFeedSQL = "SELECT f.*, pp.external_id AS external_id, p.name AS player_name FROM sync_feeds f " +
	"JOIN player_platforms pp ON pp.player_id = f.player_id AND pp.platform = f.platform " +
	"JOIN players p ON p.id = f.player_id " +
	"WHERE f.enabled AND pp.enabled AND p.enabled AND f.access IN (?, ?)"

func (s *Service) workFeeds(ctx context.Context, clause string, args ...any) ([]*WorkFeed, error) {
	var out []*WorkFeed
	all := append([]any{model.AccessNA, model.AccessPublic}, args...)
	if err := s.db.WithContext(ctx).Raw(workFeedSQL+clause, all...).Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("service: load feeds: %w", err)
	}
	return out, nil
}

// DueFeed returns the feed whose poll is most overdue — score feeds before
// other kinds — skipping busy ones; nil when nothing is due.
func (s *Service) DueFeed(ctx context.Context, interval time.Duration, busy Busy) (*WorkFeed, error) {
	feeds, err := s.workFeeds(ctx,
		" AND (f.last_polled_at IS NULL OR f.last_polled_at <= ?) ORDER BY f.last_polled_at, f.player_id, f.platform, f.feed",
		s.Now().Add(-interval))
	if err != nil {
		return nil, err
	}
	var other *WorkFeed
	for _, f := range feeds {
		if busy.has(f.Platform, f.Feed) {
			continue
		}
		if f.Feed == model.KindScore {
			return f, nil
		}
		if other == nil {
			other = f
		}
	}
	return other, nil
}

// NextPollAt is when the next feed becomes due (ok=false: nothing to poll).
func (s *Service) NextPollAt(ctx context.Context, interval time.Duration) (time.Time, bool, error) {
	feeds, err := s.workFeeds(ctx, " ORDER BY f.last_polled_at LIMIT 1") // SQLite sorts NULL first
	if err != nil || len(feeds) == 0 {
		return time.Time{}, false, err
	}
	if feeds[0].LastPolledAt == nil {
		return s.Now(), true, nil
	}
	return feeds[0].LastPolledAt.Add(interval), true, nil
}

// NextBackfillFeed picks, round robin after last (a FeedKey string), an
// already-polled feed whose backfill is not done and not deferred. scores
// selects score feeds (tier 4); otherwise the other kinds (tier 6).
func (s *Service) NextBackfillFeed(ctx context.Context, last string, scores bool, busy Busy) (*WorkFeed, error) {
	op := "="
	if !scores {
		op = "<>"
	}
	feeds, err := s.workFeeds(ctx,
		" AND f.feed "+op+" ? AND f.backfill_state <> ? AND f.last_polled_at IS NOT NULL"+
			" AND (f.backfill_retry_at IS NULL OR f.backfill_retry_at <= ?)",
		model.KindScore, model.BackfillDone, s.Now())
	if err != nil {
		return nil, err
	}
	byKey := map[string]*WorkFeed{}
	var keys []string
	for _, f := range feeds {
		if busy.has(f.Platform, f.Feed) {
			continue
		}
		k := f.Key().String()
		byKey[k] = f
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	slices.Sort(keys)
	return byKey[pickAfter(keys, last)], nil
}

func (s *Service) updateFeed(ctx context.Context, k FeedKey, upd map[string]any) error {
	f := s.q.SyncFeed
	info, err := f.WithContext(ctx).Where(f.PlayerID.Eq(k.PlayerID), f.Platform.Eq(k.Platform), f.Feed.Eq(k.Kind)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: update feed %s: %w", k, err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: feed %s", ErrNotFound, k)
	}
	return nil
}

// MarkFeedPolled records a successful poll and clears the feed's error.
func (s *Service) MarkFeedPolled(ctx context.Context, k FeedKey) error {
	return s.updateFeed(ctx, k, map[string]any{"last_polled_at": s.Now(), "last_error": ""})
}

func (s *Service) MarkFeedError(ctx context.Context, k FeedKey, msg string) error {
	return s.updateFeed(ctx, k, map[string]any{"last_error": msg})
}

func (s *Service) SetFeedBackfill(ctx context.Context, k FeedKey, state string, nextPage, totalPages int) error {
	return s.updateFeed(ctx, k, map[string]any{
		"backfill_state": state, "backfill_page": nextPage, "backfill_total_pages": totalPages, "backfill_retry_at": nil,
	})
}

func (s *Service) DeferFeedBackfill(ctx context.Context, k FeedKey, until time.Time, msg string) error {
	return s.updateFeed(ctx, k, map[string]any{"backfill_retry_at": until, "last_error": msg})
}

// MarkIdentityError records an account-level error; disable stops every feed
// of that account (the platform reports the player gone). The player and its
// other accounts keep running.
func (s *Service) MarkIdentityError(ctx context.Context, playerID, platformName, msg string, disable bool) error {
	pp := s.q.PlayerPlatform
	upd := map[string]any{"last_error": msg}
	if disable {
		upd["enabled"] = false
	}
	info, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(playerID), pp.Platform.Eq(platformName)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: account error: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: %s account of player %s", ErrNotFound, platformName, playerID)
	}
	return nil
}

// RequestPoll makes every feed of a player due now.
func (s *Service) RequestPoll(ctx context.Context, id string) error {
	if _, err := s.GetPlayer(ctx, id); err != nil {
		return err
	}
	f := s.q.SyncFeed
	if _, err := f.WithContext(ctx).Where(f.PlayerID.Eq(id)).Update(f.LastPolledAt, nil); err != nil {
		return fmt.Errorf("service: request poll: %w", err)
	}
	s.Wake()
	return nil
}
```

- [ ] **Step 5: Move the player helpers and add the read models**

Delete `internal/service/backfill.go`. In `internal/service/players.go`:

1. Remove the old `RequestPoll` (now in `feeds.go`).
2. Add (moved from `backfill.go`, unchanged) `updatePlayer` and `UpdatePlayerProfile`.
3. Replace `PlayerSummary` and add the read models (imports `cmp`, `slices`):

```go
// Identity is one linked platform account with its feeds.
type Identity struct {
	model.PlayerPlatform
	Feeds []model.SyncFeed
}

// Feed returns the account's feed of the given kind (zero value when missing).
func (i Identity) Feed(kind string) model.SyncFeed {
	for _, f := range i.Feeds {
		if f.Feed == kind {
			return f
		}
	}
	return model.SyncFeed{}
}

type PlayerSummary struct {
	model.Player
	Counts     Counts
	Identities []Identity // display order: the primary account first (spec §4.2)
}

// Sync is the primary account's score feed: the player's headline sync state.
func (p PlayerSummary) Sync() model.SyncFeed {
	if len(p.Identities) == 0 {
		return model.SyncFeed{}
	}
	return p.Identities[0].Feed(model.KindScore)
}

// Error is the first account or feed error, for compact views.
func (p PlayerSummary) Error() string {
	for _, id := range p.Identities {
		if id.LastError != "" {
			return id.LastError
		}
		for _, f := range id.Feeds {
			if f.LastError != "" {
				return f.LastError
			}
		}
	}
	return ""
}

// platformOrder ranks platforms for display: the legacy platform first.
// Task 6 switches it to the registry's priorities.
func (s *Service) platformOrder(name string) int {
	if name == model.PlatformScoreSaber {
		return 0
	}
	return 1
}

// identitiesBy loads accounts with their feeds, for one player or all.
func (s *Service) identitiesBy(ctx context.Context, playerID string) (map[string][]Identity, error) {
	pp, f := s.q.PlayerPlatform, s.q.SyncFeed
	ido, fdo := pp.WithContext(ctx), f.WithContext(ctx)
	if playerID != "" {
		ido, fdo = ido.Where(pp.PlayerID.Eq(playerID)), fdo.Where(f.PlayerID.Eq(playerID))
	}
	ids, err := ido.Find()
	if err != nil {
		return nil, fmt.Errorf("service: load accounts: %w", err)
	}
	feeds, err := fdo.Order(f.Feed).Find()
	if err != nil {
		return nil, fmt.Errorf("service: load feeds: %w", err)
	}
	feedsOf := map[[2]string][]model.SyncFeed{}
	for _, fd := range feeds {
		k := [2]string{fd.PlayerID, fd.Platform}
		feedsOf[k] = append(feedsOf[k], *fd)
	}
	out := map[string][]Identity{}
	for _, id := range ids {
		out[id.PlayerID] = append(out[id.PlayerID], Identity{PlayerPlatform: *id, Feeds: feedsOf[[2]string{id.PlayerID, id.Platform}]})
	}
	for pid := range out {
		slices.SortFunc(out[pid], func(a, b Identity) int {
			return cmp.Or(cmp.Compare(s.platformOrder(a.Platform), s.platformOrder(b.Platform)), cmp.Compare(a.Platform, b.Platform))
		})
	}
	return out, nil
}

// GetPlayerSummary loads one player with counts and accounts.
func (s *Service) GetPlayerSummary(ctx context.Context, id string) (PlayerSummary, error) {
	p, err := s.GetPlayer(ctx, id)
	if err != nil {
		return PlayerSummary{}, err
	}
	counts, err := s.countsBy(ctx, id)
	if err != nil {
		return PlayerSummary{}, err
	}
	ids, err := s.identitiesBy(ctx, id)
	if err != nil {
		return PlayerSummary{}, err
	}
	return PlayerSummary{Player: *p, Counts: counts[id], Identities: ids[id]}, nil
}
```

4. In `ListPlayers`, load accounts once and attach them:

```go
	ids, err := s.identitiesBy(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]PlayerSummary, 0, len(players))
	for _, p := range players {
		out = append(out, PlayerSummary{Player: *p, Counts: counts[p.ID], Identities: ids[p.ID]})
	}
	return out, nil
```

5. In `SetPlayerEnabled`, when `enabled` is true, re-enable the player's accounts before waking:

```go
	if enabled {
		pp := s.q.PlayerPlatform
		if _, err := pp.WithContext(ctx).Where(pp.PlayerID.Eq(id)).Updates(map[string]any{"enabled": true, "last_error": ""}); err != nil {
			return fmt.Errorf("service: enable accounts: %w", err)
		}
		s.Wake()
	}
```

- [ ] **Step 6: Select replays through feeds**

In `internal/service/replays.go` replace the tier constants, `replayCandidates` and `NextReplay`:

```go
const (
	TierNew           ReplayTier = iota // plays set at or after their feed started
	TierBackfill                        // older scores
	TierBackfillOther                   // older plays of other kinds (attempts)
)

func (s *Service) replayCandidates(ctx context.Context, tier ReplayTier, busy Busy) query.IScoreDo {
	q, p, pp, f := s.q.Score, s.q.Player, s.q.PlayerPlatform, s.q.SyncFeed
	now := s.Now()
	do := q.WithContext(ctx).
		Join(p, p.ID.EqCol(q.PlayerID)).
		Join(pp, pp.PlayerID.EqCol(q.PlayerID), pp.Platform.EqCol(q.Platform)).
		Join(f, f.PlayerID.EqCol(q.PlayerID), f.Platform.EqCol(q.Platform), f.Feed.EqCol(q.Kind)).
		Where(q.ReplayState.Eq(model.ReplayPending), p.Enabled.Is(true), pp.Enabled.Is(true), f.Enabled.Is(true),
			f.Access.In(model.AccessNA, model.AccessPublic)).
		Where(q.WithContext(ctx).Where(q.NextAttemptAt.IsNull()).Or(q.NextAttemptAt.Lte(now)))
	for pk := range busy {
		do = do.Not(q.Platform.Eq(pk.Platform), q.Kind.Eq(pk.Kind))
	}
	switch tier {
	case TierNew:
		return do.Where(q.SetAt.GteCol(f.StartedAt))
	case TierBackfill:
		return do.Where(q.SetAt.LtCol(f.StartedAt), q.Kind.Eq(model.KindScore))
	default:
		return do.Where(q.SetAt.LtCol(f.StartedAt), q.Kind.Neq(model.KindScore))
	}
}

// NextReplay returns the newest pending replay of the next player (round
// robin after lastPlayer) in the given tier, skipping busy platforms; nil
// when there is none.
func (s *Service) NextReplay(ctx context.Context, tier ReplayTier, lastPlayer string, busy Busy) (*model.Score, error) {
	q := s.q.Score
	var ids []string
	if err := s.replayCandidates(ctx, tier, busy).Distinct(q.PlayerID).Order(q.PlayerID).Pluck(q.PlayerID, &ids); err != nil {
		return nil, fmt.Errorf("service: replay players: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	pick := pickAfter(ids, lastPlayer)
	sc, err := s.replayCandidates(ctx, tier, busy).Select(q.ALL).Preload(q.Leaderboard, q.Player).
		Where(q.PlayerID.Eq(pick)).Order(q.SetAt.Desc()).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: next replay: %w", err)
	}
	return sc, nil
}
```

(`range busy` iterates a nil map fine. If gen's `Not` with two conditions does not render `NOT (a AND b)`, use `do.Where(q.WithContext(ctx).Not(q.Platform.Eq(pk.Platform), q.Kind.Eq(pk.Kind)))` and log the deviation — `TestNextReplaySkipsBusyPlatform` pins the behaviour.)

- [ ] **Step 7: Run the service tests**

Run: `go test -race ./internal/service/...`
Expected: `ok`.

- [ ] **Step 8: Switch the worker to feeds**

In `internal/archiver/worker.go`: rename the field `lastBackfillPlayer` → `lastBackfillFeed string`, and replace the body of `Step` after `w.maybePrune(ctx)` with:

```go
	busy := w.busy()
	wf, err := w.svc.DueFeed(ctx, st.PollInterval, busy)
	if err != nil {
		return false, err
	}
	if wf != nil {
		return true, w.poll(ctx, wf)
	}
	if did, err := w.nextReplay(ctx, service.TierNew, busy); did || err != nil {
		return did, err
	}
	if did, err := w.nextBackfill(ctx, true, busy); did || err != nil {
		return did, err
	}
	if did, err := w.nextReplay(ctx, service.TierBackfill, busy); did || err != nil {
		return did, err
	}
	if did, err := w.nextBackfill(ctx, false, busy); did || err != nil {
		return did, err
	}
	return w.nextReplay(ctx, service.TierBackfillOther, busy)
}

// busy lists the (platform, kind) pairs whose limiter is not ready (Task 8).
func (w *Worker) busy() service.Busy { return nil }

func (w *Worker) nextReplay(ctx context.Context, tier service.ReplayTier, busy service.Busy) (bool, error) {
	sc, err := w.svc.NextReplay(ctx, tier, w.lastReplayPlayer, busy)
	if err != nil || sc == nil {
		return false, err
	}
	w.lastReplayPlayer = sc.PlayerID
	return true, w.download(ctx, sc)
}

func (w *Worker) nextBackfill(ctx context.Context, scores bool, busy service.Busy) (bool, error) {
	wf, err := w.svc.NextBackfillFeed(ctx, w.lastBackfillFeed, scores, busy)
	if err != nil || wf == nil {
		return false, err
	}
	w.lastBackfillFeed = wf.Key().String()
	return true, w.backfill(ctx, wf)
```

(Keep the deferred `recover`, the settings/paused check and `maybePrune` at the top of `Step` as they are.)

Replace `internal/archiver/poll.go` with:

```go
package archiver

import (
	"context"
	"errors"
	"fmt"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
)

// feedEvent builds a sync event about one feed.
func feedEvent(wf *service.WorkFeed, level, kind, msg string) model.SyncEvent {
	return model.SyncEvent{
		Level: level, Kind: kind, PlayerID: service.Ptr(wf.PlayerID),
		Platform: service.Ptr(wf.Platform), Feed: service.Ptr(wf.Feed), Message: msg,
	}
}

func (w *Worker) poll(ctx context.Context, wf *service.WorkFeed) error {
	w.setStatus(StateRunning, "Polling "+wf.PlayerName)
	k := wf.Key()
	var newScores, newReplays, pagesRead, totalPages int
	reachedEnd := false
	for page := 1; page <= MaxPollPages; page++ {
		sp, err := w.client.Scores(ctx, wf.ExternalID, page)
		if err != nil {
			return w.clientError(ctx, wf, err, true)
		}
		pagesRead, totalPages = page, sp.Metadata.TotalPages
		if page == 1 && len(sp.Data) > 0 {
			if err := w.svc.UpdatePlayerProfile(ctx, wf.PlayerID, sp.Data[0].Score.Player); err != nil {
				return err
			}
		}
		res, err := w.svc.UpsertScores(ctx, wf.PlayerID, sp.Data)
		if err != nil {
			return err
		}
		newScores += res.New
		newReplays += res.NewReplays
		if res.Known > 0 || len(sp.Data) == 0 || page >= sp.Metadata.TotalPages {
			reachedEnd = true
			break
		}
	}
	switch {
	case wf.BackfillState == model.BackfillPending && wf.BackfillPage <= 1:
		if reachedEnd {
			if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillDone, pagesRead+1, totalPages); err != nil {
				return err
			}
		} else if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
	case !reachedEnd && wf.BackfillState == model.BackfillDone:
		if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindBackfill, fmt.Sprintf(
			"more than %d pages of new scores since the last poll; resuming backfill from page %d", MaxPollPages, MaxPollPages+1)))
	}
	if err := w.svc.MarkFeedPolled(ctx, k); err != nil {
		return err
	}
	if newScores > 0 {
		w.svc.Log(ctx, feedEvent(wf, model.LevelInfo, model.KindScores, fmt.Sprintf("%d new scores, %d with replays", newScores, newReplays)))
	}
	return nil
}

func (w *Worker) backfill(ctx context.Context, wf *service.WorkFeed) error {
	page := max(wf.BackfillPage, 1)
	w.setStatus(StateRunning, fmt.Sprintf("Backfilling %s · page %d", wf.PlayerName, page))
	sp, err := w.client.Scores(ctx, wf.ExternalID, page)
	if err != nil {
		return w.clientError(ctx, wf, err, false)
	}
	if _, err := w.svc.UpsertScores(ctx, wf.PlayerID, sp.Data); err != nil {
		return err
	}
	k := wf.Key()
	total := sp.Metadata.TotalPages
	if len(sp.Data) == 0 || page >= total {
		if err := w.svc.SetFeedBackfill(ctx, k, model.BackfillDone, page+1, total); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelInfo, model.KindBackfill, fmt.Sprintf("backfill listing complete (%d pages)", total)))
		return nil
	}
	return w.svc.SetFeedBackfill(ctx, k, model.BackfillRunning, page+1, total)
}

// clientError classifies a platform error during a poll (polling=true) or a backfill page.
func (w *Worker) clientError(ctx context.Context, wf *service.WorkFeed, err error, polling bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	k := wf.Key()
	switch {
	case errors.Is(err, scoresaber.ErrRateLimited):
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindRateLimit, "rate limited by ScoreSaber; waiting for the limit to reset"))
		return nil
	case errors.Is(err, scoresaber.ErrNotFound):
		const msg = "player not found on ScoreSaber; tracking of this account disabled"
		if err := w.svc.MarkIdentityError(ctx, wf.PlayerID, wf.Platform, msg, true); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelError, model.KindPoll, msg))
		return nil
	}
	msg := err.Error()
	if polling {
		if err := w.svc.MarkFeedPolled(ctx, k); err != nil {
			return err
		}
		if err := w.svc.MarkFeedError(ctx, k, msg); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelError, model.KindPoll, "poll failed: "+msg))
		return nil
	}
	if err := w.svc.DeferFeedBackfill(ctx, k, w.svc.Now().Add(service.BackfillRetryDelay), msg); err != nil {
		return err
	}
	w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindBackfill, "backfill page failed, retrying in 5m: "+msg))
	return nil
}
```

In `internal/archiver/download.go`, make replay events carry the platform and feed — change the `ev` closure to:

```go
	ev := func(level, msg string) {
		w.svc.Log(ctx, model.SyncEvent{
			Level: level, Kind: model.KindReplay, PlayerID: service.Ptr(sc.PlayerID), ScoreID: service.Ptr(sc.ID),
			Platform: service.Ptr(sc.Platform), Feed: service.Ptr(sc.Kind), Message: msg,
		})
	}
```

- [ ] **Step 9: Port the archiver tests**

In `internal/archiver/poll_test.go` and `download_test.go` apply these mechanical replacements (the account ID still equals the player ID in this task):

| Old | New |
|---|---|
| `p, _ := e.svc.GetPlayer(ctx, "1001")` followed by `p.BackfillState` / `p.BackfillPage` / `p.BackfillTotalPages` / `p.LastPolledAt` / `p.LastError` / `p.BackfillRetryAt` | `f := testutil.ScoreFeed(t, e.svc, "1001")` and the same field on `f` |
| `e.svc.SetBackfill(ctx, X, a, b, c)` | `e.svc.SetFeedBackfill(ctx, testutil.ScoreFeedKey(X), a, b, c)` |
| `e.svc.MarkPolled(ctx, X)` | `e.svc.MarkFeedPolled(ctx, testutil.ScoreFeedKey(X))` |

Replace `TestPollPlayerNotFoundDisables` with:

```go
func TestPollPlayerNotFoundDisablesTheAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scoresErr["1001"] = fmt.Errorf("%w: gone", scoresaber.ErrNotFound)
	e.step(t)
	sum, err := e.svc.GetPlayerSummary(ctx, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Enabled || sum.Identities[0].Enabled || !strings.Contains(sum.Error(), "not found") {
		t.Fatalf("summary = %+v", sum)
	}
	if e.step(t) {
		t.Fatal("a disabled account must not be polled again")
	}
}
```

In `TestPollServerErrorRetriesNextInterval` the final check becomes:

```go
	f := testutil.ScoreFeed(t, e.svc, "1001")
	if f.LastPolledAt == nil || !strings.Contains(f.LastError, "502") {
		t.Fatalf("feed = %+v", f)
	}
```

Add a test pinning that events carry the feed:

```go
func TestPollEventsCarryPlatformAndFeed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	e.step(t)
	evs, _, _ := e.svc.ListEvents(ctx, service.EventFilter{Kind: model.KindScores})
	if len(evs) != 1 || evs[0].Platform == nil || *evs[0].Platform != model.PlatformScoreSaber || evs[0].Feed == nil || *evs[0].Feed != model.KindScore {
		t.Fatalf("events = %+v", evs)
	}
}
```

- [ ] **Step 10: Run the worker tests**

Run: `go test -race ./internal/archiver/...`
Expected: `ok`. `TestStepPriority` must pass unchanged (tier order is the same for score-only data).

- [ ] **Step 11: Switch the web UI to summaries**

`internal/web/admin.go` — replace `playerSummary`:

```go
func (h *Handler) playerSummary(r *http.Request, id string) (service.PlayerSummary, error) {
	return h.svc.GetPlayerSummary(r.Context(), id)
}
```

`internal/web/public.go` — in `player`, load the summary instead of `GetPlayer` + `PlayerCounts`:

```go
	sum, err := h.svc.GetPlayerSummary(ctx, id)
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	pl := &sum.Player
```

…then build the view with `views.PlayerView{Player: pl, Sync: sum.Sync(), Counts: sum.Counts, Scores: list, Filter: f, Now: h.svc.Now()}`, delete the `PlayerCounts` call and use `sum.Counts.Archived` in the OpenGraph description.

`internal/web/admin_sync.go` — in `syncView` replace the next-poll computation:

```go
		next := now
		if s := p.Sync(); s.LastPolledAt != nil {
			next = s.LastPolledAt.Add(st.PollInterval)
		}
```

`internal/web/views/player.templ` — add `Sync model.SyncFeed` to `PlayerView` (after `Player`) and replace `v.Player.BackfillState`, `v.Player.BackfillPage`, `v.Player.BackfillTotalPages` with `v.Sync.BackfillState`, `v.Sync.BackfillPage`, `v.Sync.BackfillTotalPages`.

`internal/web/views/admin.templ` — replace the two helpers and their call sites:

```go
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
```

Call sites: `{ backfillLabel(pl.Sync()) }`, `{ lastPoll(pl.Sync(), now) }`, and the error line becomes:

```templ
			if pl.Error() != "" {
				<p class="mt-1 max-w-56 truncate text-xs text-destructive" title={ pl.Error() }>{ pl.Error() }</p>
			}
```

`internal/web/views/sync.templ` — `{ backfillLabel(q.Player.Sync()) }`.

Run: `make generate` (regenerates `*_templ.go`).

- [ ] **Step 12: Switch the API to summaries and add `identities[]`**

`internal/api/dto.go` — add the DTOs and mark the old fields deprecated:

```go
type Feed struct {
	Kind         string     `json:"kind" enum:"score,attempt"`
	Enabled      bool       `json:"enabled"`
	Access       string     `json:"access" enum:"n/a,unknown,public,private"`
	StartedAt    time.Time  `json:"started_at"`
	LastPolledAt *time.Time `json:"last_polled_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Backfill     Backfill   `json:"backfill"`
}

type Identity struct {
	Platform  string    `json:"platform" example:"scoresaber"`
	ID        string    `json:"id" doc:"The player's ID on that platform" example:"76561198038925092"`
	Enabled   bool      `json:"enabled"`
	LinkedAt  time.Time `json:"linked_at"`
	LastError string    `json:"last_error,omitempty"`
	Feeds     []Feed    `json:"feeds"`
}
```

In `Player`, change the three sync fields and add `Identities`:

```go
	LastPolledAt *time.Time   `json:"last_polled_at,omitempty" deprecated:"true" doc:"Deprecated: use identities[].feeds"`
	LastError    string       `json:"last_error,omitempty" deprecated:"true" doc:"Deprecated: use identities[] and their feeds"`
	Backfill     Backfill     `json:"backfill" deprecated:"true" doc:"Deprecated: use identities[].feeds"`
	Identities   []Identity   `json:"identities"`
```

Replace `playerDTO`:

```go
func playerDTO(base string, p service.PlayerSummary) Player {
	sync := p.Sync()
	out := Player{
		ID: p.ID, Name: p.Name, AvatarURL: p.AvatarURL, Country: p.Country, Enabled: p.Enabled,
		AddedAt: p.AddedAt, LastPolledAt: sync.LastPolledAt, LastError: p.Error(),
		Backfill:   Backfill{State: sync.BackfillState, NextPage: sync.BackfillPage, TotalPages: sync.BackfillTotalPages},
		Replays:    ReplayCounts{Scores: p.Counts.Scores, Archived: p.Counts.Archived, Pending: p.Counts.Pending, Failed: p.Counts.Failed, Gone: p.Counts.Gone},
		URL:        base + "/p/" + p.ID,
		Identities: make([]Identity, 0, len(p.Identities)),
	}
	for _, id := range p.Identities {
		dto := Identity{Platform: id.Platform, ID: id.ExternalID, Enabled: id.Enabled, LinkedAt: id.LinkedAt, LastError: id.LastError, Feeds: make([]Feed, 0, len(id.Feeds))}
		for _, f := range id.Feeds {
			dto.Feeds = append(dto.Feeds, Feed{
				Kind: f.Feed, Enabled: f.Enabled, Access: f.Access, StartedAt: f.StartedAt, LastPolledAt: f.LastPolledAt, LastError: f.LastError,
				Backfill: Backfill{State: f.BackfillState, NextPage: f.BackfillPage, TotalPages: f.BackfillTotalPages},
			})
		}
		out.Identities = append(out.Identities, dto)
	}
	return out
}
```

`internal/api/players.go` — replace `summary`:

```go
func (a *API) summary(ctx context.Context, id string) (Player, error) {
	sum, err := a.svc.GetPlayerSummary(ctx, id)
	if err != nil {
		return Player{}, err
	}
	return playerDTO(httpx.BaseURLFrom(ctx), sum), nil
}
```

Add to `internal/api/api_test.go` `TestPublicReads`, after the list-players assertions:

```go
	code, one, _ := call(t, h, http.MethodGet, "/api/v1/players/1001", nil)
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
```

- [ ] **Step 13: Full suite, lint**

Run: `go test -race ./... && make lint`
Expected: all `ok`, `0 issues.` Then look for leftover readers of the stale `players` columns (Task 4 deletes the fields, so any miss becomes a compile error there anyway):

Run: `command grep -rnE '(\.Player|\bp|\bpl)\.(BackfillState|BackfillPage|BackfillTotalPages|BackfillRetryAt|LastPolledAt|LastError)\b' internal --include=*.go --include=*.templ | command grep -vE '_templ.go|/query/'`
Expected: no output (hits on `sc.LastError`/`s.LastError` of scores are fine and are not matched by this pattern).

- [ ] **Step 14: Commit**

```bash
git add -A internal
git commit -m "refactor: drive sync state from platform accounts and feeds"
```

---

## Task 4: Drop the legacy `players` sync columns

Nothing reads the six columns after Task 3. This task removes them from the model and drops them from upgraded databases with raw `ALTER TABLE … DROP COLUMN` inside the migration transaction. **This is the step that destroys data if done with GORM's `DropColumn`** — read the Global Constraints first.

**Files:**
- Modify: `internal/model/model.go` (`Player`)
- Modify: `internal/db/db.go` (`legacyPlayerColumns`, `dropLegacyColumns`, call site)
- Modify: `internal/service/players.go` (`AddPlayer` literal)
- Modify: `internal/db/db_test.go` (`seed`), `internal/db/migrate_test.go`
- Regenerate: `internal/db/query/players.gen.go`

**Interfaces:**
- Consumes: Task 2 `Migrate` structure.
- Produces: `model.Player{ID, Name, AvatarURL, Country, Enabled, AddedAt}` only. Fresh databases are created without the legacy columns, so `Migrate` treats them as non-legacy from now on.

- [ ] **Step 1: Write the failing tests**

Append to `internal/db/migrate_test.go`:

```go
var legacyColumns = []string{"backfill_state", "backfill_page", "backfill_total_pages", "backfill_retry_at", "last_polled_at", "last_error"}

func playerColumns(t *testing.T, gdb *gorm.DB) map[string]bool {
	t.Helper()
	var names []string
	if err := gdb.Raw("SELECT name FROM pragma_table_info('players')").Scan(&names).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func TestMigrateDropsLegacyColumnsKeepingRows(t *testing.T) {
	gdb, _ := openFixture(t)
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	cols := playerColumns(t, gdb)
	for _, c := range legacyColumns {
		if cols[c] {
			t.Errorf("players.%s still present", c)
		}
	}
	for _, c := range []string{"id", "name", "avatar_url", "country", "enabled", "added_at"} {
		if !cols[c] {
			t.Errorf("players.%s missing", c)
		}
	}
	var idx int64
	gdb.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_players_backfill_state'").Scan(&idx)
	if idx != 0 {
		t.Error("idx_players_backfill_state not dropped")
	}
	// A table rebuild would have cascaded: every row must still be there.
	for table, want := range map[string]int64{"players": 3, "scores": 4, "player_platforms": 3, "sync_feeds": 3, "sessions": 1} {
		if got := count(t, gdb, table); got != want {
			t.Errorf("%s: %d rows, want %d", table, got, want)
		}
	}
	p, err := query.Use(gdb).Player.WithContext(context.Background()).Where(query.Use(gdb).Player.ID.Eq("111")).First()
	if err != nil || p.Name != "Gone" || p.Enabled || !p.AddedAt.Equal(mustTime(t, "2026-10-09T11:00:00Z")) {
		t.Fatalf("player 111 = %+v %v", p, err)
	}
}

func TestMigrateSecondRunAfterDrop(t *testing.T) {
	gdb, path := openFixture(t)
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(path), db.BackupName)
	st1, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatalf("second run: %v", err)
	}
	st2, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !st1.ModTime().Equal(st2.ModTime()) || st1.Size() != st2.Size() {
		t.Fatal("the pre-upgrade backup must not be rewritten")
	}
	if got := count(t, gdb, "scores"); got != 4 {
		t.Fatalf("scores = %d", got)
	}
}
```

Also in this file's `TestMigrateFreshDatabase`, change the player literal to `&model.Player{ID: id, Name: id, Enabled: true, AddedAt: now}`, and in `internal/db/db_test.go` `seed`, change it to `&model.Player{ID: "1", Name: "p", Enabled: true, AddedAt: now}` (these stop compiling once the fields go — that is the intended failure).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/db/ -run 'TestMigrateDropsLegacyColumnsKeepingRows|TestMigrateSecondRunAfterDrop'`
Expected: FAIL — compile errors on the `model.Player` literals first; after Step 3, `players.backfill_state still present`.

- [ ] **Step 3: Implement**

`internal/model/model.go` — `Player` becomes:

```go
type Player struct {
	ID        string    `gorm:"primaryKey"` // opaque (spec §4.2); never a platform account ID
	Name      string    `gorm:"not null"`
	AvatarURL string    `gorm:"not null"`
	Country   string    `gorm:"not null"`
	Enabled   bool      `gorm:"not null"`
	AddedAt   time.Time `gorm:"not null"`
}
```

`internal/db/db.go` — add after `backfillLegacy`:

```go
// legacyPlayerColumns moved to sync_feeds (spec §4.2) and are dropped from
// upgraded databases.
var legacyPlayerColumns = []string{
	"backfill_state", "backfill_page", "backfill_total_pages", "backfill_retry_at", "last_polled_at", "last_error",
}

// dropLegacyColumns drops the moved players columns in place. Never use
// Migrator().DropColumn here: the SQLite driver implements it as a table
// rebuild whose DROP TABLE players cascades to every score and account under
// foreign_keys(1) (spec §7). SQLite refuses to drop an indexed column, so the
// index goes first.
func dropLegacyColumns(tx *gorm.DB) error {
	if err := tx.Exec("DROP INDEX IF EXISTS `idx_players_backfill_state`").Error; err != nil {
		return fmt.Errorf("drop legacy index: %w", err)
	}
	for _, c := range legacyPlayerColumns {
		ok, err := hasColumn(tx, "players", c)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		// #nosec G202 -- c comes from the constant list above
		if err := tx.Exec("ALTER TABLE `players` DROP COLUMN `" + c + "`").Error; err != nil {
			return fmt.Errorf("drop players.%s: %w", c, err)
		}
	}
	return nil
}
```

In `Migrate`'s transaction, call it after the index loop and before verification:

```go
		if legacy {
			if err := dropLegacyColumns(tx); err != nil {
				return err
			}
			return verifyMigration(tx, before)
		}
		return nil
```

`internal/service/players.go` — in `AddPlayer` drop `BackfillState: model.BackfillPending, BackfillPage: 1` from the `model.Player` literal.

Run: `make generate`

- [ ] **Step 4: Run the tests**

Run: `go test -race ./... && make lint`
Expected: all `ok`, `0 issues.` (If `go vet`/gosec reports the `#nosec` comment as unused, remove it.)

- [ ] **Step 5: Commit**

```bash
git add internal/model internal/db internal/service
git commit -m "feat(db): drop the legacy players sync columns in place"
```

---

## Task 5: Opaque player IDs

New players get a random opaque ID (`platform.NewPlayerID`); existing IDs never change. Duplicate detection moves from the `players` primary key to the account's unique index. Tests get a deterministic sequential generator so ordering-sensitive assertions (round robin) stay stable while IDs still differ from account IDs.

**Files:**
- Modify: `internal/service/service.go` (`SetIDGenerator`), `internal/service/players.go` (`AddPlayer`, `PlayerByIdentity`, `ParsePlayerRef`)
- Modify: `internal/storage/storage.go` (`ValidPlayerID` → `platform.ValidPlayerID`)
- Modify: `internal/api/players.go` (`PlayerPath`), `internal/api/sync.go` (`RetryInput`)
- Modify: `internal/web/admin.go` (`lookupPlayer`)
- Modify: `internal/testutil/service.go` (`NewServiceWithDB`, sequential IDs, `AddPlayer`)
- Test: `internal/service/players_test.go`, `internal/archiver/poll_test.go`, `internal/storage/storage_test.go`, `internal/web/admin_test.go`, plus the ID churn listed in Step 6

**Interfaces:**
- Consumes: `platform.NewPlayerID`, `platform.ValidPlayerID`.
- Produces:
  - `(*Service).SetIDGenerator(gen func() string)` (tests)
  - `(*Service).PlayerByIdentity(ctx, platformName, externalID string) (*model.Player, error)` (ErrNotFound when untracked)
  - `testutil.NewServiceWithDB(t) (*service.Service, *gorm.DB, *Resolver, *Clock)`
  - `testutil.AddPlayer(t, svc, ref string) string` — returns the new player's ID
  - test IDs are `p00000000001`, `p00000000002`, … in creation order

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/players_test.go`:

```go
func TestAddPlayerAssignsOpaqueID(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	svc.SetIDGenerator(platform.NewPlayerID)
	p, err := svc.AddPlayer(ctx, "https://scoresaber.com/u/1001")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "1001" || !platform.ValidPlayerID(p.ID) {
		t.Fatalf("player ID = %q, want an opaque ID", p.ID)
	}
	got, err := svc.PlayerByIdentity(ctx, model.PlatformScoreSaber, "1001")
	if err != nil || got.ID != p.ID {
		t.Fatalf("PlayerByIdentity = %+v %v", got, err)
	}
	if _, err := svc.PlayerByIdentity(ctx, model.PlatformScoreSaber, "1002"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("untracked account err = %v", err)
	}
}

func TestAddPlayerTwiceIsRejected(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	testutil.AddPlayer(t, svc, "1001")
	for _, ref := range []string{"1001", " https://scoresaber.com/u/1001?page=2 ", "scoresaber.com/u/1001/"} {
		if _, err := svc.AddPlayer(ctx, ref); !errors.Is(err, service.ErrPlayerExists) {
			t.Errorf("AddPlayer(%q) err = %v, want ErrPlayerExists", ref, err)
		}
	}
	list, _ := svc.ListPlayers(ctx, true)
	if len(list) != 1 {
		t.Fatalf("players = %d, want 1", len(list))
	}
}

func TestIDGeneratorCollisionRetries(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	first := testutil.AddPlayer(t, svc, "1001")
	ids := []string{first, first, "pfresh"}
	svc.SetIDGenerator(func() string { id := ids[0]; ids = ids[1:]; return id })
	p, err := svc.AddPlayer(ctx, "1002")
	if err != nil || p.ID != "pfresh" {
		t.Fatalf("collision not retried: %+v %v", p, err)
	}
}
```

(imports: `errors`, `github.com/yyewolf/ssarchiver/internal/platform`.)

Append to `internal/archiver/poll_test.go`:

```go
func TestWorkerPollsAccountIDNotPlayerID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := testutil.AddPlayer(t, e.svc, "1001")
	if id == "1001" {
		t.Fatal("test IDs must differ from account IDs")
	}
	e.fc.scores["1001"] = e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	e.step(t)
	if got := e.fc.calls(); len(got) == 0 || got[0] != "1001:1" {
		t.Fatalf("worker must call ScoreSaber with the account ID, calls = %v", got)
	}
	c, _ := e.svc.PlayerCounts(ctx, id)
	if c.Scores != 3 {
		t.Fatalf("scores must be stored under the player ID, got %d", c.Scores)
	}
}

func TestLegacyPlayerWithDifferentAccountID(t *testing.T) {
	svc, gdb, _, clk := testutil.NewServiceWithDB(t)
	fc := newFake()
	e := &env{svc: svc, clk: clk, fc: fc, w: archiver.New(svc, fc, nil)}
	ctx := context.Background()
	// A legacy-style player whose ID is digits but is not its ScoreSaber ID
	// (e.g. relinked after an unlink): the worker must use the account ID.
	now := testutil.T0
	for _, row := range []any{
		&model.Player{ID: "123", Name: "Legacy", Enabled: true, AddedAt: now},
		&model.PlayerPlatform{PlayerID: "123", Platform: model.PlatformScoreSaber, ExternalID: "1001", Enabled: true, LinkedAt: now},
		&model.SyncFeed{PlayerID: "123", Platform: model.PlatformScoreSaber, Feed: model.KindScore, Enabled: true, StartedAt: now,
			Access: model.AccessNA, BackfillState: model.BackfillPending, BackfillPage: 1},
	} {
		if err := gdb.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	fc.scores["1001"] = fc.history("1001", 1, 2, now.Add(-time.Hour))
	e.drain(t, 50)
	for _, c := range fc.calls() {
		if strings.HasPrefix(c, "123:") {
			t.Fatalf("worker called ScoreSaber with the player ID: %v", fc.calls())
		}
	}
	c, _ := svc.PlayerCounts(ctx, "123")
	if c.Scores != 2 || c.Archived != 2 {
		t.Fatalf("counts = %+v", c)
	}
}
```

(imports: `github.com/yyewolf/ssarchiver/internal/archiver`.)

In `internal/storage/storage_test.go`, `TestPutRejectsBadPlayerID`: change the list to `[]string{"../etc", "", "A1", "1/2", "a.b"}` and add a positive case:

```go
	if _, _, err := s.Put("k7m2q9x4c1ab", 1, strings.NewReader("x")); err != nil {
		t.Fatalf("opaque IDs must be accepted: %v", err)
	}
```

`internal/web/admin_test.go` `TestLookupPlayer` already ends with `e.seed()` and a lookup of `"1001"` that must contain `"Already tracked"`. Once `e.seed()` adds players with opaque IDs, that assertion fails until `lookupPlayer` checks the account instead of the player ID — it is the pinned test for Review Focus #4 (rename it `TestLookupShowsAlreadyTracked` is not needed; leave it as is). Add the URL variant right after it:

```go
	trackedURL := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"https://scoresaber.com/u/1001?page=2"}}, withCookie(c), htmx("lookup-result"))
	contains(t, trackedURL.Body.String(), "Already tracked")
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/service/ ./internal/archiver/ ./internal/storage/ ./internal/web/`
Expected: FAIL — `SetIDGenerator`/`PlayerByIdentity`/`testutil.AddPlayer`/`NewServiceWithDB` undefined.

- [ ] **Step 3: Implement the ID generator and lookups**

`internal/service/service.go` — add a field `newID func() string` to `Service`, initialise it in `New` with `newID: platform.NewPlayerID`, and add:

```go
// SetIDGenerator replaces the player ID generator (tests).
func (s *Service) SetIDGenerator(gen func() string) {
	s.clockMu.Lock()
	s.newID = gen
	s.clockMu.Unlock()
}

func (s *Service) nextID() string {
	s.clockMu.RLock()
	defer s.clockMu.RUnlock()
	return s.newID()
}
```

`internal/service/players.go`:

```go
var ssIDRe = regexp.MustCompile(`^[0-9]{1,32}$`)

// ParsePlayerRef extracts a ScoreSaber account ID from an ID or profile URL.
func ParsePlayerRef(input string) (string, error) {
	in := strings.TrimSpace(input)
	if ssIDRe.MatchString(in) {
		return in, nil
	}
	if m := playerURLRe.FindStringSubmatch(in); m != nil {
		return m[1], nil
	}
	return "", ErrInvalidPlayerRef
}

// PlayerByIdentity finds the player a platform account is linked to.
func (s *Service) PlayerByIdentity(ctx context.Context, platformName, externalID string) (*model.Player, error) {
	pp := s.q.PlayerPlatform
	link, err := pp.WithContext(ctx).Where(pp.Platform.Eq(platformName), pp.ExternalID.Eq(externalID)).First()
	if err != nil {
		return nil, notFound(err, platformName+" account "+externalID)
	}
	return s.GetPlayer(ctx, link.PlayerID)
}

// freePlayerID draws IDs until one is used by neither a player nor an alias.
func (s *Service) freePlayerID(ctx context.Context) (string, error) {
	p, a := s.q.Player, s.q.PlayerAlias
	for range 10 {
		id := s.nextID()
		np, err := p.WithContext(ctx).Where(p.ID.Eq(id)).Count()
		if err != nil {
			return "", fmt.Errorf("service: player id: %w", err)
		}
		na, err := a.WithContext(ctx).Where(a.OldID.Eq(id)).Count()
		if err != nil {
			return "", fmt.Errorf("service: player id: %w", err)
		}
		if np == 0 && na == 0 {
			return id, nil
		}
	}
	return "", errors.New("service: could not draw a free player ID")
}
```

In `AddPlayer`, after `ResolvePlayer`, check the account and draw the ID before the transaction, and use the drawn ID for every row:

```go
	if _, err := s.PlayerByIdentity(ctx, model.PlatformScoreSaber, sp.ID); err == nil {
		return nil, ErrPlayerExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	id, err := s.freePlayerID(ctx)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	p := &model.Player{ID: id, Name: sp.Name, AvatarURL: sp.Avatar, Country: sp.Country, Enabled: true, AddedAt: now}
```

(The transaction body stays as in Task 2; the account row keeps `ExternalID: sp.ID`. A concurrent add of the same account still fails the unique index → `db.IsDuplicate` → `ErrPlayerExists`.)

`internal/storage/storage.go` — replace the regexp and `ValidPlayerID`:

```go
// ValidPlayerID reports whether id is a well-formed player ID (legacy
// all-digit or opaque); see platform.ValidPlayerID.
func ValidPlayerID(id string) bool { return platform.ValidPlayerID(id) }
```

and update the package doc to `// Package storage stores replay files on disk under {root}/{player}/.`.

`internal/api/players.go` — `PlayerPath`:

```go
type PlayerPath struct {
	ID string `path:"id" pattern:"^[a-z0-9-]{1,40}$" doc:"Player ID (opaque; see identities[] for platform IDs)"`
}
```

`internal/api/sync.go` — `RetryInput.Body.PlayerID` pattern becomes `^[a-z0-9-]{1,40}$`.

`internal/web/admin.go` — in `lookupPlayer`, replace the tracked check:

```go
	_, gerr := h.svc.PlayerByIdentity(r.Context(), model.PlatformScoreSaber, sp.ID)
	render(w, r, http.StatusOK, views.PlayerPreview(sp, gerr == nil))
```

(import `github.com/yyewolf/ssarchiver/internal/model`).

`internal/testutil/service.go`:

```go
// NewService returns a Service over a temp DB/store with a fake clock and
// resolver. Player IDs are p00000000001, p00000000002, … in creation order.
func NewService(t testing.TB) (*service.Service, *Resolver, *Clock) {
	t.Helper()
	svc, _, res, clk := NewServiceWithDB(t)
	return svc, res, clk
}

// NewServiceWithDB is NewService that also returns the database, for tests
// that need rows the service cannot create.
func NewServiceWithDB(t testing.TB) (*service.Service, *gorm.DB, *Resolver, *Clock) {
	t.Helper()
	gdb := OpenDB(t)
	store, err := storage.New(filepath.Join(t.TempDir(), "replays"))
	if err != nil {
		t.Fatal(err)
	}
	res := &Resolver{Players: map[string]scoresaber.Player{
		"1001": {ID: "1001", Name: "Alice", Country: "FR", Avatar: "https://cdn.scoresaber.com/avatars/1001.jpg"},
		"1002": {ID: "1002", Name: "Bob", Country: "US", Avatar: "https://cdn.scoresaber.com/avatars/1002.jpg"},
	}}
	svc := service.New(gdb, store, res)
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	clk := NewClock(T0)
	svc.SetClock(clk.Now)
	var n atomic.Int64
	svc.SetIDGenerator(func() string { return fmt.Sprintf("p%011d", n.Add(1)) })
	return svc, gdb, res, clk
}

// AddPlayer adds a player by ScoreSaber ID or URL and returns its player ID.
func AddPlayer(t testing.TB, svc *service.Service, ref string) string {
	t.Helper()
	p, err := svc.AddPlayer(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}
```

(imports: `fmt`, `sync/atomic`, `gorm.io/gorm`.)

- [ ] **Step 4: Run the new tests**

Run: `go test ./internal/service/ -run 'TestAddPlayerAssignsOpaqueID|TestAddPlayerTwiceIsRejected|TestIDGeneratorCollisionRetries' && go test ./internal/storage/...`
Expected: `ok`. Most other tests now fail — they assumed player ID == account ID. Step 5 fixes them.

- [ ] **Step 5: Migrate the tests off "player ID == account ID"**

Rule: **an account ID** (the string passed to `AddPlayer`, the first argument of `testutil.Item`, the keys of `fakeClient.scores`/`scoresErr`, `Resolver.Players` keys, profile URLs) **stays `"1001"`/`"1002"`.** Every use of a **player ID** (any `svc`/`e.svc` method taking a player ID, `PlayerID` fields/filters, `ScoreFeedKey`/`ScoreFeed`, `/p/…` and `/api/v1/players/…` URLs, `player_id` form values, expectations on `sc.PlayerID`, the `lastPlayer`/`last` round-robin arguments) uses the ID returned by the add.

Mechanics:
- `internal/service/helpers_test.go` — make `mustAdd` return the ID: `func mustAdd(t *testing.T, svc *service.Service, ref string) string { t.Helper(); return testutil.AddPlayer(t, svc, ref) }`. Callers of `upsert(t, svc, playerID, …)` pass the returned player ID; `upsert` itself is unchanged (it also passes that ID as the payload's player, which only feeds the profile refresh these tests do not check).
- `internal/archiver/fake_test.go` — make `env.add` return `testutil.AddPlayer(t, e.svc, id)`.
- In each test, bind the IDs once: `a := mustAdd(t, svc, "1001")`, `b := mustAdd(t, svc, "1002")` (archiver: `a := e.add(t, "1001")`), then replace player-ID uses of `"1001"`/`"1002"` with `a`/`b` per the rule above. Sequential test IDs preserve creation order, so `a < b` exactly as `"1001" < "1002"` did and round-robin expectations are unchanged.
- `internal/service/replays_test.go` `seedQueue` — return the IDs too: `func seedQueue(t *testing.T) (*service.Service, *testutil.Clock, string, string)`; callers use them for `SetPlayerEnabled`, `MarkIdentityError`, `nextID(…, last)`.
- `internal/web` and `internal/api` tests that seed players (`env_test.go` seeding helpers, `api_test.go`) — capture the returned ID and use it in `/p/{id}`, `/api/v1/players/{id}`, form `player_id` values and expected `href`s.

Files to sweep (counts from `grep -c '"100[12]"'` before this task): `internal/service/{players,scores,replays,feeds,events,reconcile}_test.go`, `internal/archiver/{poll,download,worker}_test.go`, `internal/web/{env,admin,admin_sync,replay}_test.go`, `internal/web/views/urls_test.go` (only if it builds player URLs from a service-created player — literal-string URL tests stay), `internal/api/api_test.go`, `internal/app/app_test.go`.

- [ ] **Step 6: Full suite, lint**

Run: `go test -race ./... && make lint`
Expected: all `ok`, `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add -A internal
git commit -m "feat: give new players opaque IDs"
```

---
## Task 6: Registry wiring — ScoreSaber adapter, neutral upsert, worker via adapters

`internal/scoresaber` registers as the legacy platform through an adapter; the service takes a `*platform.Registry` instead of a ScoreSaber resolver and stores `platform.Play`s; the worker drives every feed through `Platform.Adapter`. Still ScoreSaber-only at runtime; the generic paths for non-legacy platforms (internal IDs, per-platform storage) arrive in Task 7.

**Files:**
- Create: `internal/scoresaber/platform.go`, `internal/scoresaber/platform_test.go`
- Modify: `internal/scoresaber/client.go` (error sentinels), `internal/scoresaber/limiter.go` (`Name`, `Ready`, snapshot aliases)
- Modify: `internal/service/service.go`, `internal/service/players.go`, `internal/service/scores.go`, `internal/service/replays.go` (`MarkReplayGone`)
- Modify: `internal/archiver/worker.go`, `internal/archiver/poll.go`, `internal/archiver/download.go`
- Modify: `internal/app/app.go`
- Modify: `internal/web/admin.go`, `internal/web/views/admin.templ` (`PlayerPreview`)
- Modify: `internal/api/players.go` (`AddPlayerInput.platform`), `internal/api/dto.go` (`Identity.ProfileURL`)
- Modify: `internal/testutil/fakes.go`, `internal/testutil/service.go`
- Test: `internal/scoresaber/platform_test.go`, `internal/scoresaber/limiter_test.go`, `internal/service/*_test.go`, `internal/archiver/*_test.go`, `internal/web/env_test.go`, `internal/api/api_test.go`

**Interfaces:**
- Consumes: Task 1 `platform.*`; Task 3 feeds; Task 5 IDs.
- Produces:
  - `scoresaber.API` interface (`Player`, `Scores`, `Replay` — `*scoresaber.Client` satisfies it)
  - `scoresaber.NewPlatform(api API, l *Limiter) platform.Platform` — `Name "scoresaber"`, `Slug "ss"`, `DisplayName "ScoreSaber"`, `Priority 0`, `Legacy true`, `ReplayExt ".dat"`, `ImageHosts ["https://cdn.scoresaber.com"]`, one required `score` feed
  - `scoresaber.Plays(items []ScoreItem) []platform.Play`
  - `scoresaber.ErrNotFound = platform.ErrNotFound`, `scoresaber.ErrRateLimited = platform.ErrRateLimited`; `scoresaber.WindowSnapshot`/`LimiterSnapshot` become aliases of the `platform` types; `(*Limiter).Name() string`, `(*Limiter).Ready(now) (bool, time.Time)`
  - `service.New(gdb *gorm.DB, store *storage.Store, reg *platform.Registry) *Service`; `(*Service).Platforms() *platform.Registry`
  - `(*Service).ResolvePlayer(ctx, ref, platformName string) (platform.Platform, platform.Profile, error)`
  - `(*Service).AddPlayer(ctx, ref, platformName string) (*model.Player, error)` (`platformName` "" = auto-detect)
  - `(*Service).UpdatePlayerProfile(ctx, id string, prof platform.Profile) error`; `(*Service).RefreshProfile(ctx, playerID, platformName string, prof platform.Profile) error`
  - `(*Service).UpsertPlays(ctx, playerID, platformName string, plays []platform.Play) (UpsertResult, error)` (replaces `UpsertScores`)
  - `(*Service).MarkReplayGone(ctx, id int64, reason string) error`
  - `archiver.New(svc *service.Service) *Worker`; `archiver.Status{State, Task, Since, Limiter platform.LimiterSnapshot, Limiters []LimiterStatus}`; `archiver.LimiterStatus{Platform, Name string; Snapshot platform.LimiterSnapshot}`
  - `views.PlayerPreview(platformName string, prof platform.Profile, tracked bool)`
  - `testutil.NewServiceWith(t, plats ...platform.Platform) (*service.Service, *gorm.DB, *Clock)`; `testutil.DefaultPlayers() map[string]scoresaber.Player`; `testutil.Upsert(t, svc, playerID string, items ...scoresaber.ScoreItem) service.UpsertResult`; `testutil.Resolver` now also implements `scoresaber.API`
- Removed: `service.Resolver`, `service.ParsePlayerRef`, `service.UpsertScores`, `archiver.Client`, `archiver.LimiterSource`.

- [ ] **Step 1: Write the failing ScoreSaber adapter tests**

`internal/scoresaber/platform_test.go`:

```go
package scoresaber_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

type stubAPI struct {
	page       scoresaber.ScorePage
	replayID   int64
	playerCall string
}

func (s *stubAPI) Player(_ context.Context, id string) (scoresaber.Player, error) {
	s.playerCall = id
	return scoresaber.Player{ID: id, Name: "Alice", Avatar: "a.jpg", Country: "FR"}, nil
}

func (s *stubAPI) Scores(context.Context, string, int) (scoresaber.ScorePage, error) { return s.page, nil }

func (s *stubAPI) Replay(_ context.Context, id int64) (io.ReadCloser, error) {
	s.replayID = id
	return io.NopCloser(strings.NewReader("r")), nil
}

func TestPlatformDescriptor(t *testing.T) {
	p := scoresaber.NewPlatform(&stubAPI{}, nil)
	if _, err := platform.NewRegistry(p); err != nil {
		t.Fatalf("descriptor must validate: %v", err)
	}
	if p.Name != model.PlatformScoreSaber || p.Slug != "ss" || !p.Legacy || p.ReplayExt != ".dat" || p.ProfileURL("42") != "https://scoresaber.com/u/42" {
		t.Fatalf("descriptor = %+v", p)
	}
}

func TestParseRefs(t *testing.T) { // the 2026-10-08 plan's Review Focus #5, now on the adapter
	p := scoresaber.NewPlatform(&stubAPI{}, nil)
	for in, want := range map[string]string{
		"https://scoresaber.com/u/76561198059961776?page=2&sort=recent": "76561198059961776",
		"scoresaber.com/u/76561198059961776/":                           "76561198059961776",
		"http://www.scoresaber.com/u/42#top":                            "42",
	} {
		if got, ok := p.ParseURL(in); !ok || got != want {
			t.Errorf("ParseURL(%q) = %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"https://beatleader.com/u/42", "https://scoresaber.com/leaderboard/42", "not a link"} {
		if _, ok := p.ParseURL(in); ok {
			t.Errorf("ParseURL(%q) accepted", in)
		}
	}
	if !p.ValidID("76561198059961776") || p.ValidID("12a") || p.ValidID("") {
		t.Error("ValidID must accept digits only")
	}
}

func TestAdapterConvertsPages(t *testing.T) {
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	api := &stubAPI{page: scoresaber.ScorePage{
		Data: []scoresaber.ScoreItem{{
			Score: scoresaber.Score{ID: 5001, Rank: 3, ModifiedScore: 900, UnmodifiedScore: 950, Accuracy: 0.9, PP: 300,
				Mods: []string{"DA", "FS"}, FullCombo: true, MaxCombo: 500, HasReplay: true, PersonalBest: true, CreatedAt: at,
				Player: scoresaber.Player{ID: "1001", Name: "Alice"}, Device: scoresaber.Device{HMD: "Quest 3"}},
			Leaderboard: scoresaber.Leaderboard{ID: 1001, MaxScore: 1000,
				Map:        scoresaber.Map{Hash: "ABC", SongName: "Song", CoverURL: "c.png"},
				Difficulty: scoresaber.Difficulty{Difficulty: 9, GameMode: "SoloStandard", RawDifficulty: "_ExpertPlus_SoloStandard"},
				Realm:      scoresaber.Realm{LeaderboardStatus: "RANKED", Stars: 10.5}},
		}},
		Metadata: scoresaber.PageMeta{TotalPages: 7},
	}}
	p := scoresaber.NewPlatform(api, nil)
	pg, err := p.Adapter.FeedPage(context.Background(), model.KindScore, "1001", 1)
	if err != nil || pg.TotalPages != 7 || len(pg.Plays) != 1 {
		t.Fatalf("page = %+v %v", pg, err)
	}
	pl := pg.Plays[0]
	if pl.Kind != model.KindScore || pl.EndType != model.EndClear || pl.ExternalID != "5001" || pl.Mods != "DA,FS" ||
		!pl.SetAt.Equal(at) || !pl.HasReplay || pl.ReplayURL != "" || pl.Profile == nil || pl.Profile.Name != "Alice" ||
		pl.Leaderboard.ExternalID != "1001" || pl.Leaderboard.GameMode != "SoloStandard" || pl.Leaderboard.Status != "RANKED" {
		t.Fatalf("play = %+v", pl)
	}
	if _, err := p.Adapter.FeedPage(context.Background(), model.KindAttempt, "1001", 1); err == nil {
		t.Fatal("ScoreSaber has no attempt feed")
	}
	if _, err := p.Adapter.Replay(context.Background(), platform.ReplayRef{Kind: model.KindScore, ExternalID: "5001"}); err != nil || api.replayID != 5001 {
		t.Fatalf("replay by score ID: %v %d", err, api.replayID)
	}
	if access, _, err := p.Adapter.ProbeAccess(context.Background(), model.KindScore, "1001"); err != nil || access != model.AccessNA {
		t.Fatalf("ProbeAccess = %q %v", access, err)
	}
	prof, err := p.Adapter.Resolve(context.Background(), "1001")
	if err != nil || prof.ExternalID != "1001" || prof.AvatarURL != "a.jpg" {
		t.Fatalf("Resolve = %+v %v", prof, err)
	}
}
```

Append to `internal/scoresaber/limiter_test.go`:

```go
func TestLimiterReady(t *testing.T) {
	l := scoresaber.NewLimiter(300)
	if l.Name() != "scoresaber" {
		t.Fatalf("Name = %q", l.Name())
	}
	if ok, _ := l.Ready(time.Now()); !ok {
		t.Fatal("fresh limiter must be ready")
	}
	ctx := context.Background()
	for range 20 { // the short window allows 20 per 10 s
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ok, at := l.Ready(now)
	if ok || !at.After(now) || at.After(now.Add(10*time.Second)) {
		t.Fatalf("full short window: Ready = %v %v", ok, at)
	}
}
```

(Match the imports already present in `limiter_test.go`; it is an external `scoresaber_test` or internal package — use the same qualifier style as its existing tests.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/scoresaber/...`
Expected: FAIL — `undefined: scoresaber.NewPlatform`, `l.Name undefined`.

- [ ] **Step 3: Implement the adapter**

`internal/scoresaber/client.go` — replace the sentinel block:

```go
var (
	ErrNotFound    = platform.ErrNotFound
	ErrRateLimited = platform.ErrRateLimited
)
```

`internal/scoresaber/limiter.go` — replace the `WindowSnapshot`/`LimiterSnapshot` struct definitions with aliases and add `Name`/`Ready`, factoring the "until" computation out of `Wait`:

```go
type (
	WindowSnapshot  = platform.WindowSnapshot
	LimiterSnapshot = platform.LimiterSnapshot
)

// Name identifies the limiter on the status page and in readiness checks.
func (l *Limiter) Name() string { return model.PlatformScoreSaber }

// nextAllowed is when the next request may go out; l.mu must be held.
func (l *Limiter) nextAllowed(now time.Time) time.Time {
	until := l.blockedUntil
	for _, w := range l.windows {
		w.prune(now)
		if len(w.hits) >= w.limit {
			if t := w.hits[0].Add(w.period); t.After(until) {
				until = t
			}
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
```

and in `Wait`, replace the inline loop computing `until` with `until := l.nextAllowed(now)`.

Create `internal/scoresaber/platform.go`:

```go
package scoresaber

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// API is the part of the ScoreSaber client the adapter uses (*Client
// implements it; tests pass fakes).
type API interface {
	Player(ctx context.Context, id string) (Player, error)
	Scores(ctx context.Context, playerID string, page int) (ScorePage, error)
	Replay(ctx context.Context, scoreID int64) (io.ReadCloser, error)
}

var (
	profileURLRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?scoresaber\.com/u/([0-9]{1,32})(?:[/?#].*)?$`)
	idRe         = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// NewPlatform is ScoreSaber's registry entry: the legacy platform (spec §4.1).
// l may be nil (tests): the platform is then not rate limited.
func NewPlatform(api API, l *Limiter) platform.Platform {
	return platform.Platform{
		Name: model.PlatformScoreSaber, Slug: "ss", DisplayName: "ScoreSaber", Priority: 0, Legacy: true,
		ReplayExt: ".dat", ImageHosts: []string{"https://cdn.scoresaber.com"},
		ProfileURL: func(id string) string { return "https://scoresaber.com/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := profileURLRe.FindStringSubmatch(strings.TrimSpace(in))
			if m == nil {
				return "", false
			}
			return m[1], true
		},
		ValidID: idRe.MatchString,
		Feeds:   []platform.FeedSpec{{Kind: model.KindScore}},
		Adapter: adapter{api: api, limiter: l},
	}
}

type adapter struct {
	api     API
	limiter *Limiter
}

func (a adapter) Resolve(ctx context.Context, id string) (platform.Profile, error) {
	p, err := a.api.Player(ctx, id)
	if err != nil {
		return platform.Profile{}, err
	}
	return platform.Profile{ExternalID: p.ID, Name: p.Name, AvatarURL: p.Avatar, Country: p.Country}, nil
}

func (a adapter) FeedPage(ctx context.Context, kind, externalID string, page int) (platform.PlayPage, error) {
	if kind != model.KindScore {
		return platform.PlayPage{}, fmt.Errorf("scoresaber: no %s feed", kind)
	}
	sp, err := a.api.Scores(ctx, externalID, page)
	if err != nil {
		return platform.PlayPage{}, err
	}
	return platform.PlayPage{Plays: Plays(sp.Data), TotalPages: sp.Metadata.TotalPages}, nil
}

func (adapter) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessNA, 0, nil
}

func (a adapter) Replay(ctx context.Context, ref platform.ReplayRef) (io.ReadCloser, error) {
	id, err := strconv.ParseInt(ref.ExternalID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("scoresaber: replay: invalid score id %q", ref.ExternalID)
	}
	return a.api.Replay(ctx, id)
}

func (a adapter) Limiters() []platform.Limiter {
	if a.limiter == nil {
		return nil
	}
	return []platform.Limiter{a.limiter}
}

func (a adapter) FeedLimiter(string) string { return a.limiterName() }

func (a adapter) ReplayLimiter(string) string { return a.limiterName() }

func (a adapter) limiterName() string {
	if a.limiter == nil {
		return ""
	}
	return a.limiter.Name()
}

// Plays converts a page of ScoreSaber scores into neutral plays.
func Plays(items []ScoreItem) []platform.Play {
	out := make([]platform.Play, 0, len(items))
	for _, it := range items {
		sc, lb := it.Score, it.Leaderboard
		out = append(out, platform.Play{
			Leaderboard: platform.LeaderboardData{
				ExternalID: strconv.FormatInt(lb.ID, 10), SongHash: lb.Map.Hash, SongName: lb.Map.SongName,
				SongSubName: lb.Map.SongSubName, SongAuthor: lb.Map.SongAuthorName, Mapper: lb.Map.LevelAuthorName,
				Difficulty: lb.Difficulty.Difficulty, DifficultyRaw: lb.Difficulty.RawDifficulty, GameMode: lb.Difficulty.GameMode,
				CoverURL: lb.Map.CoverURL, Status: lb.Realm.LeaderboardStatus, Stars: lb.Realm.Stars, MaxScore: lb.MaxScore,
			},
			Kind: model.KindScore, EndType: model.EndClear, ExternalID: strconv.FormatInt(sc.ID, 10),
			Rank: sc.Rank, ModifiedScore: sc.ModifiedScore, UnmodifiedScore: sc.UnmodifiedScore, Accuracy: sc.Accuracy, PP: sc.PP,
			Mods: strings.Join(sc.Mods, ","), FullCombo: sc.FullCombo, MissedNotes: sc.MissedNotes, BadCuts: sc.BadCuts,
			MaxCombo: sc.MaxCombo, HMD: sc.Device.HMD, PersonalBest: sc.PersonalBest, SetAt: sc.CreatedAt.UTC(),
			HasReplay: sc.HasReplay,
			Profile:   &platform.Profile{ExternalID: sc.Player.ID, Name: sc.Player.Name, AvatarURL: sc.Player.Avatar, Country: sc.Player.Country},
		})
	}
	return out
}
```

Run: `go test -race ./internal/scoresaber/...` → `ok`.

- [ ] **Step 4: Switch the service to the registry**

`internal/service/service.go`: remove the `Resolver` interface and the `scoresaber` import; the `ss Resolver` field becomes `reg *platform.Registry`:

```go
func New(gdb *gorm.DB, store *storage.Store, reg *platform.Registry) *Service {
	return &Service{
		db: gdb, q: query.Use(gdb), store: store, reg: reg,
		now:   func() time.Time { return time.Now().UTC() },
		newID: platform.NewPlayerID,
		wake:  make(chan struct{}, 1),
	}
}

// Platforms is the registry of platforms this instance archives from.
func (s *Service) Platforms() *platform.Registry { return s.reg }
```

`internal/service/players.go`:
- delete `playerURLRe`, `ssIDRe`, `ParsePlayerRef`;
- `platformOrder` becomes `return s.reg.Priority(name)` (guard `if s.reg == nil { return 0 }`);
- `UpdatePlayerProfile` takes a `platform.Profile` (`prof.Name`, `prof.AvatarURL`, `prof.Country`; empty values ignored as before);
- replace `ResolvePlayer` and `AddPlayer`, add `RefreshProfile`:

```go
// ResolvePlayer parses an admin's reference (profile URL or ID; platformName
// "" auto-detects) and looks the account up live.
func (s *Service) ResolvePlayer(ctx context.Context, ref, platformName string) (platform.Platform, platform.Profile, error) {
	p, id, err := s.reg.ParseRef(ref, platformName)
	if err != nil {
		return platform.Platform{}, platform.Profile{}, ErrInvalidPlayerRef
	}
	prof, err := p.Adapter.Resolve(ctx, id)
	if errors.Is(err, platform.ErrNotFound) {
		return p, platform.Profile{}, fmt.Errorf("%w: no %s player %s", ErrNotFound, p.DisplayName, id)
	}
	if err != nil {
		return p, platform.Profile{}, err
	}
	if prof.ExternalID == "" {
		prof.ExternalID = id
	}
	return p, prof, nil
}

func (s *Service) AddPlayer(ctx context.Context, ref, platformName string) (*model.Player, error) {
	plat, prof, err := s.ResolvePlayer(ctx, ref, platformName)
	if err != nil {
		return nil, err
	}
	if _, err := s.PlayerByIdentity(ctx, plat.Name, prof.ExternalID); err == nil {
		return nil, ErrPlayerExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	id, err := s.freePlayerID(ctx)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	p := &model.Player{ID: id, Name: prof.Name, AvatarURL: prof.AvatarURL, Country: prof.Country, Enabled: true, AddedAt: now}
	err = s.q.Transaction(func(tx *query.Query) error {
		if err := tx.Player.WithContext(ctx).Create(p); err != nil {
			return err
		}
		if err := tx.PlayerPlatform.WithContext(ctx).Create(&model.PlayerPlatform{
			PlayerID: id, Platform: plat.Name, ExternalID: prof.ExternalID, Enabled: true, LinkedAt: now,
		}); err != nil {
			return err
		}
		for _, f := range plat.RequiredFeeds() {
			if err := tx.SyncFeed.WithContext(ctx).Create(newFeed(id, plat.Name, f.Kind, now, model.AccessNA)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if db.IsDuplicate(err) {
			return nil, ErrPlayerExists
		}
		return nil, fmt.Errorf("service: add player: %w", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: Ptr(p.ID), Platform: Ptr(plat.Name), Message: "player added: " + p.Name})
	s.Wake()
	return p, nil
}

// RefreshProfile updates the player's display identity from one platform's
// profile, but only when that platform is the player's primary enabled
// account (spec §4.2).
func (s *Service) RefreshProfile(ctx context.Context, playerID, platformName string, prof platform.Profile) error {
	ids, err := s.identitiesBy(ctx, playerID)
	if err != nil {
		return err
	}
	for _, id := range ids[playerID] {
		if !id.Enabled {
			continue
		}
		if id.Platform != platformName {
			return nil
		}
		return s.UpdatePlayerProfile(ctx, playerID, prof)
	}
	return nil
}
```

`ErrInvalidPlayerRef` keeps its message.

`internal/service/scores.go` — delete `leaderboardFrom`, `scoreFrom`, `UpsertScores` and the `scoresaber` import; add:

```go
// UpsertPlays stores a page of plays from one platform (spec §5.4). Existing
// rows only get their mutable ranking fields refreshed; archive columns are
// never touched, except none → pending when the platform newly offers a replay.
func (s *Service) UpsertPlays(ctx context.Context, playerID, platformName string, plays []platform.Play) (UpsertResult, error) {
	var res UpsertResult
	if len(plays) == 0 {
		return res, nil
	}
	p, ok := s.reg.Get(platformName)
	if !ok {
		return res, fmt.Errorf("service: upsert: unknown platform %q", platformName)
	}
	err := s.q.Transaction(func(tx *query.Query) error {
		alloc := &idAllocator{ctx: ctx, tx: tx, legacy: p.Legacy}
		lbIDs, err := upsertLeaderboards(ctx, tx, p.Name, plays, alloc)
		if err != nil {
			return err
		}
		byKey, err := existingPlays(ctx, tx, p.Name, plays)
		if err != nil {
			return err
		}
		var fresh []*model.Score
		for _, pl := range plays {
			key := pl.Kind + "|" + pl.ExternalID
			if old, ok := byKey[key]; ok {
				res.Known++
				if err := refreshPlay(ctx, tx, old, pl, &res); err != nil {
					return err
				}
				continue
			}
			id, err := alloc.score(pl.ExternalID)
			if err != nil {
				return err
			}
			row := playRow(playerID, p.Name, id, lbIDs[pl.Leaderboard.ExternalID], pl)
			if row.ReplayState == model.ReplayPending {
				res.NewReplays++
			}
			res.New++
			fresh = append(fresh, row)
			byKey[key] = row // guards against duplicates within one page
		}
		if len(fresh) > 0 {
			if err := tx.Score.WithContext(ctx).CreateInBatches(fresh, 100); err != nil {
				return fmt.Errorf("insert scores: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return UpsertResult{}, fmt.Errorf("service: upsert plays: %w", err)
	}
	return res, nil
}

// idAllocator hands out row IDs. Legacy platforms use their own numeric IDs
// (Task 7 adds the internal range for the others).
type idAllocator struct {
	ctx    context.Context
	tx     *query.Query
	legacy bool
}

func (a *idAllocator) leaderboard(externalID string) (int64, error) { return a.legacyID(externalID) }

func (a *idAllocator) score(externalID string) (int64, error) { return a.legacyID(externalID) }

func (a *idAllocator) legacyID(externalID string) (int64, error) {
	if !a.legacy {
		return 0, errors.New("service: non-legacy platforms are not supported yet")
	}
	id, err := strconv.ParseInt(externalID, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("service: invalid legacy id %q", externalID)
	}
	return id, nil
}

func upsertLeaderboards(ctx context.Context, tx *query.Query, platformName string, plays []platform.Play, alloc *idAllocator) (map[string]int64, error) {
	lb := tx.Leaderboard
	var ext []string
	seen := map[string]bool{}
	for _, pl := range plays {
		if e := pl.Leaderboard.ExternalID; !seen[e] {
			seen[e] = true
			ext = append(ext, e)
		}
	}
	existing, err := lb.WithContext(ctx).Where(lb.Platform.Eq(platformName), lb.ExternalID.In(ext...)).Find()
	if err != nil {
		return nil, fmt.Errorf("load leaderboards: %w", err)
	}
	ids := make(map[string]int64, len(ext))
	for _, e := range existing {
		ids[e.ExternalID] = e.ID
	}
	rows := make([]*model.Leaderboard, 0, len(ext))
	done := map[string]bool{}
	for _, pl := range plays {
		d := pl.Leaderboard
		if done[d.ExternalID] {
			continue
		}
		done[d.ExternalID] = true
		id, ok := ids[d.ExternalID]
		if !ok {
			if id, err = alloc.leaderboard(d.ExternalID); err != nil {
				return nil, err
			}
			ids[d.ExternalID] = id
		}
		rows = append(rows, &model.Leaderboard{
			ID: id, SongHash: d.SongHash, SongName: d.SongName, SongSubName: d.SongSubName, SongAuthor: d.SongAuthor,
			Mapper: d.Mapper, Difficulty: d.Difficulty, DifficultyRaw: d.DifficultyRaw, GameMode: d.GameMode,
			CoverURL: d.CoverURL, Status: d.Status, Stars: d.Stars, MaxScore: d.MaxScore,
			Platform: platformName, ExternalID: d.ExternalID, MapKey: platform.MapKey(d.SongHash, d.GameMode, d.Difficulty),
		})
	}
	if err := lb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}}, UpdateAll: true,
	}).CreateInBatches(rows, 100); err != nil {
		return nil, fmt.Errorf("upsert leaderboards: %w", err)
	}
	return ids, nil
}

func existingPlays(ctx context.Context, tx *query.Query, platformName string, plays []platform.Play) (map[string]*model.Score, error) {
	q := tx.Score
	ext := make([]string, 0, len(plays))
	for _, pl := range plays {
		ext = append(ext, pl.ExternalID)
	}
	rows, err := q.WithContext(ctx).Where(q.Platform.Eq(platformName), q.ExternalID.In(ext...)).Find()
	if err != nil {
		return nil, fmt.Errorf("load existing scores: %w", err)
	}
	out := make(map[string]*model.Score, len(rows))
	for _, r := range rows {
		out[r.Kind+"|"+r.ExternalID] = r
	}
	return out, nil
}

func refreshPlay(ctx context.Context, tx *query.Query, old *model.Score, pl platform.Play, res *UpsertResult) error {
	upd := map[string]any{"rank": pl.Rank, "pp": pl.PP, "personal_best": pl.PersonalBest, "has_replay": pl.HasReplay || old.HasReplay}
	if pl.HasReplay && old.ReplayState == model.ReplayNone {
		upd["replay_state"] = model.ReplayPending
		res.NewReplays++
	}
	if pl.ReplayURL != "" && old.ReplayState != model.ReplayArchived {
		upd["replay_url"] = pl.ReplayURL
	}
	if _, err := tx.Score.WithContext(ctx).Where(tx.Score.ID.Eq(old.ID)).Updates(upd); err != nil {
		return fmt.Errorf("update score %d: %w", old.ID, err)
	}
	return nil
}

func playRow(playerID, platformName string, id, lbID int64, pl platform.Play) *model.Score {
	state := model.ReplayNone
	if pl.HasReplay {
		state = model.ReplayPending
	}
	var url *string
	if pl.ReplayURL != "" {
		url = &pl.ReplayURL
	}
	return &model.Score{
		ID: id, PlayerID: playerID, LeaderboardID: lbID, Rank: pl.Rank,
		ModifiedScore: pl.ModifiedScore, UnmodifiedScore: pl.UnmodifiedScore, Accuracy: pl.Accuracy, PP: pl.PP,
		Mods: pl.Mods, FullCombo: pl.FullCombo, MissedNotes: pl.MissedNotes, BadCuts: pl.BadCuts,
		MaxCombo: pl.MaxCombo, HMD: pl.HMD, PersonalBest: pl.PersonalBest, SetAt: pl.SetAt.UTC(),
		HasReplay: pl.HasReplay, ReplayState: state,
		Platform: platformName, Kind: pl.Kind, EndType: pl.EndType, EndTime: pl.EndTime, ExternalID: pl.ExternalID, ReplayURL: url,
	}
}
```

(imports in `scores.go`: `errors`, `strconv`, `gorm.io/gorm/clause`, `internal/db/query`, `internal/model`, `internal/platform`.)

`internal/service/replays.go` — `MarkReplayGone(ctx context.Context, id int64, reason string) error` stores `reason` in `last_error`.

- [ ] **Step 5: Switch the worker to adapters**

`internal/archiver/worker.go`:
- delete the `Client` and `LimiterSource` interfaces and the `client`/`limiter` fields;
- `func New(svc *service.Service) *Worker { return &Worker{svc: svc, status: Status{State: StateIdle, Since: svc.Now()}} }`;
- replace `Status`/`Status()`:

```go
// LimiterStatus is one platform limiter's state, for the status page and API.
type LimiterStatus struct {
	Platform string // display name
	Name     string
	Snapshot platform.LimiterSnapshot
}

type Status struct {
	State    State
	Task     string
	Since    time.Time
	Limiter  platform.LimiterSnapshot // the legacy platform's limiter (API windows/blocked_until)
	Limiters []LimiterStatus
}

// Status returns a snapshot for the UI/API.
func (w *Worker) Status() Status {
	w.mu.RLock()
	st := w.status
	w.mu.RUnlock()
	reg := w.svc.Platforms()
	if reg == nil {
		return st
	}
	legacySet := false
	for _, p := range reg.All() {
		for _, l := range p.Adapter.Limiters() {
			snap := l.Snapshot()
			st.Limiters = append(st.Limiters, LimiterStatus{Platform: p.DisplayName, Name: l.Name(), Snapshot: snap})
			if p.Legacy && !legacySet {
				st.Limiter, legacySet = snap, true
			}
			if st.State == StateRunning && snap.Waiting {
				st.State = StateRateLimited
			}
		}
	}
	return st
}

func (w *Worker) platform(name string) (platform.Platform, error) {
	p, ok := w.svc.Platforms().Get(name)
	if !ok {
		return platform.Platform{}, fmt.Errorf("archiver: unknown platform %q", name)
	}
	return p, nil
}
```

`internal/archiver/poll.go` — in `poll` and `backfill`, look the platform up first (`p, err := w.platform(wf.Platform); if err != nil { return err }`) and replace the ScoreSaber calls:

```go
		pg, err := p.Adapter.FeedPage(ctx, wf.Feed, wf.ExternalID, page)
		if err != nil {
			return w.clientError(ctx, p, wf, err, true)
		}
		pagesRead, totalPages = page, pg.TotalPages
		if page == 1 && len(pg.Plays) > 0 && pg.Plays[0].Profile != nil {
			if err := w.svc.RefreshProfile(ctx, wf.PlayerID, wf.Platform, *pg.Plays[0].Profile); err != nil {
				return err
			}
		}
		res, err := w.svc.UpsertPlays(ctx, wf.PlayerID, wf.Platform, pg.Plays)
		if err != nil {
			return err
		}
		newScores += res.New
		newReplays += res.NewReplays
		if res.Known > 0 || len(pg.Plays) == 0 || page >= pg.TotalPages {
			reachedEnd = true
			break
		}
```

(In `backfill`: `pg, err := p.Adapter.FeedPage(ctx, wf.Feed, wf.ExternalID, page)`, `w.svc.UpsertPlays(ctx, wf.PlayerID, wf.Platform, pg.Plays)`, `total := pg.TotalPages`, `len(pg.Plays) == 0`.) The "new scores" event uses the feed's noun:

```go
func noun(kind string) string {
	if kind == model.KindScore {
		return "scores"
	}
	return kind + "s"
}
```

→ `fmt.Sprintf("%d new %s, %d with replays", newScores, noun(wf.Feed), newReplays)`.

`clientError` gains the platform and classifies with the shared sentinels:

```go
func (w *Worker) clientError(ctx context.Context, p platform.Platform, wf *service.WorkFeed, err error, polling bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	k := wf.Key()
	switch {
	case errors.Is(err, platform.ErrRateLimited):
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindRateLimit, "rate limited by "+p.DisplayName+"; waiting for the limit to reset"))
		return nil
	case errors.Is(err, platform.ErrNotFound):
		msg := "player not found on " + p.DisplayName + "; tracking of this account disabled"
		if err := w.svc.MarkIdentityError(ctx, wf.PlayerID, wf.Platform, msg, true); err != nil {
			return err
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelError, model.KindPoll, msg))
		return nil
	}
	// … unchanged from Task 3 (poll failure / deferred backfill)
}
```

`internal/archiver/download.go` — fetch through the adapter:

```go
	p, err := w.platform(sc.Platform)
	if err != nil {
		return err
	}
	ref := platform.ReplayRef{Kind: sc.Kind, ExternalID: sc.ExternalID}
	if sc.ReplayURL != nil {
		ref.URL = *sc.ReplayURL
	}
	body, err := p.Adapter.Replay(ctx, ref)
```

and in the error switch use `platform.ErrRateLimited` ("rate limited by " + p.DisplayName + …) and `platform.ErrNotFound` → `w.svc.MarkReplayGone(ctx, sc.ID, "replay no longer available on "+p.DisplayName)` with the event `"replay no longer available on " + p.DisplayName`. (`Store().Put` stays until Task 7.)

- [ ] **Step 6: Wire the app, web and API**

`internal/app/app.go`:

```go
	limiter := scoresaber.NewLimiter(cfg.HourlyBudget)
	var copts []scoresaber.Option
	if opts.ScoreSaberURL != "" {
		copts = append(copts, scoresaber.WithBaseURL(opts.ScoreSaberURL))
	}
	reg, err := platform.NewRegistry(scoresaber.NewPlatform(scoresaber.NewClient(limiter, copts...), limiter))
	if err != nil {
		_ = db.Close(gdb)
		return nil, fmt.Errorf("app: platforms: %w", err)
	}
	svc := service.New(gdb, store, reg)
	worker := archiver.New(svc)
```

`internal/web/views/admin.templ` — `PlayerPreview(platformName string, prof platform.Profile, tracked bool)`: use `prof.AvatarURL`, `prof.Name`, `prof.ExternalID`, `prof.Country`, and add a hidden platform field next to the hidden ref:

```templ
				<input type="hidden" name="ref" value={ prof.ExternalID }/>
				<input type="hidden" name="platform" value={ platformName }/>
```

(Replace the `scoresaber` import with `internal/platform`.)

`internal/web/admin.go`:

```go
func (h *Handler) lookupPlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	plat, prof, err := h.svc.ResolvePlayer(r.Context(), r.PostFormValue("ref"), r.PostFormValue("platform"))
	switch {
	case errors.Is(err, service.ErrInvalidPlayerRef):
		render(w, r, http.StatusOK, views.FormError(sentence(err)))
		return
	case isNotFound(err):
		render(w, r, http.StatusOK, views.FormError("No "+plat.DisplayName+" player found for that ID."))
		return
	case err != nil:
		render(w, r, http.StatusOK, views.FormError(plat.DisplayName+" could not be reached: "+err.Error()))
		return
	}
	_, gerr := h.svc.PlayerByIdentity(r.Context(), plat.Name, prof.ExternalID)
	render(w, r, http.StatusOK, views.PlayerPreview(plat.Name, prof, gerr == nil))
}
```

and in `addPlayer`: `p, err := h.svc.AddPlayer(r.Context(), r.PostFormValue("ref"), r.PostFormValue("platform"))`.

`internal/api/players.go` — `AddPlayerInput.Body` gains

```go
		Platform string `json:"platform,omitempty" maxLength:"32" doc:"Platform of a bare ID (default: scoresaber). Profile URLs pick their platform."`
```

and the handler calls `a.svc.AddPlayer(ctx, in.Body.Ref, in.Body.Platform)`.

`internal/api/dto.go` — `Identity` gains `ProfileURL string \`json:"profile_url"\``; `playerDTO` takes the registry (`func playerDTO(base string, reg *platform.Registry, p service.PlayerSummary) Player`) and fills it with `if pl, ok := reg.Get(id.Platform); ok { dto.ProfileURL = pl.ProfileURL(id.ExternalID) }`. Callers pass `a.svc.Platforms()`.

- [ ] **Step 7: Rework the test fakes**

`internal/testutil/fakes.go` — make `Resolver` a fake `scoresaber.API` and add the shared player map:

```go
// DefaultPlayers are the ScoreSaber accounts the fakes know.
func DefaultPlayers() map[string]scoresaber.Player {
	return map[string]scoresaber.Player{
		"1001": {ID: "1001", Name: "Alice", Country: "FR", Avatar: "https://cdn.scoresaber.com/avatars/1001.jpg"},
		"1002": {ID: "1002", Name: "Bob", Country: "US", Avatar: "https://cdn.scoresaber.com/avatars/1002.jpg"},
	}
}

// Scores returns an empty listing (service tests drive upserts directly).
func (r *Resolver) Scores(context.Context, string, int) (scoresaber.ScorePage, error) {
	return scoresaber.ScorePage{}, nil
}

func (r *Resolver) Replay(_ context.Context, id int64) (io.ReadCloser, error) {
	return nil, fmt.Errorf("%w: replay %d", scoresaber.ErrNotFound, id)
}
```

`internal/testutil/service.go`:

```go
// NewServiceWithDB is NewService that also returns the database.
func NewServiceWithDB(t testing.TB) (*service.Service, *gorm.DB, *Resolver, *Clock) {
	t.Helper()
	res := &Resolver{Players: DefaultPlayers()}
	svc, gdb, clk := NewServiceWith(t, scoresaber.NewPlatform(res, nil))
	return svc, gdb, res, clk
}

// NewServiceWith builds a test service over the given platforms. Player IDs
// are p00000000001, p00000000002, … in creation order.
func NewServiceWith(t testing.TB, plats ...platform.Platform) (*service.Service, *gorm.DB, *Clock) {
	t.Helper()
	gdb := OpenDB(t)
	store, err := storage.New(filepath.Join(t.TempDir(), "replays"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := platform.NewRegistry(plats...)
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(gdb, store, reg)
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	clk := NewClock(T0)
	svc.SetClock(clk.Now)
	var n atomic.Int64
	svc.SetIDGenerator(func() string { return fmt.Sprintf("p%011d", n.Add(1)) })
	return svc, gdb, clk
}

// AddPlayer adds a player by reference (platform auto-detected) and returns its ID.
func AddPlayer(t testing.TB, svc *service.Service, ref string) string {
	t.Helper()
	p, err := svc.AddPlayer(context.Background(), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// Upsert stores ScoreSaber score items for a player.
func Upsert(t testing.TB, svc *service.Service, playerID string, items ...scoresaber.ScoreItem) service.UpsertResult {
	t.Helper()
	res, err := svc.UpsertPlays(context.Background(), playerID, model.PlatformScoreSaber, scoresaber.Plays(items))
	if err != nil {
		t.Fatal(err)
	}
	return res
}
```

- [ ] **Step 8: Port the tests**

- Every `svc.AddPlayer(ctx, ref)` → `svc.AddPlayer(ctx, ref, "")` (or `testutil.AddPlayer`).
- Every `svc.UpsertScores(ctx, pid, items)` → `svc.UpsertPlays(ctx, pid, model.PlatformScoreSaber, scoresaber.Plays(items))` or `testutil.Upsert(t, svc, pid, items...)` (service `helpers_test.go` `upsert`, `replays_test.go` `seedQueue`, archiver `download_test.go` `ready`, web `env_test.go` `seed`).
- `svc.UpdatePlayerProfile(ctx, id, scoresaber.Player{Name: "Alice2", Avatar: "a.jpg"})` → `platform.Profile{Name: "Alice2", AvatarURL: "a.jpg"}`.
- `svc.MarkReplayGone(ctx, id)` → `svc.MarkReplayGone(ctx, id, "replay no longer available on ScoreSaber")`.
- `internal/service/players_test.go` `TestParsePlayerRef` — delete (moved to `scoresaber.TestParseRefs` and `platform.TestParseRef`); `ResolvePlayer` callers take the extra platform argument and the `(platform, profile, error)` result.
- `internal/archiver/fake_test.go`:
  - `fakeClient` gains a resolver method:

    ```go
    func (f *fakeClient) Player(_ context.Context, id string) (scoresaber.Player, error) {
    	if p, ok := testutil.DefaultPlayers()[id]; ok {
    		return p, nil
    	}
    	return scoresaber.Player{ID: id, Name: "Player " + id}, nil
    }
    ```

  - `newEnv`: `svc, _, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil))` and `w: archiver.New(svc)`.
- `TestLegacyPlayerWithDifferentAccountID` (Task 5): `svc, gdb, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil))`, `archiver.New(svc)`.
- `TestStatusReportsRateLimited` → uses a real limiter in the registry:

```go
func TestStatusReportsRateLimited(t *testing.T) {
	l := scoresaber.NewLimiter(300)
	l.Observe(http.Header{"Retry-After": {"60"}}, http.StatusTooManyRequests)
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(newFake(), l))
	st := archiver.New(svc).Status()
	if st.State != archiver.StateIdle || len(st.Limiters) != 1 || st.Limiters[0].Platform != "ScoreSaber" || st.Limiter.BlockedUntil.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}
```

  (`Observe` with a 429 and `Retry-After: 60` blocks for 60 s, so `Snapshot().BlockedUntil` is set. Delete the old `fakeLimiter` type; import `net/http`.)

- [ ] **Step 9: Full suite, lint**

Run: `go test -race ./... && make lint`
Expected: all `ok`, `0 issues.` `internal/app` e2e tests exercise the real client through the adapter against the fake ScoreSaber server.

- [ ] **Step 10: Commit**

```bash
git add -A internal
git commit -m "refactor: route all platform access through the registry and adapters"
```

---

## Task 7: Per-platform replay storage + genericity test (fake third platform)

Non-legacy platforms get internal row IDs (≥ `platform.InternalIDBase`) and replay files under `replays/{player}/{platform}/{rowID}{ext}`; the legacy layout is untouched. A scripted fake platform proves the whole pipeline is platform-agnostic.

**Files:**
- Modify: `internal/storage/storage.go` (`Loc` API), `internal/storage/storage_test.go`
- Modify: `internal/service/scores.go` (internal ID range), `internal/service/replays.go` (`PutReplay`, `OpenReplay`, `replayLoc`), `internal/service/reconcile.go`
- Modify: `internal/archiver/download.go`, `internal/web/replay.go`, `internal/web/env_test.go` (seed uses `PutReplay`)
- Create: `internal/testutil/fakeplatform.go`
- Test: `internal/service/scores_test.go`, `internal/service/reconcile_test.go`, `internal/archiver/generic_test.go` (new)

**Interfaces:**
- Consumes: Task 6 registry/adapters.
- Produces:
  - `storage.Loc{PlayerID, Dir string; RowID int64; Ext string}`; `(*Store).Path(Loc) string`, `Put(Loc, io.Reader) (int64, string, error)`, `Open(Loc) (*os.File, error)`, `Remove(Loc) error`, `RemovePlayer(playerID) error` (unchanged); `storage.Entry{Loc; Path string}`; `Scan() ([]Entry, int, error)`
  - `(*Service).PutReplay(sc *model.Score, r io.Reader) (int64, string, error)`, `(*Service).OpenReplay(sc *model.Score) (*os.File, error)`, `(*Service).ReplayPath(sc *model.Score) (string, error)`
  - `testutil.FakePlatform` (`NewFakePlatform()`, `.Platform() platform.Platform`, `.SetPlays(kind, account string, plays ...platform.Play)`, fields `Profiles`, `Replays`, `Limiter *FakeLimiter`), `testutil.FakeLimiter` (`Block(until time.Time)`), `testutil.FakePlay(kind, externalID, lbExternalID string, setAt time.Time, replay bool) platform.Play` — platform `testplat`, slug `tp`, ext `.tpr`, URLs `https://tp.example/u/{id}`

- [ ] **Step 1: Add the fake platform `internal/testutil/fakeplatform.go`**

```go
package testutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
)

// FakePlatform is a scripted non-legacy platform ("testplat", slug "tp") for
// genericity tests (spec §9): a required score feed and an optional attempt
// feed that needs access, string IDs, replays fetched by URL.
type FakePlatform struct {
	mu       sync.Mutex
	PerPage  int
	Profiles map[string]platform.Profile
	plays    map[string][]platform.Play // kind + "/" + account, newest first
	Replays  map[string][]byte          // replay URL → bytes
	Calls    []string                   // "kind:account:page"
	Limiter  *FakeLimiter
}

func NewFakePlatform() *FakePlatform {
	return &FakePlatform{
		PerPage:  2,
		Profiles: map[string]platform.Profile{"abc": {ExternalID: "abc", Name: "Tess", Country: "SE", AvatarURL: "https://img.tp.example/abc.png"}},
		plays:    map[string][]platform.Play{},
		Replays:  map[string][]byte{},
		Limiter:  &FakeLimiter{},
	}
}

var (
	tpURLRe = regexp.MustCompile(`^https://tp\.example/u/([a-z0-9]{1,16})$`)
	tpIDRe  = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
)

// Platform is the registry entry.
func (f *FakePlatform) Platform() platform.Platform {
	return platform.Platform{
		Name: "testplat", Slug: "tp", DisplayName: "TestPlat", Priority: 50, ReplayExt: ".tpr",
		ImageHosts: []string{"https://img.tp.example"},
		ProfileURL: func(id string) string { return "https://tp.example/u/" + id },
		ParseURL: func(in string) (string, bool) {
			m := tpURLRe.FindStringSubmatch(in)
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
				LinkText: "Open settings", LinkURL: "https://tp.example/settings",
			}},
		},
		Adapter: f,
	}
}

// SetPlays scripts a feed listing (newest first).
func (f *FakePlatform) SetPlays(kind, account string, plays ...platform.Play) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plays[kind+"/"+account] = plays
	for _, p := range plays {
		if p.HasReplay {
			f.Replays[p.ReplayURL] = []byte("tp-replay-" + p.ExternalID)
		}
	}
}

func (f *FakePlatform) Resolve(_ context.Context, id string) (platform.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.Profiles[id]
	if !ok {
		return platform.Profile{}, fmt.Errorf("%w: testplat player %s", platform.ErrNotFound, id)
	}
	return p, nil
}

func (f *FakePlatform) FeedPage(_ context.Context, kind, account string, page int) (platform.PlayPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, fmt.Sprintf("%s:%s:%d", kind, account, page))
	all := f.plays[kind+"/"+account]
	total := (len(all) + f.PerPage - 1) / f.PerPage
	var out []platform.Play
	if start := (page - 1) * f.PerPage; start < len(all) {
		out = all[start:min(start+f.PerPage, len(all))]
	}
	return platform.PlayPage{Plays: out, TotalPages: total}, nil
}

func (f *FakePlatform) ProbeAccess(context.Context, string, string) (string, int64, error) {
	return model.AccessPublic, 0, nil
}

func (f *FakePlatform) Replay(_ context.Context, ref platform.ReplayRef) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.Replays[ref.URL]
	if !ok {
		return nil, fmt.Errorf("%w: %s", platform.ErrNotFound, ref.URL)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *FakePlatform) Limiters() []platform.Limiter { return []platform.Limiter{f.Limiter} }
func (f *FakePlatform) FeedLimiter(string) string    { return f.Limiter.Name() }
func (f *FakePlatform) ReplayLimiter(string) string  { return f.Limiter.Name() }

// FakeLimiter is ready unless blocked.
type FakeLimiter struct {
	mu    sync.Mutex
	until time.Time
}

func (l *FakeLimiter) Name() string { return "testplat" }

func (l *FakeLimiter) Block(until time.Time) {
	l.mu.Lock()
	l.until = until
	l.mu.Unlock()
}

func (l *FakeLimiter) Ready(now time.Time) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.until.After(now) {
		return false, l.until
	}
	return true, time.Time{}
}

func (l *FakeLimiter) Snapshot() platform.LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return platform.LimiterSnapshot{BlockedUntil: l.until}
}

// FakePlay builds a testplat play on leaderboard lbExternalID.
func FakePlay(kind, externalID, lbExternalID string, setAt time.Time, replay bool) platform.Play {
	p := platform.Play{
		Leaderboard: platform.LeaderboardData{
			ExternalID: lbExternalID, SongHash: "HASH-" + lbExternalID, SongName: "Song " + lbExternalID,
			Difficulty: 7, DifficultyRaw: "Expert", GameMode: "Standard", Status: "UNRANKED", MaxScore: 1000,
		},
		Kind: kind, EndType: model.EndClear, ExternalID: externalID, ModifiedScore: 900, Accuracy: 0.9,
		PersonalBest: kind == model.KindScore, SetAt: setAt, HasReplay: replay,
	}
	if replay {
		p.ReplayURL = "https://tp.example/replays/" + externalID + ".tpr"
	}
	return p
}
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/service/scores_test.go`:

```go
func TestUpsertPlaysAllocatesInternalIDs(t *testing.T) {
	fp := testutil.NewFakePlatform()
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(&testutil.Resolver{Players: testutil.DefaultPlayers()}, nil), fp.Platform())
	ctx := context.Background()
	id := testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	at := testutil.T0.Add(-time.Hour)
	first := []platform.Play{
		testutil.FakePlay(model.KindScore, "s2", "lb-x", at.Add(time.Minute), true),
		testutil.FakePlay(model.KindScore, "s1", "lb-x", at, false),
	}
	res, err := svc.UpsertPlays(ctx, id, "testplat", first)
	if err != nil || res.New != 2 || res.NewReplays != 1 {
		t.Fatalf("first upsert = %+v %v", res, err)
	}
	list, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: id})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("list = %+v %v", list, err)
	}
	byExt := map[string]*model.Score{}
	for _, s := range list.Items {
		byExt[s.ExternalID] = s
		if s.ID < platform.InternalIDBase || s.Platform != "testplat" || s.LeaderboardID < platform.InternalIDBase {
			t.Fatalf("row = %+v", s)
		}
	}
	if byExt["s2"].ID != byExt["s1"].ID+1 && byExt["s1"].ID != byExt["s2"].ID+1 {
		t.Fatalf("IDs must be sequential: %d %d", byExt["s1"].ID, byExt["s2"].ID)
	}
	if lb := byExt["s1"].Leaderboard; lb.ExternalID != "lb-x" || lb.MapKey != "hash-lb-x/Standard/7" {
		t.Fatalf("leaderboard = %+v", lb)
	}
	// Re-upserting is idempotent and keeps IDs; new plays continue the sequence.
	again, err := svc.UpsertPlays(ctx, id, "testplat", append([]platform.Play{testutil.FakePlay(model.KindScore, "s3", "lb-y", at.Add(2*time.Minute), true)}, first...))
	if err != nil || again.New != 1 || again.Known != 2 {
		t.Fatalf("second upsert = %+v %v", again, err)
	}
	s3, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: id})
	if err != nil || len(s3.Items) != 3 || s3.Items[0].ID != max(byExt["s1"].ID, byExt["s2"].ID)+1 {
		t.Fatalf("after second upsert = %+v %v", s3.Items, err)
	}
	// The legacy platform keeps its own IDs.
	legacy := testutil.AddPlayer(t, svc, "1001")
	testutil.Upsert(t, svc, legacy, testutil.Item("1001", 77, 1077, at, true))
	if sc, err := svc.GetScore(ctx, 77); err != nil || sc.Platform != model.PlatformScoreSaber {
		t.Fatalf("legacy score = %+v %v", sc, err)
	}
}
```

Create `internal/archiver/generic_test.go`:

```go
package archiver_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// TestThirdPlatformEndToEnd is spec §9's genericity test: a platform that
// only exists in tests must work through link, poll, backfill, replay storage
// and reconciliation with no code outside its adapter.
func TestThirdPlatformEndToEnd(t *testing.T) {
	fp := testutil.NewFakePlatform()
	fc := newFake()
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil), fp.Platform())
	w := archiver.New(svc)
	ctx := context.Background()

	tess, err := svc.AddPlayer(ctx, "https://tp.example/u/abc", "") // URL picks the platform
	if err != nil {
		t.Fatal(err)
	}
	if tess.Name != "Tess" {
		t.Fatalf("profile not resolved: %+v", tess)
	}
	alice := testutil.AddPlayer(t, svc, "1001")
	fc.scores["1001"] = fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	var plays []platform.Play
	for i := range 5 { // 3 pages at PerPage 2
		plays = append(plays, testutil.FakePlay(model.KindScore, "s"+string(rune('a'+i)), "lb-"+string(rune('a'+i%2)), testutil.T0.Add(-time.Duration(i)*time.Minute), true))
	}
	fp.SetPlays(model.KindScore, "abc", plays...)

	for range 100 {
		did, err := w.Step(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			break
		}
	}

	c, _ := svc.PlayerCounts(ctx, tess.ID)
	if c.Scores != 5 || c.Archived != 5 {
		t.Fatalf("testplat counts = %+v", c)
	}
	if c, _ := svc.PlayerCounts(ctx, alice); c.Archived != 2 {
		t.Fatalf("the legacy platform must keep working alongside: %+v", c)
	}
	list, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: tess.ID, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range list.Items {
		if sc.ID < platform.InternalIDBase || sc.Platform != "testplat" {
			t.Fatalf("row = %+v", sc)
		}
		path, err := svc.ReplayPath(sc)
		if err != nil {
			t.Fatal(err)
		}
		want := svc.Store().Path(storageLoc(tess.ID, "testplat", sc.ID, ".tpr"))
		if path != want {
			t.Fatalf("replay path = %s, want %s", path, want)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != "tp-replay-"+sc.ExternalID {
			t.Fatalf("replay file %s: %q %v", path, b, err)
		}
	}
	if f := testutil.ScoreFeed(t, svc, tess.ID); f.BackfillState != model.BackfillDone {
		t.Fatalf("testplat backfill = %+v", f)
	}

	// Reconciliation understands the per-platform layout.
	victim := list.Items[0]
	path, _ := svc.ReplayPath(victim)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ReconcileStorage(ctx)
	if err != nil || res.Requeued != 1 || res.Orphans != 0 {
		t.Fatalf("reconcile = %+v %v", res, err)
	}
}
```

with a tiny helper in the same file:

```go
func storageLoc(player, dir string, row int64, ext string) storage.Loc {
	return storage.Loc{PlayerID: player, Dir: dir, RowID: row, Ext: ext}
}
```

(import `github.com/yyewolf/ssarchiver/internal/storage`.)

Update `internal/storage/storage_test.go` to the `Loc` API (every `s.Put(id, n, r)` → `s.Put(storage.Loc{PlayerID: id, RowID: n, Ext: ".dat"}, r)`; same for `Open`/`Remove`/`Path`) and add:

```go
func TestPlatformLayout(t *testing.T) {
	s, root := newStore(t)
	l := storage.Loc{PlayerID: "k7m2q9x4c1ab", Dir: "beatleader", RowID: 1 << 62, Ext: ".bsor"}
	if _, _, err := s.Put(l, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "k7m2q9x4c1ab", "beatleader", "4611686018427387904.bsor")
	if s.Path(l) != want {
		t.Fatalf("Path = %s", s.Path(l))
	}
	legacy := storage.Loc{PlayerID: "k7m2q9x4c1ab", RowID: 5, Ext: ".dat"}
	if _, _, err := s.Put(legacy, strings.NewReader("y")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "k7m2q9x4c1ab", "beatleader", "junk.txt"), []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := s.Scan()
	if err != nil || len(entries) != 2 {
		t.Fatalf("Scan = %+v %v", entries, err)
	}
	for _, e := range entries {
		if e.Path != s.Path(e.Loc) {
			t.Fatalf("entry %+v does not round-trip", e)
		}
	}
	for _, bad := range []storage.Loc{
		{PlayerID: "a", Dir: "../x", RowID: 1, Ext: ".dat"},
		{PlayerID: "a", Dir: "Bad", RowID: 1, Ext: ".dat"},
		{PlayerID: "a", RowID: 1, Ext: "dat"},
		{PlayerID: "a", RowID: 1, Ext: ".d/t"},
	} {
		if _, _, err := s.Put(bad, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidID) {
			t.Errorf("Put(%+v) err = %v", bad, err)
		}
	}
}
```

(`newStore` must return the root; if it does not, have it return `(store, root)`.)

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/storage/ ./internal/service/ ./internal/archiver/`
Expected: FAIL — `storage.Loc undefined`, `svc.ReplayPath undefined`, `non-legacy platforms are not supported yet`.

- [ ] **Step 4: Implement the storage `Loc` API**

`internal/storage/storage.go` (package doc: `// Package storage stores replay files on disk: {root}/{player}/{row}{ext} for the legacy platform, {root}/{player}/{platform}/{row}{ext} for the others (spec §5.5).`):

```go
var (
	dirRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)
	extRe = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
)

// Loc locates one replay file.
type Loc struct {
	PlayerID string
	Dir      string // platform directory; "" = legacy layout
	RowID    int64
	Ext      string // ".dat", ".bsor", …
}

func (l Loc) valid() bool {
	return ValidPlayerID(l.PlayerID) && (l.Dir == "" || dirRe.MatchString(l.Dir)) && extRe.MatchString(l.Ext)
}

func (s *Store) dir(l Loc) string {
	if l.Dir == "" {
		return filepath.Join(s.root, l.PlayerID)
	}
	return filepath.Join(s.root, l.PlayerID, l.Dir)
}

func (s *Store) Path(l Loc) string {
	return filepath.Join(s.dir(l), strconv.FormatInt(l.RowID, 10)+l.Ext)
}

type Entry struct {
	Loc
	Path string
}
```

`Put`, `Open`, `Remove` take a `Loc`: validate with `if !l.valid() { return …, fmt.Errorf("%w: %+v", ErrInvalidID, l) }`, use `dir := s.dir(l)` for `MkdirAll` and the directory fsync, and `s.Path(l)` for the file. The body of `Put` is otherwise unchanged.

`Scan` — keep the `.tmp` removal; then classify files by their path relative to the root:

```go
		rel, rerr := filepath.Rel(s.root, path)
		if rerr != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		var l Loc
		switch len(parts) {
		case 2:
			l.PlayerID = parts[0]
		case 3:
			l.PlayerID, l.Dir = parts[0], parts[1]
		default:
			return nil
		}
		l.Ext = filepath.Ext(name)
		id, err := strconv.ParseInt(strings.TrimSuffix(name, l.Ext), 10, 64)
		if err != nil {
			return nil
		}
		l.RowID = id
		if l.valid() {
			entries = append(entries, Entry{Loc: l, Path: path})
		}
		return nil
```

- [ ] **Step 5: Implement internal IDs, replay locations and reconciliation**

`internal/service/scores.go` — give `idAllocator` the internal range:

```go
// idAllocator hands out row IDs: legacy platforms use their own numeric IDs;
// the others get sequential internal IDs from platform.InternalIDBase
// (spec §4.5), looked up once per transaction.
type idAllocator struct {
	ctx            context.Context
	tx             *query.Query
	legacy         bool
	nextLB, nextSc int64
}

func (a *idAllocator) leaderboard(externalID string) (int64, error) {
	if a.legacy {
		return a.legacyID(externalID)
	}
	lb := a.tx.Leaderboard
	return a.next(&a.nextLB, func(out *[]int64) error {
		return lb.WithContext(a.ctx).Where(lb.ID.Gte(platform.InternalIDBase)).Order(lb.ID.Desc()).Limit(1).Pluck(lb.ID, out)
	})
}

func (a *idAllocator) score(externalID string) (int64, error) {
	if a.legacy {
		return a.legacyID(externalID)
	}
	q := a.tx.Score
	return a.next(&a.nextSc, func(out *[]int64) error {
		return q.WithContext(a.ctx).Where(q.ID.Gte(platform.InternalIDBase)).Order(q.ID.Desc()).Limit(1).Pluck(q.ID, out)
	})
}

func (a *idAllocator) next(cur *int64, maxID func(*[]int64) error) (int64, error) {
	if *cur == 0 {
		var ids []int64
		if err := maxID(&ids); err != nil {
			return 0, fmt.Errorf("service: allocate id: %w", err)
		}
		*cur = platform.InternalIDBase
		if len(ids) == 1 {
			*cur = ids[0] + 1
		}
	}
	id := *cur
	*cur++
	return id, nil
}

func (a *idAllocator) legacyID(externalID string) (int64, error) {
	id, err := strconv.ParseInt(externalID, 10, 64)
	if err != nil || id <= 0 || id >= platform.InternalIDBase {
		return 0, fmt.Errorf("service: invalid legacy id %q", externalID)
	}
	return id, nil
}
```

`internal/service/replays.go` — add:

```go
// replayLoc places a row's replay file (spec §5.5).
func (s *Service) replayLoc(sc *model.Score) (storage.Loc, error) {
	p, ok := s.reg.Get(sc.Platform)
	if !ok {
		return storage.Loc{}, fmt.Errorf("service: unknown platform %q", sc.Platform)
	}
	l := storage.Loc{PlayerID: sc.PlayerID, RowID: sc.ID, Ext: p.ReplayExt}
	if !p.Legacy {
		l.Dir = p.Name
	}
	return l, nil
}

// ReplayPath is where a row's replay file lives (or would live).
func (s *Service) ReplayPath(sc *model.Score) (string, error) {
	l, err := s.replayLoc(sc)
	if err != nil {
		return "", err
	}
	return s.store.Path(l), nil
}

// PutReplay stores a row's replay file.
func (s *Service) PutReplay(sc *model.Score, r io.Reader) (int64, string, error) {
	l, err := s.replayLoc(sc)
	if err != nil {
		return 0, "", err
	}
	return s.store.Put(l, r)
}

// OpenReplay opens a row's archived replay file.
func (s *Service) OpenReplay(sc *model.Score) (*os.File, error) {
	l, err := s.replayLoc(sc)
	if err != nil {
		return nil, err
	}
	return s.store.Open(l)
}
```

`internal/service/reconcile.go` — match files to rows by their expected path:

```go
func (s *Service) ReconcileStorage(ctx context.Context) (ReconcileResult, error) {
	var res ReconcileResult
	entries, removed, err := s.store.Scan()
	if err != nil {
		return res, err
	}
	res.RemovedTmp = removed
	onDisk := make(map[string]storage.Entry, len(entries))
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		onDisk[e.Path] = e
		ids = append(ids, e.RowID)
	}
	q := s.q.Score
	matched := map[string]bool{}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		rows, err := q.WithContext(ctx).Where(q.ID.In(chunk...)).Find()
		if err != nil {
			return res, fmt.Errorf("service: reconcile load: %w", err)
		}
		for _, sc := range rows {
			path, err := s.ReplayPath(sc)
			if err != nil {
				continue // row of a platform that is no longer registered
			}
			e, ok := onDisk[path]
			if !ok {
				continue
			}
			matched[path] = true
			if sc.ReplayState == model.ReplayArchived {
				continue
			}
			size, sum, err := storage.HashFile(e.Path)
			if err != nil {
				slog.Warn("reconcile: hash failed", "path", e.Path, "err", err)
				continue
			}
			if err := s.MarkReplayArchived(ctx, sc.ID, size, sum); err != nil {
				return res, err
			}
			res.Adopted++
		}
	}
	res.Orphans = len(onDisk) - len(matched)
	archived, err := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayArchived)).Find()
	if err != nil {
		return res, fmt.Errorf("service: reconcile archived: %w", err)
	}
	for _, sc := range archived {
		path, err := s.ReplayPath(sc)
		if err != nil {
			continue
		}
		if _, ok := onDisk[path]; ok {
			continue
		}
		if err := s.updateScore(ctx, sc.ID, map[string]any{
			"replay_state": model.ReplayPending, "replay_size": 0, "replay_sha256": "", "archived_at": nil,
			"attempts": 0, "next_attempt_at": nil, "last_error": "file missing on disk; re-downloading",
		}); err != nil {
			return res, err
		}
		res.Requeued++
	}
	return res, nil
}
```

(The archived scan now loads full rows instead of plucking IDs; replay counts are in the tens of thousands at most, which is fine for a startup task.)

Callers: `internal/archiver/download.go` → `size, sum, perr := w.svc.PutReplay(sc, body)`; `internal/web/replay.go` → `f, err := h.svc.OpenReplay(sc)`; `internal/web/env_test.go` `seed` and `internal/api/api_test.go` `seed` → load score 1 with `svc.GetScore(ctx, 1)` and call `svc.PutReplay(sc, strings.NewReader(…same bytes as before…))`; existing storage-path assertions in `internal/service/reconcile_test.go`/`internal/archiver/download_test.go` switch to `svc.ReplayPath(sc)` or `Store().Path(storage.Loc{PlayerID: id, RowID: n, Ext: ".dat"})`.

- [ ] **Step 6: Run the tests**

Run: `go test -race ./... && make lint`
Expected: all `ok`, `0 issues.` `TestThirdPlatformEndToEnd` passes with no testplat-specific code outside `internal/testutil`.

- [ ] **Step 7: Commit**

```bash
git add -A internal
git commit -m "feat: per-platform replay storage and internal IDs; prove genericity with a fake platform"
```

---

## Task 8: Limiter readiness, multi-limiter status, account links

The worker stops blocking on a busy platform (spec §5.1), the sync page shows every platform's limiter, and readable account links `/p/{slug}/{externalID}` redirect to the opaque player page.

**Files:**
- Modify: `internal/service/feeds.go` (`NextPollAt` busy-aware)
- Modify: `internal/archiver/worker.go` (`busy`, `Step`, `idleFor`), `internal/archiver/export_test.go` (new)
- Modify: `internal/web/web.go` (route), `internal/web/public.go` (`accountLink`), `internal/web/views/sync.templ` (budget card)
- Modify: `internal/api/sync.go` (`limiters[]`), `internal/api/players.go` (`get-player-by-account`)
- Test: `internal/archiver/readiness_test.go` (new), `internal/service/feeds_test.go`, `internal/web/public_test.go`, `internal/web/admin_sync_test.go`, `internal/api/api_test.go`

**Interfaces:**
- Consumes: Task 6 `Platform.Adapter.{Limiters,FeedLimiter,ReplayLimiter}`, `platform.Limiter.Ready`; Task 7 `testutil.FakePlatform`/`FakeLimiter`.
- Produces:
  - `(*Service).NextPollAt(ctx, interval time.Duration, busy Busy) (time.Time, bool, error)`
  - `archiver.(*Worker).busy() (feeds, replays service.Busy, wake time.Time)`
  - `archiver.IdleFor` (test export of `(*Worker).idleFor`)
  - route `GET /p/{slug}/{externalID}` → 301 `/p/{id}`; API `GET /api/v1/players/by/{platform}/{externalID}` (operation `get-player-by-account`)
  - API `SyncStatus.Limiters []Limiter` (`{platform, name, blocked_until?, windows[]}`)

- [ ] **Step 1: Write the failing tests**

`internal/archiver/export_test.go`:

```go
package archiver

// IdleFor exposes idleFor to archiver_test.
var IdleFor = (*Worker).idleFor
```

`internal/archiver/readiness_test.go`:

```go
package archiver_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestBusyPlatformIsSkipped(t *testing.T) {
	fp := testutil.NewFakePlatform()
	fc := newFake()
	svc, _, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil), fp.Platform())
	w := archiver.New(svc)
	ctx := context.Background()
	tess := testutil.AddPlayer(t, svc, "https://tp.example/u/abc") // p00000000001: would be polled first
	testutil.AddPlayer(t, svc, "1001")
	fp.SetPlays(model.KindScore, "abc", testutil.FakePlay(model.KindScore, "s1", "lb-a", testutil.T0.Add(-time.Hour), true))
	fc.scores["1001"] = fc.history("1001", 1, 1, testutil.T0.Add(-time.Hour))
	fp.Limiter.Block(testutil.T0.Add(30 * time.Second))

	if did, err := w.Step(ctx); err != nil || !did {
		t.Fatalf("step = %v %v", did, err)
	}
	if len(fp.Calls) != 0 {
		t.Fatalf("busy testplat must not be called, calls = %v", fp.Calls)
	}
	if got := fc.calls(); len(got) == 0 || got[0] != "1001:1" {
		t.Fatalf("ScoreSaber must be polled while testplat is busy, calls = %v", got)
	}
	clk.Advance(31 * time.Second)
	for range 20 {
		if did, _ := w.Step(ctx); !did {
			break
		}
	}
	if f := testutil.ScoreFeed(t, svc, tess); f.LastPolledAt == nil {
		t.Fatal("testplat must be polled once its limiter is ready")
	}
}

func TestIdleWakesAtLimiterReadiness(t *testing.T) {
	fp := testutil.NewFakePlatform()
	svc, _, _ := testutil.NewServiceWith(t, scoresaber.NewPlatform(newFake(), nil), fp.Platform())
	w := archiver.New(svc)
	ctx := context.Background()
	testutil.AddPlayer(t, svc, "https://tp.example/u/abc")
	fp.Limiter.Block(testutil.T0.Add(20 * time.Second))
	if did, _ := w.Step(ctx); did {
		t.Fatal("the only due feed is busy: no work")
	}
	if got := archiver.IdleFor(w, ctx); got != 20*time.Second {
		t.Fatalf("idle = %v, want the 20s until the limiter is ready (not a 1s spin)", got)
	}
}
```

Append to `internal/service/feeds_test.go` (inside `TestDueFeed`, after the busy `DueFeed` check, or as its own test):

```go
func TestNextPollAtSkipsBusy(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	busy := service.Busy{{Platform: model.PlatformScoreSaber, Kind: model.KindScore}: true}
	if _, ok, err := svc.NextPollAt(ctx, time.Minute, busy); err != nil || ok {
		t.Fatalf("busy feeds must not count: ok=%v err=%v", ok, err)
	}
	if at, ok, _ := svc.NextPollAt(ctx, time.Minute, nil); !ok || !at.Equal(testutil.T0) {
		t.Fatalf("NextPollAt = %v %v", at, ok)
	}
}
```

Update the existing `NextPollAt(ctx, 10*time.Minute)` calls to pass `nil`.

`internal/web/public_test.go`:

```go
func TestAccountLinkRedirects(t *testing.T) {
	e := newEnv(t)
	e.seed()
	res := e.do(http.MethodGet, "/p/ss/1001", nil)
	if res.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d", res.Code)
	}
	id := e.playerID("1001")
	if loc := res.Header().Get("Location"); loc != "/p/"+id {
		t.Fatalf("Location = %q, want /p/%s", loc, id)
	}
	for _, path := range []string{"/p/ss/9999", "/p/zz/1001"} {
		if got := e.do(http.MethodGet, path, nil).Code; got != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, got)
		}
	}
}
```

(Add to `env_test.go`: `func (e *testEnv) playerID(account string) string { p, err := e.svc.PlayerByIdentity(context.Background(), model.PlatformScoreSaber, account); if err != nil { e.t.Fatal(err) }; return p.ID }` — if Task 5 already stored the seeded ID on `testEnv`, use that instead.)

`internal/web/admin_sync_test.go` `TestSyncPage` — set the status with a `Limiters` entry and assert the per-platform title:

```go
	snap := scoresaber.LimiterSnapshot{Windows: []scoresaber.WindowSnapshot{
		{Name: "short", Limit: 20, Used: 3, ServerRemaining: -1},
		{Name: "medium", Limit: 60, Used: 9, ServerRemaining: -1},
		{Name: "long", Limit: 300, Used: 120, ServerRemaining: 200},
	}}
	e.status.st = archiver.Status{
		State: archiver.StateRunning, Task: "Downloading replay 42 · Alice · Song", Since: testutil.T0,
		Limiter: snap, Limiters: []archiver.LimiterStatus{{Platform: "ScoreSaber", Name: "scoresaber", Snapshot: snap}},
	}
```

and add `"ScoreSaber budget"` to the `contains` list.

`internal/api/api_test.go` — add to `TestPublicReads`:

```go
	code, byAcc, _ := call(t, h, http.MethodGet, "/api/v1/players/by/scoresaber/1001", nil)
	if code != 200 || byAcc["name"] != "Alice" {
		t.Fatalf("by-account lookup = %d %v", code, byAcc)
	}
	if code, _, _ := call(t, h, http.MethodGet, "/api/v1/players/by/scoresaber/9999", nil); code != 404 {
		t.Fatalf("unknown account = %d", code)
	}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/archiver/ ./internal/service/ ./internal/web/ ./internal/api/`
Expected: FAIL — testplat polled while blocked, `IdleFor` returns 1s, `/p/ss/1001` 404, by-account 404.

- [ ] **Step 3: Implement readiness in the worker**

`internal/service/feeds.go` — `NextPollAt` skips busy feeds:

```go
// NextPollAt is when the next non-busy feed becomes due (ok=false: none).
func (s *Service) NextPollAt(ctx context.Context, interval time.Duration, busy Busy) (time.Time, bool, error) {
	feeds, err := s.workFeeds(ctx, " ORDER BY f.last_polled_at") // SQLite sorts NULL first
	if err != nil {
		return time.Time{}, false, err
	}
	for _, f := range feeds {
		if busy.has(f.Platform, f.Feed) {
			continue
		}
		if f.LastPolledAt == nil {
			return s.Now(), true, nil
		}
		return f.LastPolledAt.Add(interval), true, nil
	}
	return time.Time{}, false, nil
}
```

`internal/archiver/worker.go` — replace `busy` and use it in `Step` and `idleFor`:

```go
// busy lists, per (platform, kind), the feeds and replay downloads whose
// limiter is not ready, and the earliest time one becomes ready (spec §5.1).
func (w *Worker) busy() (feeds, replays service.Busy, wake time.Time) {
	reg := w.svc.Platforms()
	if reg == nil {
		return nil, nil, time.Time{}
	}
	now := w.svc.Now()
	feeds, replays = service.Busy{}, service.Busy{}
	for _, p := range reg.All() {
		notReady := map[string]bool{} // limiter name → not ready; unknown and "" names are ready
		for _, l := range p.Adapter.Limiters() {
			if ok, at := l.Ready(now); !ok {
				notReady[l.Name()] = true
				if wake.IsZero() || at.Before(wake) {
					wake = at
				}
			}
		}
		for _, f := range p.Feeds {
			pk := service.PlatformKind{Platform: p.Name, Kind: f.Kind}
			if notReady[p.Adapter.FeedLimiter(f.Kind)] {
				feeds[pk] = true
			}
			if notReady[p.Adapter.ReplayLimiter(f.Kind)] {
				replays[pk] = true
			}
		}
	}
	return feeds, replays, wake
}
```

In `Step`: `feedBusy, replayBusy, _ := w.busy()`; pass `feedBusy` to `DueFeed`/`NextBackfillFeed` and `replayBusy` to `NextReplay` (update `nextReplay`/`nextBackfill` parameter usage accordingly).

In `idleFor`:

```go
	feedBusy, _, wake := w.busy()
	if st, err := w.svc.Settings(ctx); err == nil {
		consider(w.svc.NextPollAt(ctx, st.PollInterval, feedBusy))
	}
	consider(w.svc.NextRetryAt(ctx))
	consider(wake, !wake.IsZero(), nil)
	return max(d, time.Second)
```

- [ ] **Step 4: Multi-limiter status page and API**

`internal/web/views/sync.templ` — the budget card loops over limiters (title per platform; keep the window rows exactly as today):

```templ
templ budgetCard(v SyncView) {
	for _, l := range v.Status.Limiters {
		@card.Card() {
			@card.Header() {
				@card.Title() {
					{ l.Platform } budget
				}
				@card.Description() {
					Requests sent by this instance in each { l.Platform } rate-limit window.
				}
			}
			@card.Content() {
				<div class="flex flex-col gap-4">
					for _, w := range l.Snapshot.Windows {
						<div class="flex flex-col gap-1.5">
							<div class="flex justify-between text-sm">
								<span>{ windowLabel(w.Name) }</span>
								<span class="tabular-nums text-muted-foreground">{ strconv.Itoa(w.Used) } / { strconv.Itoa(w.Limit) }</span>
							</div>
							@progress.Progress(progress.Props{Value: w.Used, Max: max(w.Limit, 1)})
							if w.ServerRemaining >= 0 {
								<p class="text-xs text-muted-foreground">{ l.Platform } reports { strconv.Itoa(w.ServerRemaining) } remaining</p>
							}
						</div>
					}
					if len(l.Snapshot.Windows) == 0 {
						<p class="text-sm text-muted-foreground">No requests sent yet.</p>
					}
				</div>
			}
		}
	}
}
```

and the status card's "asked us to wait" line loops too:

```templ
				for _, l := range v.Status.Limiters {
					if !l.Snapshot.BlockedUntil.IsZero() {
						<p class="text-muted-foreground">{ l.Platform } asked us to wait; resuming { Until(l.Snapshot.BlockedUntil, time.Now()) }.</p>
					}
				}
```

If the page lays cards out in a grid that expected exactly one budget card, wrap the loop in `<div class="flex flex-col gap-4">`. Run `make generate`.

`internal/api/sync.go` — add:

```go
type Limiter struct {
	Platform   string     `json:"platform" example:"ScoreSaber"`
	Name       string     `json:"name" example:"scoresaber"`
	BlockedTil *time.Time `json:"blocked_until,omitempty"`
	Windows    []Window   `json:"windows"`
}
```

with `Limiters []Limiter \`json:"limiters"\`` on `SyncStatus` (keep `windows`/`blocked_until` from the legacy `st.Limiter`, documented as "the ScoreSaber limiter; see limiters[]"), filled from `st.Limiters` with the same window conversion. `Window.Name` loses its `enum` tag (other platforms name windows differently).

- [ ] **Step 5: Account links**

`internal/web/web.go` — register after `GET /p/{id}`:

```go
	mux.HandleFunc("GET /p/{slug}/{externalID}", h.accountLink)
```

`internal/web/public.go`:

```go
// accountLink resolves a readable account URL (/p/ss/{id}) to the player's
// page; it keeps working through merges (spec §6.1).
func (h *Handler) accountLink(w http.ResponseWriter, r *http.Request) {
	p, ok := h.svc.Platforms().BySlug(r.PathValue("slug"))
	if !ok {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	pl, err := h.svc.PlayerByIdentity(r.Context(), p.Name, r.PathValue("externalID"))
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/p/"+url.PathEscape(pl.ID), http.StatusMovedPermanently)
}
```

`internal/api/players.go` — register:

```go
type AccountPath struct {
	Platform   string `path:"platform" maxLength:"32" example:"scoresaber"`
	ExternalID string `path:"externalID" maxLength:"64" example:"76561198038925092"`
}

	huma.Register(a.api, huma.Operation{
		OperationID: "get-player-by-account", Method: http.MethodGet, Path: "/api/v1/players/by/{platform}/{externalID}",
		Summary: "Find a player by a platform account", Tags: []string{"Players"},
	}, func(ctx context.Context, in *AccountPath) (*PlayerOutput, error) {
		pl, err := a.svc.PlayerByIdentity(ctx, in.Platform, in.ExternalID)
		if err != nil {
			return nil, mapErr(err)
		}
		dto, err := a.summary(ctx, pl.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: dto}, nil
	})
```

- [ ] **Step 6: Run the tests**

Run: `go test -race ./... && make lint`
Expected: all `ok`, `0 issues.`

- [ ] **Step 7: Commit**

```bash
git add -A internal
git commit -m "feat: skip rate-limited platforms, show every limiter, add account links"
```

---

## Task 9: Upgrade end-to-end test + docs

Proves a real v1 data directory boots on the new code and serves everything it served before, then documents the upgrade.

**Files:**
- Create: `internal/testutil/fixture.go` (`LoadSQLFile`)
- Modify: `internal/db/migrate_test.go` (use `testutil.LoadSQLFile`)
- Test: `internal/app/upgrade_test.go` (new)
- Modify: `README.md`

**Interfaces:**
- Consumes: everything above; `internal/db/testdata/v1.sql`.
- Produces: `testutil.LoadSQLFile(t, dbPath, sqlPath string)`.

- [ ] **Step 1: Move the fixture loader to testutil**

`internal/testutil/fixture.go`:

```go
package testutil

import (
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/db"
)

// LoadSQLFile creates dbPath from a one-statement-per-line SQL file
// (internal/db/testdata/v1.sql) without migrating it.
func LoadSQLFile(t testing.TB, dbPath, sqlPath string) {
	t.Helper()
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	gdb, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(gdb) }()
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if _, err := sqlDB.Exec(line); err != nil {
			t.Fatalf("fixture: %v\n%s", err, line)
		}
	}
}
```

In `internal/db/migrate_test.go`, `openFixture` becomes: build the path, `testutil.LoadSQLFile(t, path, "testdata/v1.sql")`, then `db.Open(path)` and return it.

- [ ] **Step 2: Write the upgrade test**

`internal/app/upgrade_test.go` (package `app_test`, like `app_test.go`; imports `context`, `net/http`, `net/http/httptest`, `os`, `path/filepath`, `strings`, `testing`, and `internal/{app,config,db,testutil}`):

```go
func TestUpgradeFromV1DataDir(t *testing.T) {
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
```

The score page renders without the viewer bundle, and setup is complete in the fixture (it has a user), so `requireSetup` lets the requests through. huma serialises struct fields in declaration order, so `"identities":[{"platform":"scoresaber","id":"…"` is stable; if it is not, decode the body and compare fields.

- [ ] **Step 3: Run it**

Run: `go test -race ./internal/app/ -run TestUpgradeFromV1DataDir -v`
Expected: PASS.

- [ ] **Step 4: Update the README**

Add an **Upgrading** subsection to the existing upgrade/operations part of `README.md`:

```markdown
### Upgrading to multi-platform (from versions before platform support)

The first start of this version migrates the database in place:

- A copy of the database is saved first as `ssarchiver.pre-platforms.db` in the data directory. Keep it until you are happy with the upgrade; to roll back, stop the new version and put it back as `ssarchiver.db`.
- The migration checks that no player, score or leaderboard row was lost and refuses to start otherwise (the error names the backup).
- Existing players, URLs (`/p/{id}`, `/s/{id}`, `/embed/{id}`, `/r/{id}.dat`) and replay files are unchanged.
- New players get an opaque ID (e.g. `/p/k7m2q9x4c1ab`). Readable links by account work for everyone: `/p/ss/{scoresaberID}` redirects to the player.
- API: players gain `identities[]` (one per linked platform account, with their sync `feeds[]`); the top-level `last_polled_at`, `last_error` and `backfill` fields are deprecated. `GET /api/v1/players/by/{platform}/{id}` finds a player by account. `GET /api/v1/sync` gains `limiters[]`.
- A ScoreSaber "player not found" now disables that account instead of the player; enabling the player again re-enables its accounts.
```

and a short contributor section:

```markdown
### Adding a platform

Platforms are registered in `internal/app/app.go`. A platform is a client package (like `internal/scoresaber`) that returns a `platform.Platform` descriptor: name/slug, replay extension, profile URL parsing, feeds, and an `Adapter` that resolves players, lists feed pages as `platform.Play`s, downloads replays and exposes its rate limiters. No migration is needed: platform names are plain strings in the database. `internal/testutil.FakePlatform` is a complete minimal example, exercised end to end by `internal/archiver/generic_test.go`.
```

- [ ] **Step 5: Final verification**

Run: `make generate && git diff --exit-code -- ':!docs' && go test -race ./... && make lint`
Expected: no codegen drift, all `ok`, `0 issues.`

- [ ] **Step 6: Commit**

```bash
git add -A internal README.md
git commit -m "test: upgrade a v1 data directory end to end; document the upgrade"
```
