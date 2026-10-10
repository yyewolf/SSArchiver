# SSArchiver — BeatLeader support — Design

Date: 2026-10-10
Status: Draft for review
Builds on: 2026-10-08-ssarchiver-design.md (§ numbering below is its own)

## 1. Purpose

SSArchiver tracks players by ScoreSaber identity and archives ScoreSaber replays. BeatLeader is a
second leaderboard platform for the same game; its replays live on a CDN and are not pruned. This
design adds BeatLeader as a first-class platform so an instance owner can:

1. **Add a player straight from BeatLeader** — track a BeatLeader-only player (no ScoreSaber
   identity required).
2. **Add BeatLeader to a ScoreSaber player** — link a BeatLeader identity onto an already-tracked
   ScoreSaber player; both platforms' scores and replays are archived under that one player.
3. **Add ScoreSaber to a BeatLeader player** — the reverse link onto a BeatLeader-only player.

Success criteria:

- A tracked player's replays from both platforms are archived and served (pages, `.bsor`/`.dat`
  downloads, ArcViewer embeds) exactly like ScoreSaber replays are today.
- The public player page shows one merged, deduplicated score list; a row played on both platforms
  shows both scores, each linking to its score page.
- Every existing URL (`/p/{id}`, `/s/{id}`, `/embed/{id}`, `/r/{id}.dat`), every stored replay file
  and every aggregated count keeps working unchanged after upgrade.
- The JSON API scores listing gains `platform`, `type`, `min_score`, `max_score` filters.
- Linking an identity that is already tracked elsewhere refuses with a clear error and points at an
  explicit merge action; nothing moves without the admin confirming a merge.

**Non-goals (this iteration)**: replay file parsing (e.g. BSOR `failTime`), BeatLeader leaderboard
contexts beyond the default, auto-discovering links from BeatLeader's `linkedIds`, per-platform
enable toggles, configurable BeatLeader rate budgets, notifications.

## 2. External facts (verified live 2026-10-10)

BeatLeader public API, `https://api.beatleader.xyz`, no auth (swagger: `/swagger/blapi/swagger.json`):

- `GET /player/{id}` — profile: `id` (string, numeric: Steam `76561…` or Oculus IDs), `name`,
  `avatar`, `country`, `platform` (`steam`/`oculuspc`/…), `externalProfileUrl`. 404 when the player
  does not exist.
- `GET /player/{id}/scores?sortBy=date&order=desc&page=N&count=100` —
  `{metadata:{page,itemsPerPage,total}, data:[ScoreResponse]}`. Returns **one score per leaderboard
  (current personal best)**; improvements create new score IDs. Verified: page of 100 rows had 100
  distinct `leaderboardId`s and `playCount > 1` on several rows. A player with zero scores returns
  **404** (unlike ScoreSaber, where scores-404 means "player gone").
- `ScoreResponse`: `id` (int64), `baseScore`, `modifiedScore`, `accuracy`, `pp`, `rank`,
  `modifiers` (comma-separated string, e.g. `"DA,BE"`), `badCuts`, `missedNotes`, `bombCuts`,
  `wallsHit`, `pauses`, `fullCombo`, `maxCombo`, `maxStreak`, `hmd` (int code), `controller`,
  `platform` (string with game version), `timeset`/`timepost` (unix seconds), `playCount`,
  `leaderboardId` (**string**, e.g. `"54cb991"`), `replay` (URL),
  `leaderboard{id, song{hash, name, subName?, author, mapper, coverImage}, difficulty{value (1–9,
  same scale as ScoreSaber), modeName ("Standard", …), difficultyName ("ExpertPlus", …),
  status (0–7), stars, maxScore, notes}}`.
- Difficulty `status` enum: `unranked(0), nominated(1), qualified(2), ranked(3), unrankable(4),
  outdated(5), inevent(6), oST(7)`.
- Replays: `replay` field carries a direct CDN URL of the form
  `https://cdn.replays.beatleader.xyz/{scoreId}-{playerId}-{difficultyName}-{modeName}-{HASH}.bsor`.
  Plain GET, no auth. BSOR format — identical family to ScoreSaber `.dat` files; ArcViewer plays it
  (the embedded build already accepts arbitrary `replayURL`s).
- Rate limits: response headers `x-rate-limit-limit: 10s`, `x-rate-limit-remaining`,
  `x-rate-limit-reset` (RFC3339). Observed ~50 requests / 10 s window.
- `endType` (`unknown/clear/fail/restart/quit/practice`) exists in the API schema but only on
  authenticated "my attempts" endpoints — public score listings contain **completed plays only**.
  Failed runs are never submitted to either platform. (The BSOR `info` block has a `failTime` field;
  parsing replays is a non-goal, see §1.)
- Profile URLs: `https://www.beatleader.com/u/{id}`.

## 3. Decisions

| Topic | Decision |
|---|---|
| Identity model | One `players` row per person; per-platform identities in a new `player_platforms` table; player ID internal and immutable |
| Schema migration | Additive only: new table, new columns, new indexes, column drops on `players`; **no primary-key rebuilds** (SQLite FK-safe) |
| Cross-platform ID collisions | BL score/leaderboard IDs are strings or collide with SS numeric IDs → BL rows get deterministic internal IDs (64-bit hash of `platform:external_id`, collision-retried); the platform's own ID lives in `external_id` |
| Merged view | Player page groups scores by (song hash, mode, difficulty); one row per group with a score chip per platform |
| Link conflicts | Refuse with a clear error; explicit merge action (`POST /players/{source}/merge`) moves data into the surviving player |
| Replay storage | SS layout untouched (`replays/{player}/{id}.dat`); BL under `replays/{player}/beatleader/{id}.bsor` |
| Rate limiting | Separate static BL limiter (~40 req / 10 s, headroom under the observed 50), honours `x-rate-limit-reset`; no new config knobs |
| `type` filter semantics | `complete` → `fullCombo = true`; `fail` → `fullCombo = false` (both platforms only record completed plays) |
| Pagination | Existing `page` / `per_page` params kept as-is |
| Score filters apply to | `min_score`/`max_score` on `modified_score` (the displayed score) |

## 4. Data model

All timestamps UTC. `model.All()` order extended with `PlayerPlatform`.

### 4.1 players

Columns unchanged **except** the per-platform sync state moves out (§4.2): drop `backfill_state`,
`backfill_page`, `backfill_total_pages`, `backfill_retry_at`, `last_polled_at`, `last_error`
(SQLite `DROP COLUMN`; the bundled pure-Go SQLite supports it).

- `id` (string PK) — internal, immutable, never renumbered: existing rows keep their ScoreSaber ID
  (so `/p/{ssid}` keeps resolving); new BeatLeader-only players get `bl-{external_id}` (ScoreSaber
  IDs are numeric, so the `bl-` prefix can never collide).
- `name`, `avatar_url`, `country` — display identity. Rule: refreshed from ScoreSaber when a
  ScoreSaber identity is linked (existing `UpdatePlayerProfile` flow), otherwise from BeatLeader.
- `enabled` — master toggle, applies to all platforms.

### 4.2 player_platforms (new)

| Column | Notes |
|---|---|
| `player_id` | FK → players, `OnDelete:CASCADE`, part of PK |
| `platform` | `scoresaber` \| `beatleader`, part of PK |
| `external_id` | The platform's own player ID (string). Unique index `(platform, external_id)` |
| `backfill_state` | `pending \| running \| done` (reuses `model.Backfill*` constants) |
| `backfill_page`, `backfill_total_pages` | Resume cursor, 1-based |
| `backfill_retry_at` | Nullable; set after a failed backfill page |
| `last_polled_at` | Nullable |
| `last_error` | Empty string when healthy; platform-scoped (e.g. "player not found on ScoreSaber") disables only this row, the player and its other platform keep running |

Exactly one row per linked identity; a player must keep ≥ 1 (unlinking the last one is refused —
delete the player instead).

### 4.3 leaderboards

Add `platform` and `external_id` (string), unique index `(platform, external_id)`:

- ScoreSaber rows (all existing rows): `platform='scoresaber'`, `external_id = itoa(id)`; `id`
  keeps being the ScoreSaber leaderboard ID.
- BeatLeader rows: `external_id` = the BL leaderboard ID string; `id` = deterministic int64 from a
  64-bit hash of `"beatleader:" + external_id` (on the astronomically unlikely PK collision the
  insert is retried with a counter suffix folded into the hash input). The public surface never
  exposes this internal ID — BL score routes use `external_id`.
- Column mapping from BL payloads: `song_hash` = `song.hash` (lowercase), `song_name`/`sub`/`author`
  from `song`, `mapper` = `song.mapper`, `difficulty` = `difficulty.value` (same 1–9 scale),
  `difficulty_raw` = `difficulty.difficultyName`, `game_mode` = `difficulty.modeName`,
  `cover_url` = `song.coverImage`, `status` = `RANKED` when `difficulty.status == ranked(3)` else
  `UNRANKED`, `stars` = `difficulty.stars` (0 when null), `max_score` = `difficulty.maxScore`.

### 4.4 scores

Add `platform`, `external_id` (string), `replay_url` (nullable — BL CDN URL); unique index
`(platform, external_id)`:

- ScoreSaber rows unchanged; `platform='scoresaber'`, `external_id = itoa(id)`, `replay_url` null.
- BeatLeader rows: `id` = internal hash-derived int64 (same derivation as §4.3),
  `external_id` = BL score ID, `leaderboard_id` = FK to the BL leaderboard row (§4.3),
  `modified_score` = `modifiedScore`, `unmodified_score` = `baseScore`, `accuracy`, `pp`, `rank`,
  `mods` = `modifiers` (already comma-separated), `full_combo`, `missed_notes`, `bad_cuts`,
  `max_combo`, `hmd` = BL `hmd` code mapped to a display name where known, else the raw code,
  `personal_best` = true (BL listing is PBs), `set_at` = `timeset` (UTC),
  `has_replay` = `replay != ""`, `replay_state` = `pending` when a replay URL exists else `none`,
  `replay_url` = the URL.
- The replay state machine (`none/pending/archived/gone/failed`, attempts, backoff) is shared
  verbatim across platforms.

### 4.5 sync_events

Add nullable `platform` column (populated for per-player events; `worker` events about global state
stay null). UI log gains a platform filter; existing kinds reused.

## 5. Sync worker

Work units become **(player, platform)** pairs from `players ⋈ player_platforms` where
`players.enabled`. The single-goroutine loop and tier priorities are unchanged:

1. **Poll** — for a `player_platforms` row whose `last_polled_at` is older than `poll_interval`:
   walk score pages newest-first (SS `sort=recent&personalBest=all`, BL `sortBy=date`), upsert, stop
   at a known `(platform, external_id)`, the end, or `MaxPollPages` (5). On page 1 refresh the
   player's display profile: from the ScoreSaber payload when that platform is polled, otherwise
   BeatLeader-only players fetch `GET /player/{id}` on each poll.
2. **New replays** — `pending` with `set_at >= player.added_at`, newest first, either platform.
3. **Backfill listing** — one page per non-done `player_platforms` row from `backfill_page`; total
   pages = `ceil(total / per_page)` from metadata (BL has no `totalPages`; SS keeps its own).
4. **Backfill replays** — remaining `pending`, newest first.

Round-robin across players *and platforms* within each tier. Platform adapter interface keeps poll/
backfill/download generic:

```go
type platformClient interface {
    // ScoresPage returns one page of scores for the platform identity.
    ScoresPage(ctx, externalID string, page int) (ScorePage, error)
    // Replay streams the replay for a stored score row (SS: by score ID; BL: row.ReplayURL).
    Replay(ctx, sc *model.Score) (io.ReadCloser, error)
}
```

Platform-specific error semantics:

| Case | ScoreSaber | BeatLeader |
|---|---|---|
| Scores endpoint 404 | player deleted → platform row `last_error`, platform disabled | **no scores** → treated as an empty page / end of listing |
| Player endpoint 404 | same as above | same (platform row disabled, player stays for other platform) |
| 429 / limit headers | SS limiter pause (3 windows) | BL limiter pause until `x-rate-limit-reset` |
| Replay 404 | `gone` | `gone` (BL CDN file removed) |

### 5.1 BeatLeader client (`internal/beatleader`)

Mirrors `internal/scoresaber`: typed client, no app logic. `NewClient(l *Limiter, opts...)` with
`Player(ctx, id)`, `Scores(ctx, playerID, page)` (`sortBy=date&order=desc&count=100`),
`Replay(ctx, url)`. Errors: `ErrNotFound`, `ErrRateLimited`, `StatusError`. Limiter: single 10 s
sliding window at 40 req (headroom under the observed 50), injectable clock, blocks until
`x-rate-limit-reset` when the server reports exhaustion or on 429. Replay downloads need no extra
limiter: the worker is single-goroutine, so downloads are already sequential.

### 5.2 Upsert semantics

- Upsert keyed on `(platform, external_id)`; a page's leaderboards are upserted first (BL rows
  derived per §4.3).
- Existing BL score row: refresh rank/pp/accuracy/score/personal-best; **if `timeset` or `replay`
  URL changed** → `replay_state=pending` (re-download; the file name is the row ID, so the latest
  replay replaces the old one — the platform never exposes superseded replays anyway).
- New `(platform, external_id)` → new row (improvements that mint a new BL score ID naturally land
  as new rows; superseded rows, if ever present, are preserved like ScoreSaber's).

### 5.3 Replay storage & reconciliation

- SS: `replays/{playerID}/{scoreRowID}.dat` (unchanged). BL: `replays/{playerID}/beatleader/`
  `{scoreRowID}.bsor`.
- Startup reconciliation scans both layouts: stray `.tmp` deleted; a `.bsor`/`.dat` without an
  `archived` row is adopted if its score row exists; an `archived` row with a missing file returns
  to `pending`.

## 6. HTTP surface

### 6.1 Public (all unchanged routes keep their exact semantics)

| Route | Content |
|---|---|
| `GET /p/{id}` | Player page — now the **merged, deduplicated list**: rows grouped by (song hash, mode, difficulty value) with a score chip per platform (each linking to its score page, showing score %, rank, platform badge); header stat chips per platform (scores tracked, replays archived). Route pattern relaxed to accept `bl-…` IDs |
| `GET /s/bl/{externalID}` | BeatLeader score page (same components as the SS page: cover, map, difficulty, stats, viewer iframe, download, embed code) |
| `GET /r/bl/{externalID}.bsor` | Raw BL replay — same treatment as `/r/{id}.dat`: `Access-Control-Allow-Origin: *`, Range, `ETag` = sha256, immutable cache, `Content-Disposition: attachment` |
| `GET /embed/bl/{externalID}` | Viewer wrapper → `/viewer/?replayURL={base}/r/bl/{id}.bsor&noProxy=true&…`; `frame-ancestors *` |

`/s/{id}`, `/r/{id}.dat`, `/embed/{id}` resolve ScoreSaber rows only (platform-qualified), so a BL
score ID colliding with an SS one can never shadow an existing embed.

CSP `img-src` gains `https://cdn.beatsaver.com https://cdn.assets.beatleader.xyz
https://avatars.steamstatic.com` (BL covers/avatars). Everything else unchanged.

### 6.2 Admin JSON API (`/api/v1`, session-cookie auth)

New/changed operations:

| Operation | Notes |
|---|---|
| `POST /players` | Body `{ref, platform?}`. URL pastes auto-detect (scoresaber.com / beatleader.com); bare IDs default `platform=scoresaber` (backward compatible), `platform` overrides. Resolves against the chosen platform before insert |
| `POST /players/{id}/identities` | Body `{platform, ref}`. Resolves the identity live, then links. **409** `identity_linked_elsewhere` (message names the other player, suggests merge); **409** if the player already has that platform; display fields refresh immediately from ScoreSaber when SS is the linked platform |
| `DELETE /players/{id}/identities/{platform}` | `?delete_files=` (default false). Drops that platform's scores (+ files when asked). **409** when it is the player's last identity |
| `POST /players/{source}/merge` | Body `{into: targetID}`. Moves score rows (duplicates by `(platform, external_id)` dropped together with their files), moves replay files, union of identities (target's per-platform row wins on collision), deletes the source player. Logged as a `worker` sync event |
| `GET /players/{id}/scores` | Filters below |
| `GET /scores/bl/{externalID}` | Single BL score (mirrors `GET /scores/{id}`) |

Player DTO: adds `identities[]` — `{platform, id (external), profile_url, last_polled_at,
backfill_state, backfill_page, backfill_total_pages, last_error, counts{…}}`; existing top-level
aggregated counts unchanged.

Score listing filters (all combinable with existing `search`, `state`, `ranked`, `page`, `per_page`):

| Param | Values | Meaning |
|---|---|---|
| `platform` | `all` (default) \| `scoresaber` \| `beatleader` | Restrict to one platform |
| `type` | `fail` \| `complete` | `complete` → `full_combo = true`; `fail` → `full_combo = false` (documented in OpenAPI; both platforms only record completed plays) |
| `min_score`, `max_score` | int64 | Inclusive bounds on `modified_score` |

The service `ScoreFilter` struct carries the new fields; `service.ListScoresMerged(playerID, …)`
(backed by the same filters where applicable) serves the merged web view, which groups in Go by
(song hash, mode, difficulty).

### 6.3 Web admin UI

- **Manage**: single add input; platform auto-detected from pasted URLs (a BL URL flips the
  selector; bare IDs default ScoreSaber, switchable). Player rows show platform chips with
  per-platform backfill/error state. Row actions: **Link identity** (for each missing platform),
  **Merge…** (dialog: pick target player, type-to-search), **Unlink** (per platform, with
  `delete_files` checkbox and last-identity guard).
- **Sync**: queue table gains a platform column (backfill page X/Y per platform); event log gains a
  platform filter; budget meters section gains the BL limiter snapshot next to the SS one.

## 7. Migration

Single idempotent step inside `db.Migrate`, ordered:

1. `AutoMigrate` — creates `player_platforms`, adds `platform`/`external_id`/`replay_url` to
   `scores` + `leaderboards`, `platform` to `sync_events`.
2. Backfill (one transaction): `leaderboards`/`scores` set `platform='scoresaber'` and
   `external_id = itoa(id)` where `platform` is empty; `players` get one `player_platforms` row
   (`scoresaber`, `external_id = id`) carrying their current backfill/poll/error values.
3. Create the unique indexes (after backfill; `IfNotExists`).
4. Drop the six moved `players` columns (no-op-safe on fresh databases).
5. `PRAGMA foreign_key_check` must return zero rows or the migration fails hard (abort startup).

Fresh installs run the same path with empty backfills. A committed test fixture — a v1 database
built from the current models with seeded players/scores/replays — is migrated in tests and asserts:
row counts preserved, `platform`/`external_id` populated, `player_platforms` rows created, and the
public routes resolving post-migration.

## 8. Documentation

README: player management section mentions both platforms and the new admin actions; API section
documents the new filters and endpoints; no new environment variables.

## 9. Testing

- **`beatleader`**: httptest fixtures — player, scores page, scoreless-404, player-404, 429 with
  `x-rate-limit-*` headers, replay stream; limiter window/reset behaviour under an injectable
  clock; opt-in `-tags live` smoke test mirroring `scoresaber`'s.
- **`service`**: link/unlink (incl. last-identity and already-linked refusals), merge (score moves,
  file moves, duplicate dropping, identity union), BL upsert (new row; in-place improvement with
  `timeset`/URL change → re-download), merged-list dedup grouping, score filters (`platform`,
  `type`, `min_score`, `max_score`, combinations).
- **`archiver`**: fake platform clients + injectable clock — per-platform work selection and
  round-robin, poll stop-at-known, BL scores-404-as-end, replay-by-URL, platform-scoped error
  disable, backfill resume per platform.
- **`api` / `web`**: new filter params end-to-end, 409 conflict mappings, BL routes (CORS, Range,
  ETag, embed framing), merged player page render with both-platform rows, CSP additions.
- **`db`**: migration fixture test (§7).
- `make generate` (gorm gen) after model changes; `make test lint` green; CI codegen drift check
  must stay clean.
