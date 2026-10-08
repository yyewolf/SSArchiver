# SSArchiver — Design

Date: 2026-10-08
Status: Draft for review

## 1. Purpose

ScoreSaber prunes replays (replay slots with retention states such as `PRUNE_CANDIDATE`). SSArchiver is a
self-hosted service that continuously archives the replays of players chosen by the instance owner, and serves
them back publicly: as browsable pages, raw `.dat` downloads, and an in-browser 3D viewer (ArcViewer) that can
be embedded on third-party sites such as portfolios.

**Success criteria**

- Once a replay is visible on ScoreSaber for a tracked player, SSArchiver archives it before it is pruned
  (given the instance is running and within rate-limit budget).
- Superseded replays (player beat their own score) stay in the archive.
- Any archived replay can be downloaded, watched, and embedded with a copy-pasted `<iframe>`.
- The UI is clean and consistent (shadcn look, neutral palette, no gradients), light and dark.
- One self-contained binary / minimal rootless container image.

**Non-goals (v1)**: multiple user accounts, API tokens, BeatLeader support, replay parsing/analytics,
oEmbed, notifications.

## 2. External facts (verified 2026-10-08)

ScoreSaber v2 API (`https://scoresaber.com/api/openapi.json`), no auth:

- `GET /api/v2/players/{id}` — player profile.
- `GET /api/v2/players/{id}/scores?sort=recent&limit=100&page=N&personalBest=all` — paginated scores.
  Without `personalBest=all` only current personal bests are returned (4274 vs 8372 for a test player);
  some non-PB scores still have replays, so we always pass `personalBest=all`.
  Response: `{data: [{score: {...,hasReplay,personalBest,createdAt,...}, leaderboard: {id, map:{hash,songName,...,coverUrl}, difficulty:{...}, realm:{leaderboardStatus,stars,...}}}], metadata:{page,itemsPerPage,totalItems,totalPages}}`.
- `GET /api/v2/scores/{id}/replay` — `application/octet-stream`, `Content-Disposition: attachment; filename="{id}.dat"`,
  typically ~1–3 MB (LZMA-compressed already; stored as-is).
- Rate limits (headers `x-ratelimit-{limit,remaining,reset}-{short,medium,long}`): **20 / 10 s, 60 / 60 s, 360 / 3600 s**.
- The legacy `/api/game/telemetry/downloadReplay` now requires a game session and is not used.

ArcViewer (`github.com/AllPoland/ArcViewer`, GPL-3.0):

- Unity WebGL build published on the `deploy` branch (`index.html`, `Build/`, `TemplateData/`, ~80 MB raw;
  `ArcViewer.wasm` 53 MB, `ArcViewer.data` 26 MB).
- URL parameters: `replayURL`, `noProxy`, `uiOff`, `autoPlay`, `loop`, `t`, `mode`, `difficulty`, `ssScoreId`, …
  It fetches the map itself from BeatSaver by hash.

## 3. Decisions

| Topic | Decision |
|---|---|
| Visibility | Public by default; login only guards admin actions |
| Viewer | Self-hosted ArcViewer, pinned `deploy` commit SHA, precompressed and `go:embed`ded |
| Backfill | Full history on player add, throttled, resumable, newest first |
| Accounts | Single admin, created by first-run setup |
| Architecture | One service layer; huma JSON API + templ/htmx UI on one `net/http` mux; in-process sync worker |
| DB | SQLite via pure-Go driver (`github.com/glebarez/sqlite`), GORM + GORM gen typed queries |
| UI kit | templUI (shadcn port for templ), Tailwind v4 standalone CLI, htmx |
| Image | `FROM scratch`, rootless (65532), read-only-rootfs compatible |

## 4. Architecture

```
cmd/ssarchiver/            main → cobra root
internal/cli/              cobra commands: serve, migrate, healthcheck, user reset-password, version
internal/config/           flags + env (SSA_DATA_DIR, SSA_LISTEN, SSA_BASE_URL, SSA_HOURLY_BUDGET, SSA_LOG_LEVEL)
internal/model/            GORM models (input to gorm gen)
internal/db/               open, pragmas (WAL, foreign_keys, busy_timeout), migrations; gen output in internal/db/query
internal/scoresaber/       typed v2 client + rate limiter; no app logic
internal/storage/          replay blob store: {data}/replays/{playerID}/{scoreID}.dat
internal/service/          players, scores, auth/sessions, settings, sync status read models
internal/sync/             worker: poll, backfill, replay download, event log
internal/api/              huma operations under /api/v1
internal/web/              templ components/pages, htmx handlers, static assets (embedded), middleware
internal/viewer/           go:generate fetcher + embedded precompressed ArcViewer + handler
```

Dependency direction: `cli → (api, web, sync) → service → (db/query, storage, scoresaber)`. `api` and `web`
never touch GORM directly.

`serve` opens the DB, runs migrations, reconciles storage, then runs the HTTP server and the sync worker in one
`errgroup` bound to a signal-cancelled context (SIGINT/SIGTERM), with a 15 s graceful HTTP shutdown.

Config defaults: `SSA_DATA_DIR=./data` (`/data` in the image), `SSA_LISTEN=:8080`, `SSA_HOURLY_BUDGET=300`.
`SSA_BASE_URL` is required for correct absolute URLs in embeds and OpenGraph; if unset, it is derived from the
request (`X-Forwarded-Proto`/`Host` honoured only when `SSA_TRUST_PROXY=true`).

## 5. Data model

All timestamps UTC.

- **players**: `id` (ScoreSaber ID, PK, string), `name`, `avatar_url`, `country`, `enabled` (bool),
  `added_at`, `last_polled_at` (nullable), `last_error` (nullable), `backfill_state` (`pending|running|done`),
  `backfill_page` (int, resume cursor), `backfill_total_pages` (int).
- **leaderboards**: `id` (PK), `song_hash`, `song_name`, `song_sub_name`, `song_author`, `mapper`,
  `difficulty` (int), `difficulty_raw`, `game_mode`, `cover_url`, `status` (`RANKED|QUALIFIED|LOVED|UNRANKED`),
  `stars`, `max_score`.
- **scores**: `id` (ScoreSaber score ID, PK), `player_id` (FK), `leaderboard_id` (FK), `rank`, `modified_score`,
  `unmodified_score`, `accuracy`, `pp`, `mods` (comma string), `full_combo`, `missed_notes`, `bad_cuts`,
  `max_combo`, `hmd`, `personal_best`, `set_at`, `has_replay`, `replay_state`
  (`none|pending|archived|gone|failed`), `replay_size`, `replay_sha256`, `archived_at`, `attempts`,
  `next_attempt_at`, `last_error`. Indexes: `(player_id, set_at DESC)`, `(replay_state, set_at DESC)`,
  `(leaderboard_id)`.
- **users**: `id`, `username` (unique), `password_hash` (argon2id, PHC string), `created_at`.
- **sessions**: `token_hash` (sha256 of a 32-byte random token, PK), `user_id`, `expires_at` (30 days,
  sliding), `created_at`.
- **settings**: `key` (PK), `value` — `instance_title`, `poll_interval` (default 10m), `worker_paused`.
- **sync_events**: `id`, `at`, `level` (`info|warn|error`), `kind` (`poll|scores|replay|backfill|ratelimit|worker`),
  `player_id` (nullable), `score_id` (nullable), `message`. Pruned hourly to 7 days / 10 000 rows.

Deleting a player removes its rows; the admin chooses whether to also delete files.

## 6. Sync worker

### 6.1 Rate limiter (`scoresaber.Limiter`)

Wraps every outgoing request. Three sliding windows (10 s/20, 60 s/60, 3600 s/`SSA_HOURLY_BUDGET`, default 300 to
leave headroom on a shared IP). After each response it reads `x-ratelimit-remaining-*`/`reset-*` and, when the
server reports less remaining than our own window, blocks until the server's reset. A 429 pauses all requests
until the longest reported reset. The limiter exposes a snapshot (used/limit/reset per window, last server
values) for the status page. Uses an injectable clock for tests.

### 6.2 Work selection

Single goroutine loop. Each tick picks the first available unit of work in priority order:

1. **Poll** — an enabled player whose `last_polled_at` is older than `poll_interval`: list
   `sort=recent&personalBest=all` pages until a page contains an already-known score ID (or the end). Upsert
   leaderboards and scores; `has_replay` → `replay_state=pending`. Set `last_polled_at`.
2. **New replays** — `pending` replays with `set_at >= player.added_at`, newest first.
3. **Backfill listing** — one page for a player with `backfill_state != done`, from `backfill_page`;
   advance cursor; `done` when past `totalPages`.
4. **Backfill replays** — remaining `pending` replays (with `next_attempt_at <= now`), newest first.

Players are round-robined within each tier so one large backfill does not starve others. If no work is
available, the loop sleeps until the earliest of: next poll due, next retry due, or a wake signal (manual
"poll now", player added, resume). When `worker_paused` is set, the loop idles.

New players start with `backfill_state=pending` and are polled immediately.

### 6.3 Replay download

Stream to `{data}/replays/{player}/{score}.dat.tmp` while hashing (sha256), `fsync`, rename to `.dat`, then
update the row (`archived`, size, hash, `archived_at`). Startup reconciliation: delete stray `.tmp` files;
a `.dat` without an `archived` row is re-hashed and adopted if its score row exists, otherwise deleted;
an `archived` row whose file is missing returns to `pending`.

### 6.4 Errors

| Case | Behaviour |
|---|---|
| Replay 404 | `gone`, never retried; event `warn` |
| 5xx / network / timeout | `attempts++`, `next_attempt_at` = backoff 1m, 5m, 15m, 1h; after 5 attempts `failed` |
| 429 | limiter pause (6.1); work unit retried, no attempt counted |
| Player 404 | player disabled, `last_error` set, event `error` |
| Malformed JSON | event `error`, player skipped this tick |
| Disk full / write error | event `error`, worker pauses itself (`worker_paused=true`) |

The worker never panics the process; a recovered panic is logged as an `error` event and the loop continues.

### 6.5 Status

The worker publishes an in-memory status (state: `running|idle|paused|ratelimited`, current task description,
ratelimit reset time) read by `service` together with DB aggregates per player
(archived/pending/failed/gone/total, backfill page X/Y, next poll). ETA = pending replays ÷ effective hourly
budget remaining after polling cost.

## 7. HTTP surface

### 7.1 Public

| Route | Content |
|---|---|
| `GET /` | Tracked players grid: avatar, name, country, archived replay count |
| `GET /p/{id}` | Player page: header, backfill progress bar while running, score table (htmx search by song, filters: ranked, has replay, archived; pagination 50/page) |
| `GET /s/{scoreID}` | Score page: cover, map, difficulty, stats; inline viewer iframe; Download `.dat`; Copy embed code; OpenGraph/Twitter meta |
| `GET /embed/{scoreID}` | Full-viewport viewer only; query `autoplay`, `loop`, `ui` (0/1) mapped to ArcViewer params; `frame-ancestors *` |
| `GET /r/{scoreID}.dat` | Raw replay via `http.ServeContent` (Range), `Access-Control-Allow-Origin: *`, `ETag` = sha256, `Cache-Control: public, max-age=31536000, immutable`, `Content-Disposition: attachment` |
| `GET /viewer/*` | Embedded ArcViewer (7.4) |
| `GET /api/v1/players`, `/players/{id}`, `/players/{id}/scores`, `/scores/{id}` | huma JSON |
| `GET /api/docs`, `/api/openapi.json` | huma docs |
| `GET /healthz` | `200 ok` when DB reachable |

Non-archived scores render on the score page with an explanatory state ("pending", "pruned before it could be
archived", "failed"); `/r/` and `/embed/` return 404 for them.

Embed snippet: `<iframe src="{base}/embed/{id}" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>`.
`/embed/{id}` is a minimal HTML page containing one full-viewport iframe pointing at
`/viewer/?replayURL={base}/r/{id}.dat&noProxy=true&autoPlay=…&loop=…&uiOff=…` (no redirect, so the public URL
stays stable if viewer parameters change).

### 7.2 Admin

- `GET/POST /setup` — available only while `users` is empty; creation runs in a transaction that re-checks the
  count; afterwards 404. All other routes redirect to `/setup` while no user exists.
- `GET/POST /login`, `POST /logout`.
- `GET /admin` — players management: add by ID or ScoreSaber profile URL (resolved live, shows preview before
  confirm), enable/disable, poll now, delete (checkbox: delete files).
- `GET /admin/sync` — status page, htmx refresh every 3 s:
  worker state + current task + pause/resume; budget meters per window (ours and server-reported);
  per-player queue table (next poll, backfill page X/Y, archived/pending/failed/gone, ETA, Poll now,
  Retry failed); activity log (filter by level/kind/player, paginated); failed replays table with per-row
  and bulk retry.
- `GET/POST /admin/settings` — instance title, poll interval, change password.
- Admin JSON under `/api/v1` (POST/PATCH/DELETE players, POST poll, POST sync pause/resume/retry, GET sync
  status) uses the same session cookie.
- CLI `ssarchiver user reset-password` for lockout recovery.

### 7.3 Security

- Session cookie `ssa_session`: HttpOnly, `Secure` when served over HTTPS, `SameSite=Lax`, server-side row.
- `http.CrossOriginProtection` on all non-safe methods (CSRF).
- Login: per-IP limiter (5 attempts/min), constant-time compare via argon2id verify.
- Headers everywhere: `X-Content-Type-Options: nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`,
  CSP `default-src 'self'; img-src 'self' https://cdn.scoresaber.com data:; frame-ancestors 'none'`
  (`frame-src 'self'` on score pages).
- `/embed/*` and `/viewer/*`: `frame-ancestors *`; `/viewer/*` CSP additionally allows `'wasm-unsafe-eval'`,
  `'unsafe-inline'` scripts/styles required by the Unity loader, and `connect-src` for BeatSaver
  (`https://*.beatsaver.com`, `https://*.beatmaps.io`) and self.

### 7.4 Viewer embedding

- `internal/viewer/fetch.go` (run by `go generate ./internal/viewer`): downloads the `deploy` branch tarball at
  `ArcViewerSHA` (constant), verifies its sha256 (constant), extracts to `internal/viewer/dist/`, writes `.br`
  and `.gz` siblings for compressible files, and copies ArcViewer's `LICENSE`.
- `dist/` is git-ignored except a committed placeholder `dist/PLACEHOLDER`, so `//go:embed dist` always
  compiles (plain `go build`/lint work without the 80 MB download). When `dist/index.html` is absent the
  handler returns `503` "viewer not bundled — run `go generate ./internal/viewer`" and score pages hide the
  viewer. `make build`, CI and goreleaser `before.hooks` run the generator; the release workflow fails if
  `dist/index.html` is missing.
- Handler picks `.br` → `.gz` → raw by `Accept-Encoding`, sets `Content-Encoding`, `Vary: Accept-Encoding`,
  correct MIME (`application/wasm`, `application/javascript`, `application/octet-stream` for `.data`), and
  long-lived caching keyed on the SHA (`/viewer/` responses carry `ETag: "{sha}-{file}"`).
- `GET /viewer/LICENSE` serves ArcViewer's GPL-3.0 text; the footer and release notes link to the source at the
  pinned SHA. SSArchiver remains BSD-3-Clause; ArcViewer is distributed as a separate aggregated work.
- Updating ArcViewer = change SHA + checksum constants, regenerate.

## 8. UI

- templ components using **templUI** (copied into `internal/web/components/ui`, per templUI's model), shadcn
  neutral tokens, Inter/system font stack, light/dark via `prefers-color-scheme` + toggle (stored in
  `localStorage`), no gradients, no decorative shadows beyond shadcn defaults.
- Tailwind v4 standalone CLI (pinned version, checksum-verified download in `scripts/`) builds
  `internal/web/static/app.css`; htmx served from embedded static (pinned, no CDN).
- Pages: layout with top nav (instance title, Players, Admin when logged in), tables using shadcn table style,
  badges for difficulty / ranked / replay state, cards for players, toasts for admin actions.

## 9. Build, quality, release

- **Tooling** pinned via `go tool` directives in `go.mod`: `templ`, `gorm gen` generator, `golangci-lint` v2.
- **Makefile**: `generate` (gorm gen, templ, tailwind, viewer), `build`, `test`, `lint`, `dev` (templ watch +
  rebuild/restart).
- Generated Go code and built CSS are **committed**; the viewer bundle is **not** (see 7.4). CI fails on
  codegen drift (`git diff --exit-code` after `make generate`, viewer excluded).
- **Binary**: `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X …version/commit/date"`. Imports
  `time/tzdata` and `golang.org/x/crypto/x509roots/fallback` so it runs with no OS files.
- **golangci-lint v2** (`.golangci.yml`): errcheck, govet, staticcheck, revive, gosec, bodyclose, noctx,
  sqlclosecheck, errorlint, misspell, unconvert; formatters gofumpt + goimports; generated files excluded.
- **GitHub Actions**
  - `ci.yml` (PR + main): setup-go, generate, `git diff --exit-code`, lint, `go test -race ./...`, build.
  - `release.yml` (tags `v*`): goreleaser —
    binaries linux/darwin/windows × amd64/arm64, archives, checksums, SBOMs (syft);
    multi-arch image `ghcr.io/yewolf/ssarchiver:{version,latest}`;
    keyless **cosign** signatures for checksums and image;
    **SLSA build provenance** via `actions/attest-build-provenance` for archives and image (verifiable with
    `gh attestation verify`). Permissions: `contents: write`, `packages: write`, `id-token: write`,
    `attestations: write`.
  - All third-party actions pinned by commit SHA; Dependabot for `gomod` and `github-actions`.

### 9.1 Container image

- `FROM scratch`; contents: the binary at `/ssarchiver` and an empty `/data` owned by `65532:65532`
  (created via `COPY --chown`). No shell, no package manager, no OS files.
- `USER 65532:65532`, `EXPOSE 8080`, `VOLUME /data`, `ENV SSA_DATA_DIR=/data`,
  `HEALTHCHECK CMD ["/ssarchiver","healthcheck"]`, OCI labels (source, revision, version, licenses).
- Works with `--read-only --cap-drop=ALL --security-opt=no-new-privileges` (all writes, including temp
  files and SQLite WAL, under `/data`) and under rootless Docker/Podman (README documents `:U` volume flag /
  `chown 65532` for bind mounts).
- Expected size ≈ 35–40 MB, dominated by the embedded ArcViewer.

## 10. Testing

- `scoresaber`: `httptest` server with recorded JSON fixtures (scores page, player, replay bytes, 404, 429 with
  headers); limiter windows and header sync with a fake clock.
- `sync`: fake client + fake clock — priority order, round-robin, poll stops at known score, backfill resume
  after restart, 404 → `gone`, backoff → `failed`, 429 pause, disk-error self-pause, storage reconciliation.
- `service` / `db`: real SQLite in `t.TempDir()`.
- `web` / `api`: `httptest` — setup only once (concurrent setup race), auth redirects, CSRF rejection,
  `/r/` CORS + Range + ETag, `/embed` framing headers, viewer content-encoding negotiation.
- No live ScoreSaber calls in CI; opt-in `go test -tags live ./internal/scoresaber` smoke test.
