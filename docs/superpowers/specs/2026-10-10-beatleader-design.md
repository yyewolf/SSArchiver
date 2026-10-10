# SSArchiver — Multi-platform support (BeatLeader first) — Design

Date: 2026-10-10
Status: Draft for review (rev 3 — review findings folded in, BeatLeader attempts, generic
platform model)
Builds on: 2026-10-08-ssarchiver-design.md (§ numbering below is its own)

## 1. Purpose

SSArchiver tracks players by ScoreSaber identity and archives ScoreSaber replays. This design
makes the platform a first-class, pluggable concept and adds **BeatLeader** as the second
platform. A third platform should later be a new client package plus one registry entry, with
no schema migration.

BeatLeader also records **attempts**: failed, quit, restarted and practice runs, plus clears that
did not beat the personal best. Each attempt has its own replay, and BeatLeader drops old attempt
replays over time. An instance owner can:

1. **Add a player straight from BeatLeader** — track a BeatLeader-only player.
2. **Add BeatLeader to a ScoreSaber player** — link a BeatLeader identity onto an already-tracked
   player; both platforms' scores and replays are archived under that one player.
3. **Add ScoreSaber to a BeatLeader player** — the reverse link.
4. **Archive BeatLeader attempts** — opt-in per BeatLeader identity, for players whose BeatLeader
   history is public (no authentication, §2.2). When it is not public, the admin UI explains
   exactly what the player must switch on (§6.3).

Success criteria:

- A tracked player's replays from every linked platform, and opted-in attempts, are archived and
  served (pages, raw downloads, ArcViewer embeds) like ScoreSaber replays are today.
- The public player page shows one merged list with one row per map (song, mode, difficulty).
  Each row shows every platform's personal best, and every other play of that map (superseded
  scores, attempts) stays reachable from the row.
- Every existing URL (`/p/{id}`, `/s/{id}`, `/embed/{id}`, `/r/{id}.dat`), stored replay file and
  aggregated count keeps working unchanged after upgrade, including after a merge (merged-away
  player IDs redirect).
- The JSON API scores listing gains `platform`, `type`, `min_score`, `max_score` filters, where
  `type=fail` means a run that actually failed.
- Linking an identity already tracked elsewhere refuses with a clear error pointing at an
  explicit merge; nothing moves without the admin confirming a merge.
- The upgrade cannot lose data: the migration verifies row counts and aborts on any mismatch.
- No table or column is specific to one platform. Tests register a fake third platform to prove
  it (§9).

**Non-goals (this iteration)**:

- BeatLeader OAuth (attempts only for public-history players, §2.2).
- Choosing which attempt end types to archive.
- Replay file parsing, BeatLeader leaderboard contexts beyond the default, auto-discovering links
  from BeatLeader's `linkedIds`, configurable per-platform rate budgets, and notifications.
- Implementing a third platform.

## 2. External facts (verified live 2026-10-10)

### 2.1 BeatLeader scores

Public API `https://api.beatleader.xyz`, no auth (swagger: `/swagger/blapi/swagger.json`).

- `GET /player/{id}` — profile: `id` (string, numeric: Steam `76561…` or Oculus IDs), `name`,
  `avatar` (on `cdn.assets.beatleader.xyz`), `country`, `platform`. **404** when the player does
  not exist. Store the `id` from the response, not the pasted ref (BeatLeader maps alternate
  account IDs to a main ID).
- `GET /player/{id}/scores?sortBy=date&order=desc&page=N&count=100` (count ≤ 100) —
  `{metadata:{page,itemsPerPage,total}, data:[ScoreResponse]}`. Returns **one score per
  leaderboard (current personal best)**; an improvement is a new score with a new ID and the old
  one leaves the listing. **404** for a non-existent player and for a player with zero scores.
- `ScoreResponse`: `id` (int), `baseScore`, `modifiedScore`, `accuracy` (0–1), `pp`, `rank`,
  `modifiers` (comma-separated, e.g. `"DA,BE"`), `badCuts`, `missedNotes`, `bombCuts`, `wallsHit`,
  `pauses`, `fullCombo`, `maxCombo`, `maxStreak`, `hmd` (int code), `controller`, `platform`,
  `timeset`/`timepost` (unix seconds), `playCount`, `leaderboardId` (**string**, e.g.
  `"54cb991"`), `replay` (URL), `leaderboard{id, song{hash (lowercase), name, subName?, author,
  mapper, coverImage}, difficulty{value (1–9, same scale as ScoreSaber), modeName ("Standard",
  "OneSaber", …), difficultyName ("ExpertPlus", …), status (0–7), stars, maxScore}}`.
- Difficulty `status`: `unranked(0), nominated(1), qualified(2), ranked(3), unrankable(4),
  outdated(5), inevent(6), oST(7)`.
- Score replays: `https://cdn.replays.beatleader.xyz/{scoreId}-{playerId}-{diff}-{mode}-{HASH}.bsor`.
  Plain GET, no auth, not pruned. BSOR format; ArcViewer plays it from any `replayURL`.
- Rate limits: headers `x-rate-limit-limit: 10s`, `x-rate-limit-remaining`, `x-rate-limit-reset`
  (RFC3339). Observed 50 requests per 10 s window.
- Covers on `eu.cdn.beatsaver.com` (also `cdn.beatsaver.com`, `na.cdn.beatsaver.com`); full
  covers and avatars on `cdn.assets.beatleader.xyz`.
- Profile URLs: `https://www.beatleader.com/u/{id}`.

### 2.2 BeatLeader attempts

- `GET /player/{id}/scoresstats?sortBy=date&order=desc&page=N&count=100` (not in the public
  swagger; `sortBy` **defaults to pp**, so it must be passed). Same envelope as scores, sorted by
  `timepost` descending (verified).
- **Access rule** (server source, `StatsController.GetScoresStats`): allowed when the caller is
  the player, an admin, or the player's `ShowStatsPublic` setting is on; otherwise **401**.
  - That setting is the **"Public history (auto-synced)"** switch under **Settings → Scores** on
    beatleader.com (website source, `ScoreSettings.svelte`). The player must be signed in.
  - The website switch can display "on" after a **failed** save: `toggleHistoryPublic` assigns the
    new value in a `finally` block, so a failed save leaves the switch looking on.
    Confirmed live: the instance owner turned it on, the switch showed on, and the server still
    had it off (`scoresstats` 401, `/players` listing `showStatsPublic=false`). The hint therefore
    tells the player to reload the page and check that it stayed on (§6.3).
  - The endpoint also answers **401 for a non-existent player**, so existence is checked with
    `GET /player/{id}`.
  - The `profileSettings.showStatsPublic` value in `GET /player/{id}` is not reliable (it read
    `false` for a player whose attempts were public, while the `/players` listing read `true`), so
    access is decided by probing `scoresstats?count=1`.
  - Live check: a top-50 player with public history → 200 with 66,285 attempts. The instance
    owner's profile `76561198038925092` → **401** while private (the default), then **200** once
    the setting was really saved. It had 27 attempts, all from 2024-01-27: 24 with
    `replay: null`, and the 3 clears pointing at their PB scores' `cdn.replays` files. This
    matches the retention cutoff below.
- Item (`AttemptResponseWithMyScore`): `id` (int, **separate ID space from score IDs**),
  `endType` (`unknown(0) clear(1) fail(2) restart(3) quit(4) practice(5)`), `time` (seconds into
  the song when the run ended), `timepost` (unix seconds; `timeset` is null), `baseScore`,
  `modifiedScore`, `accuracy`, `pp`, `rank`, `modifiers`, `badCuts`, `missedNotes`, `fullCombo`,
  `maxCombo`, `hmd`, `leaderboardId`, an embedded `leaderboard` (same shape as on scores), and
  `replay` (URL or **null**).
- Attempt replays:
  - A `clear` attempt that became the personal best points at the **same**
    `cdn.replays.beatleader.xyz` file as the score.
  - Every other attempt points at `https://api.beatleader.xyz/otherreplays/{playerId}-{n}-{diff}-{mode}-{HASH}.bsor`.
    It is served by the API host and **consumes the API rate limit**. The download is gated by the
    **same** `ShowStatsPublic` rule (server source, `ReplaysProxyController.GetOtherReplay`; pinned
    attempts are exempt). When the history goes private, the downloads answer 401 too, which §5.1
    treats as loss of access.
  - Retention is not guaranteed. For the sampled player, replays were present back to 2024-05 and
    `null` from 2023-12 onwards.
- Volume: the sampled player's attempts were mostly replay-backed after 2024-05; one sample replay
  was 178 KB. A heavy player can mean several GB and a few hours of API budget, which is why
  attempts are opt-in.
- Authenticated access exists (BeatLeader OAuth2 for registered apps), but every tracked player
  would have to authorize this instance and their tokens would need storing and refreshing. It is
  out of scope; the feed `access` field (§4.3) leaves room for an `oauth` mode later.

### 2.3 ScoreSaber values that matter for cross-platform matching

ScoreSaber `songHash` is **uppercase** and `gameMode` is prefixed (`SoloStandard`,
`SoloOneSaber`, …). BeatLeader uses lowercase hashes and bare mode names (`Standard`). Grouping
uses a normalized key (§4.5), never the raw fields.

## 3. Decisions

| Topic | Decision |
|---|---|
| Platform model | A Go **platform registry** (§4.1) describes each platform; the database only stores platform names as strings. Adding a platform needs no migration |
| Identity model | One `players` row per person; identities in `player_platforms`; sync cursors and opt-in state in `sync_feeds` (one per identity × feed); player ID opaque and immutable (random for new players, existing IDs kept as-is); readable links via `/p/{slug}/{externalID}` |
| Feeds | Each platform declares its feeds (`scores` always on; optional ones such as BeatLeader `attempts` are opt-in, may require an access probe, and carry an access hint) |
| Attempts | Stored in `scores` with `kind=attempt`, reusing the replay pipeline |
| Schema migration | Additive + raw `ALTER TABLE … DROP COLUMN`; never GORM's `DropColumn` (it rebuilds the table, §7); count-verified, single transaction, pre-migration `VACUUM INTO` backup |
| Internal IDs for non-ScoreSaber rows | Allocated sequentially from a reserved range starting at 2^62 shared by all non-ScoreSaber platforms; public routes use the platform's own ID |
| Merged view | Grouped in SQL by normalized `map_key`, paginated over groups; one chip per platform (its current PB) plus an expandable list of every other play |
| Link conflicts | Refuse with a clear error; explicit merge, allowed only when the two players' platforms are disjoint; merged-away IDs become redirecting aliases |
| Replay storage | ScoreSaber legacy layout untouched (`replays/{player}/{rowID}.dat`); every other platform under `replays/{player}/{platform}/{rowID}{ext}` |
| Rate limiting | Limiters are per platform and per host class; the worker skips units whose limiter is not ready instead of blocking |
| Replay URL safety | Each platform declares an allowlist of replay URL prefixes; nothing else is fetched, and redirects off the allowlist are refused |
| Replay immutability | An archived replay is never re-downloaded or replaced, so `immutable` caching stays valid |
| `type` filter | `complete` (default), `fail`, `quit`, `restart`, `practice`, `all` |
| Score filters | `min_score`/`max_score` on `modified_score`; chips show the raw score next to the % |
| Pagination | Existing `page` / `per_page` kept |

## 4. Data model

### 4.1 Platform registry (`internal/platform`)

The only place that knows which platforms exist. Each platform registers a descriptor:

```go
type Platform struct {
    Name        string   // stored in DB columns: "scoresaber", "beatleader"
    Slug        string   // URL prefix: "ss", "bl" ([a-z]{2,8}, not "map")
    DisplayName string   // "ScoreSaber", "BeatLeader"
    Priority    int      // display-identity order: lower wins (ScoreSaber 0, BeatLeader 10)
    Legacy      bool     // ScoreSaber only: bare player IDs, legacy routes and storage layout
    ReplayExt   string   // ".dat", ".bsor"
    ImageHosts  []string // CSP img-src additions
    ProfileURL  func(externalID string) string
    ParseURL    func(input string) (externalID string, ok bool) // this platform's profile URLs
    ValidID     func(input string) bool                         // bare IDs (auto-detected only for Legacy)
    Feeds       []FeedSpec
    Adapter     Adapter // built by the platform's package at startup
}

type FeedSpec struct {
    Kind        string // the row kind it produces, also its key in sync_feeds: "score" | "attempt"
    Optional    bool   // false: created with the identity; true: admin opt-in
    NeedsAccess bool   // probe before use; access can be "private" (optional feeds only)
    AccessHint  *Hint  // shown when access is private (§6.3)
}

type Hint struct {
    Title    string
    Steps    []string // short imperative steps for the player
    LinkText string
    LinkURL  string   // e.g. https://beatleader.com/settings
}
```

`Adapter` bundles `Resolve(ctx, externalID) (Profile, error)`,
`FeedPage(ctx, kind, externalID, page) (platform.PlayPage, error)`,
`ProbeAccess(ctx, kind, externalID) (access string, total int64, error)`,
`Replay(ctx, platform.ReplayRef) (io.ReadCloser, error)`, `Limiters() []platform.Limiter`, and
`FeedLimiter(kind)`/`ReplayLimiter(kind)`, which name the limiter a listing or a download
consumes. Each adapter applies its own replay URL allowlist: a play whose URL is not allowed is
reported without a replay. Platform clients live in their own packages (`internal/scoresaber`,
`internal/beatleader`), and adapters translate their payloads into the neutral `platform.Play`
(§5.2). Score feeds are always required. Tiers order feeds by kind: score work first, then
everything else (§5.1).

Everything platform-shaped is derived from the registry:

- the API's `platform` filter enum and the `{platform}` path validation
- route slugs (including the `/p/{slug}/{externalID}` account links), the storage directory
  and CSP image hosts
- Sync-page limiter meters and the admin "Link identity" menu

### 4.2 players and player_aliases

`players` keeps `id`, `name`, `avatar_url`, `country`, `enabled`, `added_at`. The six sync-state
columns (`backfill_state`, `backfill_page`, `backfill_total_pages`, `backfill_retry_at`,
`last_polled_at`, `last_error`) move to `sync_feeds` and are dropped (§7).

- `id` — **opaque**, internal, immutable, `^[a-z0-9-]{1,40}$` (path-safe; replaces the
  digits-only rule in `storage.ValidPlayerID`, the API `PlayerPath` pattern and
  `ParsePlayerRef`). No code reads meaning from it: platform account IDs come only from
  `player_platforms.external_id`.
  - New players get a random ID: 12 characters from the lowercase base32 alphabet
    `0-9a-z` minus `i l o u`, first character a letter (so it is never all digits like a legacy
    ID), from `crypto/rand`. The insert retries on a collision with a player or an alias.
  - Existing rows keep their current ID (their ScoreSaber ID), unchanged forever. Rewriting them
    was rejected: it would move every replay directory non-atomically with the database (risking
    re-downloads of already-pruned ScoreSaber replays), rebuild every foreign key, and change the
    `id` API clients already store. Old and new IDs are equally opaque strings to the system.
  - Readable, durable links go through the account instead: `/p/{slug}/{externalID}` (§6.1).
- `name`, `avatar_url`, `country` — refreshed from the enabled identity with the lowest registry
  `Priority`.
- `enabled` — master toggle over all identities.

`player_aliases(old_id PK, player_id FK → players CASCADE)` records merged-away IDs (§6.2). When
a player who is the target of aliases is merged away, those aliases are re-pointed at the new
survivor, so lookups never chain.

### 4.3 player_platforms and sync_feeds

`player_platforms` — one row per linked identity, no platform-specific columns:

| Column | Notes |
|---|---|
| `player_id` | FK → players, `OnDelete:CASCADE`; PK part |
| `platform` | Registry `Name`; PK part |
| `external_id` | The platform's player ID. Unique index `(platform, external_id)` |
| `enabled` | Identity switch. Set false when the platform reports the player gone; re-enabled by the admin. Other identities keep running |
| `linked_at` | Link time (migrated rows: `players.added_at`) |
| `last_error` | Identity-level error (e.g. "player not found on BeatLeader") |

A player keeps ≥ 1 identity (unlinking the last one is refused; delete the player instead).

`sync_feeds` — one row per (identity, feed) that has been created, holding cursor and opt-in
state:

| Column | Notes |
|---|---|
| `player_id`, `platform` | Composite FK → `player_platforms`, `OnDelete:CASCADE`; PK parts |
| `feed` | `FeedSpec.Kind` (`score` \| `attempt`); PK part. Keying feeds by the row kind they produce lets replay selection join `sync_feeds.feed = scores.kind` directly |
| `enabled` | Always true for non-optional feeds; the opt-in switch for optional ones |
| `started_at` | When the feed (last) started; plays with `set_at >= started_at` go to the "new" replay tier. Migrated: `players.added_at`; new links: link time; optional feeds: enable time |
| `access` | `n/a` (no probe needed) \| `unknown` \| `public` \| `private` |
| `access_checked_at` | Nullable; last probe |
| `remote_total` | Last item total reported by the platform (shown in the UI) |
| `backfill_state` | `pending \| running \| done` (`model.Backfill*`) |
| `backfill_page`, `backfill_total_pages` | Resume cursor, 1-based |
| `backfill_retry_at` | Nullable; set for 5 minutes after a failed backfill page |
| `last_polled_at` | Nullable |
| `last_error` | Feed-level transient error |

Non-optional feeds are created with the identity. An optional feed row is created the first time
the admin enables it; disabling sets `enabled=false` and keeps the cursor, rows and files.

### 4.4 Row kinds and end types (generic vocabulary)

- `kind`: `score` (a leaderboard submission) \| `attempt` (any other recorded run).
- `end_type`: `clear` \| `fail` \| `restart` \| `quit` \| `practice` \| `unknown`. Scores are
  always `clear`.

A platform maps its own values onto these. New values are added here and to the `type` filter, not
per platform.

### 4.5 leaderboards

Add `platform`, `external_id` (string) — unique index `(platform, external_id)` — and `map_key`
(indexed):

- `map_key` = `lower(song_hash) + "/" + mode + "/" + difficulty`, where `mode` is the game mode
  with a leading `Solo` stripped (`SoloStandard` → `Standard`). Normalization lives in one
  function the adapters call, and the migration applies the same rule. Raw `song_hash`/`game_mode`
  are stored as received.
- ScoreSaber rows (all existing rows): `platform='scoresaber'`, `external_id = itoa(id)`; `id`
  stays the ScoreSaber leaderboard ID.
- Other platforms: `external_id` = the platform's leaderboard ID; `id` = next free internal ID
  ≥ 2^62, allocated inside the upsert transaction (writes are serialized by
  `_txlock=immediate`). Lookups go through `(platform, external_id)` only.
- BeatLeader mapping:
  - From `song`: `song_hash` = `hash`; `song_name`, `song_sub_name`, `song_author`; `mapper` =
    `mapper`; `cover_url` = `coverImage`.
  - From `difficulty`: `difficulty` = `value`; `difficulty_raw` = `difficultyName`; `game_mode` =
    `modeName`; `stars` = `stars` (0 when null); `max_score` = `maxScore`.
  - `status` = `RANKED` for ranked(3), `QUALIFIED` for qualified(2), else `UNRANKED`.

### 4.6 scores

Add `platform`, `kind`, `end_type`, `end_time` (nullable float, seconds into the song; attempts),
`external_id` (string), `replay_url` (nullable). Unique index `(platform, kind, external_id)`
(platforms may use separate ID spaces per kind, as BeatLeader does). Index
`(player_id, kind, set_at DESC)`.

- ScoreSaber rows (all existing rows): `platform='scoresaber'`, `kind='score'`,
  `end_type='clear'`, `external_id = itoa(id)`, `replay_url` null. Nothing else changes.
- BeatLeader scores: `id` = internal ID, `kind='score'`, `end_type='clear'`, `external_id` =
  score ID, `leaderboard_id` = the internal BL leaderboard ID, `modified_score` = `modifiedScore`,
  `unmodified_score` = `baseScore`, `accuracy`, `pp`, `rank`, `mods` = `modifiers`, `full_combo`,
  `missed_notes`, `bad_cuts`, `max_combo`, `hmd` = display name of the HMD code (raw code when
  unknown), `personal_best` = true, `set_at` = `timeset`, `has_replay`, `replay_state`
  (`pending` when the URL is allowlisted, else `none`), `replay_url`. When a new score row is
  inserted for a platform that lists PBs only, older score rows of the same player, platform and
  leaderboard get `personal_best=false`.
- BeatLeader attempts: as above with `kind='attempt'`, `end_type` from `endType`, `end_time` =
  `time`, `set_at` = `timepost`, `personal_best` = false, `external_id` = attempt ID. **Skipped**
  when `endType=clear` and the replay is on `cdn.replays.beatleader.xyz` (it is the PB score the
  scores feed archives).
- The replay state machine (`none/pending/archived/gone/failed`, attempts counter, backoff) is
  shared by all rows. `MarkReplayGone` and similar messages name the row's platform instead of
  hard-coding "ScoreSaber".

### 4.7 sync_events

Add nullable `platform` and `feed` (null for global `worker` events). `score_id` keeps referring
to the internal row ID; links are built through the platform-aware URL helpers (§6.1). The log
gains platform and feed filters.

## 5. Sync worker

### 5.1 Work selection

Work units are `sync_feeds` rows with `enabled`, whose identity and player are enabled. Feeds
with `access='private'` are excluded, and so are feeds with `access='unknown'` until probed.
Single goroutine, one unit per step, in priority order:

1. **Access probe** — a feed with `FeedSpec.NeedsAccess` whose `access` is `unknown`, or
   `private` with `access_checked_at` older than 24 h. It calls `ProbeAccess`: `public` stores
   `remote_total`, `private` stores the state and logs a `warn` event carrying the feed's hint
   title. For BeatLeader this is `scoresstats?count=1`, where 200 means public and 401 means
   private.
2. **Poll** — a feed whose `last_polled_at` is older than `poll_interval` (non-optional feeds
   first). It walks pages newest-first, upserts, and stops at a known `(platform, kind,
   external_id)`, the end, or `MaxPollPages` (5). The v1 first-poll and 5-page-cap rules apply
   per feed. On page 1 of a `score` feed it refreshes the display profile per §4.2; for a
   platform whose score payloads lack the profile (BeatLeader), it calls `Resolve`.
3. **New replays** — `pending` rows with `set_at >= feed.started_at`, any kind, newest first.
4. **Backfill listing, non-optional feeds** — one page for a feed not `done`.
5. **Backfill replays, `kind=score`** — remaining `pending` score rows.
6. **Backfill listing, optional feeds** — one page.
7. **Backfill replays, other kinds** — remaining `pending` attempt rows.

Optional feeds come after all score work because one player's history can be tens of thousands of
attempts. ScoreSaber replays, pruned soonest, are never starved by them. Round-robin runs across
feeds within each tier.

**Limiter readiness**: each `platform.Limiter` exposes `Ready(now) (bool, time.Time)`. Selection
skips units whose limiter is not ready. Each feed, and each replay through its URL host, maps to
one limiter. The idle timer also considers the earliest readiness. A pause on one platform never
stalls another; `Wait` stays in the clients as a safety net. Worker `Status` carries a list of
named limiter snapshots.

**Access lost mid-run**: when a listing (or a replay download of that feed) answers 401 or 403,
the feed flips to `private` and stops until a probe says otherwise. Rows and files are kept.

### 5.2 Neutral types

The service layer works on platform-neutral types defined in `internal/platform` (so that
platform packages can produce them without importing the service). `UpsertScores` is replaced by
`UpsertPlays`, which no longer takes `scoresaber.ScoreItem`:

```go
// platform.Play is one normalized score or attempt with its leaderboard.
type Play struct {
    Leaderboard LeaderboardData // incl. platform external ID and map_key inputs
    Kind, EndType, ExternalID   string
    EndTime                     *float64
    ReplayURL                   string // empty: platform fetches by ID (ScoreSaber)
    // … score fields mirroring model.Score
}
type PlayPage struct{ Plays []Play; TotalPages int }
```

Errors are classified with shared sentinels in `internal/platform` (`ErrNotFound`,
`ErrRateLimited`, `ErrUnauthorized`). Clients wrap them; `scoresaber.ErrNotFound` etc. become
aliases so existing call sites keep compiling.

| Case | ScoreSaber | BeatLeader scores | BeatLeader attempts |
|---|---|---|---|
| Listing 404 | identity disabled, `last_error` set | end of listing (zero scores) | — |
| Listing 401/403 | — | — | feed → `private` (§5.1) |
| Player endpoint 404 | identity disabled | identity disabled | (same identity) |
| 429 / limit headers | SS limiter pause | BL API limiter pause until `x-rate-limit-reset` | same |
| Replay 404 | `gone` | `gone` | `gone` |
| Replay URL not allowlisted | — | `replay_state=none`, `warn` event | same |

### 5.3 BeatLeader client (`internal/beatleader`)

Mirrors `internal/scoresaber`: typed client, no app logic. `NewClient(api, cdn *Limiter,
opts...)` with `Player(ctx, id)`, `Scores(ctx, playerID, page)`, `Attempts(ctx, playerID, page,
count)`, `Replay(ctx, url)`.

- Limiters: single 10 s sliding window; `beatleader/api` 40 req (headroom under the observed 50),
  `beatleader/cdn` 20 req. Both use an injectable clock and block until `x-rate-limit-reset` on
  exhaustion or 429. `otherreplays` downloads use the API limiter.
- Replay allowlist: `https://cdn.replays.beatleader.xyz/` and
  `https://api.beatleader.xyz/otherreplays/`. `CheckRedirect` enforces the same list.

Registry entry: `Name: "beatleader"`, `Slug: "bl"`, `Priority: 10`, `ReplayExt: ".bsor"`, feeds
`scores` (non-optional) and `attempts` (optional, `NeedsAccess`, hint in §6.3).

### 5.4 Upsert semantics

- Keyed on `(platform, kind, external_id)`; a page's leaderboards are upserted first.
- Existing row: refresh `rank`, `pp`, `personal_best` (scores only); `none → pending` when a
  replay newly appears; `replay_url` is updated only while the row is not `archived`. An archived
  replay is never replaced; if the platform later reports a different URL, a `warn` event is
  logged and the archive is kept.
- New key → new row. A BeatLeader improvement mints a new score ID and lands as a new row; the
  superseded row keeps its archived replay with `personal_best=false`.

### 5.5 Replay storage & reconciliation

- `storage.Store` methods take the platform. Legacy platform: `replays/{playerID}/{rowID}.dat`.
  Others: `replays/{playerID}/{platform}/{rowID}{ext}`.
- `Scan` derives the player from the first path segment under the root, and the platform from the
  optional second segment (validated against the registry); files in unknown platform directories
  are counted as orphans and kept. Existing rules are unchanged:
  - stray `.tmp` files are deleted
  - a file without an `archived` row is adopted when its row exists with the same player and
    platform
  - an `archived` row with a missing file returns to `pending`

## 6. HTTP surface

### 6.1 Public (all existing routes keep their exact semantics)

`{slug}` is any registered non-legacy slug (unknown slug → 404). Legacy ScoreSaber keeps its bare
routes.

| Route | Content |
|---|---|
| `GET /p/{id}` | Merged player page (below). An ID found in `player_aliases` → **301** to `/p/{survivor}` |
| `GET /p/{slug}/{externalID}` | Account link, e.g. `/p/bl/76561198038925092` or `/p/ss/…` (the legacy platform gets a slug here too): looks up `player_platforms` and **301**s to `/p/{id}`; 404 when that account is not tracked. Stays valid through merges. The player page shows these links as the shareable URLs |
| `GET /p/{id}/map?key={map_key}` | htmx fragment: every play of that map for the player (all platforms and kinds, newest first), filters applied. The literal `map` segment takes precedence over `/p/{slug}/{externalID}` in the mux; no slug may be named `map` |
| `GET /s/{slug}/{externalID}` | Score page for that platform (same components as the SS page) |
| `GET /s/{slug}/attempt/{externalID}` | Attempt page: same, plus end-type badge and "ended at m:ss" |
| `GET /r/{slug}/{externalID}{ext}`, `GET /r/{slug}/attempt/{externalID}{ext}` | Raw replay, same treatment as `/r/{id}.dat`: CORS `*`, Range, `ETag` = sha256, immutable cache, `Content-Disposition: attachment` |
| `GET /embed/{slug}/{externalID}`, `GET /embed/{slug}/attempt/{externalID}` | Viewer wrapper → `/viewer/?replayURL={base}/r/{slug}/…&noProxy=true&…`; `frame-ancestors *` |

`/s/{id}`, `/r/{id}.dat`, `/embed/{id}` resolve ScoreSaber rows only, so no other platform can
shadow an existing embed. All score links (player table, sync page, events, OpenGraph) go through
row-based helpers (`views.ScoreURL(sc)`, `ReplayPath(sc)`, `EmbedPath(sc)`,
`ViewerSrc(base, sc, …)`) instead of int64 IDs.

**Merged player page**:

- Filters apply per play row (search, ranked, state, platform, type, score bounds); a map appears
  when any of its plays match.
- Groups are built in SQL: `GROUP BY map_key ORDER BY MAX(set_at) DESC LIMIT/OFFSET`, with the
  total from `COUNT(DISTINCT map_key)`; a second query loads the chips for the page's keys.
  Nothing loads the whole history.
- Each row shows map info, one chip per platform with its current PB (score %, raw score, rank,
  platform badge, link), and "N more plays", which expands via `/p/{id}/map`.
- The header shows per-platform chips (scores, replays archived) and per-optional-feed counts
  when enabled.

Fail/quit replays end early. If the pinned ArcViewer build does not play a truncated BSOR,
attempt pages hide the viewer and keep the download (checked during implementation, recorded in
the plan).

CSP `img-src` is built from the registry's `ImageHosts`. BeatLeader adds `https://cdn.beatsaver.com
https://*.cdn.beatsaver.com https://cdn.assets.beatleader.xyz`.

### 6.2 Admin JSON API (`/api/v1`, session-cookie auth)

`PlayerPath` pattern becomes `^[a-z0-9-]{1,40}$`; a merged-away ID resolves to its survivor.
`{platform}` path params and the `platform` body fields take a registry `Name` (OpenAPI enum
generated from the registry).

| Operation | Notes |
|---|---|
| `POST /players` | Body `{ref, platform?}`. URL pastes are matched by each platform's `ParseURL`; bare IDs default to the legacy platform (backward compatible); `platform` overrides. Resolves live, stores the canonical external ID, creates the player with a fresh opaque ID. **409** `identity_linked_elsewhere` |
| `GET /players/by/{platform}/{externalID}` | Player lookup by account (same body as `GET /players/{id}`) |
| `POST /players/{id}/identities` | Body `{platform, ref}`. Resolves live, links, creates non-optional feeds. **409** `identity_linked_elsewhere` (names the other player, suggests merge); **409** `platform_already_linked` |
| `PATCH /players/{id}/identities/{platform}` | Body `{enabled}` |
| `DELETE /players/{id}/identities/{platform}` | `?delete_files=` (default false). Removes the identity, its feeds and all its rows, plus files when asked. **409** `last_identity` |
| `PATCH /players/{id}/identities/{platform}/feeds/{kind}` | Body `{enabled}`; only optional feeds (**422** otherwise). Enabling runs the access probe synchronously and returns the feed (`access`, `remote_total`, `hint` when private) |
| `POST /players/{id}/identities/{platform}/feeds/{kind}/check` | Re-probe access now ("Check again"); same response |
| `POST /players/{source}/merge` | Body `{into}`. Rules below |
| `GET /players/{id}/scores` | Filters below |
| `GET /scores/{platform}/{externalID}`, `GET /scores/{platform}/attempt/{externalID}` | Single row (mirrors `GET /scores/{id}`) |

**Merge** (`source` → `into`):

- Refused with **409** `merge_platform_conflict` when both players have an identity on the same
  platform. Identities are globally unique, so merging is only ever needed for disjoint
  platforms, and no identity or score is ever dropped.
- Order is chosen so that a crash at any point loses nothing:
  1. Hard-link each archived file into the target's directory (copy + fsync if linking fails).
  2. In one transaction: re-point `scores.player_id`, `player_platforms`, `sync_feeds`; insert the
     alias `source → into`; re-point aliases that pointed at `source`; delete the source player.
  3. Remove the source's replay directory.

  A crash after step 1 or step 2 leaves only orphan duplicates; every archived row keeps a file.
- Logged as a `worker` sync event; `/p/{source}` redirects from then on.

**Player DTO**:

- Adds `identities[]`: `{platform, id (external), profile_url, enabled, last_error, linked_at,
  feeds[{feed, optional, enabled, access, hint?, remote_total, started_at, last_polled_at,
  backfill{state, next_page, total_pages}, last_error}], counts{…}}`.
- Existing top-level `last_polled_at`, `last_error`, `backfill` are kept for compatibility. They
  come from the `score` feed of the lowest-`Priority` identity and are marked deprecated in
  OpenAPI.
- Top-level counts keep their meaning: `kind=score` rows only. Counts for other kinds are
  reported per feed.

**Score DTO**:

- Adds `platform`, `kind`, `end_type`, `end_time`, `external_id`, and `leaderboard.external_id`.
- `id` and `leaderboard.id` keep meaning "ScoreSaber ID" and are `0` for other platforms; internal
  IDs are never exposed.
- `url`, `download_url` and `embed_url` are built per platform.

**Score listing filters** (combinable with existing `search`, `state`, `ranked`, `page`,
`per_page`):

| Param | Values | Meaning |
|---|---|---|
| `platform` | `all` (default) \| any registry name | Restrict to one platform |
| `type` | comma list of `complete` (default) \| `fail` \| `quit` \| `restart` \| `practice` \| `all` | `complete` = `kind=score` rows plus `clear` attempts; the others = attempts with that end type; `all` = everything |
| `min_score`, `max_score` | int64 | Inclusive bounds on `modified_score` (for attempts: the score when the run ended) |

The default `type=complete` keeps the listing backward compatible: without optional feeds it
returns exactly what it returns today. `service.ScoreFilter` carries the new fields;
`service.ListMapGroups(playerID, filter)` serves the merged web view.

### 6.3 Web admin UI

- **Manage**:
  - A single add input. The platform is auto-detected from pasted URLs via the registry; bare IDs
    default to ScoreSaber and can be switched.
  - Player rows show one chip per identity with feed state and errors.
  - Row actions: **Link identity** (one entry per unlinked registered platform), **Merge…**
    (target picker with type-to-search, disabled with an explanation when platforms overlap),
    **Unlink** (per platform, `delete_files` checkbox, last-identity guard), and **Enable/disable
    identity**.
- **Optional feed switches** (e.g. "Archive attempts" on BeatLeader identities):
  - Before enabling, a confirmation shows `remote_total` when known and a storage warning
    (attempt history can reach several GB).
  - Enabling probes immediately. When the result is `private`, an inline **access hint** is shown
    under the switch, from the feed's `Hint`. The BeatLeader attempts hint reads:

    > **Attempt history is private on BeatLeader.** SSArchiver can only archive attempts when
    > the player makes their history public:
    > 1. Sign in on beatleader.com with this account.
    > 2. Open **Settings → Scores**.
    > 3. Turn on **Public history (auto-synced)**.
    > 4. Reload the page and check the switch is still on (BeatLeader can show it on even when
    >    saving failed).
    >
    > [Open BeatLeader settings](https://beatleader.com/settings) · **Check again**
    >
    > SSArchiver also re-checks every 24 hours. Old attempt replays are dropped by BeatLeader over
    > time, so the sooner this is on, the more can be saved.

  - The same hint shows whenever the feed is `private`: in the identity chip as a warning icon
    with tooltip, and on the Sync page queue row (with a "Check again" button). This includes the
    case where access was lost mid-run.
  - The hint never appears on public pages; it is an admin concern.
- **Sync**: the queue table lists feeds (platform, feed, access, backfill page X/Y). The event log
  gains platform and feed filters. Budget meters list every registered limiter.

## 7. Migration

`db.Migrate` detects a legacy database by the presence of `players.backfill_state` and runs, in
order:

0. **Backup**: `VACUUM INTO '{data}/ssarchiver.pre-platforms.db'` (skipped when it already
   exists). Record row counts of `players`, `scores`, `leaderboards`.
1. Schema:
   - `AutoMigrate(Player, PlayerPlatform)` first.
   - Then `sync_feeds` is created with a raw `CREATE TABLE IF NOT EXISTS`. Its composite foreign
     key `(player_id, platform) → player_platforms` cannot be expressed as a GORM relation: GORM
     puts it on the wrong table (verified against the pinned GORM: it emitted the constraint on
     `player_platforms` referencing `sync_feeds`). The `SyncFeed` model therefore declares no
     relation.
   - Then `AutoMigrate` for the remaining models. On the existing table it leaves `sync_feeds`
     alone (verified: a second run emits no DDL).
   - Every new `NOT NULL` column on an existing table carries a GORM `default:''`, so
     `ADD COLUMN` works on populated tables.
   - Unique and composite indexes on new columns are **not** declared in model tags: `AutoMigrate`
     would build them over empty duplicate values and fail.
2. **One transaction** (SQLite DDL is transactional):
   1. Backfill:
      - `leaderboards` → `platform='scoresaber'`, `external_id = id`, `map_key` per §4.5.
      - `scores` → `platform='scoresaber'`, `kind='score'`, `end_type='clear'`,
        `external_id = id`.
      - `player_platforms` (`scoresaber`, `external_id = id`, `enabled = true`,
        `linked_at = added_at`).
      - A `score` feed (`enabled = true`, `access='n/a'`, `started_at = added_at`) carrying each
        player's current backfill/poll/error values.

      Inserts use `WHERE NOT EXISTS`, so a rerun is a no-op.
   2. Create the unique and composite indexes (`IF NOT EXISTS`).
   3. `DROP INDEX IF EXISTS idx_players_backfill_state`, then raw
      `ALTER TABLE players DROP COLUMN …` for each legacy column still present (SQLite refuses to
      drop an indexed column).
   4. Verify: the step-0 row counts are unchanged, `player_platforms` and `sync_feeds` each have
      one row per player, and `PRAGMA foreign_key_check` is empty. Any mismatch rolls back and
      aborts startup with the backup path in the error.

**Never use `Migrator().DropColumn` here.** The pinned `glebarez/sqlite` driver implements it as a
table rebuild (`CREATE players__temp`, copy, `DROP TABLE players`, rename) without disabling
foreign keys. With `foreign_keys(1)` set in the DSN, `DROP TABLE players` cascades and deletes
every score and identity row. `foreign_key_check` would not notice, which is why step 2.4 compares
counts.

Fresh installs have no legacy columns, so only step 1 and the index creation run. Registering a
future platform needs no migration: platform names are plain strings, and optional feeds create
their `sync_feeds` rows on demand.

## 8. Documentation

README:

- Player management covers platforms, linking, merging, and attempt archiving: the "Public
  history (auto-synced)" requirement, the storage warning, and that old attempt replays may
  already be gone on BeatLeader.
- The API section documents the new filters, endpoints and DTO fields, plus the deprecated
  top-level player sync fields.
- A short "Adding a platform" note for contributors: registry entry, adapter, client package,
  fixtures. No new environment variables.

## 9. Testing

- **`platform`**:
  - Registry validation: unique names and slugs, slugs non-numeric, at most one legacy platform.
  - `map_key` normalization: an SS `SoloStandard`/uppercase row and a BL `Standard`/lowercase row
    give the same key.
- **Genericity**: a fake third platform (`testplat`, slug `tp`, one non-optional and one optional
  feed with `NeedsAccess`) is registered in `service`, `archiver` and `web` tests. It must work
  end-to-end with no code outside its own test adapter: link, poll, backfill, replay storage
  path, routes `/s/tp/…`, merged page chips, filters and access hint.
- **`beatleader`**: httptest fixtures (recorded live) for:
  - player
  - scores page, scoreless-404, player-404
  - attempts page (mixed end types, `otherreplays` and `cdn.replays` URLs, a null replay)
  - attempts-401 (any private-history profile; also covers the non-existent-player case)
  - 429 with `x-rate-limit-*` headers
  - replay stream; replay URL allowlist and redirect refusal

  Also: limiter window, reset and `Ready` under an injectable clock, and an opt-in `-tags live`
  smoke test against the instance owner's profile `76561198038925092`: player, scores, and
  attempts (200 while its history stays public).
- **`service`**:
  - Link/unlink, including last-identity and already-linked refusals.
  - Opaque IDs: format, first character a letter, collision retry against players and aliases.
  - **ID-is-opaque guard**: every test fixture player gets an opaque ID that differs from its
    account IDs. One legacy-style player has ID `"123"` with its ScoreSaber account unlinked and
    a different BeatLeader account linked. Any code path that still uses `players.id` as a
    platform ID fails these tests.
  - Merge: disjoint success with alias written and re-pointed, platform-conflict refusal, file
    links.
  - Alias resolution.
  - Upserts: new row, superseded PB flag cleared, archived replay never replaced; clear-on-CDN
    attempts skipped; separate ID spaces per kind.
  - SQL group pagination and totals; all filters and combinations; `type` default compatibility.
  - Optional feed enable/disable keeps rows.
- **`archiver`**: fake platforms plus an injectable clock:
  - tier order including optional tiers
  - round-robin across feeds
  - limiter-not-ready skipping (a BL pause doesn't stall SS)
  - access probe public/private, re-probe after 24 h, 401 mid-backfill → private
  - poll stop-at-known per feed, BL scores-404-as-end
  - identity-scoped disable
  - backfill resume per feed
  - `started_at` tier split for a late-linked identity
- **`storage`**: both layouts, opaque and legacy player IDs, Scan player/platform derivation, unknown
  platform directories kept as orphans.
- **`api` / `web`**:
  - new filter params end-to-end
  - 409/422 mappings
  - feed PATCH/check responses with the hint
  - per-platform score/attempt routes (CORS, Range, ETag, embed framing), unknown slug 404
  - alias 301
  - merged player page with multi-platform rows and the map fragment
  - access-hint rendering on the manage and sync pages, absent on public pages
  - DTO compatibility fields
  - registry-built CSP
- **`db`**: a committed v1 fixture database built from the current models (players with backfill
  in progress and done, scores in every replay state, files) is migrated. The test asserts:
  - unchanged row counts
  - populated `platform`/`kind`/`external_id`/`map_key`
  - identity and feed rows carrying the old cursor values
  - legacy columns gone
  - public routes resolving

  A second run is a no-op.
- `make generate` (gorm gen) after model changes; `make test lint` green; the CI codegen drift
  check stays clean.
