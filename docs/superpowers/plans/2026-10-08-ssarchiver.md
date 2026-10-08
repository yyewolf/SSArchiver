# SSArchiver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **Update the Progress Tracking section (below) as you go — it is the hand-off contract between agents.**

**Goal:** Build SSArchiver, a self-hosted Go service that continuously archives ScoreSaber replays for admin-chosen players and serves them publicly as pages, raw `.dat` downloads, and an embeddable self-hosted ArcViewer.

**Architecture:** One `internal/service` layer (GORM + gorm gen over SQLite) is consumed by three thin front-ends: a templ/htmx web UI, a huma JSON API, and an in-process `archiver` worker that polls ScoreSaber under a strict client-side rate limiter and stores replays on disk. Everything ships as one static binary (ArcViewer embedded gzip'd) and a `FROM scratch`, rootless container image released by goreleaser with cosign signatures and SLSA provenance.

**Tech Stack:** Go 1.27 · cobra · net/http `ServeMux` · huma v2 (humago adapter) · templ · htmx 2 · shadcn-templ v2 (shadcn/ui port) · Tailwind v4 standalone CLI · GORM + gorm gen · `github.com/glebarez/sqlite` (pure Go) · argon2id · golangci-lint v2 · goreleaser v2 · GitHub Actions · cosign · `actions/attest-build-provenance`.

**Spec:** [`docs/superpowers/specs/2026-10-08-ssarchiver-design.md`](../specs/2026-10-08-ssarchiver-design.md) — read it before starting any task. Section numbers below (e.g. "spec §6.2") refer to it.

## Global Constraints

Every task's requirements implicitly include all of these.

- Module path `github.com/yyewolf/ssarchiver`; `go 1.27` in `go.mod`. Container image `ghcr.io/yyewolf/ssarchiver`.
- Pinned versions (do not "upgrade while you're there"):
  - `github.com/spf13/cobra v1.10.2`, `github.com/danielgtaylor/huma/v2 v2.39.1`, `github.com/a-h/templ v0.3.1070`
  - `gorm.io/gorm v1.31.2`, `gorm.io/gen v0.3.29`, `github.com/glebarez/sqlite v1.11.0`
  - `github.com/alexedwards/argon2id v1.0.0`, `golang.org/x/sync v0.23.0`, `golang.org/x/crypto v0.57.0`
  - shadcn-templ CLI `github.com/axadrn/shadcn-templ/v2/cmd/shadcn-templ@v2.0.0-beta.13`
  - golangci-lint `v2.14.0`, goreleaser `v2.18.2` (`~> v2` in CI), Tailwind CLI `v4.3.3`, htmx `2.0.10`
  - ArcViewer `deploy` branch commit `c776256497b66f7c91a74162cfcd943b0f45ee2e` (v0.8.1-beta); ArcViewer `LICENSE` from `main` commit `a7b2d984f91346ade9f25e523afb5ed67cb2cb84`
- Builds use `CGO_ENABLED=0` and `-trimpath`. Tests may use cgo (`go test -race` needs it).
- **No Node.js toolchain.** CSS = Tailwind standalone binary; JS = vendored files.
- All runtime writes go under `SSA_DATA_DIR` (SQLite DB + WAL, replays, temp files). Nothing may write to `/tmp` or the CWD at runtime (read-only rootfs).
- Outbound network at runtime: only `https://scoresaber.com` (the browser additionally fetches maps for ArcViewer).
- Logging via `log/slog` only. Errors wrapped with `%w` and context (`fmt.Errorf("storage: rename %s: %w", p, err)`).
- `context.Context` is the first parameter of every function doing I/O.
- **Never use `gorm:"default:..."` on `bool` columns** (GORM would turn an explicit `false` into the default). Set every field explicitly instead.
- Status/state columns are plain `string` with constants in `internal/model` (gorm gen handles plain strings best).
- Timestamps are stored and compared in UTC (`time.Now().UTC()` via `service.Now()`).
- `api`, `web` and `archiver` never import `gorm.io/*` or `internal/db/query`; they go through `internal/service`.
- UI: only shadcn-templ components + Tailwind utility classes using the theme tokens (`bg-background`, `text-muted-foreground`, `border`, …). **No gradients, no custom colours, no decorative shadows.** Light + dark.
- Generated code is committed: `*_templ.go`, `internal/db/query/*`, `internal/web/static/css/app.css`, shadcn-templ components and JS bundle. The ArcViewer bundle (`internal/viewer/dist/*` except `PLACEHOLDER`) is **not** committed.
- Every commit must pass `go build ./...` and `go test ./...`. Commit messages use Conventional Commits (`feat:`, `fix:`, `test:`, `chore:`, `ci:`, `docs:`).
- ScoreSaber facts (spec §2): scores endpoint `GET /api/v2/players/{id}/scores?sort=recent&limit=100&page=N&personalBest=all`; replay `GET /api/v2/scores/{id}/replay`; player `GET /api/v2/players/{id}/basic`; limits 20/10 s, 60/60 s, 360/h; 404 body is JSON `{"statusCode":404,...}`.

## Review Focus

Inputs/failure modes the spec implies but that are easy to get wrong. Each has a pinned test in the owning task.

1. **Re-polling a known score must not clobber archive state** — an upsert that rewrites `replay_state`/`replay_sha256`/`archived_at` would silently "un-archive" replays. Expected: only rank/pp/personal_best/has_replay change. → Task 6 `TestUpsertScoresPreservesArchiveState`.
2. **Concurrent first-run `/setup` submissions** — two browsers racing setup must yield exactly one admin. → Task 7 `TestSetupConcurrentOnlyOneWins`.
3. **Replay download failing mid-stream** (network drop, context cancel) — must leave neither a `.dat` nor a `.tmp`, keep the score `pending`, and count one attempt (not mark it archived). → Task 5 `TestPutSourceErrorLeavesNothing`, Task 9 `TestDownloadMidStreamFailure`.
4. **ScoreSaber 429 / `remaining=0`** — worker must stop sending until reset and must not burn a retry attempt on the score. → Task 3 `TestObserve429...`, Task 9 `TestDownloadRateLimitedDoesNotCountAttempt`.
5. **Player reference input variants** — admins paste `https://scoresaber.com/u/76561198059961776?page=2&sort=recent`, `scoresaber.com/u/…/`, IDs with whitespace, or garbage/other hosts. Expected: the first three resolve, the rest give a clear validation error and no request. → Task 6 `TestParsePlayerRef`.

---

## Progress Tracking

**Rules for every agent working on this plan:**

1. Before starting a task: set its row to `🟡 in progress`, fill `Owner` (agent/session id or name) and today's date in `Started`.
2. Tick each step checkbox (`- [x]`) in the task body as soon as it is done — not in batches.
3. When the task's final commit lands: set `✅ done`, put the short commit SHA in `Commit`, and add one line to **Verification log** with the exact commands run and their result (e.g. `go test ./internal/service/... → ok`).
4. If you deviate from the plan (renamed function, different library call, extra file), add an entry to **Deviations log** *and* fix any later task text that refers to the old name. Later agents only read their own task.
5. If blocked: set `⛔ blocked`, explain in **Session hand-off**, stop.
6. Before ending a session: update **Session hand-off** (current task, next step, anything half-done, uncommitted files). Commit the plan file together with your work (`docs: update plan progress`).

Status legend: `⬜ todo` · `🟡 in progress` · `✅ done` · `⛔ blocked`

| # | Task | Status | Owner | Started | Commit |
|---|------|--------|-------|---------|--------|
| 1 | Project bootstrap (go.mod, cobra, config, lint, Makefile) | ✅ done | kilo (euria-code) | 2026-10-08 | 7bab24d |
| 2 | Models, SQLite, gorm gen | ✅ done | kilo (euria-code) | 2026-10-08 | 6a44ea8 |
| 3 | ScoreSaber rate limiter | ✅ done | kilo (euria-code) | 2026-10-08 | 3f13084 |
| 4 | ScoreSaber client | ✅ done | kilo (euria-code) | 2026-10-08 | ca78f43 |
| 5 | Replay blob storage | ✅ done | kilo (euria-code) | 2026-10-08 | 1dac253 |
| 6 | Service: players, scores, replay queue, events log | ✅ done | kilo (euria-code) | 2026-10-08 | 1a38195 |
| 7 | Service: auth, sessions, settings, events listing | ✅ done | kilo (euria-code) | 2026-10-08 | f1880fc |
| 8 | Archiver: poll, backfill, loop | ✅ done | kilo (euria-code) | 2026-10-08 | a6eead1 |
| 9 | Archiver: replay download, reconciliation, status | ✅ done | kilo (euria-code) | 2026-10-08 | 6aca494 |
| 10 | Embedded ArcViewer | ✅ done | kilo (euria-code) | 2026-10-08 | 83b5059 |
| 11 | Web foundation (shadcn-templ, Tailwind, layout, middleware, home) | ✅ done | kilo (euria-code) + SDD implementer subagent | 2026-10-08 | 5970ec8 |
| 12 | Web auth (setup, login, logout) | ⬜ todo | | | |
| 13 | Public player & score pages | ⬜ todo | | | |
| 14 | Replay download & embed endpoints | ⬜ todo | | | |
| 15 | Admin: players & settings | ⬜ todo | | | |
| 16 | Admin: sync status page | ⬜ todo | | | |
| 17 | huma JSON API | ⬜ todo | | | |
| 18 | App wiring, serve/healthcheck/migrate/user commands, e2e test | ⬜ todo | | | |
| 19 | Packaging: Dockerfile, goreleaser, CI/release workflows, README | ⬜ todo | | | |

### Session hand-off

_Current task:_ Task 11 done (commit 5970ec8; executed via subagent-driven development with a clean task review)
_Next step:_ Task 12 Step 1 (web auth — setup, login, logout)
_Half-done / uncommitted:_ —
_Notes for next agent:_ go.mod is `go 1.27` per user instruction. Web foundation (Task 11) landed with 4 forced deviations: registry item is `native-select` (dir `components/nativeselect`), vendored htmx 2.0.10 sha256 is `71ea6718…` (brief's checksum stale — do not "fix" it back), pinned CLI generates `icon.Icon(name)` factory not named icon funcs, lint-forced tweaks (errors.Is in Recover, `httptest.NewRequestWithContext` in tests, gofumpt, ST1023) plus 3 extra behavior tests + `helpers_test.go`. `components/aspectratio` is an unused CLI-pulled dep — leave it. Next: web auth (Task 12), then public pages (13-14), admin (15-16), API (17), wiring (18), packaging (19).

### Deviations log

| Date | Task | Deviation | Reason | Later tasks updated? |
|------|------|-----------|--------|----------------------|
| 2026-10-08 | 1 | `go.mod` uses `go 1.27` instead of `go 1.26` (Tech Stack + Global Constraints updated) | User instruction: "use go 1.27" | Yes — plan header/constraints updated |
| 2026-10-08 | 1 | `internal/cli/version.go` discards the `fmt.Fprintf` return (`_, _ =`) | golangci-lint v2.14.0 errcheck rejects the unchecked call | No |
| 2026-10-08 | 2 | Bumped transitive deps `gorm.io/plugin/dbresolver` 1.5.3→1.6.2 and `golang.org/x/tools` →v0.51.0 (x/mod, x/sys follow) | gen v0.3.29's pinned transitives don't compile with gorm v1.31.2 (`undefined: gorm.Stmt`) / Go 1.27 (x/tools tokeninternal); gen/gorm/sqlite stay at pinned versions | No |
| 2026-10-08 | 4 | Live smoke test uses player id `76561198038925092` instead of `1922350521131465` | User asked their own profile be used for tests | No |
| 2026-10-08 | 5 | `Store.Put` writes `.tmp` with `0o600` instead of `0o640` | gosec G302 (pinned linter) rejects group-readable files; replays need no group access | No |
| 2026-10-08 | 5 | `Store.Scan` removes `.tmp` via `os.Root` scoped to the store root; added `_ =` discards on `Close` | gosec G122 (TOCTOU) + errcheck under pinned linter; behaviour unchanged | No |
| 2026-10-08 | 6 | None — plan code compiled and passed as written (gen `GteCol`/`LtCol` and `Update(col, nil)` worked; `Preload(q.Score.Leaderboard)` relation present) | — | — |
| 2026-10-08 | 7 | None — plan code compiled and passed as written (incl. concurrent setup race via `_txlock=immediate`) | — | — |
| 2026-10-08 | 8 | `Worker.lastReplayPlayer` field and `fakeClient.replaysCalled()` removed in Task 8 (re-added by Task 9) | pinned `unused` linter rejects members only used by Task 9 | Yes — Task 9 must re-add them |
| 2026-10-08 | 9 | Re-added `lastReplayPlayer` + `replaysCalled()` exactly as the plan originally specified; otherwise none | resolves the Task 8 deviation | Yes — deviation resolved |
| 2026-10-08 | 10 | Lint fixes on plan code: `_ =` discards on deferred `Close`s; `writeGzip` dirs 0o750 + `WriteFile` 0o600 (G301/G306); on-the-fly decompression bounded by `maxViewerFile` = 256 MB (G110); tests use `httptest.NewRequestWithContext` (noctx) | pinned linter v2.14.0; behaviour unchanged | No |
| 2026-10-08 | 11 | shadcn-templ registry item is `native-select`, not `nativeselect` (generated dir stays `components/nativeselect`) | pinned beta.13 registry has no `nativeselect` item; official docs use `native-select` | No |
| 2026-10-08 | 11 | Vendored htmx 2.0.10 hashes `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de`, not the plan's `4b2fd977…` | plan checksum stale/unpublished for canonical unpkg artifact; verified 3 ways (unpkg package.json, jsDelivr byte-identical, embedded version string) | No |
| 2026-10-08 | 11 | Icons used via `icon.Icon("sun"|"log-out"|"circle-alert")` factory instead of named funcs | pinned CLI generates an `Icon(name)` factory, not named icon functions | No |
| 2026-10-08 | 11 | Lint fixes on plan code: `errors.Is(err, http.ErrAbortHandler)` in Recover (errorlint), gofumpt reformat, ST1023 `var hd :=`, `httptest.NewRequestWithContext` in new tests; extra behavior tests (`TestSessionCookieAuthenticates`, `TestHTMXRedirectUsesHXRedirect`, `helpers_test.go`) to keep scaffolding lint-clean without nolint | pinned linter v2.14.0 | No |
| 2026-10-08 | 11 | `components/aspectratio` present but unused (CLI-pulled registry dependency); go.mod indirect bumps forced by templ v0.3.1070 (`x/crypto` 0.57.0, `x/text`, `go-isatty`, `tool` directive) | CLI behaviour; all four pinned tool versions intact | No |

### Verification log

| Date | Task | Command(s) | Result |
|------|------|------------|--------|
| 2026-10-08 | 1 | `go test ./internal/config/...` (pre-impl) | FAIL: no non-test Go files (expected) |
| 2026-10-08 | 1 | `go test ./internal/config/...` | ok |
| 2026-10-08 | 1 | `go test ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/ssarchiver` | all ok, build succeeds |
| 2026-10-08 | 1 | `make lint` (after `_, _ = fmt.Fprintf` fix) | 0 issues. |
| 2026-10-08 | 1 | `go build ./... && go test ./...` (go 1.27.0) | all ok |
| 2026-10-08 | 2 | `go generate ./internal/db && go mod tidy` | query pkg generated; `func Use` + `Leaderboard` relation present |
| 2026-10-08 | 2 | `go test ./internal/db/...` | ok (pragmas, preload, cascade, IsDuplicate) |
| 2026-10-08 | 2 | `golangci-lint fmt` + `make lint` | 0 issues. (gofumpt field alignment in model.go) |
| 2026-10-08 | 2 | `go build ./... && go test ./...` | all ok |
| 2026-10-08 | 3 | `go test ./internal/scoresaber/...` (pre-impl) | FAIL: undefined: NewLimiter (expected) |
| 2026-10-08 | 3 | `go test -race ./internal/scoresaber/...` | ok |
| 2026-10-08 | 3 | `go build ./... && go test ./... && make lint` | all ok, 0 issues. |
| 2026-10-08 | 4 | `go test ./internal/scoresaber/...` (pre-impl) | FAIL: undefined: scoresaber.ErrRateLimited/StatusError (expected) |
| 2026-10-08 | 4 | `go test -race ./internal/scoresaber/... && go vet -tags live ./internal/scoresaber/...` | ok, vet clean |
| 2026-10-08 | 4 | `go test -tags live -run Live ./internal/scoresaber -v` | PASS (real API, player 76561198038925092) |
| 2026-10-08 | 4 | `make lint` (after errcheck/gofumpt fixes, commit 7513419) | 0 issues. |
| 2026-10-08 | 5 | `go test ./internal/storage/...` (pre-impl) | FAIL: no non-test Go files (expected) |
| 2026-10-08 | 5 | `go test -race ./internal/storage/...` | ok |
| 2026-10-08 | 5 | `go test ./... && make lint` (after gosec/errcheck fixes) | all ok, 0 issues. |
| 2026-10-08 | 6 | `go test ./internal/service/...` (pre-impl) | FAIL: no non-test Go files (expected) |
| 2026-10-08 | 6 | `go mod tidy && go test -race ./internal/service/...` | ok (all 4 test files, incl. archive-state preservation + ParsePlayerRef) |
| 2026-10-08 | 6 | `go test ./... && make lint` | all ok, 0 issues. |
| 2026-10-08 | 7 | `go test ./internal/service/...` (pre-impl) | FAIL: pruneEvents undefined (expected) |
| 2026-10-08 | 7 | `go mod tidy && go test -race ./internal/service/...` | ok (auth incl. concurrent setup, settings, events) |
| 2026-10-08 | 7 | `go test ./... && make lint` | all ok, 0 issues. |
| 2026-10-08 | 8 | `go test ./internal/archiver/...` (pre-impl) | FAIL: no non-test Go files (expected) |
| 2026-10-08 | 8 | `go test -race ./internal/archiver/...` | ok (poll/backfill/loop tests) |
| 2026-10-08 | 8 | `go test ./... && make lint` (after removing Task-9-only members) | all ok, 0 issues. |
| 2026-10-08 | 9 | `go test ./internal/service/... ./internal/archiver/...` (pre-impl) | FAIL: ReconcileStorage/ReconcileResult/replaysCalled undefined (expected) |
| 2026-10-08 | 9 | `go test -race ./internal/service/... ./internal/archiver/...` | ok (reconcile + download + priority + status tests; Task 8 tests still pass) |
| 2026-10-08 | 9 | `go test ./... && make lint` | all ok, 0 issues. |
| 2026-10-08 | 10 | `go test ./internal/viewer/...` (pre-impl) | FAIL: no non-test Go files (expected) |
| 2026-10-08 | 10 | `go test -race ./internal/viewer/...` | ok |
| 2026-10-08 | 10 | `go generate ./internal/viewer` ×2 | dist = 30 MB; second run: "viewer bundle up to date"; git ignores dist/* except PLACEHOLDER |
| 2026-10-08 | 10 | `go test ./... && make lint` (after lint fixes) | all ok, 0 issues. |
| 2026-10-08 | 11 | `go test ./internal/httpx/...` (pre-impl) | FAIL: no non-test Go files (expected) |
| 2026-10-08 | 11 | `go test ./internal/httpx/...` | ok (nonce, nosniff, DocsCSP, BaseURL/XFF, ClientIP, Recover) |
| 2026-10-08 | 11 | `go test ./internal/web/views/...` (pre-impl) | FAIL: undefined: TimeAgo … (expected) |
| 2026-10-08 | 11 | `go test ./internal/web/views/...` | ok (all 22 format assertions) |
| 2026-10-08 | 11 | `go test ./internal/web/ -v -run 'TestHealthz\|TestRedirectsToSetupUntilDone\|TestHomeListsPlayersWithSecurityHeaders\|TestStaticCaching\|TestCrossSitePostRejected'` | 5/5 PASS |
| 2026-10-08 | 11 | `make generate` | gen-go, gen-templ, gen-css all succeed |
| 2026-10-08 | 11 | `go test -race ./... && make lint && CGO_ENABLED=0 go build -trimpath ./...` | 11 packages ok, 0 issues., build OK |
| 2026-10-08 | 11 | controller spot-checks post-review: `make lint`, `sha256sum internal/web/static/js/htmx.min.js`, `bin/tailwindcss`, `go test ./...` | 0 issues., hash 71ea6718… matches ruling, v4.3.3 present, 11 pkgs ok |

---

## File Map

```
cmd/ssarchiver/main.go                 entrypoint: signal ctx, tzdata + x509 fallback roots
internal/buildinfo/buildinfo.go        Version/Commit/Date (ldflags)
internal/config/config.go              env + flags → Config, Validate, paths
internal/cli/{root,version,serve,healthcheck,migrate,user}.go   cobra commands
internal/model/model.go                GORM models + state constants
internal/db/db.go                      Open (pragmas), Migrate, //go:generate
internal/db/gen/main.go                gorm gen generator program
internal/db/query/*.gen.go             GENERATED typed queries (committed)
internal/scoresaber/{limiter,client,types}.go   rate limiter + v2 API client
internal/storage/storage.go            replay files: Put/Open/Remove/Scan/HashFile
internal/service/service.go            Service struct, errors, clock, wake channel
internal/service/{players,scores,replays,backfill,events,auth,settings,stats,reconcile}.go
internal/archiver/{worker,poll,download,status}.go   background worker
internal/viewer/viewer.go              constants, embed, manifest parsing, HTTP handler
internal/viewer/manifest.txt           per-file sha256 manifest of the ArcViewer build
internal/viewer/fetch/main.go          go:generate program downloading + gzipping the build
internal/viewer/dist/PLACEHOLDER       committed; real bundle is generated & git-ignored
internal/httpx/httpx.go                ctx helpers (user, base URL), security headers, CSP, logging, recover
internal/web/web.go                    Handler, Deps, Routes, Middleware, render helpers
internal/web/{pages,auth,public,replay,admin,admin_sync}.go   handlers
internal/web/views/*.templ             templ pages and partials (+ generated *_templ.go)
internal/web/components/**             shadcn-templ components (CLI-generated, committed)
internal/web/utils/shadcn-templ.go     shadcn-templ utils (CLI-generated)
internal/web/assets/css/*.css          Tailwind input (globals.css etc.)
internal/web/static/{static.go,css,js} embedded static files + cache-busted URLs
internal/api/{api,dto,players,scores,sync}.go   huma operations
internal/app/app.go                    wiring + Run (HTTP server + worker)
internal/testutil/*.go                 shared fakes: DB, clock, resolver, status, fixtures
scripts/fetch-tailwind.sh              pinned, checksum-verified Tailwind CLI download
components.json                        shadcn-templ CLI config (repo root)
Makefile, .golangci.yml, .goreleaser.yaml, Dockerfile, docker/data/.keep
.github/workflows/{ci,release}.yml, .github/dependabot.yml
README.md, THIRD_PARTY_NOTICES.md
```

---
## Task 1: Project bootstrap

**Files:**
- Create: `go.mod`, `cmd/ssarchiver/main.go`, `internal/buildinfo/buildinfo.go`, `internal/config/config.go`, `internal/config/config_test.go`, `internal/cli/root.go`, `internal/cli/version.go`, `internal/cli/root_test.go`, `Makefile`, `.golangci.yml`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `config.Config{DataDir, Listen, BaseURL string; HourlyBudget int; LogLevel string; TrustProxy bool}`
  - `config.FromEnv() Config`, `(*Config).BindFlags(*pflag.FlagSet)`, `(*Config).Validate() error`, `(Config).DBPath() string`, `(Config).ReplayDir() string`, `config.ParseLogLevel(string) (slog.Level, error)`
  - `buildinfo.Version`, `buildinfo.Commit`, `buildinfo.Date` (string vars), `buildinfo.UserAgent() string`
  - `cli.NewRootCmd() *cobra.Command` (root holds `cfg *config.Config` shared with subcommands via `newXCmd(cfg)` constructors), `cli.Execute(ctx) int`

- [x] **Step 1: Initialise the module and dependencies**

```bash
go mod init github.com/yyewolf/ssarchiver
go mod edit -go=1.26
go get github.com/spf13/cobra@v1.10.2
go get golang.org/x/crypto/x509roots/fallback@latest
```

- [x] **Step 2: Write the failing config tests** — `internal/config/config_test.go`

```go
package config_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/config"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SSA_DATA_DIR", "SSA_LISTEN", "SSA_BASE_URL", "SSA_HOURLY_BUDGET", "SSA_LOG_LEVEL", "SSA_TRUST_PROXY"} {
		t.Setenv(k, "")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearEnv(t)
	c := config.FromEnv()
	if c.DataDir != "./data" || c.Listen != ":8080" || c.HourlyBudget != 300 || c.LogLevel != "info" || c.TrustProxy || c.BaseURL != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("SSA_DATA_DIR", "/srv/ssa")
	t.Setenv("SSA_LISTEN", "127.0.0.1:9000")
	t.Setenv("SSA_BASE_URL", "https://replays.example.com/")
	t.Setenv("SSA_HOURLY_BUDGET", "120")
	t.Setenv("SSA_LOG_LEVEL", "debug")
	t.Setenv("SSA_TRUST_PROXY", "true")
	c := config.FromEnv()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/srv/ssa" || c.Listen != "127.0.0.1:9000" || c.HourlyBudget != 120 || !c.TrustProxy {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if c.BaseURL != "https://replays.example.com" {
		t.Fatalf("base URL not normalised: %q", c.BaseURL)
	}
	if got, want := c.DBPath(), filepath.Join("/srv/ssa", "ssarchiver.db"); got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
	if got, want := c.ReplayDir(), filepath.Join("/srv/ssa", "replays"); got != want {
		t.Fatalf("ReplayDir = %q, want %q", got, want)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*config.Config){
		"budget zero":       func(c *config.Config) { c.HourlyBudget = 0 },
		"budget above 360":  func(c *config.Config) { c.HourlyBudget = 361 },
		"budget unparsable": func(c *config.Config) { c.HourlyBudget = -1 },
		"relative base url": func(c *config.Config) { c.BaseURL = "replays.example.com" },
		"ftp base url":      func(c *config.Config) { c.BaseURL = "ftp://example.com" },
		"bad log level":     func(c *config.Config) { c.LogLevel = "loud" },
		"empty data dir":    func(c *config.Config) { c.DataDir = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			c := config.FromEnv()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestInvalidBudgetEnvFailsValidation(t *testing.T) {
	clearEnv(t)
	t.Setenv("SSA_HOURLY_BUDGET", "lots")
	c := config.FromEnv()
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unparsable SSA_HOURLY_BUDGET")
	}
}

func TestParseLogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		got, err := config.ParseLogLevel(in)
		if err != nil || got != want {
			t.Fatalf("ParseLogLevel(%q) = %v, %v", in, got, err)
		}
	}
}
```

- [x] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/config/...`
Expected: FAIL — `package github.com/yyewolf/ssarchiver/internal/config` has no non-test Go files / undefined: `config.FromEnv`.

- [x] **Step 4: Implement config** — `internal/config/config.go`

```go
// Package config loads SSArchiver's runtime configuration from the
// environment and command-line flags.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// Config is the process configuration. Zero values are not meaningful; use FromEnv.
type Config struct {
	DataDir      string
	Listen       string
	BaseURL      string
	HourlyBudget int
	LogLevel     string
	TrustProxy   bool
}

// FromEnv builds a Config from SSA_* environment variables with defaults.
func FromEnv() Config {
	return Config{
		DataDir:      envOr("SSA_DATA_DIR", "./data"),
		Listen:       envOr("SSA_LISTEN", ":8080"),
		BaseURL:      os.Getenv("SSA_BASE_URL"),
		HourlyBudget: envInt("SSA_HOURLY_BUDGET", 300),
		LogLevel:     envOr("SSA_LOG_LEVEL", "info"),
		TrustProxy:   os.Getenv("SSA_TRUST_PROXY") == "true" || os.Getenv("SSA_TRUST_PROXY") == "1",
	}
}

// BindFlags registers flags whose defaults are the current (env-derived) values.
func (c *Config) BindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "data directory for the database and replays (SSA_DATA_DIR)")
	fs.StringVar(&c.Listen, "listen", c.Listen, "HTTP listen address (SSA_LISTEN)")
	fs.StringVar(&c.BaseURL, "base-url", c.BaseURL, "public base URL, e.g. https://replays.example.com (SSA_BASE_URL)")
	fs.IntVar(&c.HourlyBudget, "hourly-budget", c.HourlyBudget, "max ScoreSaber requests per hour, 1-360 (SSA_HOURLY_BUDGET)")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "debug, info, warn or error (SSA_LOG_LEVEL)")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", c.TrustProxy, "trust X-Forwarded-* headers (SSA_TRUST_PROXY)")
}

// Validate checks values and normalises BaseURL (no trailing slash).
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return errors.New("config: data dir must not be empty")
	}
	if c.HourlyBudget < 1 || c.HourlyBudget > 360 {
		return fmt.Errorf("config: hourly budget must be between 1 and 360, got %d", c.HourlyBudget)
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: base url must be an absolute http(s) URL, got %q", c.BaseURL)
		}
		c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	}
	if _, err := ParseLogLevel(c.LogLevel); err != nil {
		return err
	}
	return nil
}

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "ssarchiver.db") }

// ReplayDir is the root directory of archived replay files.
func (c Config) ReplayDir() string { return filepath.Join(c.DataDir, "replays") }

// ParseLogLevel maps a case-insensitive level name to slog.Level.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("config: unknown log level %q", s)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt returns def when unset and -1 when unparsable (Validate rejects -1).
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}
```

- [x] **Step 5: Run config tests**

Run: `go mod tidy && go test ./internal/config/...`
Expected: `ok  github.com/yyewolf/ssarchiver/internal/config`

- [x] **Step 6: Write the failing CLI test** — `internal/cli/root_test.go`

```go
package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/cli"
)

func TestVersionCommand(t *testing.T) {
	root := cli.NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ssarchiver dev") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}
```

- [x] **Step 7: Implement buildinfo, root, version, main**

`internal/buildinfo/buildinfo.go`:

```go
// Package buildinfo holds version metadata injected via -ldflags at build time.
package buildinfo

// Set with -X github.com/yyewolf/ssarchiver/internal/buildinfo.Version=... etc.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// UserAgent is sent on every ScoreSaber request.
func UserAgent() string {
	return "SSArchiver/" + Version + " (+https://github.com/yyewolf/SSArchiver)"
}
```

`internal/cli/root.go`:

```go
// Package cli defines the ssarchiver command tree.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/config"
)

// NewRootCmd builds the command tree. Subcommands share one *config.Config.
func NewRootCmd() *cobra.Command {
	cfg := config.FromEnv()
	root := &cobra.Command{
		Use:           "ssarchiver",
		Short:         "Archive and serve ScoreSaber replays",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cfg.BindFlags(root.PersistentFlags())
	root.AddCommand(newVersionCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context) int {
	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
```

`internal/cli/version.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/buildinfo"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "ssarchiver %s (commit %s, built %s)\n", buildinfo.Version, buildinfo.Commit, buildinfo.Date)
		},
	}
}
```

`cmd/ssarchiver/main.go`:

```go
// Command ssarchiver archives and serves ScoreSaber replays.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // the scratch image has no zoneinfo

	_ "golang.org/x/crypto/x509roots/fallback" // the scratch image has no CA bundle

	"github.com/yyewolf/ssarchiver/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
```

- [x] **Step 8: Run tests and build**

Run: `go mod tidy && go test ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/ssarchiver`
Expected: all `ok`, build succeeds.

- [x] **Step 9: Add lint config, Makefile, .gitignore**

`.golangci.yml`:

```yaml
version: "2"
run:
  timeout: 5m
linters:
  default: standard
  enable:
    - bodyclose
    - errorlint
    - gosec
    - misspell
    - noctx
    - revive
    - sqlclosecheck
    - unconvert
  settings:
    revive:
      rules:
        - name: exported
          disabled: true
        - name: package-comments
          disabled: true
    gosec:
      excludes:
        - G304 # file paths are validated by storage
  exclusions:
    generated: lax
    paths:
      - internal/db/query
      - internal/web/components
      - internal/web/utils
    rules:
      - path: _test\.go
        linters: [gosec, errcheck]
formatters:
  enable:
    - gofumpt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/yyewolf/ssarchiver
  exclusions:
    generated: lax
    paths:
      - internal/db/query
      - internal/web/components
      - internal/web/utils
```

`Makefile` (later tasks append targets — keep this exact structure):

```make
GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
LDFLAGS  := -s -w

.PHONY: generate build test lint tidy

generate:

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ssarchiver ./cmd/ssarchiver

test:
	go test -race ./...

lint:
	$(GOLANGCI) run

tidy:
	go mod tidy
```

Append to `.gitignore`:

```gitignore

# SSArchiver
/bin/
/data/
/dist/
internal/viewer/dist/*
!internal/viewer/dist/PLACEHOLDER
```

- [x] **Step 10: Lint**

Run: `make lint`
Expected: `0 issues.` Fix anything reported (typically formatting: run `$(GOLANGCI) fmt`).

- [ ] **Step 11: Commit**

```bash
git add go.mod go.sum cmd internal Makefile .golangci.yml .gitignore
git commit -m "chore: bootstrap module, cobra root, config and lint setup"
```

---

## Task 2: Models, SQLite, gorm gen

**Files:**
- Create: `internal/model/model.go`, `internal/db/db.go`, `internal/db/gen/main.go`, `internal/db/query/*` (generated), `internal/db/db_test.go`, `internal/testutil/db.go`
- Modify: `Makefile` (`generate` target)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - Models `model.Player`, `model.Leaderboard`, `model.Score` (with `Player *Player`, `Leaderboard *Leaderboard` belongs-to), `model.User`, `model.Session` (with `User *User`), `model.Setting`, `model.SyncEvent`; `model.All() []any`
  - Constants `model.BackfillPending|Running|Done`, `model.ReplayNone|Pending|Archived|Gone|Failed`, `model.LevelInfo|Warn|Error`, `model.KindPoll|Scores|Replay|Backfill|RateLimit|Worker`
  - `db.Open(path string) (*gorm.DB, error)`, `db.Migrate(*gorm.DB) error`, `db.IsDuplicate(error) bool`
  - Generated `query.Use(*gorm.DB) *query.Query` with `q.Player`, `q.Score`, … and `IPlayerDo`/`IScoreDo`… interfaces (mode `gen.WithQueryInterface`)
  - `testutil.OpenDB(t testing.TB) *gorm.DB` (temp dir, migrated, closed on cleanup)

- [x] **Step 1: Write the models** — `internal/model/model.go`

```go
// Package model holds the GORM models. They are the input of gorm gen
// (internal/db/gen) — run `make generate` after changing them.
package model

import "time"

// Player backfill states.
const (
	BackfillPending = "pending"
	BackfillRunning = "running"
	BackfillDone    = "done"
)

// Score replay states (spec §5).
const (
	ReplayNone     = "none"     // ScoreSaber has no replay for this score
	ReplayPending  = "pending"  // waiting to be downloaded
	ReplayArchived = "archived" // stored on disk
	ReplayGone     = "gone"     // ScoreSaber returned 404 before we got it
	ReplayFailed   = "failed"   // gave up after MaxReplayAttempts
)

// Sync event levels and kinds.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"

	KindPoll      = "poll"
	KindScores    = "scores"
	KindReplay    = "replay"
	KindBackfill  = "backfill"
	KindRateLimit = "ratelimit"
	KindWorker    = "worker"
)

type Player struct {
	ID                 string `gorm:"primaryKey"`
	Name               string `gorm:"not null"`
	AvatarURL          string `gorm:"not null"`
	Country            string `gorm:"not null"`
	Enabled            bool   `gorm:"not null"`
	AddedAt            time.Time `gorm:"not null"`
	LastPolledAt       *time.Time
	LastError          string `gorm:"not null"`
	BackfillState      string `gorm:"not null;index"`
	BackfillPage       int    `gorm:"not null"` // next page to fetch, 1-based
	BackfillTotalPages int    `gorm:"not null"`
	BackfillRetryAt    *time.Time
}

type Leaderboard struct {
	ID            int64   `gorm:"primaryKey;autoIncrement:false"`
	SongHash      string  `gorm:"not null;index"`
	SongName      string  `gorm:"not null;index"`
	SongSubName   string  `gorm:"not null"`
	SongAuthor    string  `gorm:"not null"`
	Mapper        string  `gorm:"not null"`
	Difficulty    int     `gorm:"not null"`
	DifficultyRaw string  `gorm:"not null"`
	GameMode      string  `gorm:"not null"`
	CoverURL      string  `gorm:"not null"`
	Status        string  `gorm:"not null"` // RANKED, QUALIFIED, LOVED, UNRANKED
	Stars         float64 `gorm:"not null"`
	MaxScore      int64   `gorm:"not null"`
}

type Score struct {
	ID              int64        `gorm:"primaryKey;autoIncrement:false"`
	PlayerID        string       `gorm:"not null;index:idx_scores_player_set,priority:1"`
	Player          *Player      `gorm:"constraint:OnDelete:CASCADE"`
	LeaderboardID   int64        `gorm:"not null;index"`
	Leaderboard     *Leaderboard `gorm:"constraint:OnDelete:RESTRICT"`
	Rank            int          `gorm:"not null"`
	ModifiedScore   int64        `gorm:"not null"`
	UnmodifiedScore int64        `gorm:"not null"`
	Accuracy        float64      `gorm:"not null"` // 0..1
	PP              float64      `gorm:"not null"`
	Mods            string       `gorm:"not null"` // comma separated
	FullCombo       bool         `gorm:"not null"`
	MissedNotes     int          `gorm:"not null"`
	BadCuts         int          `gorm:"not null"`
	MaxCombo        int          `gorm:"not null"`
	HMD             string       `gorm:"not null"`
	PersonalBest    bool         `gorm:"not null"`
	SetAt           time.Time    `gorm:"not null;index:idx_scores_player_set,priority:2,sort:desc;index:idx_scores_state_set,priority:2,sort:desc"`
	HasReplay       bool         `gorm:"not null"`
	ReplayState     string       `gorm:"not null;index:idx_scores_state_set,priority:1"`
	ReplaySize      int64        `gorm:"not null"`
	ReplaySHA256    string       `gorm:"not null"`
	ArchivedAt      *time.Time
	Attempts        int    `gorm:"not null"`
	NextAttemptAt   *time.Time
	LastError       string `gorm:"not null"`
}

type User struct {
	ID           uint      `gorm:"primaryKey"`
	Username     string    `gorm:"not null;uniqueIndex"`
	PasswordHash string    `gorm:"not null"`
	CreatedAt    time.Time `gorm:"not null"`
}

type Session struct {
	TokenHash string    `gorm:"primaryKey"` // hex sha256 of the cookie token
	UserID    uint      `gorm:"not null;index"`
	User      *User     `gorm:"constraint:OnDelete:CASCADE"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null"`
}

type Setting struct {
	Key   string `gorm:"primaryKey"`
	Value string `gorm:"not null"`
}

type SyncEvent struct {
	ID       int64     `gorm:"primaryKey"`
	At       time.Time `gorm:"not null;index"`
	Level    string    `gorm:"not null;index"`
	Kind     string    `gorm:"not null;index"`
	PlayerID *string   `gorm:"index"`
	ScoreID  *int64
	Message  string `gorm:"not null"`
}

// All returns every model, in migration order.
func All() []any {
	return []any{&Player{}, &Leaderboard{}, &Score{}, &User{}, &Session{}, &Setting{}, &SyncEvent{}}
}
```

- [x] **Step 2: Write db.Open / Migrate** — `internal/db/db.go`

```go
// Package db opens the SQLite database and runs migrations.
package db

//go:generate go run ./gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// pragmas: WAL for concurrent reads, FKs for cascades, busy_timeout so the
// worker and HTTP handlers wait instead of failing, temp_store(memory) so
// SQLite never writes temp files outside the data dir (read-only rootfs),
// _txlock=immediate so write transactions take the lock up front (no
// SQLITE_BUSY on lock upgrade; also serialises first-run setup).
const pragmas = "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
	"&_pragma=temp_store(memory)&_pragma=synchronous(NORMAL)&_txlock=immediate"

// Open opens (creating if needed) the database at path.
func Open(path string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("db: create dir: %w", err)
	}
	gdb, err := gorm.Open(sqlite.Open("file:"+path+pragmas), &gorm.Config{
		Logger:         logger.Discard,
		NowFunc:        func() time.Time { return time.Now().UTC() },
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("db: handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(8)
	return gdb, nil
}

// Migrate creates/updates the schema.
func Migrate(gdb *gorm.DB) error {
	if err := gdb.AutoMigrate(model.All()...); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

// Close closes the underlying sql.DB.
func Close(gdb *gorm.DB) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// IsDuplicate reports whether err is a unique/primary-key violation.
func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "UNIQUE constraint failed")
}
```

- [x] **Step 3: Write the generator** — `internal/db/gen/main.go`

```go
// Command gen generates typed queries into internal/db/query.
// Run via `go generate ./internal/db` (cwd = internal/db).
package main

import (
	"gorm.io/gen"

	"github.com/yyewolf/ssarchiver/internal/model"
)

func main() {
	g := gen.NewGenerator(gen.Config{
		OutPath: "./query",
		Mode:    gen.WithQueryInterface,
	})
	g.ApplyBasic(model.All()...)
	g.Execute()
}
```

- [x] **Step 4: Generate and inspect**

```bash
go get gorm.io/gorm@v1.31.2 gorm.io/gen@v0.3.29 github.com/glebarez/sqlite@v1.11.0
go generate ./internal/db
go mod tidy
ls internal/db/query
grep -n "func Use" internal/db/query/gen.go
grep -n "Leaderboard " internal/db/query/scores.gen.go | head
```

Expected: `gen.go`, `players.gen.go`, `scores.gen.go`, … exist; `func Use(db *gorm.DB, opts ...gen.DOOption) *Query`; `scores.gen.go` contains a `Leaderboard` relation field (used later for `Preload(q.Score.Leaderboard)`). If the relation field is missing, record a deviation and use `Preload(field.NewRelation("Leaderboard", ""))` where later tasks call `Preload(q.Score.Leaderboard)`.

Update `Makefile`:

```make
generate: gen-go

gen-go:
	go generate ./internal/db
```

- [x] **Step 5: Write the test helper** — `internal/testutil/db.go`

```go
// Package testutil holds fakes and helpers shared by tests. It must only be
// imported from _test.go files.
package testutil

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db"
)

// OpenDB returns a migrated database in a temp dir, closed at test end.
func OpenDB(t testing.TB) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(gdb); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	return gdb
}
```

- [x] **Step 6: Write the DB tests** — `internal/db/db_test.go`

```go
package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestPragmas(t *testing.T) {
	gdb := testutil.OpenDB(t)
	var fk, temp int
	var mode string
	gdb.Raw("PRAGMA foreign_keys").Scan(&fk)
	gdb.Raw("PRAGMA journal_mode").Scan(&mode)
	gdb.Raw("PRAGMA temp_store").Scan(&temp)
	if fk != 1 || mode != "wal" || temp != 2 {
		t.Fatalf("pragmas: foreign_keys=%d journal_mode=%s temp_store=%d", fk, mode, temp)
	}
}

func seed(t *testing.T, q *query.Query) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := q.Player.WithContext(ctx).Create(&model.Player{ID: "1", Name: "p", Enabled: true, AddedAt: now, BackfillState: model.BackfillPending, BackfillPage: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.Leaderboard.WithContext(ctx).Create(&model.Leaderboard{ID: 10, SongName: "Song"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Score.WithContext(ctx).Create(&model.Score{ID: 100, PlayerID: "1", LeaderboardID: 10, SetAt: now, ReplayState: model.ReplayPending}); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripWithPreload(t *testing.T) {
	q := query.Use(testutil.OpenDB(t))
	seed(t, q)
	sc, err := q.Score.WithContext(context.Background()).Preload(q.Score.Leaderboard).Where(q.Score.ID.Eq(100)).First()
	if err != nil {
		t.Fatal(err)
	}
	if sc.Leaderboard == nil || sc.Leaderboard.SongName != "Song" {
		t.Fatalf("leaderboard not preloaded: %+v", sc.Leaderboard)
	}
}

func TestDeletePlayerCascadesScores(t *testing.T) {
	q := query.Use(testutil.OpenDB(t))
	seed(t, q)
	ctx := context.Background()
	if _, err := q.Player.WithContext(ctx).Where(q.Player.ID.Eq("1")).Delete(); err != nil {
		t.Fatal(err)
	}
	n, err := q.Score.WithContext(ctx).Count()
	if err != nil || n != 0 {
		t.Fatalf("scores left after cascade: %d (%v)", n, err)
	}
}

func TestIsDuplicate(t *testing.T) {
	q := query.Use(testutil.OpenDB(t))
	seed(t, q)
	err := q.Player.WithContext(context.Background()).Create(&model.Player{ID: "1", AddedAt: time.Now()})
	if !db.IsDuplicate(err) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if db.IsDuplicate(nil) {
		t.Fatal("nil is not a duplicate")
	}
}
```

- [x] **Step 7: Run tests**

Run: `go test ./internal/db/...`
Expected: `ok`. (If `TestPragmas` reports `temp_store=0`, the driver ignored the pragma: switch the DSN to `_pragma=temp_store(2)` and log a deviation.)

- [ ] **Step 8: Commit**

```bash
make lint
git add go.mod go.sum internal/model internal/db internal/testutil Makefile
git commit -m "feat: add models, SQLite setup and gorm gen queries"
```

---

## Task 3: ScoreSaber rate limiter

**Files:**
- Create: `internal/scoresaber/limiter.go`, `internal/scoresaber/limiter_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `scoresaber.NewLimiter(hourly int) *Limiter`
  - `(*Limiter).Wait(ctx) error` — blocks until a request may be sent and records it
  - `(*Limiter).Observe(h http.Header, status int)` — feeds response headers back
  - `(*Limiter).Snapshot() LimiterSnapshot`
  - `type LimiterSnapshot struct{ Windows []WindowSnapshot; BlockedUntil time.Time; Waiting bool; WaitUntil time.Time }`
  - `type WindowSnapshot struct{ Name string; Limit, Used int; Period time.Duration; ServerRemaining int /* -1 unknown */; ServerResetAt time.Time }`

Behaviour (spec §6.1): three sliding windows `short` 20/10 s, `medium` 60/60 s, `long` hourly/1 h. After each response, for every window whose `x-ratelimit-remaining-<name>` is `<= 0`, block all requests until `now + x-ratelimit-reset-<name>` seconds (values > 1e9 are treated as unix epochs). On HTTP 429 with no exhausted window reported, block for `Retry-After` seconds, else 60 s. A 429 must **not** block for the long window's reset unless the long window itself is exhausted.

Tests use `testing/synctest` (Go 1.25+): inside the bubble `time.Now`, timers and `context.WithTimeout` use a fake clock that jumps forward when all goroutines are blocked, so `time.Since(start)` measures exactly how long `Wait` blocked.

- [x] **Step 1: Write the failing tests** — `internal/scoresaber/limiter_test.go`

```go
package scoresaber

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func waitN(t *testing.T, l *Limiter, n int) {
	t.Helper()
	for range n {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShortWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		start := time.Now()
		waitN(t, l, 20)
		if d := time.Since(start); d != 0 {
			t.Fatalf("first 20 waited %v", d)
		}
		waitN(t, l, 1)
		if d := time.Since(start); d != 10*time.Second {
			t.Fatalf("21st waited until %v, want 10s", d)
		}
	})
}

func TestMediumWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		start := time.Now()
		waitN(t, l, 60) // 20 at 0s, 20 at 10s, 20 at 20s
		if d := time.Since(start); d != 20*time.Second {
			t.Fatalf("60th at %v, want 20s", d)
		}
		waitN(t, l, 1)
		if d := time.Since(start); d != 60*time.Second {
			t.Fatalf("61st at %v, want 60s", d)
		}
	})
}

func TestHourlyBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(3)
		start := time.Now()
		waitN(t, l, 4)
		if d := time.Since(start); d != time.Hour {
			t.Fatalf("4th at %v, want 1h", d)
		}
	})
}

func TestObserveRemainingZeroBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		h := http.Header{}
		h.Set("x-ratelimit-remaining-medium", "0")
		h.Set("x-ratelimit-reset-medium", "42")
		h.Set("x-ratelimit-remaining-long", "100")
		h.Set("x-ratelimit-reset-long", "3600")
		l.Observe(h, http.StatusOK)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != 42*time.Second {
			t.Fatalf("waited %v, want 42s", d)
		}
	})
}

func TestObserve429WithoutHeadersBacksOffOneMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		l.Observe(http.Header{}, http.StatusTooManyRequests)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != time.Minute {
			t.Fatalf("waited %v, want 1m", d)
		}
	})
}

func TestObserve429UsesExhaustedWindowNotLongReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		h := http.Header{}
		h.Set("x-ratelimit-remaining-short", "0")
		h.Set("x-ratelimit-reset-short", "7")
		h.Set("x-ratelimit-remaining-long", "250")
		h.Set("x-ratelimit-reset-long", "3600")
		l.Observe(h, http.StatusTooManyRequests)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != 7*time.Second {
			t.Fatalf("waited %v, want 7s", d)
		}
	})
}

func TestObserve429RetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		h := http.Header{}
		h.Set("Retry-After", "15")
		l.Observe(h, http.StatusTooManyRequests)
		start := time.Now()
		waitN(t, l, 1)
		if d := time.Since(start); d != 15*time.Second {
			t.Fatalf("waited %v, want 15s", d)
		}
	})
}

func TestWaitHonoursContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		l.Observe(http.Header{}, http.StatusTooManyRequests) // blocked 1m
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := l.Wait(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
		if s := l.Snapshot(); s.Waiting {
			t.Fatal("snapshot still reports waiting after cancellation")
		}
	})
}

func TestSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := NewLimiter(300)
		waitN(t, l, 5)
		h := http.Header{}
		h.Set("x-ratelimit-remaining-long", "290")
		h.Set("x-ratelimit-reset-long", "1200")
		l.Observe(h, http.StatusOK)
		s := l.Snapshot()
		if len(s.Windows) != 3 {
			t.Fatalf("windows = %d", len(s.Windows))
		}
		for _, w := range s.Windows {
			if w.Used != 5 {
				t.Fatalf("%s used = %d, want 5", w.Name, w.Used)
			}
		}
		long := s.Windows[2]
		if long.Name != "long" || long.Limit != 300 || long.ServerRemaining != 290 || !long.ServerResetAt.Equal(time.Now().Add(20*time.Minute)) {
			t.Fatalf("long window snapshot wrong: %+v", long)
		}
		if s.Windows[0].ServerRemaining != -1 {
			t.Fatal("unknown server remaining must be -1")
		}
		time.Sleep(11 * time.Second)
		if used := l.Snapshot().Windows[0].Used; used != 0 {
			t.Fatalf("short window not pruned: %d", used)
		}
	})
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scoresaber/...`
Expected: FAIL — `undefined: NewLimiter`.

- [x] **Step 3: Implement** — `internal/scoresaber/limiter.go`

```go
// Package scoresaber is a minimal client for ScoreSaber's public v2 API with
// a client-side rate limiter that mirrors the server's limits.
package scoresaber

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type WindowSnapshot struct {
	Name            string
	Limit           int
	Used            int
	Period          time.Duration
	ServerRemaining int // -1 when unknown
	ServerResetAt   time.Time
}

type LimiterSnapshot struct {
	Windows      []WindowSnapshot
	BlockedUntil time.Time // zero when not blocked by server feedback
	Waiting      bool
	WaitUntil    time.Time
}

type window struct {
	name            string
	limit           int
	period          time.Duration
	hits            []time.Time
	serverRemaining int
	serverResetAt   time.Time
}

func (w *window) prune(now time.Time) {
	i := 0
	for i < len(w.hits) && !w.hits[i].Add(w.period).After(now) {
		i++
	}
	w.hits = w.hits[i:]
}

// Limiter enforces ScoreSaber's three request windows for this process.
type Limiter struct {
	mu           sync.Mutex
	windows      []*window
	blockedUntil time.Time
	waitUntil    time.Time
}

// NewLimiter returns a limiter with ScoreSaber's short/medium windows and the
// given hourly budget for the long window.
func NewLimiter(hourly int) *Limiter {
	return &Limiter{windows: []*window{
		{name: "short", limit: 20, period: 10 * time.Second, serverRemaining: -1},
		{name: "medium", limit: 60, period: time.Minute, serverRemaining: -1},
		{name: "long", limit: hourly, period: time.Hour, serverRemaining: -1},
	}}
}

// Wait blocks until a request may be sent, then records it in every window.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		until := l.blockedUntil
		for _, w := range l.windows {
			w.prune(now)
			if len(w.hits) >= w.limit {
				if t := w.hits[0].Add(w.period); t.After(until) {
					until = t
				}
			}
		}
		if !until.After(now) {
			for _, w := range l.windows {
				w.hits = append(w.hits, now)
			}
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

// Observe records rate-limit headers from a ScoreSaber response.
func (l *Limiter) Observe(h http.Header, status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, w := range l.windows {
		reset, hasReset := headerInt(h, "x-ratelimit-reset-"+w.name)
		if hasReset {
			w.serverResetAt = resetTime(now, reset)
		}
		if rem, ok := headerInt(h, "x-ratelimit-remaining-"+w.name); ok {
			w.serverRemaining = int(rem)
			if rem <= 0 && hasReset && w.serverResetAt.After(l.blockedUntil) {
				l.blockedUntil = w.serverResetAt
			}
		}
	}
	if status == http.StatusTooManyRequests && !l.blockedUntil.After(now) {
		d := time.Minute
		if ra, ok := headerInt(h, "Retry-After"); ok && ra > 0 {
			d = time.Duration(ra) * time.Second
		}
		l.blockedUntil = now.Add(d)
	}
}

// Snapshot returns the current usage for display.
func (l *Limiter) Snapshot() LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	s := LimiterSnapshot{Waiting: !l.waitUntil.IsZero(), WaitUntil: l.waitUntil}
	if l.blockedUntil.After(now) {
		s.BlockedUntil = l.blockedUntil
	}
	for _, w := range l.windows {
		w.prune(now)
		s.Windows = append(s.Windows, WindowSnapshot{
			Name: w.name, Limit: w.limit, Used: len(w.hits), Period: w.period,
			ServerRemaining: w.serverRemaining, ServerResetAt: w.serverResetAt,
		})
	}
	return s
}

func headerInt(h http.Header, key string) (int64, bool) {
	v := h.Get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil
}

func resetTime(now time.Time, v int64) time.Time {
	if v > 1_000_000_000 {
		return time.Unix(v, 0).UTC()
	}
	return now.Add(time.Duration(v) * time.Second)
}
```

- [x] **Step 4: Run tests**

Run: `go test -race ./internal/scoresaber/...`
Expected: `ok`.

- [x] **Step 5: Commit**

```bash
make lint
git add internal/scoresaber
git commit -m "feat: add ScoreSaber client-side rate limiter"
```

---

## Task 4: ScoreSaber client

**Files:**
- Create: `internal/scoresaber/types.go`, `internal/scoresaber/client.go`, `internal/scoresaber/client_test.go`, `internal/scoresaber/live_test.go`, `internal/scoresaber/testdata/player.json`, `internal/scoresaber/testdata/scores_page.json`

**Interfaces:**
- Consumes: `NewLimiter`, `(*Limiter).Wait`, `(*Limiter).Observe` (Task 3); `buildinfo.UserAgent()` (Task 1).
- Produces:
  - Types `scoresaber.Player{ID, Name, Avatar, Country string}`, `ScorePage{Data []ScoreItem; Metadata PageMeta}`, `PageMeta{Page, ItemsPerPage, TotalItems, TotalPages int}`, `ScoreItem{Score Score; Leaderboard Leaderboard}`, `Score{ID int64; Rank int; UnmodifiedScore, ModifiedScore int64; Accuracy, PP float64; Mods []string; BadCuts, MissedNotes, MaxCombo int; FullCombo, HasReplay, PersonalBest bool; CreatedAt time.Time; Player Player; Device Device}`, `Device{HMD string}`, `Leaderboard{ID int64; Map Map; Difficulty Difficulty; MaxScore int64; Realm Realm}`, `Map{Hash, SongName, SongSubName, SongAuthorName, LevelAuthorName, CoverURL string}`, `Difficulty{Difficulty int; GameMode, RawDifficulty string}`, `Realm{LeaderboardStatus string; Stars float64}`
  - `scoresaber.NewClient(l *Limiter, opts ...Option) *Client`; options `WithBaseURL(string)`, `WithHTTPClient(*http.Client)`, `WithUserAgent(string)`
  - `(*Client).Player(ctx, id string) (Player, error)`, `(*Client).Scores(ctx, playerID string, page int) (ScorePage, error)`, `(*Client).Replay(ctx, scoreID int64) (io.ReadCloser, error)`, `(*Client).Limiter() *Limiter`
  - Errors: `scoresaber.ErrNotFound`, `scoresaber.ErrRateLimited`, `*scoresaber.StatusError{StatusCode int; Body string}`
  - `scoresaber.ScoresPageSize = 100`

- [x] **Step 1: Add fixtures** (trimmed real responses, spec §2)

`internal/scoresaber/testdata/player.json`:

```json
{"id":"1922350521131465","name":"oermer","playerNameInGame":"oermer","country":"US","role":null,"avatar":"https://cdn.scoresaber.com/avatars/1922350521131465.jpg","avatarVersion":1783881321,"permissions":0,"banned":false,"silenced":false,"inactive":false,"stats":{"rank":1,"totalPP":22001.04}}
```

`internal/scoresaber/testdata/scores_page.json`:

```json
{"data":[
 {"score":{"id":94461650,"rank":1,"unmodifiedScore":1098260,"modifiedScore":1098260,"accuracy":0.9728111395051176,"pp":0,"weight":0,"mods":["BE"],"badCuts":0,"missedNotes":0,"maxCombo":1235,"fullCombo":true,"hasReplay":true,"personalBest":true,"createdAt":"2026-10-04T20:39:10.461Z","player":{"id":"1922350521131465","name":"oermer","avatar":"https://cdn.scoresaber.com/avatars/1922350521131465.jpg","country":"US"},"device":{"hmd":"Quest 3","controllerLeft":"Touch","controllerRight":"Touch"}},
  "leaderboard":{"id":2250508,"map":{"id":1717016,"hash":"4850C7BC85D89F832A96C6036347773E3858CED7","songName":"Hell of a time","songSubName":"","songAuthorName":"Quadeca","levelAuthorName":"oermergeesh","coverUrl":"https://cdn.scoresaber.com/covers/4850C7BC85D89F832A96C6036347773E3858CED7.png"},"difficulty":{"id":2250508,"difficulty":9,"gameMode":"SoloStandard","rawDifficulty":"_ExpertPlus_SoloStandard"},"maxScore":1128955,"realm":{"leaderboardStatus":"UNRANKED","stars":0}}},
 {"score":{"id":94461001,"rank":12,"unmodifiedScore":900000,"modifiedScore":900000,"accuracy":0.91,"pp":312.5,"mods":[],"badCuts":1,"missedNotes":3,"maxCombo":400,"fullCombo":false,"hasReplay":false,"personalBest":false,"createdAt":"2026-10-04T20:30:00.000Z","player":{"id":"1922350521131465","name":"oermer","avatar":"https://cdn.scoresaber.com/avatars/1922350521131465.jpg","country":"US"},"device":{"hmd":"Quest 3"}},
  "leaderboard":{"id":700290,"map":{"hash":"4640065298E79DC3D61A15695AEB7FED95B42B30","songName":"Just The Way You Are","songSubName":"","songAuthorName":"Milky","levelAuthorName":"Rail Zen","coverUrl":"https://cdn.scoresaber.com/covers/4640065298E79DC3D61A15695AEB7FED95B42B30.png"},"difficulty":{"difficulty":7,"gameMode":"SoloStandard","rawDifficulty":"_Expert_SoloStandard"},"maxScore":689195,"realm":{"leaderboardStatus":"RANKED","stars":7.25}}}
],"metadata":{"page":3,"itemsPerPage":100,"totalItems":8372,"totalPages":84}}
```

- [x] **Step 2: Write the failing tests** — `internal/scoresaber/client_test.go`

```go
package scoresaber_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

func newClient(t *testing.T, h http.HandlerFunc) (*scoresaber.Client, *scoresaber.Limiter) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	l := scoresaber.NewLimiter(300)
	return scoresaber.NewClient(l, scoresaber.WithBaseURL(srv.URL), scoresaber.WithUserAgent("test-agent")), l
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPlayer(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/players/1922350521131465/basic" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write(fixture(t, "player.json"))
	})
	p, err := c.Player(context.Background(), "1922350521131465")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "1922350521131465" || p.Name != "oermer" || p.Country != "US" || !strings.HasSuffix(p.Avatar, ".jpg") {
		t.Fatalf("unexpected player: %+v", p)
	}
}

func TestScores(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v2/players/42/scores" || q.Get("sort") != "recent" || q.Get("limit") != "100" ||
			q.Get("personalBest") != "all" || q.Get("page") != "3" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write(fixture(t, "scores_page.json"))
	})
	page, err := c.Scores(context.Background(), "42", 3)
	if err != nil {
		t.Fatal(err)
	}
	if page.Metadata.TotalPages != 84 || len(page.Data) != 2 {
		t.Fatalf("metadata/data wrong: %+v", page.Metadata)
	}
	s := page.Data[0]
	if s.Score.ID != 94461650 || !s.Score.HasReplay || s.Score.Accuracy < 0.97 || s.Score.Mods[0] != "BE" ||
		s.Score.Device.HMD != "Quest 3" || s.Score.Player.Name != "oermer" {
		t.Fatalf("score wrong: %+v", s.Score)
	}
	if !s.Score.CreatedAt.Equal(time.Date(2026, 10, 4, 20, 39, 10, 461_000_000, time.UTC)) {
		t.Fatalf("createdAt = %v", s.Score.CreatedAt)
	}
	lb := page.Data[1].Leaderboard
	if lb.ID != 700290 || lb.Map.SongName != "Just The Way You Are" || lb.Map.LevelAuthorName != "Rail Zen" ||
		lb.Difficulty.Difficulty != 7 || lb.Realm.LeaderboardStatus != "RANKED" || lb.Realm.Stars != 7.25 {
		t.Fatalf("leaderboard wrong: %+v", lb)
	}
}

func TestReplayStreamsBody(t *testing.T) {
	payload := bytes.Repeat([]byte("ScoreSaber Replay "), 1000)
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/scores/94461650/replay" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	})
	rc, err := c.Replay(context.Background(), 94461650)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatal("payload mismatch")
	}
}

func TestNotFound(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Not Found","code":"NOT_FOUND"}`))
	})
	if _, err := c.Replay(context.Background(), 1); !errors.Is(err, scoresaber.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := c.Player(context.Background(), "1"); !errors.Is(err, scoresaber.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRateLimitedFeedsLimiter(t *testing.T) {
	c, l := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-remaining-short", "0")
		w.Header().Set("x-ratelimit-reset-short", "9")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := c.Scores(context.Background(), "1", 1); !errors.Is(err, scoresaber.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if l.Snapshot().BlockedUntil.IsZero() {
		t.Fatal("limiter not blocked after 429")
	}
}

func TestServerError(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	})
	_, err := c.Scores(context.Background(), "1", 1)
	var se *scoresaber.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusBadGateway || se.Body != "upstream down" {
		t.Fatalf("err = %#v", err)
	}
}

func TestMalformedJSON(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{nope")) })
	if _, err := c.Scores(context.Background(), "1", 1); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v, want decode error", err)
	}
}
```

- [x] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/scoresaber/...`
Expected: FAIL — `undefined: scoresaber.NewClient`.

- [x] **Step 4: Implement types** — `internal/scoresaber/types.go`

```go
package scoresaber

import "time"

type Player struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Avatar  string `json:"avatar"`
	Country string `json:"country"`
}

type ScorePage struct {
	Data     []ScoreItem `json:"data"`
	Metadata PageMeta    `json:"metadata"`
}

type PageMeta struct {
	Page         int `json:"page"`
	ItemsPerPage int `json:"itemsPerPage"`
	TotalItems   int `json:"totalItems"`
	TotalPages   int `json:"totalPages"`
}

type ScoreItem struct {
	Score       Score       `json:"score"`
	Leaderboard Leaderboard `json:"leaderboard"`
}

type Score struct {
	ID              int64     `json:"id"`
	Rank            int       `json:"rank"`
	UnmodifiedScore int64     `json:"unmodifiedScore"`
	ModifiedScore   int64     `json:"modifiedScore"`
	Accuracy        float64   `json:"accuracy"`
	PP              float64   `json:"pp"`
	Mods            []string  `json:"mods"`
	BadCuts         int       `json:"badCuts"`
	MissedNotes     int       `json:"missedNotes"`
	MaxCombo        int       `json:"maxCombo"`
	FullCombo       bool      `json:"fullCombo"`
	HasReplay       bool      `json:"hasReplay"`
	PersonalBest    bool      `json:"personalBest"`
	CreatedAt       time.Time `json:"createdAt"`
	Player          Player    `json:"player"`
	Device          Device    `json:"device"`
}

type Device struct {
	HMD string `json:"hmd"`
}

type Leaderboard struct {
	ID         int64      `json:"id"`
	Map        Map        `json:"map"`
	Difficulty Difficulty `json:"difficulty"`
	MaxScore   int64      `json:"maxScore"`
	Realm      Realm      `json:"realm"`
}

type Map struct {
	Hash            string `json:"hash"`
	SongName        string `json:"songName"`
	SongSubName     string `json:"songSubName"`
	SongAuthorName  string `json:"songAuthorName"`
	LevelAuthorName string `json:"levelAuthorName"`
	CoverURL        string `json:"coverUrl"`
}

type Difficulty struct {
	Difficulty    int    `json:"difficulty"`
	GameMode      string `json:"gameMode"`
	RawDifficulty string `json:"rawDifficulty"`
}

type Realm struct {
	LeaderboardStatus string  `json:"leaderboardStatus"`
	Stars             float64 `json:"stars"`
}
```

- [x] **Step 5: Implement the client** — `internal/scoresaber/client.go`

```go
package scoresaber

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/buildinfo"
)

const (
	DefaultBaseURL = "https://scoresaber.com"
	ScoresPageSize = 100
)

var (
	ErrNotFound    = errors.New("scoresaber: not found")
	ErrRateLimited = errors.New("scoresaber: rate limited")
)

// StatusError is returned for unexpected non-2xx responses.
type StatusError struct {
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("scoresaber: unexpected status %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL   string
	hc        *http.Client
	limiter   *Limiter
	userAgent string
}

type Option func(*Client)

func WithBaseURL(u string) Option          { return func(c *Client) { c.baseURL = u } }
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }
func WithUserAgent(ua string) Option        { return func(c *Client) { c.userAgent = ua } }

// NewClient returns a client whose every request goes through l.
func NewClient(l *Limiter, opts ...Option) *Client {
	c := &Client{
		baseURL:   DefaultBaseURL,
		hc:        &http.Client{Timeout: 2 * time.Minute},
		limiter:   l,
		userAgent: buildinfo.UserAgent(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) Limiter() *Limiter { return c.limiter }

// Player fetches a player's basic profile.
func (c *Client) Player(ctx context.Context, id string) (Player, error) {
	var p Player
	err := c.getJSON(ctx, "/api/v2/players/"+url.PathEscape(id)+"/basic", nil, &p)
	return p, err
}

// Scores fetches one page (most recent first, all plays incl. non-PB) of a player's scores.
func (c *Client) Scores(ctx context.Context, playerID string, page int) (ScorePage, error) {
	var sp ScorePage
	q := url.Values{
		"sort":         {"recent"},
		"limit":        {strconv.Itoa(ScoresPageSize)},
		"personalBest": {"all"},
		"page":         {strconv.Itoa(page)},
	}
	err := c.getJSON(ctx, "/api/v2/players/"+url.PathEscape(playerID)+"/scores", q, &sp)
	return sp, err
}

// Replay streams a replay file. The caller must close the body.
func (c *Client) Replay(ctx context.Context, scoreID int64) (io.ReadCloser, error) {
	resp, err := c.do(ctx, "/api/v2/scores/"+strconv.FormatInt(scoreID, 10)+"/replay", nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("scoresaber: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scoresaber: GET %s: %w", path, err)
	}
	c.limiter.Observe(resp.Header, resp.StatusCode)
	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		drain(resp)
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
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
	resp, err := c.do(ctx, path, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("scoresaber: decode %s: %w", path, err)
	}
	return nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
```

- [x] **Step 6: Add the opt-in live smoke test** — `internal/scoresaber/live_test.go`

```go
//go:build live

package scoresaber_test

import (
	"context"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

// Run manually: go test -tags live -run Live ./internal/scoresaber
func TestLiveScoreSaber(t *testing.T) {
	c := scoresaber.NewClient(scoresaber.NewLimiter(10))
	ctx := context.Background()
	p, err := c.Player(ctx, "1922350521131465")
	if err != nil || p.Name == "" {
		t.Fatalf("player: %+v %v", p, err)
	}
	page, err := c.Scores(ctx, p.ID, 1)
	if err != nil || len(page.Data) == 0 {
		t.Fatalf("scores: %v", err)
	}
}
```

- [x] **Step 7: Run tests**

Run: `go test -race ./internal/scoresaber/... && go vet -tags live ./internal/scoresaber/...`
Expected: `ok`; vet clean.

- [x] **Step 8: Commit**

```bash
make lint
git add internal/scoresaber
git commit -m "feat: add ScoreSaber v2 API client"
```

---

## Task 5: Replay blob storage

**Files:**
- Create: `internal/storage/storage.go`, `internal/storage/storage_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `storage.New(root string) (*Store, error)`
  - `(*Store).Path(playerID string, scoreID int64) string` → `{root}/{playerID}/{scoreID}.dat`
  - `(*Store).Put(playerID string, scoreID int64, r io.Reader) (size int64, sha256hex string, err error)`
  - `(*Store).Open(playerID string, scoreID int64) (*os.File, error)`
  - `(*Store).Remove(playerID string, scoreID int64) error` (missing file is not an error)
  - `(*Store).RemovePlayer(playerID string) error`
  - `(*Store).Scan() (entries []Entry, removedTmp int, err error)`; `type Entry struct{ PlayerID string; ScoreID int64; Path string }`
  - `storage.HashFile(path string) (int64, string, error)`
  - `storage.ValidPlayerID(id string) bool` (`^[0-9]{1,32}$`)
  - Errors: `storage.ErrWrite` (local filesystem failure — the worker pauses on it), `storage.ErrEmpty` (source produced 0 bytes), `storage.ErrInvalidID`. Errors from reading the source are returned wrapped **without** `ErrWrite`.

- [x] **Step 1: Write the failing tests** — `internal/storage/storage_test.go`

```go
package storage_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/yyewolf/ssarchiver/internal/storage"
)

func newStore(t *testing.T) (*storage.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "replays")
	s, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestPutWritesAtomically(t *testing.T) {
	s, _ := newStore(t)
	content := strings.Repeat("replay-bytes", 100)
	size, sum, err := s.Put("76561198059961776", 42, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(content))
	if size != int64(len(content)) || sum != hex.EncodeToString(want[:]) {
		t.Fatalf("size=%d sum=%s", size, sum)
	}
	got, err := os.ReadFile(s.Path("76561198059961776", 42))
	if err != nil || string(got) != content {
		t.Fatalf("file content mismatch: %v", err)
	}
	if _, err := os.Stat(s.Path("76561198059961776", 42) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tmp file left behind")
	}
}

func TestPutSourceErrorLeavesNothing(t *testing.T) {
	s, _ := newStore(t)
	boom := errors.New("connection reset")
	r := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(boom))
	_, _, err := s.Put("1", 7, r)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped source error", err)
	}
	if errors.Is(err, storage.ErrWrite) {
		t.Fatal("source errors must not be reported as ErrWrite")
	}
	for _, p := range []string{s.Path("1", 7), s.Path("1", 7) + ".tmp"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after failed put", p)
		}
	}
}

func TestPutEmpty(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.Put("1", 1, strings.NewReader("")); !errors.Is(err, storage.ErrEmpty) {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

func TestPutRejectsBadPlayerID(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"../etc", "", "12a", "1/2"} {
		if _, _, err := s.Put(id, 1, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidID) {
			t.Fatalf("Put(%q) err = %v", id, err)
		}
	}
}

func TestPutWriteFailureIsErrWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	s, root := newStore(t)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })
	if _, _, err := s.Put("1", 1, strings.NewReader("x")); !errors.Is(err, storage.ErrWrite) {
		t.Fatalf("err = %v, want ErrWrite", err)
	}
}

func TestScanRemovesTmpAndListsDat(t *testing.T) {
	s, root := newStore(t)
	if _, _, err := s.Put("5", 50, strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "5", "51.dat.tmp"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "5", "notes.txt"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	entries, removed, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(entries) != 1 || entries[0].PlayerID != "5" || entries[0].ScoreID != 50 {
		t.Fatalf("removed=%d entries=%+v", removed, entries)
	}
}

func TestRemoveAndRemovePlayer(t *testing.T) {
	s, _ := newStore(t)
	_, _, _ = s.Put("9", 1, strings.NewReader("a"))
	_, _, _ = s.Put("9", 2, strings.NewReader("b"))
	if err := s.Remove("9", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("9", 1); err != nil {
		t.Fatal("removing a missing file must not fail")
	}
	if err := s.RemovePlayer("9"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open("9", 2); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
}

func TestHashFile(t *testing.T) {
	s, _ := newStore(t)
	_, sum, _ := s.Put("3", 3, strings.NewReader("hello"))
	size, got, err := storage.HashFile(s.Path("3", 3))
	if err != nil || size != 5 || got != sum {
		t.Fatalf("HashFile = %d %s %v", size, got, err)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/storage/...`
Expected: FAIL — `undefined: storage.New`.

- [x] **Step 3: Implement** — `internal/storage/storage.go`

```go
// Package storage stores replay files on disk as {root}/{player}/{score}.dat.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrWrite     = errors.New("storage: write failed")
	ErrEmpty     = errors.New("storage: empty replay")
	ErrInvalidID = errors.New("storage: invalid player id")
)

var playerIDRe = regexp.MustCompile(`^[0-9]{1,32}$`)

// ValidPlayerID reports whether id is a ScoreSaber player id (digits only).
func ValidPlayerID(id string) bool { return playerIDRe.MatchString(id) }

type Store struct{ root string }

type Entry struct {
	PlayerID string
	ScoreID  int64
	Path     string
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("%w: create %s: %w", ErrWrite, root, err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Path(playerID string, scoreID int64) string {
	return filepath.Join(s.root, playerID, strconv.FormatInt(scoreID, 10)+".dat")
}

type trackingReader struct {
	r   io.Reader
	err error
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		t.err = err
	}
	return n, err
}

// Put streams r to a temp file, fsyncs, then renames it into place.
func (s *Store) Put(playerID string, scoreID int64, r io.Reader) (int64, string, error) {
	if !ValidPlayerID(playerID) {
		return 0, "", fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	dir := filepath.Join(s.root, playerID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, "", fmt.Errorf("%w: mkdir %s: %w", ErrWrite, dir, err)
	}
	final := s.Path(playerID, scoreID)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, "", fmt.Errorf("%w: create %s: %w", ErrWrite, tmp, err)
	}
	fail := func(e error) (int64, string, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return 0, "", e
	}
	h := sha256.New()
	src := &trackingReader{r: r}
	n, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		if src.err != nil {
			return fail(fmt.Errorf("storage: reading replay: %w", src.err))
		}
		return fail(fmt.Errorf("%w: write %s: %w", ErrWrite, tmp, err))
	}
	if n == 0 {
		return fail(ErrEmpty)
	}
	if err := f.Sync(); err != nil {
		return fail(fmt.Errorf("%w: fsync %s: %w", ErrWrite, tmp, err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("%w: close %s: %w", ErrWrite, tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("%w: rename %s: %w", ErrWrite, final, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) Open(playerID string, scoreID int64) (*os.File, error) {
	if !ValidPlayerID(playerID) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	return os.Open(s.Path(playerID, scoreID))
}

func (s *Store) Remove(playerID string, scoreID int64) error {
	if !ValidPlayerID(playerID) {
		return fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	if err := os.Remove(s.Path(playerID, scoreID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("storage: remove: %w", err)
	}
	return nil
}

func (s *Store) RemovePlayer(playerID string) error {
	if !ValidPlayerID(playerID) {
		return fmt.Errorf("%w: %q", ErrInvalidID, playerID)
	}
	if err := os.RemoveAll(filepath.Join(s.root, playerID)); err != nil {
		return fmt.Errorf("storage: remove player dir: %w", err)
	}
	return nil
}

// Scan deletes leftover *.tmp files and lists every {player}/{score}.dat.
func (s *Store) Scan() ([]Entry, int, error) {
	var entries []Entry
	removed := 0
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".tmp") {
			if err := os.Remove(path); err == nil {
				removed++
			}
			return nil
		}
		idStr, ok := strings.CutSuffix(name, ".dat")
		if !ok {
			return nil
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		player := filepath.Base(filepath.Dir(path))
		if err != nil || !ValidPlayerID(player) {
			return nil
		}
		entries = append(entries, Entry{PlayerID: player, ScoreID: id, Path: path})
		return nil
	})
	if err != nil {
		return nil, removed, fmt.Errorf("storage: scan: %w", err)
	}
	return entries, removed, nil
}

// HashFile returns size and hex sha256 of a file.
func HashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
```

- [x] **Step 4: Run tests**

Run: `go test -race ./internal/storage/...`
Expected: `ok`.

- [x] **Step 5: Commit**

```bash
make lint
git add internal/storage
git commit -m "feat: add atomic replay file storage"
```

---
## Task 6: Service — players, scores, replay queue, events log

**Files:**
- Create: `internal/service/service.go`, `internal/service/events.go`, `internal/service/players.go`, `internal/service/scores.go`, `internal/service/replays.go`, `internal/service/backfill.go`
- Create tests: `internal/service/players_test.go`, `internal/service/scores_test.go`, `internal/service/replays_test.go`, `internal/service/backfill_test.go`
- Create: `internal/testutil/fakes.go`, `internal/testutil/service.go`

**Interfaces:**
- Consumes: `query.Use`, models and constants (Task 2); `db.IsDuplicate` (Task 2); `storage.Store`, `storage.ValidPlayerID` (Task 5); `scoresaber.Player`, `scoresaber.ScoreItem`, `scoresaber.ErrNotFound` (Task 4).
- Produces (all methods on `*service.Service`; `ctx context.Context` first):
  - `service.New(gdb *gorm.DB, store *storage.Store, ss Resolver) *Service`; `type Resolver interface{ Player(ctx, id string) (scoresaber.Player, error) }`
  - `Now() time.Time`, `SetClock(func() time.Time)`, `Store() *storage.Store`, `Wake()`, `WakeC() <-chan struct{}`, `Ping(ctx) error`, `service.Ptr[T](v T) *T`
  - Errors: `ErrNotFound`, `ErrPlayerExists`, `ErrInvalidPlayerRef`
  - Events: `Log(ctx, model.SyncEvent)`
  - Players: `service.ParsePlayerRef(string) (string, error)`, `ResolvePlayer(ctx, input) (scoresaber.Player, error)`, `AddPlayer(ctx, input) (*model.Player, error)`, `GetPlayer(ctx, id) (*model.Player, error)`, `ListPlayers(ctx, includeDisabled bool) ([]PlayerSummary, error)`, `PlayerCounts(ctx, id) (Counts, error)`, `SetPlayerEnabled(ctx, id, enabled bool) error`, `DeletePlayer(ctx, id string, deleteFiles bool) error`, `RequestPoll(ctx, id) error`
  - `type Counts struct{ Scores, Archived, Pending, Failed, Gone int64 }` with `Replays() int64`; `type PlayerSummary struct{ model.Player; Counts Counts }`
  - Scores: `UpsertScores(ctx, playerID string, items []scoresaber.ScoreItem) (UpsertResult, error)`; `type UpsertResult struct{ New, Known, NewReplays int }`; `ListScores(ctx, ScoreFilter) (ScoreList, error)`; `type ScoreFilter struct{ PlayerID, Search string; RankedOnly bool; State string; Page, PerPage int }` with `service.FilterWithReplay = "replay"`, `service.FilterArchived = "archived"`; `type ScoreList struct{ Items []*model.Score; Total int64; Page, PerPage, Pages int }`; `GetScore(ctx, id int64) (*model.Score, error)` (Leaderboard + Player preloaded)
  - Replay queue: `type ReplayTier int` with `TierNew`, `TierBackfill`; `NextReplay(ctx, tier, lastPlayer string) (*model.Score, error)` (nil,nil when empty; Leaderboard + Player preloaded); `MarkReplayArchived(ctx, id, size int64, sha string) error`; `MarkReplayGone(ctx, id int64) error`; `MarkReplayAttemptFailed(ctx, id int64, cause error) (gaveUp bool, next time.Time, err error)`; `RetryFailed(ctx, playerID string, scoreID int64) (int64, error)`; `ListFailedReplays(ctx, page, perPage int) ([]*model.Score, int64, error)`; `NextRetryAt(ctx) (time.Time, bool, error)`; `service.MaxReplayAttempts = 5`; `service.Backoff(attempt int) time.Duration`
  - Poll/backfill: `DuePlayer(ctx, interval) (*model.Player, error)`; `NextPollAt(ctx, interval) (time.Time, bool, error)`; `MarkPolled(ctx, id) error`; `MarkPlayerError(ctx, id, msg string, disable bool) error`; `UpdatePlayerProfile(ctx, id string, p scoresaber.Player) error`; `NextBackfillPlayer(ctx, lastPlayer string) (*model.Player, error)`; `SetBackfill(ctx, id, state string, nextPage, totalPages int) error`; `DeferBackfill(ctx, id string, until time.Time, msg string) error`; `service.BackfillRetryDelay = 5 * time.Minute`
  - testutil: `testutil.Resolver{Players map[string]scoresaber.Player; Calls int}`, `testutil.NewClock(time.Time) *Clock` (`Now`, `Advance`, `Set`), `testutil.Item(playerID string, scoreID, lbID int64, setAt time.Time, hasReplay bool) scoresaber.ScoreItem`, `testutil.NewService(t) (*service.Service, *Resolver, *Clock)` — clock starts at `testutil.T0 = 2026-10-08T12:00:00Z`; resolver knows players `"1001"` (Alice, FR) and `"1002"` (Bob, US).

Implementation notes:
- `ScoreSaber player ids` are digit strings; reuse `storage.ValidPlayerID`.
- Any gen query that uses `Join` **must** `Select(q.Score.ALL)` before `Find/First` (otherwise joined columns like `players.id` overwrite `scores.id`), and must **not** call `Count()` on a DO that has `Select(ALL)` (SQLite rejects `COUNT(scores.*)`). Build the filtered DO with a helper and call it twice.
- If the generated `field.Time` lacks `GteCol`/`LtCol`, use `field.NewUnsafeFieldRaw("scores.set_at >= players.added_at")` and log a deviation.

- [x] **Step 1: Write the test helpers** — `internal/testutil/fakes.go`

```go
package testutil

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

// T0 is the default fake "now" for service tests.
var T0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// Resolver is a fake service.Resolver.
type Resolver struct {
	mu      sync.Mutex
	Players map[string]scoresaber.Player
	Calls   int
}

func (r *Resolver) Player(_ context.Context, id string) (scoresaber.Player, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls++
	p, ok := r.Players[id]
	if !ok {
		return scoresaber.Player{}, fmt.Errorf("%w: player %s", scoresaber.ErrNotFound, id)
	}
	return p, nil
}

// Clock is a manually advanced clock.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock(t time.Time) *Clock { return &Clock{t: t} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// Item builds a ScoreSaber score item with sensible defaults.
func Item(playerID string, scoreID, lbID int64, setAt time.Time, hasReplay bool) scoresaber.ScoreItem {
	return scoresaber.ScoreItem{
		Score: scoresaber.Score{
			ID: scoreID, Rank: 1, ModifiedScore: 1_000_000, UnmodifiedScore: 1_000_000, Accuracy: 0.95,
			Mods: []string{}, MaxCombo: 500, FullCombo: true, HasReplay: hasReplay, PersonalBest: true,
			CreatedAt: setAt, Player: scoresaber.Player{ID: playerID, Name: "Player " + playerID},
			Device: scoresaber.Device{HMD: "Quest 3"},
		},
		Leaderboard: scoresaber.Leaderboard{
			ID: lbID,
			Map: scoresaber.Map{
				Hash: fmt.Sprintf("HASH%d", lbID), SongName: fmt.Sprintf("Song %d", lbID),
				SongAuthorName: "Artist", LevelAuthorName: "Mapper",
				CoverURL: "https://cdn.scoresaber.com/covers/x.png",
			},
			Difficulty: scoresaber.Difficulty{Difficulty: 9, GameMode: "SoloStandard", RawDifficulty: "_ExpertPlus_SoloStandard"},
			MaxScore:   1_100_000,
			Realm:      scoresaber.Realm{LeaderboardStatus: "UNRANKED"},
		},
	}
}
```

`internal/testutil/service.go`:

```go
package testutil

import (
	"path/filepath"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

// NewService returns a Service over a temp DB/store with a fake clock and resolver.
func NewService(t testing.TB) (*service.Service, *Resolver, *Clock) {
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
	clk := NewClock(T0)
	svc.SetClock(clk.Now)
	return svc, res, clk
}
```

- [x] **Step 2: Write the failing player tests** — `internal/service/players_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestParsePlayerRef(t *testing.T) {
	ok := map[string]string{
		"76561198059961776":                                          "76561198059961776",
		"  76561198059961776 \n":                                     "76561198059961776",
		"https://scoresaber.com/u/76561198059961776":                 "76561198059961776",
		"https://scoresaber.com/u/76561198059961776?page=2&sort=recent": "76561198059961776",
		"scoresaber.com/u/1922350521131465/":                         "1922350521131465",
		"http://www.scoresaber.com/u/1922350521131465#top":           "1922350521131465",
	}
	for in, want := range ok {
		got, err := service.ParsePlayerRef(in)
		if err != nil || got != want {
			t.Errorf("ParsePlayerRef(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "../123", "https://evil.com/u/123", "https://scoresaber.com/u/12a", "https://scoresaber.com/leaderboard/123"} {
		if _, err := service.ParsePlayerRef(in); !errors.Is(err, service.ErrInvalidPlayerRef) {
			t.Errorf("ParsePlayerRef(%q) err = %v, want ErrInvalidPlayerRef", in, err)
		}
	}
}

func TestAddPlayer(t *testing.T) {
	svc, res, _ := testutil.NewService(t)
	ctx := context.Background()
	p, err := svc.AddPlayer(ctx, "https://scoresaber.com/u/1001")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Alice" || !p.Enabled || p.BackfillState != model.BackfillPending || p.BackfillPage != 1 || !p.AddedAt.Equal(testutil.T0) {
		t.Fatalf("unexpected player: %+v", p)
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("AddPlayer must wake the worker")
	}
	if _, err := svc.AddPlayer(ctx, "1001"); !errors.Is(err, service.ErrPlayerExists) {
		t.Fatalf("duplicate add err = %v", err)
	}
	if _, err := svc.AddPlayer(ctx, "9999"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown player err = %v", err)
	}
	calls := res.Calls
	if _, err := svc.AddPlayer(ctx, "not a player"); !errors.Is(err, service.ErrInvalidPlayerRef) || res.Calls != calls {
		t.Fatalf("invalid ref must fail without calling ScoreSaber (err=%v)", err)
	}
}

func TestListPlayersWithCounts(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	items := []scoreItem{{1, true}, {2, true}, {3, false}}
	upsert(t, svc, "1001", clk, items)
	if err := svc.MarkReplayArchived(ctx, 1, 10, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPlayerEnabled(ctx, "1002", false); err != nil {
		t.Fatal(err)
	}
	enabled, err := svc.ListPlayers(ctx, false)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("enabled players = %d (%v)", len(enabled), err)
	}
	c := enabled[0].Counts
	if c.Scores != 3 || c.Archived != 1 || c.Pending != 1 || c.Replays() != 2 {
		t.Fatalf("counts = %+v", c)
	}
	all, _ := svc.ListPlayers(ctx, true)
	if len(all) != 2 || all[0].Name != "Alice" || all[1].Name != "Bob" {
		t.Fatalf("all players = %+v", all)
	}
}

func TestDeletePlayer(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	upsert(t, svc, "1001", clk, []scoreItem{{1, true}})
	if _, _, err := svc.Store().Put("1001", 1, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePlayer(ctx, "1001", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetPlayer(ctx, "1001"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("player still exists: %v", err)
	}
	if _, err := svc.GetScore(ctx, 1); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("score still exists: %v", err)
	}
	if _, err := svc.Store().Open("1001", 1); err == nil {
		t.Fatal("replay file still exists")
	}
	if err := svc.DeletePlayer(ctx, "1001", false); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestRequestPollWakesAndClearsLastPolled(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	<-svc.WakeC()
	if err := svc.MarkPolled(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPoll(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.GetPlayer(ctx, "1001")
	if p.LastPolledAt != nil {
		t.Fatal("RequestPoll must clear last_polled_at")
	}
	select {
	case <-svc.WakeC():
	default:
		t.Fatal("RequestPoll must wake the worker")
	}
}
```

Shared helpers for the service tests — `internal/service/helpers_test.go`:

```go
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

type scoreItem struct {
	id        int64
	hasReplay bool
}

func mustAdd(t *testing.T, svc *service.Service, id string) {
	t.Helper()
	if _, err := svc.AddPlayer(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

// upsert inserts items set one minute apart after the current fake time,
// on leaderboard id = score id + 1000.
func upsert(t *testing.T, svc *service.Service, playerID string, clk *testutil.Clock, items []scoreItem) service.UpsertResult {
	t.Helper()
	var list []scoresaber.ScoreItem
	for i, it := range items {
		list = append(list, testutil.Item(playerID, it.id, it.id+1000, clk.Now().Add(time.Duration(i+1)*time.Minute), it.hasReplay))
	}
	res, err := svc.UpsertScores(context.Background(), playerID, list)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
```

- [x] **Step 3: Write the failing score tests** — `internal/service/scores_test.go`

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

func TestUpsertScoresStates(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	mustAdd(t, svc, "1001")
	res := upsert(t, svc, "1001", clk, []scoreItem{{1, true}, {2, false}})
	if res.New != 2 || res.Known != 0 || res.NewReplays != 1 {
		t.Fatalf("result = %+v", res)
	}
	s1, _ := svc.GetScore(context.Background(), 1)
	s2, _ := svc.GetScore(context.Background(), 2)
	if s1.ReplayState != model.ReplayPending || s2.ReplayState != model.ReplayNone {
		t.Fatalf("states = %s, %s", s1.ReplayState, s2.ReplayState)
	}
	if s1.Leaderboard == nil || s1.Leaderboard.SongName != "Song 1001" || s1.Player == nil || s1.Player.Name != "Alice" {
		t.Fatalf("GetScore must preload leaderboard and player: %+v", s1)
	}
}

func TestUpsertScoresPreservesArchiveState(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	item := testutil.Item("1001", 1, 1001, testutil.T0, true)
	if _, err := svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item}); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkReplayArchived(ctx, 1, 1234, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	item.Score.Rank = 7
	item.Score.PersonalBest = false
	res, err := svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item})
	if err != nil {
		t.Fatal(err)
	}
	if res.Known != 1 || res.New != 0 || res.NewReplays != 0 {
		t.Fatalf("result = %+v", res)
	}
	s, _ := svc.GetScore(ctx, 1)
	if s.Rank != 7 || s.PersonalBest {
		t.Fatalf("mutable fields not updated: rank=%d pb=%v", s.Rank, s.PersonalBest)
	}
	if s.ReplayState != model.ReplayArchived || s.ReplaySHA256 != "deadbeef" || s.ReplaySize != 1234 || s.ArchivedAt == nil {
		t.Fatalf("archive state clobbered: %+v", s)
	}
}

func TestUpsertScoresReplayAppearsLater(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	item := testutil.Item("1001", 1, 1001, testutil.T0, false)
	_, _ = svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item})
	item.Score.HasReplay = true
	res, _ := svc.UpsertScores(ctx, "1001", []scoresaber.ScoreItem{item})
	s, _ := svc.GetScore(ctx, 1)
	if res.NewReplays != 1 || s.ReplayState != model.ReplayPending || !s.HasReplay {
		t.Fatalf("res=%+v state=%s", res, s.ReplayState)
	}
}

func TestListScoresFiltersAndPaging(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	var items []scoresaber.ScoreItem
	for i := int64(1); i <= 5; i++ {
		it := testutil.Item("1001", i, 1000+i, testutil.T0.Add(time.Duration(i)*time.Minute), i%2 == 1)
		if i == 2 {
			it.Leaderboard.Map.SongName = "Ghost Rule"
			it.Leaderboard.Realm.LeaderboardStatus = "RANKED"
		}
		if i == 4 {
			it.Leaderboard.Map.LevelAuthorName = "GhostMapper"
		}
		items = append(items, it)
	}
	if _, err := svc.UpsertScores(ctx, "1001", items); err != nil {
		t.Fatal(err)
	}
	_ = svc.MarkReplayArchived(ctx, 5, 1, "x")

	all, err := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", PerPage: 2, Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 5 || all.Pages != 3 || len(all.Items) != 2 || all.Items[0].ID != 5 || all.Items[0].Leaderboard == nil {
		t.Fatalf("page 1 = %+v", all)
	}
	p3, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", PerPage: 2, Page: 3})
	if len(p3.Items) != 1 || p3.Items[0].ID != 1 {
		t.Fatalf("page 3 = %+v", p3.Items)
	}
	ids := func(l service.ScoreList) []int64 {
		var out []int64
		for _, s := range l.Items {
			out = append(out, s.ID)
		}
		return out
	}
	search, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", Search: "ghost"})
	if got := ids(search); len(got) != 2 || got[0] != 4 || got[1] != 2 {
		t.Fatalf("search ids = %v, want [4 2] (song name + mapper match)", got)
	}
	ranked, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", RankedOnly: true})
	if got := ids(ranked); len(got) != 1 || got[0] != 2 {
		t.Fatalf("ranked ids = %v", got)
	}
	withReplay, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", State: service.FilterWithReplay})
	if withReplay.Total != 3 {
		t.Fatalf("with replay total = %d", withReplay.Total)
	}
	archived, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", State: service.FilterArchived})
	if got := ids(archived); len(got) != 1 || got[0] != 5 {
		t.Fatalf("archived ids = %v", got)
	}
	wild, _ := svc.ListScores(ctx, service.ScoreFilter{PlayerID: "1001", Search: "%"})
	if wild.Total != 5 {
		t.Fatalf("a bare %% must not act as a wildcard filter that drops rows: total = %d", wild.Total)
	}
}
```

- [x] **Step 4: Write the failing replay-queue tests** — `internal/service/replays_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func seedQueue(t *testing.T) (*service.Service, *testutil.Clock) {
	t.Helper()
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001") // added at T0
	mustAdd(t, svc, "1002")
	items := map[string][]scoresaber.ScoreItem{
		"1001": {
			testutil.Item("1001", 1, 11, testutil.T0.Add(time.Minute), true),
			testutil.Item("1001", 2, 12, testutil.T0.Add(2*time.Minute), true),
			testutil.Item("1001", 3, 13, testutil.T0.Add(-time.Hour), true),
		},
		"1002": {
			testutil.Item("1002", 4, 14, testutil.T0.Add(3*time.Minute), true),
			testutil.Item("1002", 5, 15, testutil.T0.Add(-2*time.Hour), true),
		},
	}
	for p, list := range items {
		if _, err := svc.UpsertScores(ctx, p, list); err != nil {
			t.Fatal(err)
		}
	}
	return svc, clk
}

func nextID(t *testing.T, svc *service.Service, tier service.ReplayTier, last string) int64 {
	t.Helper()
	s, err := svc.NextReplay(context.Background(), tier, last)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		return 0
	}
	if s.Leaderboard == nil || s.Player == nil {
		t.Fatal("NextReplay must preload leaderboard and player")
	}
	return s.ID
}

func TestNextReplayTiersAndRoundRobin(t *testing.T) {
	svc, clk := seedQueue(t)
	ctx := context.Background()
	if got := nextID(t, svc, service.TierNew, ""); got != 2 {
		t.Fatalf("new/'' = %d, want 2 (Alice newest)", got)
	}
	if got := nextID(t, svc, service.TierNew, "1001"); got != 4 {
		t.Fatalf("new/after Alice = %d, want 4 (Bob)", got)
	}
	if got := nextID(t, svc, service.TierNew, "1002"); got != 2 {
		t.Fatalf("new/after Bob = %d, want 2 (wrap to Alice)", got)
	}
	if got := nextID(t, svc, service.TierBackfill, ""); got != 3 {
		t.Fatalf("backfill/'' = %d, want 3", got)
	}
	if err := svc.SetPlayerEnabled(ctx, "1001", false); err != nil {
		t.Fatal(err)
	}
	if got := nextID(t, svc, service.TierNew, ""); got != 4 {
		t.Fatalf("disabled players must be skipped, got %d", got)
	}
	if _, _, err := svc.MarkReplayAttemptFailed(ctx, 4, errors.New("502")); err != nil {
		t.Fatal(err)
	}
	if got := nextID(t, svc, service.TierNew, ""); got != 0 {
		t.Fatalf("deferred score returned early: %d", got)
	}
	clk.Advance(time.Minute)
	if got := nextID(t, svc, service.TierNew, ""); got != 4 {
		t.Fatalf("deferred score not returned after backoff: %d", got)
	}
}

func TestMarkReplayAttemptFailedBackoff(t *testing.T) {
	svc, clk := seedQueue(t)
	ctx := context.Background()
	wants := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	for i, want := range wants {
		gaveUp, next, err := svc.MarkReplayAttemptFailed(ctx, 1, errors.New("boom"))
		if err != nil || gaveUp || !next.Equal(clk.Now().Add(want)) {
			t.Fatalf("attempt %d: gaveUp=%v next=%v err=%v", i+1, gaveUp, next, err)
		}
	}
	gaveUp, _, err := svc.MarkReplayAttemptFailed(ctx, 1, errors.New("boom"))
	if err != nil || !gaveUp {
		t.Fatalf("5th attempt must give up (err=%v)", err)
	}
	s, _ := svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayFailed || s.Attempts != service.MaxReplayAttempts || s.LastError != "boom" || s.NextAttemptAt != nil {
		t.Fatalf("score after giving up: %+v", s)
	}
	failed, total, err := svc.ListFailedReplays(ctx, 1, 20)
	if err != nil || total != 1 || failed[0].ID != 1 || failed[0].Leaderboard == nil {
		t.Fatalf("ListFailedReplays = %v %d %v", failed, total, err)
	}
	n, err := svc.RetryFailed(ctx, "", 0)
	if err != nil || n != 1 {
		t.Fatalf("RetryFailed = %d, %v", n, err)
	}
	s, _ = svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayPending || s.Attempts != 0 || s.LastError != "" {
		t.Fatalf("score after retry: %+v", s)
	}
}

func TestMarkReplayGoneAndArchived(t *testing.T) {
	svc, clk := seedQueue(t)
	ctx := context.Background()
	if err := svc.MarkReplayGone(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkReplayArchived(ctx, 2, 99, "cafe"); err != nil {
		t.Fatal(err)
	}
	s1, _ := svc.GetScore(ctx, 1)
	s2, _ := svc.GetScore(ctx, 2)
	if s1.ReplayState != model.ReplayGone {
		t.Fatalf("score 1 state = %s", s1.ReplayState)
	}
	if s2.ReplayState != model.ReplayArchived || s2.ReplaySize != 99 || s2.ArchivedAt == nil || !s2.ArchivedAt.Equal(clk.Now()) {
		t.Fatalf("score 2 = %+v", s2)
	}
	if err := svc.MarkReplayArchived(ctx, 999, 1, "x"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown score err = %v", err)
	}
}

func TestNextRetryAt(t *testing.T) {
	svc, clk := seedQueue(t)
	ctx := context.Background()
	if _, ok, _ := svc.NextRetryAt(ctx); ok {
		t.Fatal("no deferred scores yet")
	}
	_, _, _ = svc.MarkReplayAttemptFailed(ctx, 1, errors.New("x"))
	at, ok, err := svc.NextRetryAt(ctx)
	if err != nil || !ok || !at.Equal(clk.Now().Add(time.Minute)) {
		t.Fatalf("NextRetryAt = %v %v %v", at, ok, err)
	}
}
```

- [x] **Step 5: Write the failing poll/backfill tests** — `internal/service/backfill_test.go`

```go
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestDuePlayer(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	p, err := svc.DuePlayer(ctx, 10*time.Minute)
	if err != nil || p == nil {
		t.Fatalf("never-polled player must be due: %v %v", p, err)
	}
	_ = svc.MarkPolled(ctx, "1001")
	clk.Advance(time.Minute)
	_ = svc.MarkPolled(ctx, "1002")
	if p, _ := svc.DuePlayer(ctx, 10*time.Minute); p != nil {
		t.Fatalf("nobody should be due, got %s", p.ID)
	}
	clk.Advance(10 * time.Minute)
	if p, _ := svc.DuePlayer(ctx, 10*time.Minute); p == nil || p.ID != "1001" {
		t.Fatalf("oldest poll first, got %+v", p)
	}
	_ = svc.SetPlayerEnabled(ctx, "1001", false)
	if p, _ := svc.DuePlayer(ctx, 10*time.Minute); p == nil || p.ID != "1002" {
		t.Fatalf("disabled players are never due, got %+v", p)
	}
	at, ok, err := svc.NextPollAt(ctx, 10*time.Minute)
	if err != nil || !ok || !at.Equal(testutil.T0.Add(11*time.Minute)) {
		t.Fatalf("NextPollAt = %v %v %v", at, ok, err)
	}
}

func TestMarkPlayerErrorAndProfile(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	if err := svc.MarkPlayerError(ctx, "1001", "player not found", true); err != nil {
		t.Fatal(err)
	}
	p, _ := svc.GetPlayer(ctx, "1001")
	if p.Enabled || p.LastError != "player not found" {
		t.Fatalf("player = %+v", p)
	}
	_ = svc.UpdatePlayerProfile(ctx, "1001", scoresaber.Player{Name: "Alice2", Avatar: "a.jpg"})
	p, _ = svc.GetPlayer(ctx, "1001")
	if p.Name != "Alice2" || p.AvatarURL != "a.jpg" || p.Country != "FR" {
		t.Fatalf("profile update = %+v (empty fields must not overwrite)", p)
	}
	_ = svc.MarkPolled(ctx, "1001")
	p, _ = svc.GetPlayer(ctx, "1001")
	if p.LastError != "" {
		t.Fatal("MarkPolled must clear last_error")
	}
}

func TestNextBackfillPlayer(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	mustAdd(t, svc, "1002")
	if p, _ := svc.NextBackfillPlayer(ctx, ""); p != nil {
		t.Fatal("players must be polled once before backfilling")
	}
	_ = svc.MarkPolled(ctx, "1001")
	_ = svc.MarkPolled(ctx, "1002")
	if p, _ := svc.NextBackfillPlayer(ctx, ""); p == nil || p.ID != "1001" {
		t.Fatalf("got %+v", p)
	}
	if p, _ := svc.NextBackfillPlayer(ctx, "1001"); p == nil || p.ID != "1002" {
		t.Fatalf("round robin: got %+v", p)
	}
	if err := svc.SetBackfill(ctx, "1002", model.BackfillDone, 85, 84); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.NextBackfillPlayer(ctx, "1001"); p == nil || p.ID != "1001" {
		t.Fatalf("done players are skipped: got %+v", p)
	}
	if err := svc.DeferBackfill(ctx, "1001", clk.Now().Add(5*time.Minute), "502"); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.NextBackfillPlayer(ctx, ""); p != nil {
		t.Fatalf("deferred player returned early: %+v", p)
	}
	clk.Advance(5 * time.Minute)
	p, _ := svc.NextBackfillPlayer(ctx, "")
	if p == nil || p.ID != "1001" || p.LastError != "502" {
		t.Fatalf("deferred player after delay: %+v", p)
	}
	_ = svc.SetBackfill(ctx, "1001", model.BackfillRunning, 3, 84)
	p, _ = svc.GetPlayer(ctx, "1001")
	if p.BackfillPage != 3 || p.BackfillTotalPages != 84 || p.BackfillRetryAt != nil {
		t.Fatalf("SetBackfill = %+v", p)
	}
}
```

- [x] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/service/...`
Expected: FAIL — `undefined: service.New` etc.

- [x] **Step 7: Implement the core** — `internal/service/service.go`

```go
// Package service holds SSArchiver's business logic. It is the only package
// (besides internal/db) that talks to GORM.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrPlayerExists     = errors.New("player is already tracked")
	ErrInvalidPlayerRef = errors.New("enter a ScoreSaber player ID or profile URL (https://scoresaber.com/u/<id>)")
)

// Resolver looks players up on ScoreSaber (implemented by *scoresaber.Client).
type Resolver interface {
	Player(ctx context.Context, id string) (scoresaber.Player, error)
}

type Service struct {
	db    *gorm.DB
	q     *query.Query
	store *storage.Store
	ss    Resolver

	clockMu sync.RWMutex
	now     func() time.Time

	wake chan struct{}
}

func New(gdb *gorm.DB, store *storage.Store, ss Resolver) *Service {
	return &Service{
		db:    gdb,
		q:     query.Use(gdb),
		store: store,
		ss:    ss,
		now:   func() time.Time { return time.Now().UTC() },
		wake:  make(chan struct{}, 1),
	}
}

func (s *Service) Now() time.Time {
	s.clockMu.RLock()
	defer s.clockMu.RUnlock()
	return s.now().UTC()
}

// SetClock replaces the time source (tests).
func (s *Service) SetClock(now func() time.Time) {
	s.clockMu.Lock()
	s.now = now
	s.clockMu.Unlock()
}

func (s *Service) Store() *storage.Store { return s.store }

// Wake nudges the archiver worker; it never blocks.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) WakeC() <-chan struct{} { return s.wake }

func (s *Service) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func Ptr[T any](v T) *T { return &v }

func notFound(err error, what string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return err
}

// pickAfter returns the first id greater than last (ids sorted ascending), wrapping around.
func pickAfter(ids []string, last string) string {
	for _, id := range ids {
		if id > last {
			return id
		}
	}
	return ids[0]
}
```

- [x] **Step 8: Implement events logging** — `internal/service/events.go`

```go
package service

import (
	"context"
	"log/slog"

	"github.com/yyewolf/ssarchiver/internal/model"
)

// Log records a sync event in the DB and in the process log. Failures to
// store are logged and otherwise ignored.
func (s *Service) Log(ctx context.Context, e model.SyncEvent) {
	if e.At.IsZero() {
		e.At = s.Now()
	}
	level := slog.LevelInfo
	switch e.Level {
	case model.LevelWarn:
		level = slog.LevelWarn
	case model.LevelError:
		level = slog.LevelError
	}
	attrs := []any{"kind", e.Kind}
	if e.PlayerID != nil {
		attrs = append(attrs, "player", *e.PlayerID)
	}
	if e.ScoreID != nil {
		attrs = append(attrs, "score", *e.ScoreID)
	}
	slog.Log(ctx, level, e.Message, attrs...)
	if err := s.q.SyncEvent.WithContext(ctx).Create(&e); err != nil {
		slog.Warn("sync event not stored", "err", err)
	}
}
```

- [x] **Step 9: Implement players** — `internal/service/players.go`

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

var playerURLRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?scoresaber\.com/u/([0-9]{1,32})(?:[/?#].*)?$`)

// ParsePlayerRef extracts a player id from an id or a ScoreSaber profile URL.
func ParsePlayerRef(input string) (string, error) {
	in := strings.TrimSpace(input)
	if storage.ValidPlayerID(in) {
		return in, nil
	}
	if m := playerURLRe.FindStringSubmatch(in); m != nil {
		return m[1], nil
	}
	return "", ErrInvalidPlayerRef
}

type Counts struct {
	Scores, Archived, Pending, Failed, Gone int64
}

// Replays is the number of scores ScoreSaber offered a replay for.
func (c Counts) Replays() int64 { return c.Archived + c.Pending + c.Failed + c.Gone }

type PlayerSummary struct {
	model.Player
	Counts Counts
}

func (s *Service) ResolvePlayer(ctx context.Context, input string) (scoresaber.Player, error) {
	id, err := ParsePlayerRef(input)
	if err != nil {
		return scoresaber.Player{}, err
	}
	p, err := s.ss.Player(ctx, id)
	if errors.Is(err, scoresaber.ErrNotFound) {
		return scoresaber.Player{}, fmt.Errorf("%w: no ScoreSaber player %s", ErrNotFound, id)
	}
	return p, err
}

func (s *Service) AddPlayer(ctx context.Context, input string) (*model.Player, error) {
	sp, err := s.ResolvePlayer(ctx, input)
	if err != nil {
		return nil, err
	}
	p := &model.Player{
		ID: sp.ID, Name: sp.Name, AvatarURL: sp.Avatar, Country: sp.Country,
		Enabled: true, AddedAt: s.Now(), BackfillState: model.BackfillPending, BackfillPage: 1,
	}
	if err := s.q.Player.WithContext(ctx).Create(p); err != nil {
		if db.IsDuplicate(err) {
			return nil, ErrPlayerExists
		}
		return nil, fmt.Errorf("service: add player: %w", err)
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: Ptr(p.ID), Message: "player added: " + p.Name})
	s.Wake()
	return p, nil
}

func (s *Service) GetPlayer(ctx context.Context, id string) (*model.Player, error) {
	p, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).First()
	if err != nil {
		return nil, notFound(err, "player "+id)
	}
	return p, nil
}

func (s *Service) ListPlayers(ctx context.Context, includeDisabled bool) ([]PlayerSummary, error) {
	do := s.q.Player.WithContext(ctx).Order(s.q.Player.Name)
	if !includeDisabled {
		do = do.Where(s.q.Player.Enabled.Is(true))
	}
	players, err := do.Find()
	if err != nil {
		return nil, fmt.Errorf("service: list players: %w", err)
	}
	counts, err := s.countsBy(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]PlayerSummary, 0, len(players))
	for _, p := range players {
		out = append(out, PlayerSummary{Player: *p, Counts: counts[p.ID]})
	}
	return out, nil
}

func (s *Service) PlayerCounts(ctx context.Context, id string) (Counts, error) {
	counts, err := s.countsBy(ctx, id)
	if err != nil {
		return Counts{}, err
	}
	return counts[id], nil
}

func (s *Service) countsBy(ctx context.Context, playerID string) (map[string]Counts, error) {
	type row struct {
		PlayerID    string
		ReplayState string
		N           int64
	}
	var rows []row
	tx := s.db.WithContext(ctx).Model(&model.Score{}).
		Select("player_id, replay_state, COUNT(*) AS n").Group("player_id, replay_state")
	if playerID != "" {
		tx = tx.Where("player_id = ?", playerID)
	}
	if err := tx.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("service: count scores: %w", err)
	}
	out := map[string]Counts{}
	for _, r := range rows {
		c := out[r.PlayerID]
		c.Scores += r.N
		switch r.ReplayState {
		case model.ReplayArchived:
			c.Archived += r.N
		case model.ReplayPending:
			c.Pending += r.N
		case model.ReplayFailed:
			c.Failed += r.N
		case model.ReplayGone:
			c.Gone += r.N
		}
		out[r.PlayerID] = c
	}
	return out, nil
}

func (s *Service) SetPlayerEnabled(ctx context.Context, id string, enabled bool) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Update(s.q.Player.Enabled, enabled)
	if err != nil {
		return fmt.Errorf("service: set enabled: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	if enabled {
		s.Wake()
	}
	return nil
}

func (s *Service) DeletePlayer(ctx context.Context, id string, deleteFiles bool) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Delete()
	if err != nil {
		return fmt.Errorf("service: delete player: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	if deleteFiles {
		if err := s.store.RemovePlayer(id); err != nil {
			return err
		}
	}
	s.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, PlayerID: Ptr(id), Message: fmt.Sprintf("player deleted (files deleted: %v)", deleteFiles)})
	return nil
}

func (s *Service) RequestPoll(ctx context.Context, id string) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Update(s.q.Player.LastPolledAt, nil)
	if err != nil {
		return fmt.Errorf("service: request poll: %w", err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	s.Wake()
	return nil
}
```

> Note: `Update(column, nil)` with gen sets the column to NULL. If the generated `Update` rejects `nil`, use `UpdateSimple(s.q.Player.LastPolledAt.Null())` (gen ≥ 0.3.20) and log a deviation.

- [x] **Step 10: Implement scores** — `internal/service/scores.go`

```go
package service

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm/clause"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

const (
	FilterWithReplay = "replay"
	FilterArchived   = "archived"
)

type UpsertResult struct{ New, Known, NewReplays int }

type ScoreFilter struct {
	PlayerID   string
	Search     string
	RankedOnly bool
	State      string // "", FilterWithReplay, FilterArchived
	Page       int
	PerPage    int
}

type ScoreList struct {
	Items   []*model.Score
	Total   int64
	Page    int
	PerPage int
	Pages   int
}

func leaderboardFrom(lb scoresaber.Leaderboard) *model.Leaderboard {
	return &model.Leaderboard{
		ID: lb.ID, SongHash: lb.Map.Hash, SongName: lb.Map.SongName, SongSubName: lb.Map.SongSubName,
		SongAuthor: lb.Map.SongAuthorName, Mapper: lb.Map.LevelAuthorName,
		Difficulty: lb.Difficulty.Difficulty, DifficultyRaw: lb.Difficulty.RawDifficulty, GameMode: lb.Difficulty.GameMode,
		CoverURL: lb.Map.CoverURL, Status: lb.Realm.LeaderboardStatus, Stars: lb.Realm.Stars, MaxScore: lb.MaxScore,
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
	}
}

// UpsertScores stores a page of ScoreSaber scores. Existing scores only get
// their mutable ranking fields refreshed; archive columns are never touched,
// except none → pending when ScoreSaber newly offers a replay.
func (s *Service) UpsertScores(ctx context.Context, playerID string, items []scoresaber.ScoreItem) (UpsertResult, error) {
	var res UpsertResult
	if len(items) == 0 {
		return res, nil
	}
	err := s.q.Transaction(func(tx *query.Query) error {
		seen := map[int64]bool{}
		var lbs []*model.Leaderboard
		ids := make([]int64, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.Score.ID)
			if !seen[it.Leaderboard.ID] {
				seen[it.Leaderboard.ID] = true
				lbs = append(lbs, leaderboardFrom(it.Leaderboard))
			}
		}
		if err := tx.Leaderboard.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}}, UpdateAll: true,
		}).CreateInBatches(lbs, 100); err != nil {
			return fmt.Errorf("upsert leaderboards: %w", err)
		}
		existing, err := tx.Score.WithContext(ctx).Where(tx.Score.ID.In(ids...)).Find()
		if err != nil {
			return fmt.Errorf("load existing scores: %w", err)
		}
		byID := make(map[int64]*model.Score, len(existing))
		for _, e := range existing {
			byID[e.ID] = e
		}
		var fresh []*model.Score
		for _, it := range items {
			sc := it.Score
			old, ok := byID[sc.ID]
			if !ok {
				row := scoreFrom(playerID, it)
				if row.ReplayState == model.ReplayPending {
					res.NewReplays++
				}
				res.New++
				fresh = append(fresh, row)
				byID[sc.ID] = row // guards against duplicates within one page
				continue
			}
			res.Known++
			upd := map[string]any{"rank": sc.Rank, "pp": sc.PP, "personal_best": sc.PersonalBest, "has_replay": sc.HasReplay || old.HasReplay}
			if sc.HasReplay && old.ReplayState == model.ReplayNone {
				upd["replay_state"] = model.ReplayPending
				res.NewReplays++
			}
			if _, err := tx.Score.WithContext(ctx).Where(tx.Score.ID.Eq(sc.ID)).Updates(upd); err != nil {
				return fmt.Errorf("update score %d: %w", sc.ID, err)
			}
		}
		if len(fresh) > 0 {
			if err := tx.Score.WithContext(ctx).CreateInBatches(fresh, 100); err != nil {
				return fmt.Errorf("insert scores: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return UpsertResult{}, fmt.Errorf("service: upsert scores: %w", err)
	}
	return res, nil
}

var likeStripper = strings.NewReplacer("%", "", "_", "")

func (s *Service) ListScores(ctx context.Context, f ScoreFilter) (ScoreList, error) {
	if f.PerPage <= 0 {
		f.PerPage = 50
	}
	f.PerPage = min(f.PerPage, 100)
	f.Page = max(f.Page, 1)
	q, lb := s.q.Score, s.q.Leaderboard
	filtered := func() query.IScoreDo {
		do := q.WithContext(ctx).Join(lb, lb.ID.EqCol(q.LeaderboardID))
		if f.PlayerID != "" {
			do = do.Where(q.PlayerID.Eq(f.PlayerID))
		}
		if term := likeStripper.Replace(strings.TrimSpace(f.Search)); term != "" {
			p := "%" + term + "%"
			do = do.Where(q.WithContext(ctx).Where(lb.SongName.Like(p)).Or(lb.SongAuthor.Like(p)).Or(lb.Mapper.Like(p)))
		}
		if f.RankedOnly {
			do = do.Where(lb.Status.Eq("RANKED"))
		}
		switch f.State {
		case FilterWithReplay:
			do = do.Where(q.HasReplay.Is(true))
		case FilterArchived:
			do = do.Where(q.ReplayState.Eq(model.ReplayArchived))
		}
		return do
	}
	total, err := filtered().Count()
	if err != nil {
		return ScoreList{}, fmt.Errorf("service: count scores: %w", err)
	}
	items, err := filtered().Select(q.ALL).Preload(q.Leaderboard).
		Order(q.SetAt.Desc(), q.ID.Desc()).Offset((f.Page - 1) * f.PerPage).Limit(f.PerPage).Find()
	if err != nil {
		return ScoreList{}, fmt.Errorf("service: list scores: %w", err)
	}
	pages := int((total + int64(f.PerPage) - 1) / int64(f.PerPage))
	return ScoreList{Items: items, Total: total, Page: f.Page, PerPage: f.PerPage, Pages: max(pages, 1)}, nil
}

func (s *Service) GetScore(ctx context.Context, id int64) (*model.Score, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).Where(q.ID.Eq(id)).First()
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("score %d", id))
	}
	return sc, nil
}
```

- [x] **Step 11: Implement the replay queue** — `internal/service/replays.go`

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

type ReplayTier int

const (
	TierNew      ReplayTier = iota // scores set after the player was added
	TierBackfill                   // historical scores
)

const MaxReplayAttempts = 5

var backoffSchedule = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

// Backoff is the delay after the given (1-based) failed attempt.
func Backoff(attempt int) time.Duration {
	i := min(max(attempt-1, 0), len(backoffSchedule)-1)
	return backoffSchedule[i]
}

func (s *Service) replayCandidates(ctx context.Context, tier ReplayTier) query.IScoreDo {
	q, p := s.q.Score, s.q.Player
	now := s.Now()
	do := q.WithContext(ctx).Join(p, p.ID.EqCol(q.PlayerID)).
		Where(q.ReplayState.Eq(model.ReplayPending), p.Enabled.Is(true)).
		Where(q.WithContext(ctx).Where(q.NextAttemptAt.IsNull()).Or(q.NextAttemptAt.Lte(now)))
	if tier == TierNew {
		return do.Where(q.SetAt.GteCol(p.AddedAt))
	}
	return do.Where(q.SetAt.LtCol(p.AddedAt))
}

// NextReplay returns the newest pending replay of the next player (round
// robin after lastPlayer) in the given tier, or nil when there is none.
func (s *Service) NextReplay(ctx context.Context, tier ReplayTier, lastPlayer string) (*model.Score, error) {
	q := s.q.Score
	var ids []string
	if err := s.replayCandidates(ctx, tier).Distinct(q.PlayerID).Order(q.PlayerID).Pluck(q.PlayerID, &ids); err != nil {
		return nil, fmt.Errorf("service: replay players: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	pick := pickAfter(ids, lastPlayer)
	sc, err := s.replayCandidates(ctx, tier).Select(q.ALL).Preload(q.Leaderboard, q.Player).
		Where(q.PlayerID.Eq(pick)).Order(q.SetAt.Desc()).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: next replay: %w", err)
	}
	return sc, nil
}

func (s *Service) updateScore(ctx context.Context, id int64, upd map[string]any) error {
	info, err := s.q.Score.WithContext(ctx).Where(s.q.Score.ID.Eq(id)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: update score %d: %w", id, err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: score %d", ErrNotFound, id)
	}
	return nil
}

func (s *Service) MarkReplayArchived(ctx context.Context, id, size int64, sha string) error {
	return s.updateScore(ctx, id, map[string]any{
		"replay_state": model.ReplayArchived, "replay_size": size, "replay_sha256": sha,
		"archived_at": s.Now(), "next_attempt_at": nil, "last_error": "",
	})
}

func (s *Service) MarkReplayGone(ctx context.Context, id int64) error {
	return s.updateScore(ctx, id, map[string]any{
		"replay_state": model.ReplayGone, "next_attempt_at": nil, "last_error": "replay no longer available on ScoreSaber",
	})
}

// MarkReplayAttemptFailed records a failed download. After MaxReplayAttempts
// it marks the score failed and returns gaveUp=true.
func (s *Service) MarkReplayAttemptFailed(ctx context.Context, id int64, cause error) (bool, time.Time, error) {
	sc, err := s.q.Score.WithContext(ctx).Where(s.q.Score.ID.Eq(id)).First()
	if err != nil {
		return false, time.Time{}, notFound(err, fmt.Sprintf("score %d", id))
	}
	attempts := sc.Attempts + 1
	msg := cause.Error()
	if attempts >= MaxReplayAttempts {
		return true, time.Time{}, s.updateScore(ctx, id, map[string]any{
			"attempts": attempts, "replay_state": model.ReplayFailed, "next_attempt_at": nil, "last_error": msg,
		})
	}
	next := s.Now().Add(Backoff(attempts))
	return false, next, s.updateScore(ctx, id, map[string]any{"attempts": attempts, "next_attempt_at": next, "last_error": msg})
}

// RetryFailed requeues failed replays, optionally limited to a player or one score.
func (s *Service) RetryFailed(ctx context.Context, playerID string, scoreID int64) (int64, error) {
	q := s.q.Score
	do := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayFailed))
	if playerID != "" {
		do = do.Where(q.PlayerID.Eq(playerID))
	}
	if scoreID != 0 {
		do = do.Where(q.ID.Eq(scoreID))
	}
	info, err := do.Updates(map[string]any{"replay_state": model.ReplayPending, "attempts": 0, "next_attempt_at": nil, "last_error": ""})
	if err != nil {
		return 0, fmt.Errorf("service: retry failed: %w", err)
	}
	if info.RowsAffected > 0 {
		s.Wake()
	}
	return info.RowsAffected, nil
}

func (s *Service) ListFailedReplays(ctx context.Context, page, perPage int) ([]*model.Score, int64, error) {
	page, perPage = max(page, 1), min(max(perPage, 1), 100)
	q := s.q.Score
	items, total, err := q.WithContext(ctx).Preload(q.Leaderboard, q.Player).
		Where(q.ReplayState.Eq(model.ReplayFailed)).Order(q.SetAt.Desc()).FindByPage((page-1)*perPage, perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("service: list failed: %w", err)
	}
	return items, total, nil
}

// NextRetryAt is the earliest next_attempt_at among pending replays.
func (s *Service) NextRetryAt(ctx context.Context) (time.Time, bool, error) {
	q := s.q.Score
	sc, err := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayPending), q.NextAttemptAt.IsNotNull()).
		Order(q.NextAttemptAt).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("service: next retry: %w", err)
	}
	return *sc.NextAttemptAt, true, nil
}
```

- [x] **Step 12: Implement poll/backfill bookkeeping** — `internal/service/backfill.go`

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
)

const BackfillRetryDelay = 5 * time.Minute

func (s *Service) updatePlayer(ctx context.Context, id string, upd map[string]any) error {
	info, err := s.q.Player.WithContext(ctx).Where(s.q.Player.ID.Eq(id)).Updates(upd)
	if err != nil {
		return fmt.Errorf("service: update player %s: %w", id, err)
	}
	if info.RowsAffected == 0 {
		return fmt.Errorf("%w: player %s", ErrNotFound, id)
	}
	return nil
}

// DuePlayer returns the enabled player whose poll is most overdue, or nil.
func (s *Service) DuePlayer(ctx context.Context, interval time.Duration) (*model.Player, error) {
	p := s.q.Player
	cutoff := s.Now().Add(-interval)
	pl, err := p.WithContext(ctx).Where(p.Enabled.Is(true)).
		Where(p.WithContext(ctx).Where(p.LastPolledAt.IsNull()).Or(p.LastPolledAt.Lte(cutoff))).
		Order(p.LastPolledAt, p.ID).First() // SQLite sorts NULL first
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("service: due player: %w", err)
	}
	return pl, nil
}

// NextPollAt is when the next enabled player becomes due (ok=false: no enabled players).
func (s *Service) NextPollAt(ctx context.Context, interval time.Duration) (time.Time, bool, error) {
	p := s.q.Player
	pl, err := p.WithContext(ctx).Where(p.Enabled.Is(true)).Order(p.LastPolledAt).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("service: next poll: %w", err)
	}
	if pl.LastPolledAt == nil {
		return s.Now(), true, nil
	}
	return pl.LastPolledAt.Add(interval), true, nil
}

func (s *Service) MarkPolled(ctx context.Context, id string) error {
	return s.updatePlayer(ctx, id, map[string]any{"last_polled_at": s.Now(), "last_error": ""})
}

func (s *Service) MarkPlayerError(ctx context.Context, id, msg string, disable bool) error {
	upd := map[string]any{"last_error": msg}
	if disable {
		upd["enabled"] = false
	}
	return s.updatePlayer(ctx, id, upd)
}

// UpdatePlayerProfile refreshes name/avatar/country from score payloads; empty values are ignored.
func (s *Service) UpdatePlayerProfile(ctx context.Context, id string, sp scoresaber.Player) error {
	upd := map[string]any{}
	if sp.Name != "" {
		upd["name"] = sp.Name
	}
	if sp.Avatar != "" {
		upd["avatar_url"] = sp.Avatar
	}
	if sp.Country != "" {
		upd["country"] = sp.Country
	}
	if len(upd) == 0 {
		return nil
	}
	return s.updatePlayer(ctx, id, upd)
}

// NextBackfillPlayer picks (round robin after lastPlayer) an enabled, already
// polled player whose backfill is not done and not deferred.
func (s *Service) NextBackfillPlayer(ctx context.Context, lastPlayer string) (*model.Player, error) {
	p := s.q.Player
	players, err := p.WithContext(ctx).
		Where(p.Enabled.Is(true), p.BackfillState.Neq(model.BackfillDone), p.LastPolledAt.IsNotNull()).
		Where(p.WithContext(ctx).Where(p.BackfillRetryAt.IsNull()).Or(p.BackfillRetryAt.Lte(s.Now()))).
		Order(p.ID).Find()
	if err != nil {
		return nil, fmt.Errorf("service: next backfill: %w", err)
	}
	if len(players) == 0 {
		return nil, nil
	}
	ids := make([]string, len(players))
	for i, pl := range players {
		ids[i] = pl.ID
	}
	pick := pickAfter(ids, lastPlayer)
	for _, pl := range players {
		if pl.ID == pick {
			return pl, nil
		}
	}
	return nil, nil
}

func (s *Service) SetBackfill(ctx context.Context, id, state string, nextPage, totalPages int) error {
	return s.updatePlayer(ctx, id, map[string]any{
		"backfill_state": state, "backfill_page": nextPage, "backfill_total_pages": totalPages, "backfill_retry_at": nil,
	})
}

func (s *Service) DeferBackfill(ctx context.Context, id string, until time.Time, msg string) error {
	return s.updatePlayer(ctx, id, map[string]any{"backfill_retry_at": until, "last_error": msg})
}
```

- [x] **Step 13: Run tests**

Run: `go mod tidy && go test -race ./internal/service/...`
Expected: `ok`. Common failures and fixes:
- `ambiguous column name: id` → a `Where`/`Order` on a joined query used an unqualified column; use the gen field (`q.ID`), which is table-qualified.
- Scores with wrong IDs → you forgot `Select(q.ALL)` on a joined `Find/First`.

- [x] **Step 14: Commit**

```bash
make lint
git add internal/service internal/testutil go.mod go.sum
git commit -m "feat: add service layer for players, scores and replay queue"
```

---

## Task 7: Service — auth, sessions, settings, events listing

**Files:**
- Create: `internal/service/auth.go`, `internal/service/settings.go`, `internal/service/export_test.go`
- Modify: `internal/service/events.go` (add listing + pruning), `internal/testutil/service.go` (cheap password params)
- Create tests: `internal/service/auth_test.go`, `internal/service/settings_test.go`, `internal/service/events_test.go`

**Interfaces:**
- Consumes: Task 6 `Service`, `notFound`, `Ptr`; `model.User`, `model.Session`, `model.Setting`, `model.SyncEvent`.
- Produces:
  - `service.PasswordParams *argon2id.Params` (tests lower it), `service.SessionTTL = 30 * 24 * time.Hour`, `service.MinPasswordLength = 10`
  - Errors `ErrAlreadySetup`, `ErrInvalidCredentials`, `ErrWeakPassword`, `ErrInvalidUsername`, `ErrInvalidSettings`
  - `NeedsSetup(ctx) (bool, error)`, `Setup(ctx, username, password string) (*model.User, error)`, `Login(ctx, username, password string) (token string, err error)`, `UserForSession(ctx, token string) (*model.User, error)`, `Logout(ctx, token string) error`, `ChangePassword(ctx, userID uint, oldPW, newPW string) error`, `ResetPassword(ctx, username, newPW string) error` (empty username = the only user)
  - `type Settings struct{ InstanceTitle string; PollInterval time.Duration; WorkerPaused bool }`, `service.DefaultSettings`, `Settings(ctx) (Settings, error)`, `UpdateSettings(ctx, Settings) error`, `SetWorkerPaused(ctx, bool) error`, `service.PollIntervals = []time.Duration{5m, 10m, 15m, 30m, 1h}`
  - `type EventFilter struct{ Level, Kind, PlayerID string; Page, PerPage int }`, `ListEvents(ctx, EventFilter) ([]*model.SyncEvent, int64, error)`, `PruneEvents(ctx) (int64, error)` (7 days / 10 000 rows); `service.EventRetention = 7 * 24 * time.Hour`, `service.MaxEvents = 10000`

- [x] **Step 1: Make test hashing cheap** — in `internal/testutil/service.go`, inside `NewService` right after `svc := service.New(...)`, add:

```go
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
```

and add the import `"github.com/alexedwards/argon2id"`. Then `go get github.com/alexedwards/argon2id@v1.0.0`.

- [x] **Step 2: Write the failing auth tests** — `internal/service/auth_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

const goodPW = "correct horse battery"

func TestSetupOnce(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	if need, _ := svc.NeedsSetup(ctx); !need {
		t.Fatal("fresh instance needs setup")
	}
	if _, err := svc.Setup(ctx, "admin", "short"); !errors.Is(err, service.ErrWeakPassword) {
		t.Fatalf("weak password err = %v", err)
	}
	if _, err := svc.Setup(ctx, " ", goodPW); !errors.Is(err, service.ErrInvalidUsername) {
		t.Fatalf("blank username err = %v", err)
	}
	u, err := svc.Setup(ctx, "  admin ", goodPW)
	if err != nil || u.Username != "admin" {
		t.Fatalf("setup = %+v, %v", u, err)
	}
	if need, _ := svc.NeedsSetup(ctx); need {
		t.Fatal("setup done")
	}
	if _, err := svc.Setup(ctx, "other", goodPW); !errors.Is(err, service.ErrAlreadySetup) {
		t.Fatalf("second setup err = %v", err)
	}
}

func TestSetupConcurrentOnlyOneWins(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	var wins atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Setup(ctx, fmt.Sprintf("admin%d", i), goodPW)
			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, service.ErrAlreadySetup):
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}
	if wins.Load() != 1 {
		t.Fatalf("%d setups succeeded, want exactly 1", wins.Load())
	}
}

func TestLoginAndSessions(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	_, _ = svc.Setup(ctx, "admin", goodPW)
	if _, err := svc.Login(ctx, "admin", "wrong password!"); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("bad password err = %v", err)
	}
	if _, err := svc.Login(ctx, "ghost", goodPW); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("unknown user err = %v", err)
	}
	token, err := svc.Login(ctx, "admin", goodPW)
	if err != nil || len(token) < 40 {
		t.Fatalf("login = %q, %v", token, err)
	}
	if u, err := svc.UserForSession(ctx, token); err != nil || u.Username != "admin" {
		t.Fatalf("session user = %+v, %v", u, err)
	}
	if _, err := svc.UserForSession(ctx, "forged"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("forged token err = %v", err)
	}
	clk.Advance(29 * 24 * time.Hour) // sliding: still valid, gets extended
	if _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("session should slide: %v", err)
	}
	clk.Advance(29 * 24 * time.Hour)
	if _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("extended session expired early: %v", err)
	}
	clk.Advance(31 * 24 * time.Hour)
	if _, err := svc.UserForSession(ctx, token); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("idle session must expire, err = %v", err)
	}
	token, _ = svc.Login(ctx, "admin", goodPW)
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UserForSession(ctx, token); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("logout must invalidate the session")
	}
}

func TestChangeAndResetPassword(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	u, _ := svc.Setup(ctx, "admin", goodPW)
	token, _ := svc.Login(ctx, "admin", goodPW)
	if err := svc.ChangePassword(ctx, u.ID, "nope nope nope", "another long password"); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("wrong old password err = %v", err)
	}
	if err := svc.ChangePassword(ctx, u.ID, goodPW, "short"); !errors.Is(err, service.ErrWeakPassword) {
		t.Fatalf("weak new password err = %v", err)
	}
	if err := svc.ChangePassword(ctx, u.ID, goodPW, "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UserForSession(ctx, token); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("changing the password must end existing sessions")
	}
	if _, err := svc.Login(ctx, "admin", "another long password"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, "", "reset long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "admin", "reset long password"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, "nobody", "reset long password"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("reset unknown user err = %v", err)
	}
}
```

- [x] **Step 3: Write the failing settings/events tests** — `internal/service/settings_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestSettings(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	st, err := svc.Settings(ctx)
	if err != nil || st != service.DefaultSettings {
		t.Fatalf("defaults = %+v, %v", st, err)
	}
	want := service.Settings{InstanceTitle: "My Replays", PollInterval: 15 * time.Minute}
	if err := svc.UpdateSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Settings(ctx); got != want {
		t.Fatalf("round trip = %+v", got)
	}
	for _, bad := range []service.Settings{
		{InstanceTitle: "", PollInterval: 10 * time.Minute},
		{InstanceTitle: "x", PollInterval: 7 * time.Minute},
	} {
		if err := svc.UpdateSettings(ctx, bad); !errors.Is(err, service.ErrInvalidSettings) {
			t.Fatalf("UpdateSettings(%+v) err = %v", bad, err)
		}
	}
	if err := svc.SetWorkerPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Settings(ctx); !got.WorkerPaused || got.InstanceTitle != "My Replays" {
		t.Fatalf("pause must not touch other settings: %+v", got)
	}
}
```

`internal/service/events_test.go`:

```go
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestListEvents(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindScores, PlayerID: service.Ptr("1001"), Message: "a"})
	clk.Advance(time.Second)
	svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindReplay, PlayerID: service.Ptr("1002"), Message: "b"})
	clk.Advance(time.Second)
	svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindReplay, Message: "c"})

	all, total, err := svc.ListEvents(ctx, service.EventFilter{})
	if err != nil || total != 3 || all[0].Message != "c" {
		t.Fatalf("all = %v total=%d err=%v (newest first)", all, total, err)
	}
	errs, total, _ := svc.ListEvents(ctx, service.EventFilter{Level: model.LevelError})
	if total != 1 || errs[0].Message != "b" {
		t.Fatalf("level filter = %v", errs)
	}
	replay, total, _ := svc.ListEvents(ctx, service.EventFilter{Kind: model.KindReplay, PerPage: 1, Page: 2})
	if total != 2 || len(replay) != 1 || replay[0].Message != "b" {
		t.Fatalf("kind filter page 2 = %v", replay)
	}
	alice, total, _ := svc.ListEvents(ctx, service.EventFilter{PlayerID: "1001"})
	if total != 1 || alice[0].Message != "a" {
		t.Fatalf("player filter = %v", alice)
	}
}

func TestPruneEvents(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Message: "old"})
	clk.Advance(8 * 24 * time.Hour)
	for range 6 {
		svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindPoll, Message: "new"})
	}
	n, err := service.PruneEventsWith(svc, ctx, 7*24*time.Hour, 4)
	if err != nil || n != 3 {
		t.Fatalf("pruned %d, %v; want 3 (1 by age + 2 over the cap)", n, err)
	}
	_, total, _ := svc.ListEvents(ctx, service.EventFilter{})
	if total != 4 {
		t.Fatalf("remaining = %d", total)
	}
}
```

`internal/service/export_test.go`:

```go
package service

// PruneEventsWith exposes pruneEvents with custom limits to external tests.
var PruneEventsWith = (*Service).pruneEvents
```

- [x] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/service/...`
Expected: FAIL — `undefined: service.ErrWeakPassword`, `svc.Setup`, …

- [x] **Step 5: Implement auth** — `internal/service/auth.go`

```go
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

var (
	ErrAlreadySetup       = errors.New("setup has already been completed")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrWeakPassword       = errors.New("password must be at least 10 characters")
	ErrInvalidUsername    = errors.New("username must be 1-64 characters")
)

const (
	SessionTTL        = 30 * 24 * time.Hour
	MinPasswordLength = 10
)

// PasswordParams are the argon2id parameters for new hashes (tests lower them).
var PasswordParams = &argon2id.Params{Memory: 64 * 1024, Iterations: 1, Parallelism: 2, SaltLength: 16, KeyLength: 32}

var (
	dummyOnce sync.Once
	dummyHash string
)

// burnHash spends roughly the same time as a real verify, so unknown
// usernames are not distinguishable by timing.
func burnHash(password string) {
	dummyOnce.Do(func() { dummyHash, _ = argon2id.CreateHash("ssarchiver-dummy", PasswordParams) })
	_, _ = argon2id.ComparePasswordAndHash(password, dummyHash)
}

func validateCredentials(username, password string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" || utf8.RuneCountInString(username) > 64 {
		return "", ErrInvalidUsername
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return "", ErrWeakPassword
	}
	return username, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.q.User.WithContext(ctx).Count()
	if err != nil {
		return false, fmt.Errorf("service: count users: %w", err)
	}
	return n == 0, nil
}

// Setup creates the single admin. The count check and insert run in one
// IMMEDIATE transaction, so concurrent setups serialise and only one wins.
func (s *Service) Setup(ctx context.Context, username, password string) (*model.User, error) {
	username, err := validateCredentials(username, password)
	if err != nil {
		return nil, err
	}
	hash, err := argon2id.CreateHash(password, PasswordParams)
	if err != nil {
		return nil, fmt.Errorf("service: hash password: %w", err)
	}
	u := &model.User{Username: username, PasswordHash: hash, CreatedAt: s.Now()}
	err = s.q.Transaction(func(tx *query.Query) error {
		n, err := tx.User.WithContext(ctx).Count()
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadySetup
		}
		return tx.User.WithContext(ctx).Create(u)
	})
	if err != nil {
		if errors.Is(err, ErrAlreadySetup) {
			return nil, ErrAlreadySetup
		}
		return nil, fmt.Errorf("service: setup: %w", err)
	}
	return u, nil
}

func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	u, err := s.q.User.WithContext(ctx).Where(s.q.User.Username.Eq(strings.TrimSpace(username))).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		burnHash(password)
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("service: login: %w", err)
	}
	ok, err := argon2id.ComparePasswordAndHash(password, u.PasswordHash)
	if err != nil || !ok {
		return "", ErrInvalidCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("service: token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.Now()
	sess := &model.Session{TokenHash: hashToken(token), UserID: u.ID, ExpiresAt: now.Add(SessionTTL), CreatedAt: now}
	if err := s.q.Session.WithContext(ctx).Create(sess); err != nil {
		return "", fmt.Errorf("service: create session: %w", err)
	}
	return token, nil
}

// UserForSession resolves a cookie token. Sessions slide: when less than
// SessionTTL-24h remains, expiry is pushed back to now+SessionTTL.
func (s *Service) UserForSession(ctx context.Context, token string) (*model.User, error) {
	if token == "" {
		return nil, fmt.Errorf("%w: session", ErrNotFound)
	}
	q := s.q.Session
	sess, err := q.WithContext(ctx).Preload(q.User).Where(q.TokenHash.Eq(hashToken(token))).First()
	if err != nil {
		return nil, notFound(err, "session")
	}
	now := s.Now()
	if !sess.ExpiresAt.After(now) {
		_, _ = q.WithContext(ctx).Where(q.TokenHash.Eq(sess.TokenHash)).Delete()
		return nil, fmt.Errorf("%w: session expired", ErrNotFound)
	}
	if sess.ExpiresAt.Sub(now) < SessionTTL-24*time.Hour {
		_, _ = q.WithContext(ctx).Where(q.TokenHash.Eq(sess.TokenHash)).Update(q.ExpiresAt, now.Add(SessionTTL))
	}
	if sess.User == nil {
		return nil, fmt.Errorf("%w: session user", ErrNotFound)
	}
	return sess.User, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.q.Session.WithContext(ctx).Where(s.q.Session.TokenHash.Eq(hashToken(token))).Delete()
	return err
}

func (s *Service) setPassword(ctx context.Context, userID uint, password string) error {
	hash, err := argon2id.CreateHash(password, PasswordParams)
	if err != nil {
		return fmt.Errorf("service: hash password: %w", err)
	}
	return s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.User.WithContext(ctx).Where(tx.User.ID.Eq(userID)).Update(tx.User.PasswordHash, hash); err != nil {
			return err
		}
		_, err := tx.Session.WithContext(ctx).Where(tx.Session.UserID.Eq(userID)).Delete()
		return err
	})
}

func (s *Service) ChangePassword(ctx context.Context, userID uint, oldPW, newPW string) error {
	u, err := s.q.User.WithContext(ctx).Where(s.q.User.ID.Eq(userID)).First()
	if err != nil {
		return notFound(err, "user")
	}
	if ok, err := argon2id.ComparePasswordAndHash(oldPW, u.PasswordHash); err != nil || !ok {
		return ErrInvalidCredentials
	}
	if _, err := validateCredentials(u.Username, newPW); err != nil {
		return err
	}
	return s.setPassword(ctx, u.ID, newPW)
}

// ResetPassword is used by the CLI; an empty username targets the only user.
func (s *Service) ResetPassword(ctx context.Context, username, newPW string) error {
	do := s.q.User.WithContext(ctx)
	if username != "" {
		do = do.Where(s.q.User.Username.Eq(username))
	}
	u, err := do.Order(s.q.User.ID).First()
	if err != nil {
		return notFound(err, "user "+username)
	}
	if _, err := validateCredentials(u.Username, newPW); err != nil {
		return err
	}
	return s.setPassword(ctx, u.ID, newPW)
}
```

- [x] **Step 6: Implement settings** — `internal/service/settings.go`

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/yyewolf/ssarchiver/internal/model"
)

var ErrInvalidSettings = errors.New("invalid settings")

type Settings struct {
	InstanceTitle string
	PollInterval  time.Duration
	WorkerPaused  bool
}

var DefaultSettings = Settings{InstanceTitle: "SSArchiver", PollInterval: 10 * time.Minute}

// PollIntervals are the choices offered in the UI and accepted by UpdateSettings.
var PollIntervals = []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

const (
	keyTitle  = "instance_title"
	keyPoll   = "poll_interval"
	keyPaused = "worker_paused"
)

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	rows, err := s.q.Setting.WithContext(ctx).Find()
	if err != nil {
		return Settings{}, fmt.Errorf("service: load settings: %w", err)
	}
	st := DefaultSettings
	for _, r := range rows {
		switch r.Key {
		case keyTitle:
			if r.Value != "" {
				st.InstanceTitle = r.Value
			}
		case keyPoll:
			if d, err := time.ParseDuration(r.Value); err == nil && slices.Contains(PollIntervals, d) {
				st.PollInterval = d
			}
		case keyPaused:
			st.WorkerPaused, _ = strconv.ParseBool(r.Value)
		}
	}
	return st, nil
}

func (s *Service) putSettings(ctx context.Context, kv map[string]string) error {
	rows := make([]*model.Setting, 0, len(kv))
	for k, v := range kv {
		rows = append(rows, &model.Setting{Key: k, Value: v})
	}
	return s.q.Setting.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(rows...)
}

// UpdateSettings saves title and poll interval (WorkerPaused is ignored; use SetWorkerPaused).
func (s *Service) UpdateSettings(ctx context.Context, st Settings) error {
	title := strings.TrimSpace(st.InstanceTitle)
	if title == "" || utf8.RuneCountInString(title) > 64 {
		return fmt.Errorf("%w: title must be 1-64 characters", ErrInvalidSettings)
	}
	if !slices.Contains(PollIntervals, st.PollInterval) {
		return fmt.Errorf("%w: unsupported poll interval %s", ErrInvalidSettings, st.PollInterval)
	}
	if err := s.putSettings(ctx, map[string]string{keyTitle: title, keyPoll: st.PollInterval.String()}); err != nil {
		return fmt.Errorf("service: save settings: %w", err)
	}
	s.Wake()
	return nil
}

func (s *Service) SetWorkerPaused(ctx context.Context, paused bool) error {
	if err := s.putSettings(ctx, map[string]string{keyPaused: strconv.FormatBool(paused)}); err != nil {
		return fmt.Errorf("service: save pause: %w", err)
	}
	s.Wake()
	return nil
}
```

- [x] **Step 7: Add event listing and pruning** — append to `internal/service/events.go` (add imports `"fmt"`, `"time"`):

```go
const (
	EventRetention = 7 * 24 * time.Hour
	MaxEvents      = 10000
)

type EventFilter struct {
	Level, Kind, PlayerID string
	Page, PerPage         int
}

func (s *Service) ListEvents(ctx context.Context, f EventFilter) ([]*model.SyncEvent, int64, error) {
	if f.PerPage <= 0 {
		f.PerPage = 50
	}
	f.PerPage = min(f.PerPage, 200)
	f.Page = max(f.Page, 1)
	e := s.q.SyncEvent
	do := e.WithContext(ctx)
	if f.Level != "" {
		do = do.Where(e.Level.Eq(f.Level))
	}
	if f.Kind != "" {
		do = do.Where(e.Kind.Eq(f.Kind))
	}
	if f.PlayerID != "" {
		do = do.Where(e.PlayerID.Eq(f.PlayerID))
	}
	items, total, err := do.Order(e.ID.Desc()).FindByPage((f.Page-1)*f.PerPage, f.PerPage)
	if err != nil {
		return nil, 0, fmt.Errorf("service: list events: %w", err)
	}
	return items, total, nil
}

// PruneEvents keeps the last EventRetention and at most MaxEvents rows.
func (s *Service) PruneEvents(ctx context.Context) (int64, error) {
	return s.pruneEvents(ctx, EventRetention, MaxEvents)
}

func (s *Service) pruneEvents(ctx context.Context, maxAge time.Duration, maxCount int) (int64, error) {
	e := s.q.SyncEvent
	info, err := e.WithContext(ctx).Where(e.At.Lt(s.Now().Add(-maxAge))).Delete()
	if err != nil {
		return 0, fmt.Errorf("service: prune events by age: %w", err)
	}
	removed := info.RowsAffected
	var cut []int64
	if err := e.WithContext(ctx).Order(e.ID.Desc()).Offset(maxCount).Limit(1).Pluck(e.ID, &cut); err != nil {
		return removed, fmt.Errorf("service: prune events cutoff: %w", err)
	}
	if len(cut) == 1 {
		info, err := e.WithContext(ctx).Where(e.ID.Lte(cut[0])).Delete()
		if err != nil {
			return removed, fmt.Errorf("service: prune events by count: %w", err)
		}
		removed += info.RowsAffected
	}
	return removed, nil
}
```

- [x] **Step 8: Run tests**

Run: `go mod tidy && go test -race ./internal/service/...`
Expected: `ok`. If `TestSetupConcurrentOnlyOneWins` reports `database is locked`, confirm `_txlock=immediate` and `busy_timeout` are in the DSN (Task 2).

- [x] **Step 9: Commit**

```bash
make lint
git add internal/service internal/testutil go.mod go.sum
git commit -m "feat: add auth, sessions, settings and event log queries"
```

---
## Task 8: Archiver — poll, backfill, loop

**Files:**
- Create: `internal/archiver/worker.go`, `internal/archiver/poll.go`, `internal/archiver/fake_test.go`, `internal/archiver/poll_test.go`, `internal/archiver/worker_test.go`

**Interfaces:**
- Consumes: Task 6/7 service methods `Settings`, `DuePlayer`, `NextPollAt`, `NextRetryAt`, `UpsertScores`, `UpdatePlayerProfile`, `MarkPolled`, `MarkPlayerError`, `SetBackfill`, `DeferBackfill`, `NextBackfillPlayer`, `PruneEvents`, `Log`, `Now`, `WakeC`, `BackfillRetryDelay`; `scoresaber.ScorePage`, `scoresaber.ErrRateLimited`, `scoresaber.ErrNotFound`, `scoresaber.LimiterSnapshot`.
- Produces:
  - `archiver.Client` interface `{ Scores(ctx, playerID string, page int) (scoresaber.ScorePage, error); Replay(ctx, scoreID int64) (io.ReadCloser, error) }` (satisfied by `*scoresaber.Client`)
  - `archiver.LimiterSource` interface `{ Snapshot() scoresaber.LimiterSnapshot }` (satisfied by `*scoresaber.Limiter`; may be nil)
  - `archiver.New(svc *service.Service, c Client, l LimiterSource) *Worker`
  - `(*Worker).Run(ctx) error` (returns nil on ctx cancel), `(*Worker).Step(ctx) (did bool, err error)`, `(*Worker).Status() Status`
  - `type Status struct{ State State; Task string; Since time.Time; Limiter scoresaber.LimiterSnapshot }`; `type State string` with `StateIdle="idle"`, `StateRunning="running"`, `StatePaused="paused"`, `StateRateLimited="ratelimited"`, `StateStopped="stopped"`
  - `archiver.MaxPollPages = 5`

Behaviour (spec §6.2, §6.4): `Step` does at most one unit of work in priority order. In this task: (1) poll a due player, (2) list one backfill page. Task 9 inserts replay downloads between and after them.

Poll rules:
- Fetch pages 1..`MaxPollPages`; stop early when a page contains a known score, is empty, or is the last page (`page >= TotalPages`). Refresh the player profile from the first score's `player` object.
- If the player's backfill is `pending` at page ≤ 1 (first poll): if the poll reached the end of history mark backfill `done`, else set it `pending` at page `MaxPollPages+1` (the poll already stored pages 1–5).
- Else if the poll hit the cap without reaching a known score while backfill is `done`: set backfill `pending` at page `MaxPollPages+1` and log a warning.
- `MarkPolled`; log an `info`/`scores` event only when new scores were found.
- Errors: rate limited → log `warn`/`ratelimit`, do **not** mark polled; player 404 → disable with message; other errors → `MarkPolled` then `MarkPlayerError` (so the player retries next interval), log `error`/`poll`.

Backfill rules: fetch page `BackfillPage`; set state `running`; upsert; if the page is empty or `page >= TotalPages` → `done` (log `info`/`backfill` "backfill listing complete"), else advance to `page+1`. Errors: rate limited → log only; 404 → disable player; other → `DeferBackfill(now+BackfillRetryDelay)`.

- [x] **Step 1: Write the fake client** — `internal/archiver/fake_test.go`

```go
package archiver_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

type fakeClient struct {
	mu           sync.Mutex
	perPage      int
	scores       map[string][]scoresaber.ScoreItem // newest first
	scoresErr    map[string]error
	replays      map[int64][]byte
	replayErr    map[int64]error
	replayReader map[int64]func() io.ReadCloser
	scoreCalls   []string // "player:page"
	replayCalls  []int64
}

func newFake() *fakeClient {
	return &fakeClient{
		perPage: 2, scores: map[string][]scoresaber.ScoreItem{}, scoresErr: map[string]error{},
		replays: map[int64][]byte{}, replayErr: map[int64]error{}, replayReader: map[int64]func() io.ReadCloser{},
	}
}

func (f *fakeClient) Scores(_ context.Context, playerID string, page int) (scoresaber.ScorePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scoreCalls = append(f.scoreCalls, fmt.Sprintf("%s:%d", playerID, page))
	if err := f.scoresErr[playerID]; err != nil {
		return scoresaber.ScorePage{}, err
	}
	all := f.scores[playerID]
	total := (len(all) + f.perPage - 1) / f.perPage
	var data []scoresaber.ScoreItem
	if start := (page - 1) * f.perPage; start < len(all) {
		data = all[start:min(start+f.perPage, len(all))]
	}
	return scoresaber.ScorePage{Data: data, Metadata: scoresaber.PageMeta{Page: page, ItemsPerPage: f.perPage, TotalItems: len(all), TotalPages: total}}, nil
}

func (f *fakeClient) Replay(_ context.Context, id int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replayCalls = append(f.replayCalls, id)
	if err := f.replayErr[id]; err != nil {
		return nil, err
	}
	if fn := f.replayReader[id]; fn != nil {
		return fn(), nil
	}
	if b, ok := f.replays[id]; ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return nil, fmt.Errorf("%w: replay %d", scoresaber.ErrNotFound, id)
}

func (f *fakeClient) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.scoreCalls...)
}

func (f *fakeClient) replaysCalled() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.replayCalls...)
}

func (f *fakeClient) reset() {
	f.mu.Lock()
	f.scoreCalls, f.replayCalls = nil, nil
	f.mu.Unlock()
}

// history returns n scores (newest first) ending `newest`, one minute apart,
// with ids start, start+1, …; all have replays and fake replay bytes.
func (f *fakeClient) history(playerID string, start int64, n int, newest time.Time) []scoresaber.ScoreItem {
	var out []scoresaber.ScoreItem
	for i := range n {
		id := start + int64(i)
		out = append(out, testutil.Item(playerID, id, id+100000, newest.Add(-time.Duration(i)*time.Minute), true))
		f.replays[id] = []byte(fmt.Sprintf("replay-%d", id))
	}
	return out
}

type env struct {
	svc *service.Service
	clk *testutil.Clock
	fc  *fakeClient
	w   *archiver.Worker
}

func newEnv(t *testing.T) *env {
	t.Helper()
	svc, _, clk := testutil.NewService(t)
	fc := newFake()
	return &env{svc: svc, clk: clk, fc: fc, w: archiver.New(svc, fc, nil)}
}

func (e *env) add(t *testing.T, id string) {
	t.Helper()
	if _, err := e.svc.AddPlayer(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func (e *env) step(t *testing.T) bool {
	t.Helper()
	did, err := e.w.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return did
}

// drain runs Step until it reports no work (at most max times).
func (e *env) drain(t *testing.T, max int) {
	t.Helper()
	for range max {
		if !e.step(t) {
			return
		}
	}
	t.Fatalf("worker still busy after %d steps", max)
}
```

- [x] **Step 2: Write the failing poll/backfill tests** — `internal/archiver/poll_test.go`

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
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestFirstPollShortHistoryCompletesBackfill(t *testing.T) {
	e := newEnv(t)
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	if !e.step(t) {
		t.Fatal("first step must poll")
	}
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:1", "1001:2"}) {
		t.Fatalf("calls = %v", got)
	}
	p, _ := e.svc.GetPlayer(context.Background(), "1001")
	if p.BackfillState != model.BackfillDone || p.LastPolledAt == nil {
		t.Fatalf("player = %+v", p)
	}
}

func TestFirstPollLongHistoryHandsOverToBackfill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 20, testutil.T0.Add(-time.Hour))
	e.step(t)
	if got := e.fc.calls(); len(got) != 5 || got[4] != "1001:5" {
		t.Fatalf("first poll calls = %v", got)
	}
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.BackfillState != model.BackfillPending || p.BackfillPage != 6 || p.BackfillTotalPages != 10 {
		t.Fatalf("backfill = %s page %d/%d", p.BackfillState, p.BackfillPage, p.BackfillTotalPages)
	}
	e.fc.reset()
	e.drain(t, 100)
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:6", "1001:7", "1001:8", "1001:9", "1001:10"}) {
		t.Fatalf("backfill calls = %v", got)
	}
	p, _ = e.svc.GetPlayer(ctx, "1001")
	if p.BackfillState != model.BackfillDone {
		t.Fatalf("backfill state = %s", p.BackfillState)
	}
	c, _ := e.svc.PlayerCounts(ctx, "1001")
	if c.Scores != 20 {
		t.Fatalf("scores stored = %d", c.Scores)
	}
}

func TestPollStopsAtKnownScore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	old := e.fc.history("1001", 1, 3, testutil.T0.Add(-time.Hour))
	e.fc.scores["1001"] = old
	e.drain(t, 100)
	fresh := e.fc.history("1001", 50, 3, testutil.T0.Add(time.Hour))
	e.fc.scores["1001"] = append(fresh, old...)
	e.fc.reset()
	if err := e.svc.RequestPoll(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	e.step(t)
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:1", "1001:2"}) {
		t.Fatalf("poll must stop at the page containing a known score, calls = %v", got)
	}
	evs, _, _ := e.svc.ListEvents(ctx, service.EventFilter{Kind: model.KindScores})
	if len(evs) == 0 || !strings.Contains(evs[0].Message, "3 new scores") {
		t.Fatalf("expected a scores event, got %v", evs)
	}
}

func TestPollCapResumesBackfill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	old := e.fc.history("1001", 1, 2, testutil.T0.Add(-time.Hour))
	e.fc.scores["1001"] = old
	e.drain(t, 100)
	e.fc.scores["1001"] = append(e.fc.history("1001", 100, 12, testutil.T0.Add(time.Hour)), old...)
	_ = e.svc.RequestPoll(ctx, "1001")
	e.fc.reset()
	e.step(t)
	if got := e.fc.calls(); len(got) != 5 {
		t.Fatalf("poll must stop at MaxPollPages, calls = %v", got)
	}
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.BackfillState != model.BackfillPending || p.BackfillPage != 6 {
		t.Fatalf("backfill not resumed: %s page %d", p.BackfillState, p.BackfillPage)
	}
}

func TestPollRateLimitedDoesNotMarkPolled(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scoresErr["1001"] = fmt.Errorf("%w: test", scoresaber.ErrRateLimited)
	e.step(t)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.LastPolledAt != nil {
		t.Fatal("a rate-limited poll must stay due")
	}
	evs, _, _ := e.svc.ListEvents(ctx, service.EventFilter{Kind: model.KindRateLimit})
	if len(evs) != 1 {
		t.Fatalf("rate limit events = %d", len(evs))
	}
}

func TestPollPlayerNotFoundDisables(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scoresErr["1001"] = fmt.Errorf("%w: gone", scoresaber.ErrNotFound)
	e.step(t)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.Enabled || !strings.Contains(p.LastError, "not found") {
		t.Fatalf("player = %+v", p)
	}
}

func TestPollServerErrorRetriesNextInterval(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	_ = e.svc.SetBackfill(ctx, "1001", model.BackfillDone, 2, 1) // isolate polling from backfill work
	e.fc.scoresErr["1001"] = &scoresaber.StatusError{StatusCode: 502, Body: "bad gateway"}
	e.step(t)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.LastPolledAt == nil || !strings.Contains(p.LastError, "502") || !p.Enabled {
		t.Fatalf("player = %+v", p)
	}
	if e.step(t) {
		t.Fatal("player must not be re-polled before the interval")
	}
}

func TestBackfillErrorDefers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.scores["1001"] = e.fc.history("1001", 1, 20, testutil.T0.Add(-time.Hour))
	e.step(t) // first poll → backfill pending at page 6
	e.fc.scoresErr["1001"] = &scoresaber.StatusError{StatusCode: 502}
	e.drain(t, 100) // the backfill page fails once and is deferred (Task 9 also drains replay downloads here)
	p, _ := e.svc.GetPlayer(ctx, "1001")
	if p.BackfillRetryAt == nil || !p.BackfillRetryAt.Equal(e.clk.Now().Add(service.BackfillRetryDelay)) || !strings.Contains(p.LastError, "502") {
		t.Fatalf("backfill not deferred: %+v", p)
	}
	delete(e.fc.scoresErr, "1001")
	e.clk.Advance(service.BackfillRetryDelay)
	e.fc.reset()
	e.step(t)
	if got := e.fc.calls(); len(got) != 1 || got[0] != "1001:6" {
		t.Fatalf("deferred backfill not retried: %v", got)
	}
}
```

- [x] **Step 3: Write the failing worker tests** — `internal/archiver/worker_test.go`

```go
package archiver_test

import (
	"context"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
)

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 3s")
}

func TestPausedWorkerDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.add(t, "1001")
	if err := e.svc.SetWorkerPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if e.step(t) {
		t.Fatal("paused worker reported work")
	}
	if len(e.fc.calls()) != 0 {
		t.Fatal("paused worker called ScoreSaber")
	}
	if st := e.w.Status(); st.State != archiver.StatePaused {
		t.Fatalf("state = %s", st.State)
	}
}

func TestRunWakesAndStops(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.w.Run(ctx) }()
	eventually(t, func() bool { return e.w.Status().State == archiver.StateIdle })
	e.add(t, "1001") // AddPlayer wakes the worker
	eventually(t, func() bool { return len(e.fc.calls()) > 0 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if st := e.w.Status(); st.State != archiver.StateStopped {
		t.Fatalf("state after stop = %s", st.State)
	}
}
```

- [x] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/archiver/...`
Expected: FAIL — `undefined: archiver.New`.

- [x] **Step 5: Implement the worker loop** — `internal/archiver/worker.go`

```go
// Package archiver is the background worker that polls ScoreSaber, walks
// player history and downloads replays (spec §6).
package archiver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type Client interface {
	Scores(ctx context.Context, playerID string, page int) (scoresaber.ScorePage, error)
	Replay(ctx context.Context, scoreID int64) (io.ReadCloser, error)
}

type LimiterSource interface {
	Snapshot() scoresaber.LimiterSnapshot
}

type State string

const (
	StateIdle        State = "idle"
	StateRunning     State = "running"
	StatePaused      State = "paused"
	StateRateLimited State = "ratelimited"
	StateStopped     State = "stopped"
)

type Status struct {
	State   State
	Task    string
	Since   time.Time
	Limiter scoresaber.LimiterSnapshot
}

const (
	MaxPollPages = 5
	maxIdle      = time.Minute
	pruneEvery   = time.Hour
)

type Worker struct {
	svc     *service.Service
	client  Client
	limiter LimiterSource

	mu     sync.RWMutex
	status Status

	lastReplayPlayer   string
	lastBackfillPlayer string
	lastPrune          time.Time
}

func New(svc *service.Service, c Client, l LimiterSource) *Worker {
	return &Worker{svc: svc, client: c, limiter: l, status: Status{State: StateIdle, Since: svc.Now()}}
}

func (w *Worker) setStatus(state State, task string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status.State != state || w.status.Task != task {
		w.status = Status{State: state, Task: task, Since: w.svc.Now()}
	}
}

// Status returns a snapshot for the UI/API.
func (w *Worker) Status() Status {
	w.mu.RLock()
	st := w.status
	w.mu.RUnlock()
	if w.limiter != nil {
		st.Limiter = w.limiter.Snapshot()
		if st.State == StateRunning && st.Limiter.Waiting {
			st.State = StateRateLimited
		}
	}
	return st
}

// Run loops until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			w.setStatus(StateStopped, "")
			return nil
		}
		did, err := w.Step(ctx)
		if err != nil && ctx.Err() == nil {
			w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindWorker, Message: "worker step failed: " + err.Error()})
			did = false
		}
		if !did {
			w.idle(ctx)
		}
	}
}

// Step performs at most one unit of work. did=false means nothing was due.
func (w *Worker) Step(ctx context.Context) (did bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			did, err = false, fmt.Errorf("panic: %v", r)
		}
	}()
	st, err := w.svc.Settings(ctx)
	if err != nil {
		return false, err
	}
	if st.WorkerPaused {
		w.setStatus(StatePaused, "")
		return false, nil
	}
	w.maybePrune(ctx)

	p, err := w.svc.DuePlayer(ctx, st.PollInterval)
	if err != nil {
		return false, err
	}
	if p != nil {
		return true, w.poll(ctx, p)
	}
	bp, err := w.svc.NextBackfillPlayer(ctx, w.lastBackfillPlayer)
	if err != nil {
		return false, err
	}
	if bp != nil {
		w.lastBackfillPlayer = bp.ID
		return true, w.backfill(ctx, bp)
	}
	return false, nil
}

func (w *Worker) idle(ctx context.Context) {
	w.mu.RLock()
	paused := w.status.State == StatePaused
	w.mu.RUnlock()
	if !paused {
		w.setStatus(StateIdle, "")
	}
	t := time.NewTimer(w.idleFor(ctx))
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-w.svc.WakeC():
	}
}

func (w *Worker) idleFor(ctx context.Context) time.Duration {
	d := maxIdle
	now := w.svc.Now()
	consider := func(at time.Time, ok bool, err error) {
		if err == nil && ok && at.Sub(now) < d {
			d = at.Sub(now)
		}
	}
	if st, err := w.svc.Settings(ctx); err == nil {
		consider(w.svc.NextPollAt(ctx, st.PollInterval))
	}
	consider(w.svc.NextRetryAt(ctx))
	return max(d, time.Second)
}

func (w *Worker) maybePrune(ctx context.Context) {
	now := w.svc.Now()
	if now.Sub(w.lastPrune) < pruneEvery {
		return
	}
	w.lastPrune = now
	if n, err := w.svc.PruneEvents(ctx); err != nil {
		slog.Warn("prune events", "err", err)
	} else if n > 0 {
		slog.Debug("pruned sync events", "count", n)
	}
}
```

- [x] **Step 6: Implement poll/backfill** — `internal/archiver/poll.go`

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

func (w *Worker) poll(ctx context.Context, p *model.Player) error {
	w.setStatus(StateRunning, "Polling "+p.Name)
	var newScores, newReplays, pagesRead, totalPages int
	reachedEnd := false
	for page := 1; page <= MaxPollPages; page++ {
		sp, err := w.client.Scores(ctx, p.ID, page)
		if err != nil {
			return w.clientError(ctx, p, err, true)
		}
		pagesRead, totalPages = page, sp.Metadata.TotalPages
		if page == 1 && len(sp.Data) > 0 {
			if err := w.svc.UpdatePlayerProfile(ctx, p.ID, sp.Data[0].Score.Player); err != nil {
				return err
			}
		}
		res, err := w.svc.UpsertScores(ctx, p.ID, sp.Data)
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
	case p.BackfillState == model.BackfillPending && p.BackfillPage <= 1:
		if reachedEnd {
			if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillDone, pagesRead+1, totalPages); err != nil {
				return err
			}
		} else if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
	case !reachedEnd && p.BackfillState == model.BackfillDone:
		if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillPending, MaxPollPages+1, totalPages); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{
			Level: model.LevelWarn, Kind: model.KindBackfill, PlayerID: service.Ptr(p.ID),
			Message: fmt.Sprintf("more than %d pages of new scores since the last poll; resuming backfill from page %d", MaxPollPages, MaxPollPages+1),
		})
	}
	if err := w.svc.MarkPolled(ctx, p.ID); err != nil {
		return err
	}
	if newScores > 0 {
		w.svc.Log(ctx, model.SyncEvent{
			Level: model.LevelInfo, Kind: model.KindScores, PlayerID: service.Ptr(p.ID),
			Message: fmt.Sprintf("%d new scores, %d with replays", newScores, newReplays),
		})
	}
	return nil
}

func (w *Worker) backfill(ctx context.Context, p *model.Player) error {
	page := max(p.BackfillPage, 1)
	w.setStatus(StateRunning, fmt.Sprintf("Backfilling %s · page %d", p.Name, page))
	sp, err := w.client.Scores(ctx, p.ID, page)
	if err != nil {
		return w.clientError(ctx, p, err, false)
	}
	if _, err := w.svc.UpsertScores(ctx, p.ID, sp.Data); err != nil {
		return err
	}
	total := sp.Metadata.TotalPages
	if len(sp.Data) == 0 || page >= total {
		if err := w.svc.SetBackfill(ctx, p.ID, model.BackfillDone, page+1, total); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{
			Level: model.LevelInfo, Kind: model.KindBackfill, PlayerID: service.Ptr(p.ID),
			Message: fmt.Sprintf("backfill listing complete (%d pages)", total),
		})
		return nil
	}
	return w.svc.SetBackfill(ctx, p.ID, model.BackfillRunning, page+1, total)
}

// clientError classifies a ScoreSaber error during poll (polling=true) or backfill.
func (w *Worker) clientError(ctx context.Context, p *model.Player, err error, polling bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	pid := service.Ptr(p.ID)
	switch {
	case errors.Is(err, scoresaber.ErrRateLimited):
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindRateLimit, PlayerID: pid, Message: "rate limited by ScoreSaber; waiting for the limit to reset"})
		return nil
	case errors.Is(err, scoresaber.ErrNotFound):
		const msg = "player not found on ScoreSaber; tracking disabled"
		if err := w.svc.MarkPlayerError(ctx, p.ID, msg, true); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindPoll, PlayerID: pid, Message: msg})
		return nil
	}
	msg := err.Error()
	if polling {
		if err := w.svc.MarkPolled(ctx, p.ID); err != nil {
			return err
		}
		if err := w.svc.MarkPlayerError(ctx, p.ID, msg, false); err != nil {
			return err
		}
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindPoll, PlayerID: pid, Message: "poll failed: " + msg})
		return nil
	}
	if err := w.svc.DeferBackfill(ctx, p.ID, w.svc.Now().Add(service.BackfillRetryDelay), msg); err != nil {
		return err
	}
	w.svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindBackfill, PlayerID: pid, Message: "backfill page failed, retrying in 5m: " + msg})
	return nil
}
```

- [x] **Step 7: Run tests**

Run: `go test -race ./internal/archiver/...`
Expected: `ok`.

- [x] **Step 8: Commit**

```bash
make lint
git add internal/archiver
git commit -m "feat: add archiver worker with polling and backfill"
```

---

## Task 9: Archiver — replay download, reconciliation, status

**Files:**
- Create: `internal/archiver/download.go`, `internal/archiver/download_test.go`, `internal/service/reconcile.go`, `internal/service/reconcile_test.go`
- Modify: `internal/archiver/worker.go` (`Step` replay tiers, `Run` reconciliation)

**Interfaces:**
- Consumes: Task 5 `storage.ErrWrite`, `storage.HashFile`, `(*Store).Scan/Put`; Task 6 `NextReplay`, `MarkReplayArchived`, `MarkReplayGone`, `MarkReplayAttemptFailed`, `TierNew`, `TierBackfill`; Task 7 `SetWorkerPaused`.
- Produces:
  - `(*service.Service).ReconcileStorage(ctx) (ReconcileResult, error)`; `type ReconcileResult struct{ RemovedTmp, Adopted, Orphans, Requeued int }` with `Changed() bool`
  - Worker `Step` order becomes: poll → new replay → backfill page → old replay.

Reconciliation rules: delete stray `*.tmp`; a `.dat` whose score row exists for the same player but isn't `archived` is hashed and adopted; a `.dat` with no matching row is **left in place** and counted as an orphan (it may belong to a player deleted with "keep files"); an `archived` row with no file goes back to `pending` (attempts 0).

Download rules: success → `MarkReplayArchived` + `info`/`replay` event; `storage.ErrWrite` → `SetWorkerPaused(true)` + `error`/`worker` event, no attempt counted; ctx cancelled → return ctx error, no attempt; `ErrRateLimited` → `warn` event, no attempt; `ErrNotFound` → `MarkReplayGone` + `warn` event; anything else (incl. mid-stream source errors and empty bodies) → `MarkReplayAttemptFailed` + `warn` (or `error` when giving up).

- [x] **Step 1: Write the failing reconcile test** — `internal/service/reconcile_test.go`

```go
package service_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestReconcileStorage(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	mustAdd(t, svc, "1001")
	upsert(t, svc, "1001", clk, []scoreItem{{1, true}, {2, true}, {3, true}})
	st := svc.Store()
	if _, _, err := st.Put("1001", 1, strings.NewReader("adopt me")); err != nil { // file, row pending
		t.Fatal(err)
	}
	if err := svc.MarkReplayArchived(ctx, 2, 5, "x"); err != nil { // row archived, no file
		t.Fatal(err)
	}
	if _, _, err := st.Put("1001", 999, strings.NewReader("orphan")); err != nil { // file, no row
		t.Fatal(err)
	}
	if err := os.WriteFile(st.Path("1001", 3)+".tmp", []byte("partial"), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ReconcileStorage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := service.ReconcileResult{RemovedTmp: 1, Adopted: 1, Orphans: 1, Requeued: 1}
	if res != want || !res.Changed() {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	s1, _ := svc.GetScore(ctx, 1)
	s2, _ := svc.GetScore(ctx, 2)
	if s1.ReplayState != model.ReplayArchived || s1.ReplaySize != int64(len("adopt me")) {
		t.Fatalf("score 1 not adopted: %+v", s1)
	}
	if s2.ReplayState != model.ReplayPending || s2.ReplaySHA256 != "" || s2.ArchivedAt != nil {
		t.Fatalf("score 2 not requeued: %+v", s2)
	}
	if _, err := os.Stat(st.Path("1001", 999)); err != nil {
		t.Fatal("orphan files must be kept")
	}
	again, _ := svc.ReconcileStorage(ctx)
	if again.Changed() && again != (service.ReconcileResult{Orphans: 1}) {
		t.Fatalf("second run should only report the orphan: %+v", again)
	}
}
```

- [x] **Step 2: Write the failing download tests** — `internal/archiver/download_test.go`

```go
package archiver_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

// ready adds Alice with the given scores already listed and backfill done,
// so the next Step goes straight to replay downloads.
func ready(t *testing.T, e *env, items []scoresaber.ScoreItem) {
	t.Helper()
	ctx := context.Background()
	e.add(t, "1001")
	if _, err := e.svc.UpsertScores(ctx, "1001", items); err != nil {
		t.Fatal(err)
	}
	_ = e.svc.MarkPolled(ctx, "1001")
	_ = e.svc.SetBackfill(ctx, "1001", model.BackfillDone, 2, 1)
	e.fc.scores["1001"] = items
}

func TestDownloadArchives(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	if !e.step(t) {
		t.Fatal("expected a download")
	}
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayArchived || s.ReplaySize != int64(len("replay-1")) || s.ReplaySHA256 == "" {
		t.Fatalf("score = %+v", s)
	}
	b, err := os.ReadFile(e.svc.Store().Path("1001", 1))
	if err != nil || string(b) != "replay-1" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if st := e.w.Status(); st.State != archiver.StateRunning || !strings.Contains(st.Task, "Downloading replay 1") {
		t.Fatalf("status = %+v", st)
	}
}

func TestDownload404MarksGone(t *testing.T) {
	e := newEnv(t)
	items := e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute))
	delete(e.fc.replays, 1)
	ready(t, e, items)
	e.step(t)
	s, _ := e.svc.GetScore(context.Background(), 1)
	if s.ReplayState != model.ReplayGone {
		t.Fatalf("state = %s", s.ReplayState)
	}
}

func TestDownloadTransientErrorsBackOffThenFail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	e.fc.replayErr[1] = &scoresaber.StatusError{StatusCode: 502}
	for i := 1; i <= service.MaxReplayAttempts; i++ {
		if !e.step(t) {
			t.Fatalf("attempt %d: no work found", i)
		}
		if i < service.MaxReplayAttempts && e.step(t) {
			t.Fatalf("attempt %d: retried before backoff elapsed", i)
		}
		e.clk.Advance(service.Backoff(i))
		_ = e.svc.MarkPolled(ctx, "1001") // keep the poll from becoming due while time advances
	}
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayFailed || s.Attempts != service.MaxReplayAttempts {
		t.Fatalf("score = %+v", s)
	}
}

func TestDownloadMidStreamFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	e.fc.replayReader[1] = func() io.ReadCloser {
		return io.NopCloser(io.MultiReader(strings.NewReader("ScoreSaber Replay partial"), iotest.ErrReader(errors.New("connection reset by peer"))))
	}
	e.step(t)
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayPending || s.Attempts != 1 || !strings.Contains(s.LastError, "connection reset") {
		t.Fatalf("score = %+v", s)
	}
	dir := filepath.Dir(e.svc.Store().Path("1001", 1))
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("leftover files after failed download: %v", entries)
	}
}

func TestDownloadRateLimitedDoesNotCountAttempt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	e.fc.replayErr[1] = fmt.Errorf("%w: test", scoresaber.ErrRateLimited)
	e.step(t)
	s, _ := e.svc.GetScore(ctx, 1)
	if s.ReplayState != model.ReplayPending || s.Attempts != 0 || s.NextAttemptAt != nil {
		t.Fatalf("score = %+v", s)
	}
}

func TestStorageErrorPausesWorker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	e := newEnv(t)
	ctx := context.Background()
	ready(t, e, e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute)))
	root := filepath.Dir(filepath.Dir(e.svc.Store().Path("1001", 1)))
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })
	e.step(t)
	st, _ := e.svc.Settings(ctx)
	s, _ := e.svc.GetScore(ctx, 1)
	if !st.WorkerPaused || s.Attempts != 0 || s.ReplayState != model.ReplayPending {
		t.Fatalf("paused=%v score=%+v", st.WorkerPaused, s)
	}
}

func TestStepPriority(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "1001")
	e.fc.perPage = 1
	newer := e.fc.history("1001", 1, 1, testutil.T0.Add(time.Minute))    // after AddedAt → TierNew
	older := e.fc.history("1001", 2, 5, testutil.T0.Add(-time.Hour))     // before AddedAt → TierBackfill; 6 pages in total
	e.fc.scores["1001"] = append(newer, older...)

	e.step(t) // 1. poll (pages 1..5, backfill handed over at page 6)
	if got := e.fc.calls(); len(got) != 5 {
		t.Fatalf("step 1 should poll 5 pages, calls = %v", got)
	}
	e.fc.reset()
	e.step(t) // 2. new replay
	if got := e.fc.replaysCalled(); !slices.Equal(got, []int64{1}) {
		t.Fatalf("step 2 should download the new replay, got %v", got)
	}
	e.step(t) // 3. backfill listing (last page → backfill done)
	if got := e.fc.calls(); !slices.Equal(got, []string{"1001:6"}) {
		t.Fatalf("step 3 should list backfill page 6, got %v", got)
	}
	e.step(t) // 4. old replay (newest old first)
	if got := e.fc.replaysCalled(); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("step 4 should download replay 2, got %v", got)
	}
	_ = e.svc.RequestPoll(ctx, "1001")
	e.fc.reset()
	e.step(t) // 5. a due poll beats everything
	if got := e.fc.calls(); len(got) == 0 || got[0] != "1001:1" {
		t.Fatalf("step 5 should poll, got %v", got)
	}
}

type fakeLimiter struct{ snap scoresaber.LimiterSnapshot }

func (f fakeLimiter) Snapshot() scoresaber.LimiterSnapshot { return f.snap }

func TestStatusReportsRateLimited(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	w := archiver.New(svc, newFake(), fakeLimiter{snap: scoresaber.LimiterSnapshot{Waiting: true}})
	if st := w.Status(); st.State != archiver.StateIdle || !st.Limiter.Waiting {
		t.Fatalf("idle worker = %+v", st)
	}
}
```

- [x] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/service/... ./internal/archiver/...`
Expected: FAIL — `undefined: service.ReconcileResult`; download tests fail because `Step` never downloads.

- [x] **Step 4: Implement reconciliation** — `internal/service/reconcile.go`

```go
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

type ReconcileResult struct {
	RemovedTmp, Adopted, Orphans, Requeued int
}

func (r ReconcileResult) Changed() bool { return r != ReconcileResult{} }

// ReconcileStorage aligns replay files with score rows (spec §6.3). Orphan
// files are kept: they may belong to a player deleted with "keep files".
func (s *Service) ReconcileStorage(ctx context.Context) (ReconcileResult, error) {
	var res ReconcileResult
	entries, removed, err := s.store.Scan()
	if err != nil {
		return res, err
	}
	res.RemovedTmp = removed
	onDisk := make(map[int64]storage.Entry, len(entries))
	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		onDisk[e.ScoreID] = e
		ids = append(ids, e.ScoreID)
	}
	q := s.q.Score
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		rows, err := q.WithContext(ctx).Where(q.ID.In(chunk...)).Find()
		if err != nil {
			return res, fmt.Errorf("service: reconcile load: %w", err)
		}
		byID := make(map[int64]*model.Score, len(rows))
		for _, r := range rows {
			byID[r.ID] = r
		}
		for _, id := range chunk {
			e := onDisk[id]
			sc, ok := byID[id]
			if !ok || sc.PlayerID != e.PlayerID {
				res.Orphans++
				continue
			}
			if sc.ReplayState == model.ReplayArchived {
				continue
			}
			size, sum, err := storage.HashFile(e.Path)
			if err != nil {
				slog.Warn("reconcile: hash failed", "path", e.Path, "err", err)
				continue
			}
			if err := s.MarkReplayArchived(ctx, id, size, sum); err != nil {
				return res, err
			}
			res.Adopted++
		}
	}
	var archived []int64
	if err := q.WithContext(ctx).Where(q.ReplayState.Eq(model.ReplayArchived)).Pluck(q.ID, &archived); err != nil {
		return res, fmt.Errorf("service: reconcile archived: %w", err)
	}
	for _, id := range archived {
		if _, ok := onDisk[id]; ok {
			continue
		}
		if err := s.updateScore(ctx, id, map[string]any{
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

- [x] **Step 5: Implement downloads** — `internal/archiver/download.go`

```go
package archiver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

func describe(sc *model.Score) string {
	player, song := sc.PlayerID, "?"
	if sc.Player != nil {
		player = sc.Player.Name
	}
	if sc.Leaderboard != nil {
		song = sc.Leaderboard.SongName
	}
	return fmt.Sprintf("Downloading replay %d · %s · %s", sc.ID, player, song)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (w *Worker) download(ctx context.Context, sc *model.Score) error {
	w.setStatus(StateRunning, describe(sc))
	ev := func(level, msg string) {
		w.svc.Log(ctx, model.SyncEvent{Level: level, Kind: model.KindReplay, PlayerID: service.Ptr(sc.PlayerID), ScoreID: service.Ptr(sc.ID), Message: msg})
	}
	body, err := w.client.Replay(ctx, sc.ID)
	if err == nil {
		size, sum, perr := w.svc.Store().Put(sc.PlayerID, sc.ID, body)
		_ = body.Close()
		if perr == nil {
			if err := w.svc.MarkReplayArchived(ctx, sc.ID, size, sum); err != nil {
				return err
			}
			ev(model.LevelInfo, "archived replay ("+humanBytes(size)+")")
			return nil
		}
		if errors.Is(perr, storage.ErrWrite) {
			if err := w.svc.SetWorkerPaused(ctx, true); err != nil {
				return err
			}
			w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindWorker, Message: "storage error, worker paused: " + perr.Error()})
			return nil
		}
		err = perr
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, scoresaber.ErrRateLimited):
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelWarn, Kind: model.KindRateLimit, Message: "rate limited by ScoreSaber; waiting for the limit to reset"})
		return nil
	case errors.Is(err, scoresaber.ErrNotFound):
		if err := w.svc.MarkReplayGone(ctx, sc.ID); err != nil {
			return err
		}
		ev(model.LevelWarn, "replay no longer available on ScoreSaber")
		return nil
	}
	gaveUp, next, merr := w.svc.MarkReplayAttemptFailed(ctx, sc.ID, err)
	if merr != nil {
		return merr
	}
	if gaveUp {
		ev(model.LevelError, fmt.Sprintf("giving up after %d attempts: %v", service.MaxReplayAttempts, err))
	} else {
		ev(model.LevelWarn, fmt.Sprintf("download failed (%v); retrying at %s", err, next.Format(time.RFC3339)))
	}
	return nil
}
```

- [x] **Step 6: Wire replay tiers and reconciliation into the worker** — in `internal/archiver/worker.go`:

Replace the part of `Step` after the poll block with:

```go
	if sc, err := w.svc.NextReplay(ctx, service.TierNew, w.lastReplayPlayer); err != nil {
		return false, err
	} else if sc != nil {
		w.lastReplayPlayer = sc.PlayerID
		return true, w.download(ctx, sc)
	}
	bp, err := w.svc.NextBackfillPlayer(ctx, w.lastBackfillPlayer)
	if err != nil {
		return false, err
	}
	if bp != nil {
		w.lastBackfillPlayer = bp.ID
		return true, w.backfill(ctx, bp)
	}
	if sc, err := w.svc.NextReplay(ctx, service.TierBackfill, w.lastReplayPlayer); err != nil {
		return false, err
	} else if sc != nil {
		w.lastReplayPlayer = sc.PlayerID
		return true, w.download(ctx, sc)
	}
	return false, nil
```

and at the very top of `Run`, before the loop:

```go
	if res, err := w.svc.ReconcileStorage(ctx); err != nil {
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindWorker, Message: "storage reconciliation failed: " + err.Error()})
	} else if res.Changed() {
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, Message: fmt.Sprintf(
			"storage reconciled: %d adopted, %d re-queued, %d temp files removed, %d orphan files kept", res.Adopted, res.Requeued, res.RemovedTmp, res.Orphans)})
	}
```

- [x] **Step 7: Run tests**

Run: `go test -race ./internal/service/... ./internal/archiver/...`
Expected: `ok` (Task 8 tests still pass: downloads for fake replays succeed and are simply extra work drained by `drain`).

- [x] **Step 8: Commit**

```bash
make lint
git add internal/archiver internal/service
git commit -m "feat: download replays with backoff and reconcile storage on startup"
```

---

## Task 10: Embedded ArcViewer

**Files:**
- Create: `internal/viewer/viewer.go`, `internal/viewer/handler.go`, `internal/viewer/manifest.txt`, `internal/viewer/fetch/main.go`, `internal/viewer/dist/PLACEHOLDER`, `internal/viewer/viewer_test.go`, `internal/viewer/handler_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - Constants `viewer.DeploySHA`, `viewer.Version`, `viewer.SourceURL`, `viewer.LicenseURL`, `viewer.RawBaseURL`, `viewer.CSP`, `viewer.PlaceholderText`; var `viewer.Manifest string`
  - `viewer.ParseManifest(string) ([]ManifestEntry, error)`; `type ManifestEntry struct{ SHA256, Commit, Path string }`
  - `viewer.Bundle() fs.FS` (embedded `dist/` subtree), `viewer.Available(fs.FS) bool`
  - `viewer.NewHandler(fsys fs.FS, version string) *Handler` (an `http.Handler` mounted at `/viewer/`), `(*Handler).Available() bool`
  - Files in the bundle are stored as `<path>.gz` (gzip best compression). `index.html.gz` presence = bundle available.

- [x] **Step 1: Add the manifest** — `internal/viewer/manifest.txt` (exact content; values verified 2026-10-08)

```text
# ArcViewer (https://github.com/AllPoland/ArcViewer, GPL-3.0) WebGL build.
# Format: <sha256> <commit> <path>. Files are fetched from
# https://raw.githubusercontent.com/AllPoland/ArcViewer/<commit>/<path>
# and stored gzip'd in dist/ by `go generate ./internal/viewer`.
# To update: pick a new `deploy` branch commit, recompute every sha256, bump DeploySHA/Version in viewer.go.
ecc3e170e358eab52b778685848c8ea6a1c813b6ce6bae03d67ba1a07aae8400 c776256497b66f7c91a74162cfcd943b0f45ee2e index.html
519fa75d48f8b62c05eab2e1d040974387680b13227dc1b9834c4ab1274e591c c776256497b66f7c91a74162cfcd943b0f45ee2e Build/ArcViewer.data
388c046b76ea0d6545f304bfb8eb539079b52e991f6039c05d2f2a2d97cd8324 c776256497b66f7c91a74162cfcd943b0f45ee2e Build/ArcViewer.framework.js
2769eb0812302c6b034f7cacadf203f3d69469ab7fba7bfc1aae29953666fcbb c776256497b66f7c91a74162cfcd943b0f45ee2e Build/ArcViewer.loader.js
e2a52b226c41db8301c17ca1bb9128cb819228afb6369fdcf18d841de1e49092 c776256497b66f7c91a74162cfcd943b0f45ee2e Build/ArcViewer.wasm
fcc75a61401fbb61242039f0521d8a6e4c679d76096938e2701ce31c89f38b59 c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/BadHitsounds/BloopBadHitsound.wav
29d860968adb745c6a3f3a04f1694d25e6f53e3a777b97eb7064adbe61b926cf c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/BadHitsounds/FunkyBadHitsound.wav
e3a76dd6c0581e95c298052b6c8f96f75528982926880f21a8465e9500283ab5 c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/BadHitsounds/RecordScratchBadHitsound.wav
b0a5bf289b48f5856330100419c3153f58701f67874b7b9b9cc7a1de0dd499fd c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/BadHitsounds/VineBoomBadHitsound.wav
2ef0501909a2a36fe8d3adae6f5d3cb7d162476f95b9ae13172277d7ed70b55f c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/Hitsounds/ChromapperTick.wav
2749bd9e9897b0c90d78aa03e3bafa068535713377d56f9cea7c984a3171d09f c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/Hitsounds/GalxHitsound.wav
7ac115a8ecb425237e465a8d0b54d87f0cda616ab050945bfeae2d8e6560f3bb c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/Hitsounds/OsuHitsound.wav
e7dc5a0bbf219cab753c7159c735d3ceacc1cf6a6e50e09fb74f4d8512a32ec7 c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/Hitsounds/RabbitViewerTick.wav
6a509b2ea812ff1e2192efcca660e492e1338f5e16f98415873d7a1ff6d5b326 c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/SFX/Hitsounds/ThumpyHitsound.wav
58699f2322ae5a09cde8787a062814b1babe0616c9e4a34cd70c8c6ca9ddcb32 c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/Scripts/oggdecode.js
625497c0207e60f45fee94b5693306dc55b9a2b0882b87d84b773601bf57fd4f c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/favicon.ico
bbee7131afe8a3365906240d89184dc86234c119467f390bc4bc6802328fdb4d c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/progress-bar-empty-dark.png
3306a6244dcb3926fca38a28e3ced589df8ff1beed955eb17c0bbf01c918bc62 c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/progress-bar-full-dark.png
aee49204962fe909ab71efc30250a023d668f36900adee7a259670c2136eb22d c776256497b66f7c91a74162cfcd943b0f45ee2e TemplateData/style.css
3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986 a7b2d984f91346ade9f25e523afb5ed67cb2cb84 LICENSE
```

`internal/viewer/dist/PLACEHOLDER` (exact content, one line + newline):

```text
The ArcViewer bundle is generated here by `go generate ./internal/viewer`.
```

- [x] **Step 2: Write the failing tests** — `internal/viewer/viewer_test.go`

```go
package viewer_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/viewer"
)

func TestEmbeddedManifest(t *testing.T) {
	entries, err := viewer.ParseManifest(viewer.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 {
		t.Fatalf("entries = %d, want 20", len(entries))
	}
	seen := map[string]string{}
	for _, e := range entries {
		seen[e.Path] = e.Commit
	}
	if seen["index.html"] != viewer.DeploySHA || seen["Build/ArcViewer.wasm"] != viewer.DeploySHA {
		t.Fatal("build files must be pinned to DeploySHA")
	}
	if seen["LICENSE"] != "a7b2d984f91346ade9f25e523afb5ed67cb2cb84" {
		t.Fatal("LICENSE must be pinned")
	}
}

func TestParseManifestRejects(t *testing.T) {
	bad := []string{
		"",
		"# only comments\n",
		"abc c776256497b66f7c91a74162cfcd943b0f45ee2e index.html",
		strings.Repeat("a", 64) + " short index.html",
		strings.Repeat("a", 64) + " c776256497b66f7c91a74162cfcd943b0f45ee2e ../etc/passwd",
		strings.Repeat("a", 64) + " c776256497b66f7c91a74162cfcd943b0f45ee2e",
	}
	for _, in := range bad {
		if _, err := viewer.ParseManifest(in); err == nil {
			t.Errorf("ParseManifest(%q) accepted", in)
		}
	}
}

func TestPlaceholderMatchesCommittedFile(t *testing.T) {
	b, err := fs.ReadFile(viewer.Bundle(), "PLACEHOLDER")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != viewer.PlaceholderText {
		t.Fatalf("PLACEHOLDER content %q != PlaceholderText", b)
	}
}
```

`internal/viewer/handler_test.go`:

```go
package viewer_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/yyewolf/ssarchiver/internal/viewer"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return buf.Bytes()
}

func bundle(t *testing.T) fstest.MapFS {
	return fstest.MapFS{
		"index.html.gz":           {Data: gz(t, "<html>viewer</html>")},
		"Build/ArcViewer.wasm.gz": {Data: gz(t, "\x00asm-binary")},
		"LICENSE.gz":              {Data: gz(t, "GNU GENERAL PUBLIC LICENSE")},
	}
}

func get(h http.Handler, path, acceptEncoding string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesGzipWhenAccepted(t *testing.T) {
	fsys := bundle(t)
	h := viewer.NewHandler(fsys, "abc123")
	rec := get(h, "/viewer/Build/ArcViewer.wasm", "br, gzip;q=0.8")
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Type") != "application/wasm" {
		t.Fatalf("code=%d headers=%v", rec.Code, rec.Header())
	}
	if !bytes.Equal(rec.Body.Bytes(), fsys["Build/ArcViewer.wasm.gz"].Data) {
		t.Fatal("gzip body must be the stored bytes")
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors *") {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestDecompressesWhenGzipNotAccepted(t *testing.T) {
	h := viewer.NewHandler(bundle(t), "abc123")
	rec := get(h, "/viewer/", "identity")
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" || string(body) != "<html>viewer</html>" {
		t.Fatalf("code=%d enc=%q body=%q", rec.Code, rec.Header().Get("Content-Encoding"), body)
	}
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

func TestConditionalRequest(t *testing.T) {
	h := viewer.NewHandler(bundle(t), "abc123")
	first := get(h, "/viewer/LICENSE", "gzip")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	second := get(h, "/viewer/LICENSE", "gzip", "If-None-Match", etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("code = %d, want 304", second.Code)
	}
	plain := get(h, "/viewer/LICENSE", "", "If-None-Match", etag)
	if plain.Code != 200 {
		t.Fatal("gzip and identity representations must have different ETags")
	}
}

func TestNotFoundAndUnavailable(t *testing.T) {
	h := viewer.NewHandler(bundle(t), "abc123")
	if rec := get(h, "/viewer/nope.js", "gzip"); rec.Code != 404 {
		t.Fatalf("missing file code = %d", rec.Code)
	}
	empty := viewer.NewHandler(fstest.MapFS{"PLACEHOLDER": {Data: []byte("x")}}, "abc123")
	if empty.Available() {
		t.Fatal("bundle without index.html.gz must be unavailable")
	}
	rec := get(empty, "/viewer/", "gzip")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "go generate ./internal/viewer") {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHeadHasNoBody(t *testing.T) {
	h := viewer.NewHandler(bundle(t), "abc123")
	req := httptest.NewRequest(http.MethodHead, "/viewer/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD code=%d body=%d bytes", rec.Code, rec.Body.Len())
	}
}
```

- [x] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/viewer/...`
Expected: FAIL — package has no Go files / undefined symbols.

- [x] **Step 4: Implement** — `internal/viewer/viewer.go`

```go
// Package viewer embeds a pinned build of ArcViewer (© AllPoland, GPL-3.0,
// https://github.com/AllPoland/ArcViewer) and serves it. ArcViewer is
// distributed as a separate, unmodified work alongside SSArchiver.
package viewer

//go:generate go run ./fetch

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

const (
	DeploySHA  = "c776256497b66f7c91a74162cfcd943b0f45ee2e"
	Version    = "0.8.1-beta"
	SourceURL  = "https://github.com/AllPoland/ArcViewer/tree/" + DeploySHA
	LicenseURL = "/viewer/LICENSE"
	RawBaseURL = "https://raw.githubusercontent.com/AllPoland/ArcViewer/"

	// CSP for the viewer pages: Unity needs inline scripts and wasm; maps come
	// from BeatSaver hosts that are not enumerable (spec §7.3).
	CSP = "default-src 'self'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; " +
		"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; media-src 'self' data: blob: https:; " +
		"connect-src 'self' https:; worker-src 'self' blob:; frame-ancestors *"

	PlaceholderText = "The ArcViewer bundle is generated here by `go generate ./internal/viewer`.\n"
)

//go:embed manifest.txt
var Manifest string

//go:embed all:dist
var dist embed.FS

type ManifestEntry struct {
	SHA256 string
	Commit string
	Path   string
}

func ParseManifest(s string) ([]ManifestEntry, error) {
	var out []ManifestEntry
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 || len(f[0]) != 64 || len(f[1]) != 40 || !fs.ValidPath(f[2]) {
			return nil, fmt.Errorf("viewer: manifest line %d is invalid: %q", i+1, line)
		}
		out = append(out, ManifestEntry{SHA256: f[0], Commit: f[1], Path: f[2]})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("viewer: manifest is empty")
	}
	return out, nil
}

// Bundle is the embedded dist/ directory.
func Bundle() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Available reports whether the bundle has been generated.
func Available(fsys fs.FS) bool {
	_, err := fs.Stat(fsys, "index.html.gz")
	return err == nil
}
```

`internal/viewer/handler.go`:

```go
package viewer

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	fsys      fs.FS
	tag       string
	available bool
}

func NewHandler(fsys fs.FS, version string) *Handler {
	tag := version
	if len(tag) > 12 {
		tag = tag[:12]
	}
	return &Handler{fsys: fsys, tag: tag, available: Available(fsys)}
}

func (h *Handler) Available() bool { return h.available }

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".wasm": "application/wasm",
	".data": "application/octet-stream",
	".wav":  "audio/wav",
	".png":  "image/png",
	".ico":  "image/x-icon",
}

func contentType(name string) string {
	if name == "LICENSE" {
		return "text/plain; charset=utf-8"
	}
	if ct, ok := contentTypes[path.Ext(name)]; ok {
		return ct
	}
	return "application/octet-stream"
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "gzip" && name != "*" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.TrimSpace(k) == "q" {
				q, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
			}
		}
		if q > 0 {
			return true
		}
	}
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Security-Policy", CSP)
	if !h.available {
		http.Error(w, "viewer not bundled: run `go generate ./internal/viewer` and rebuild", http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/viewer/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	f, err := h.fsys.Open(name + ".gz")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "viewer file not seekable", http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", contentType(name))
	hd.Set("Vary", "Accept-Encoding")
	if name == "index.html" {
		hd.Set("Cache-Control", "no-cache")
	} else {
		hd.Set("Cache-Control", "public, max-age=86400")
	}
	etag := `"` + h.tag + "-" + name
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		hd.Set("Content-Encoding", "gzip")
		hd.Set("ETag", etag+`-gz"`)
		http.ServeContent(w, r, "", time.Time{}, rs)
		return
	}
	etag += `"`
	hd.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	zr, err := gzip.NewReader(rs)
	if err != nil {
		http.Error(w, "corrupt viewer file", http.StatusInternalServerError)
		return
	}
	defer zr.Close()
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, zr)
}
```

- [x] **Step 5: Run tests**

Run: `go test -race ./internal/viewer/...`
Expected: `ok`.

- [x] **Step 6: Implement the fetcher** — `internal/viewer/fetch/main.go`

```go
// Command fetch downloads the ArcViewer files listed in manifest.txt,
// verifies their sha256 and writes gzip'd copies to ./dist (run by
// `go generate ./internal/viewer`, cwd = internal/viewer).
package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/viewer"
)

func main() {
	if err := run(context.Background(), "dist"); err != nil {
		fmt.Fprintln(os.Stderr, "viewer fetch:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out string) error {
	entries, err := viewer.ParseManifest(viewer.Manifest)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(viewer.Manifest))
	stamp := hex.EncodeToString(sum[:])
	if b, err := os.ReadFile(filepath.Join(out, ".stamp")); err == nil && strings.TrimSpace(string(b)) == stamp {
		fmt.Println("viewer bundle up to date")
		return nil
	}
	tmp := out + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	hc := &http.Client{Timeout: 10 * time.Minute}
	for _, e := range entries {
		data, err := download(ctx, hc, viewer.RawBaseURL+e.Commit+"/"+e.Path)
		if err != nil {
			return err
		}
		got := sha256.Sum256(data)
		if hex.EncodeToString(got[:]) != e.SHA256 {
			return fmt.Errorf("checksum mismatch for %s: got %x", e.Path, got)
		}
		if err := writeGzip(filepath.Join(tmp, filepath.FromSlash(e.Path))+".gz", data); err != nil {
			return err
		}
		fmt.Printf("  %-60s %8d KiB\n", e.Path, len(data)/1024)
	}
	if err := os.WriteFile(filepath.Join(tmp, "PLACEHOLDER"), []byte(viewer.PlaceholderText), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".stamp"), []byte(stamp+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

func download(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

func writeGzip(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		_ = f.Close()
		return err
	}
	if _, err := zw.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
```

- [x] **Step 7: Generate the real bundle and verify**

```bash
go generate ./internal/viewer
du -sh internal/viewer/dist
ls internal/viewer/dist internal/viewer/dist/Build
git status --short internal/viewer   # only source files may show; dist/* is ignored except PLACEHOLDER
go generate ./internal/viewer        # second run prints "viewer bundle up to date"
```

Expected: `dist` ≈ 28 MB containing `index.html.gz`, `Build/ArcViewer.wasm.gz`, `LICENSE.gz`, `PLACEHOLDER`, `.stamp`; `git status` shows no `dist/` files other than (unchanged) `PLACEHOLDER`.

Update `Makefile`:

```make
.PHONY: viewer
viewer:
	go generate ./internal/viewer

build: viewer
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ssarchiver ./cmd/ssarchiver
```

(replace the existing `build` target; keep the others).

- [x] **Step 8: Commit**

```bash
make lint && go test ./internal/viewer/...
git add internal/viewer Makefile
git commit -m "feat: embed pinned ArcViewer build with gzip serving"
```

---
## Task 11: Web foundation — shadcn-templ, Tailwind, layout, middleware, home

**Files:**
- Create: `components.json`, `scripts/fetch-tailwind.sh`, `internal/web/assets/css/{globals,shadcn-tailwind,tw-animate}.css` (CLI), `internal/web/components/**` + `internal/web/utils/shadcn-templ.go` (CLI), `internal/web/static/static.go`, `internal/web/static/css/app.css` (generated), `internal/web/static/js/{htmx.min.js,theme.js,app.js}`, `internal/web/static/js/shadcn-templ-*.js` (CLI)
- Create: `internal/httpx/httpx.go`, `internal/httpx/httpx_test.go`
- Create: `internal/web/web.go`, `internal/web/pages.go`, `internal/web/views/page.go`, `internal/web/views/format.go`, `internal/web/views/format_test.go`, `internal/web/views/layout.templ`, `internal/web/views/home.templ`, `internal/web/views/common.templ`
- Create tests: `internal/web/env_test.go`, `internal/web/pages_test.go`
- Modify: `internal/testutil/fakes.go` (add `Gzip`), `Makefile`

**Interfaces:**
- Consumes: `service.*` (Tasks 6–7), `archiver.Status`/`State*` (Task 8), `viewer.Handler`, `viewer.SourceURL`, `viewer.LicenseURL` (Task 10), `config.Config` (Task 1), `buildinfo.Version`.
- Produces:
  - `httpx.SessionCookie = "ssa_session"`; `httpx.WithUser/UserFrom`, `httpx.WithBaseURL/BaseURLFrom`; `httpx.SecurityHeaders(http.Handler) http.Handler`; `httpx.BaseURL(configured string, trustProxy bool) func(http.Handler) http.Handler`; `httpx.Recover`, `httpx.Logging`; `httpx.ClientIP(r, trustProxy) string`; `httpx.IsHTTPS(r, baseURL string, trustProxy bool) bool`; `httpx.DefaultCSP(nonce) string`; consts `httpx.EmbedCSP`, `httpx.DocsCSP`
  - `static.URL(name string) string` (cache-busted `/static/<name>?v=<hash>`), `static.Handler() http.Handler`
  - `web.Deps{Service *service.Service; Status StatusSource; Viewer *viewer.Handler; Config config.Config}`, `web.StatusSource interface{ Status() archiver.Status }`, `web.New(Deps) *Handler`, `(*Handler).Routes(*http.ServeMux)`, `(*Handler).Middleware(http.Handler) http.Handler`
  - Internal helpers later web tasks use: `render(w, r, status, templ.Component)`, `(*Handler).page(r, title) views.Page`, `(*Handler).notFound(w, r, msg)`, `(*Handler).serverError(w, r, err)`, `isHTMX(r) bool`, `redirect(w, r, url)`, `parseID(string) (int64, bool)`, `(*Handler).toastOnly(w, r, toast.Type, title, desc string)`
  - `views.Page{Title, Instance, Path, Version string; User *model.User; OG *OpenGraph}`, `views.OpenGraph{Title, Description, Image, URL string}`, templ `views.Layout(Page)`, `views.Home(Page, []service.PlayerSummary)`, `views.NotFound(Page, msg string)`, `views.ServerError(Page)`, `views.ToastOOB(toast.Type, title, desc string)`, `views.FormError(msg string)`
  - Format helpers (`views/format.go`): `Number[T ~int|~int64](T) string`, `Percent(float64) string`, `PP(float64) string`, `DifficultyName(int) string`, `TimeAgo(t, now time.Time) string`, `Until(t, now time.Time) string`, `HumanDuration(time.Duration) string`, `HumanBytes(int64) string`, `Initials(string) string`, `ScoreURL(int64) string`, `ReplayPath(int64) string`, `EmbedPath(int64) string`
  - `testutil.Gzip(s string) []byte`

- [x] **Step 1: Install shadcn-templ into the repo** (verified flow for v2.0.0-beta.13 — `init` always writes root-level aliases, so we rewrite `components.json` afterwards):

```bash
ST="go run github.com/axadrn/shadcn-templ/v2/cmd/shadcn-templ@v2.0.0-beta.13"
mkdir -p internal/web/assets/css
$ST init --css internal/web/assets/css/globals.css --base-color neutral --preset nova --silent </dev/null
rm -rf utils
```

Overwrite `components.json` with exactly:

```json
{
  "$schema": "https://shadcn-templ.com/schema/components.json",
  "style": "base-nova",
  "tailwind": {
    "css": "internal/web/assets/css/globals.css",
    "baseColor": "neutral",
    "cssVariables": true
  },
  "scripts": {
    "dir": "internal/web/static/js",
    "path": "/static/js"
  },
  "rtl": false,
  "iconLibrary": "lucide",
  "menuColor": "default",
  "menuAccent": "subtle",
  "aliases": {
    "components": "github.com/yyewolf/ssarchiver/internal/web/components",
    "utils": "github.com/yyewolf/ssarchiver/internal/web/utils"
  }
}
```

Then add the components (always redirect stdin from `/dev/null`; the CLI may otherwise wait on a prompt):

```bash
$ST add utils button card table badge avatar field label input textarea nativeselect progress separator empty alert toast skeleton --silent </dev/null
go get github.com/a-h/templ@v0.3.1070
go get -tool github.com/a-h/templ/cmd/templ@v0.3.1070
go tool templ generate
go mod tidy
go build ./internal/web/...
grep -n bundleSrc internal/web/components/scripts_bundle.go
```

Expected: `internal/web/components/{button,card,…,icon,toast}/` exist, `internal/web/utils/shadcn-templ.go` exists, `bundleSrc = "/static/js/shadcn-templ-<hash>.js"`, the JS bundle is in `internal/web/static/js/`, and the build succeeds. If any import still says `github.com/yyewolf/ssarchiver/components`, the aliases were not picked up: re-check `components.json` and rerun `add … --overwrite`.

- [x] **Step 2: Configure Tailwind input** — edit `internal/web/assets/css/globals.css`: replace the first line `@import "tailwindcss";` with the two lines below (disables auto source detection — otherwise class names in `docs/` would bloat the CSS — and scans only `internal/web`):

```css
@import "tailwindcss" source(none);
@source "../../";
```

Leave the rest of the generated file (theme tokens, `.dark` block, base layer) untouched. Tailwind's default `font-sans` is the system font stack, which is what we want.

Create `scripts/fetch-tailwind.sh` (and `chmod +x` it):

```sh
#!/bin/sh
# Downloads the pinned Tailwind CSS standalone CLI to ./bin/tailwindcss and verifies its checksum.
set -eu
VERSION="v4.3.3"
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)               ASSET=tailwindcss-linux-x64;   SUM=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
  Linux-aarch64|Linux-arm64)  ASSET=tailwindcss-linux-arm64; SUM=55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
  Darwin-arm64)               ASSET=tailwindcss-macos-arm64; SUM=cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d ;;
  Darwin-x86_64)              ASSET=tailwindcss-macos-x64;   SUM=7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1 ;;
  *) echo "fetch-tailwind: unsupported platform $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac
mkdir -p bin
curl -fsSL -o bin/tailwindcss.tmp "https://github.com/tailwindlabs/tailwindcss/releases/download/${VERSION}/${ASSET}"
if command -v sha256sum >/dev/null 2>&1; then
  echo "${SUM}  bin/tailwindcss.tmp" | sha256sum -c - >/dev/null
else
  echo "${SUM}  bin/tailwindcss.tmp" | shasum -a 256 -c - >/dev/null
fi
chmod +x bin/tailwindcss.tmp
mv bin/tailwindcss.tmp bin/tailwindcss
echo "tailwindcss ${VERSION} ready"
```

Update the `Makefile` generate section to:

```make
SHADCN := go run github.com/axadrn/shadcn-templ/v2/cmd/shadcn-templ@v2.0.0-beta.13

generate: gen-go gen-templ gen-css

gen-templ:
	go tool templ generate

gen-css: bin/tailwindcss
	./bin/tailwindcss -i internal/web/assets/css/globals.css -o internal/web/static/css/app.css --minify

bin/tailwindcss:
	./scripts/fetch-tailwind.sh
```

- [x] **Step 3: Vendor htmx and write the small scripts**

```bash
mkdir -p internal/web/static/js internal/web/static/css
curl -fsSL -o internal/web/static/js/htmx.min.js https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js
echo "4b2fd977b3ae23ae06329f0168486647c60413405200c0a10fb5e6f8d3700998  internal/web/static/js/htmx.min.js" | sha256sum -c -
```

Expected: `internal/web/static/js/htmx.min.js: OK`.

`internal/web/static/js/theme.js` (loaded synchronously in `<head>` to avoid a flash of the wrong theme):

```js
(function () {
  var stored = null;
  try { stored = localStorage.getItem("theme"); } catch (e) {}
  var dark = stored ? stored === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
  document.documentElement.classList.toggle("dark", dark);
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-theme-toggle]");
    if (!btn) return;
    var next = !document.documentElement.classList.contains("dark");
    document.documentElement.classList.toggle("dark", next);
    try { localStorage.setItem("theme", next ? "dark" : "light"); } catch (e) {}
  });
})();
```

`internal/web/static/js/app.js`:

```js
// Copy-to-clipboard: <button data-copy="#selector">; the button text becomes "Copied" briefly.
document.addEventListener("click", function (e) {
  var btn = e.target.closest("[data-copy]");
  if (!btn) return;
  var src = document.querySelector(btn.getAttribute("data-copy"));
  if (!src || !navigator.clipboard) return;
  navigator.clipboard.writeText(src.value || src.textContent).then(function () {
    var label = btn.querySelector("[data-copy-label]");
    if (!label) return;
    var prev = label.textContent;
    label.textContent = "Copied";
    setTimeout(function () { label.textContent = prev; }, 1500);
  });
});
```

`internal/web/static/static.go`:

```go
// Package static serves embedded CSS/JS with cache-busting URLs.
package static

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

//go:embed css js
var files embed.FS

var (
	hashOnce sync.Once
	hashes   map[string]string
)

func computeHashes() {
	hashes = map[string]string{}
	_ = fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		hashes[p] = hex.EncodeToString(sum[:])[:10]
		return nil
	})
}

// URL returns the public URL of an embedded file with a content hash query.
func URL(name string) string {
	hashOnce.Do(computeHashes)
	if h, ok := hashes[name]; ok {
		return "/static/" + name + "?v=" + h
	}
	return "/static/" + name
}

// Handler serves /static/*; versioned URLs are cached forever.
func Handler() http.Handler {
	fsrv := http.StripPrefix("/static/", http.FileServerFS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		fsrv.ServeHTTP(w, r)
	})
}
```

Run `make gen-css` once now so `internal/web/static/css/app.css` exists (the embed needs it). Expected: `bin/tailwindcss` downloaded, `app.css` written.

- [x] **Step 4: Write the failing httpx tests** — `internal/httpx/httpx_test.go`

```go
package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/httpx"
)

func TestSecurityHeadersNonce(t *testing.T) {
	var nonce string
	h := httpx.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce = templ.GetNonce(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if nonce == "" || !strings.Contains(csp, "'nonce-"+nonce+"'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("nonce=%q csp=%q", nonce, csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") == "" {
		t.Fatalf("headers = %v", rec.Header())
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Header().Get("Content-Security-Policy") == csp {
		t.Fatal("nonce must differ per request")
	}
}

func TestDocsCSP(t *testing.T) {
	h := httpx.SecurityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/docs", nil))
	if rec.Header().Get("Content-Security-Policy") != httpx.DocsCSP {
		t.Fatalf("docs csp = %q", rec.Header().Get("Content-Security-Policy"))
	}
}

func TestBaseURL(t *testing.T) {
	var got string
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = httpx.BaseURLFrom(r.Context()) })
	cases := []struct {
		configured string
		trust      bool
		hdr        map[string]string
		want       string
	}{
		{"https://replays.example.com", false, nil, "https://replays.example.com"},
		{"", false, map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "evil.com"}, "http://example.com"},
		{"", true, map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "proxy.example.com"}, "https://proxy.example.com"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		httpx.BaseURL(c.configured, c.trust)(capture).ServeHTTP(httptest.NewRecorder(), req)
		if got != c.want {
			t.Errorf("BaseURL(%q, %v) = %q, want %q", c.configured, c.trust, got, c.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if ip := httpx.ClientIP(req, false); ip != "10.0.0.1" {
		t.Fatalf("untrusted ip = %s", ip)
	}
	if ip := httpx.ClientIP(req, true); ip != "203.0.113.9" {
		t.Fatalf("trusted ip = %s", ip)
	}
}

func TestRecover(t *testing.T) {
	h := httpx.Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
}
```

- [x] **Step 5: Run to verify failure, then implement** — `go test ./internal/httpx/...` fails (`undefined`). Write `internal/httpx/httpx.go`:

```go
// Package httpx holds HTTP helpers shared by the web UI and the JSON API.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/model"
)

const SessionCookie = "ssa_session"

const (
	// EmbedCSP: the embed page only frames the same-origin viewer and may itself be framed anywhere.
	EmbedCSP = "default-src 'none'; style-src 'unsafe-inline'; frame-src 'self'; base-uri 'none'; frame-ancestors *"
	// DocsCSP: huma's docs page loads its renderer from unpkg.
	DocsCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' https://unpkg.com; style-src 'self' 'unsafe-inline' https://unpkg.com; " +
		"img-src 'self' data: https:; font-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'"
)

// DefaultCSP is the policy for every UI page.
func DefaultCSP(nonce string) string {
	return "default-src 'self'; script-src 'self' 'nonce-" + nonce + "'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https://cdn.scoresaber.com; frame-src 'self'; connect-src 'self'; " +
		"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

type ctxKey int

const (
	userKey ctxKey = iota
	baseURLKey
)

func WithUser(ctx context.Context, u *model.User) context.Context { return context.WithValue(ctx, userKey, u) }

func UserFrom(ctx context.Context) *model.User {
	u, _ := ctx.Value(userKey).(*model.User)
	return u
}

func WithBaseURL(ctx context.Context, u string) context.Context {
	return context.WithValue(ctx, baseURLKey, u)
}

func BaseURLFrom(ctx context.Context) string {
	u, _ := ctx.Value(baseURLKey).(string)
	return u
}

// SecurityHeaders sets baseline headers and a per-request CSP nonce, which
// templ components read via templ.GetNonce. Handlers may override the CSP.
func SecurityHeaders(next http.Handler) http.Handler {
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
			h.Set("Content-Security-Policy", DefaultCSP(nonce))
		}
		next.ServeHTTP(w, r.WithContext(templ.WithNonce(r.Context(), nonce)))
	})
}

// BaseURL stores the public base URL in the request context.
func BaseURL(configured string, trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			base := configured
			if base == "" {
				scheme, host := "http", r.Host
				if r.TLS != nil {
					scheme = "https"
				}
				if trustProxy {
					if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
						scheme = p
					}
					if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
						host = fh
					}
				}
				base = scheme + "://" + host
			}
			next.ServeHTTP(w, r.WithContext(WithBaseURL(r.Context(), base)))
		})
	}
}

func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func IsHTTPS(r *http.Request, baseURL string, trustProxy bool) bool {
	return r.TLS != nil || strings.HasPrefix(baseURL, "https://") ||
		(trustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic in handler", "path", r.URL.Path, "panic", v)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/viewer/") || r.URL.Path == "/healthz" {
			return
		}
		slog.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start))
	})
}
```

Run: `go test ./internal/httpx/...` → `ok`.

- [x] **Step 6: Write the views base** — `internal/web/views/page.go`

```go
// Package views holds the templ pages and partials.
package views

import "github.com/yyewolf/ssarchiver/internal/model"

type Page struct {
	Title    string
	Instance string
	Path     string
	Version  string
	User     *model.User
	OG       *OpenGraph
}

type OpenGraph struct {
	Title, Description, Image, URL string
}

func (p Page) FullTitle() string {
	if p.Title == "" {
		return p.Instance
	}
	return p.Title + " · " + p.Instance
}

const htmxConfig = `{"includeIndicatorStyles":false,"allowEval":false,"allowScriptTags":false,"historyCacheSize":0}`
```

`internal/web/views/format.go`:

```go
package views

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func Number[T ~int | ~int64](n T) string {
	s := strconv.FormatInt(int64(n), 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func Percent(f float64) string { return fmt.Sprintf("%.2f%%", f*100) }

func PP(f float64) string {
	if f <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fpp", f)
}

func DifficultyName(d int) string {
	switch d {
	case 1:
		return "Easy"
	case 3:
		return "Normal"
	case 5:
		return "Hard"
	case 7:
		return "Expert"
	case 9:
		return "Expert+"
	}
	return "Unknown"
}

func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(math.Max(0, d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func TimeAgo(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 10*time.Second {
		return "just now"
	}
	if d > 30*24*time.Hour {
		return t.Format("2 Jan 2006")
	}
	return HumanDuration(d) + " ago"
}

func Until(t, now time.Time) string {
	if !t.After(now) {
		return "due"
	}
	return "in " + HumanDuration(t.Sub(now))
}

func HumanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func Initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		r := []rune(w)
		out = append(out, unicode.ToUpper(r[0]))
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

func ScoreURL(id int64) string   { return "/s/" + strconv.FormatInt(id, 10) }
func ReplayPath(id int64) string { return "/r/" + strconv.FormatInt(id, 10) + ".dat" }
func EmbedPath(id int64) string  { return "/embed/" + strconv.FormatInt(id, 10) }
```

`internal/web/views/format_test.go`:

```go
package views

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	checks := map[string]string{
		Number(1098260):                         "1,098,260",
		Number(-1234):                           "-1,234",
		Number(12):                              "12",
		Percent(0.9728111):                      "97.28%",
		PP(0):                                   "—",
		PP(312.5):                               "312.50pp",
		DifficultyName(9):                       "Expert+",
		DifficultyName(4):                       "Unknown",
		TimeAgo(now.Add(-3*time.Minute), now):   "3m ago",
		TimeAgo(now.Add(-2*time.Second), now):   "just now",
		TimeAgo(now.Add(-40*24*time.Hour), now): "29 Aug 2026",
		TimeAgo(time.Time{}, now):               "never",
		Until(now.Add(90*time.Second), now):     "in 1m",
		Until(now.Add(-time.Second), now):       "due",
		HumanDuration(26 * time.Hour):           "26h 0m",
		HumanBytes(2814210):                     "2.7 MB",
		Initials("oermer"):                      "O",
		Initials("Ghost Rule Fan"):              "GR",
		Initials(""):                            "?",
		ScoreURL(42):                            "/s/42",
		ReplayPath(42):                          "/r/42.dat",
		EmbedPath(42):                           "/embed/42",
	}
	for got, want := range checks {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
```

- [x] **Step 7: Write the layout and shared partials** — `internal/web/views/layout.templ`

```templ
package views

import (
	"strings"

	"github.com/yyewolf/ssarchiver/internal/viewer"
	"github.com/yyewolf/ssarchiver/internal/web/components"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
	"github.com/yyewolf/ssarchiver/internal/web/static"
)

templ Layout(p Page) {
	<!DOCTYPE html>
	<html lang="en">
		<head>
			<meta charset="utf-8"/>
			<meta name="viewport" content="width=device-width, initial-scale=1"/>
			<meta name="htmx-config" content={ htmxConfig }/>
			<title>{ p.FullTitle() }</title>
			if p.OG != nil {
				<meta property="og:type" content="website"/>
				<meta property="og:site_name" content={ p.Instance }/>
				<meta property="og:title" content={ p.OG.Title }/>
				<meta property="og:description" content={ p.OG.Description }/>
				<meta property="og:url" content={ p.OG.URL }/>
				if p.OG.Image != "" {
					<meta property="og:image" content={ p.OG.Image }/>
				}
				<meta name="twitter:card" content="summary"/>
			}
			<link rel="stylesheet" href={ static.URL("css/app.css") }/>
			<script src={ static.URL("js/theme.js") }></script>
			<script defer src={ static.URL("js/htmx.min.js") }></script>
			<script defer src={ static.URL("js/app.js") }></script>
			@components.Scripts()
		</head>
		<body class="min-h-screen bg-background font-sans text-foreground antialiased">
			<div class="flex min-h-screen flex-col">
				@siteHeader(p)
				<main class="mx-auto w-full max-w-6xl flex-1 px-4 py-8">
					{ children... }
				</main>
				@siteFooter(p)
			</div>
			@toast.Toaster()
			<div id="toasts" class="hidden"></div>
		</body>
	</html>
}

func isActive(path, href string) bool {
	if href == "/" {
		return path == "/" || strings.HasPrefix(path, "/p/") || strings.HasPrefix(path, "/s/")
	}
	return path == href
}

templ navLink(p Page, href, label string) {
	<a
		href={ templ.SafeURL(href) }
		class={ "transition-colors hover:text-foreground", templ.KV("font-medium text-foreground", isActive(p.Path, href)) }
	>{ label }</a>
}

templ siteHeader(p Page) {
	<header class="border-b">
		<div class="mx-auto flex h-14 w-full max-w-6xl items-center gap-6 px-4">
			<a href="/" class="font-semibold tracking-tight">{ p.Instance }</a>
			<nav class="flex items-center gap-4 text-sm text-muted-foreground">
				@navLink(p, "/", "Players")
				if p.User != nil {
					@navLink(p, "/admin", "Manage")
					@navLink(p, "/admin/sync", "Sync")
					@navLink(p, "/admin/settings", "Settings")
				}
			</nav>
			<div class="ml-auto flex items-center gap-1">
				@button.Button(button.Props{Variant: button.VariantGhost, Size: button.SizeIconSm, Attributes: templ.Attributes{"data-theme-toggle": "", "aria-label": "Toggle theme"}}) {
					@icon.Sun(icon.Props{Class: "hidden dark:block"})
					@icon.Moon(icon.Props{Class: "dark:hidden"})
				}
				if p.User != nil {
					<form method="post" action="/logout">
						@button.Button(button.Props{Variant: button.VariantGhost, Size: button.SizeSm, Type: button.TypeSubmit}) {
							@icon.LogOut()
							Log out
						}
					</form>
				} else {
					@button.Button(button.Props{Variant: button.VariantGhost, Size: button.SizeSm, Href: "/login"}) {
						Log in
					}
				}
			</div>
		</div>
	</header>
}

templ siteFooter(p Page) {
	<footer class="border-t">
		<div class="mx-auto flex w-full max-w-6xl flex-wrap items-center justify-between gap-2 px-4 py-6 text-xs text-muted-foreground">
			<span>SSArchiver { p.Version } · replays belong to their players · not affiliated with ScoreSaber</span>
			<span>
				Viewer: <a class="underline underline-offset-4 hover:text-foreground" href={ templ.SafeURL(viewer.SourceURL) }>ArcViewer</a>
				(<a class="underline underline-offset-4 hover:text-foreground" href={ templ.SafeURL(viewer.LicenseURL) }>GPL-3.0</a>)
				· <a class="underline underline-offset-4 hover:text-foreground" href="/api/docs">API</a>
			</span>
		</div>
	</footer>
}
```

`internal/web/views/common.templ`:

```templ
package views

import (
	"github.com/yyewolf/ssarchiver/internal/web/components/alert"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/empty"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
)

// ToastOOB appends a toast via htmx out-of-band swap into #toasts.
templ ToastOOB(t toast.Type, title, desc string) {
	<div id="toasts" hx-swap-oob="beforeend">
		@toast.Toast(toast.Props{Type: t, Title: title, Description: desc})
	</div>
}

templ FormError(msg string) {
	if msg != "" {
		@alert.Alert(alert.Props{Variant: alert.VariantDestructive}) {
			@icon.CircleAlert()
			@alert.Description() {
				{ msg }
			}
		}
	}
}

templ NotFound(p Page, msg string) {
	@Layout(p) {
		@empty.Empty(empty.Props{Class: "border border-dashed"}) {
			@empty.Header() {
				@empty.Title() {
					Not found
				}
				@empty.Description() {
					{ msg }
				}
			}
			@empty.Content() {
				@button.Button(button.Props{Variant: button.VariantOutline, Href: "/"}) {
					Back to players
				}
			}
		}
	}
}

templ ServerError(p Page) {
	@Layout(p) {
		@empty.Empty(empty.Props{Class: "border border-dashed"}) {
			@empty.Header() {
				@empty.Title() {
					Something went wrong
				}
				@empty.Description() {
					The error was logged. Try again in a moment.
				}
			}
		}
	}
}
```

`internal/web/views/home.templ`:

```templ
package views

import (
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/avatar"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/card"
	"github.com/yyewolf/ssarchiver/internal/web/components/empty"
)

templ Home(p Page, players []service.PlayerSummary) {
	@Layout(p) {
		<div class="mb-8 flex flex-col gap-1">
			<h1 class="text-2xl font-semibold tracking-tight">Players</h1>
			<p class="text-sm text-muted-foreground">Archived ScoreSaber replays, free to download, watch and embed.</p>
		</div>
		if len(players) == 0 {
			@empty.Empty(empty.Props{Class: "border border-dashed"}) {
				@empty.Header() {
					@empty.Title() {
						No players yet
					}
					@empty.Description() {
						Players added by the admin will show up here.
					}
				}
				if p.User != nil {
					@empty.Content() {
						@button.Button(button.Props{Href: "/admin"}) {
							Add a player
						}
					}
				}
			}
		} else {
			<div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
				for _, pl := range players {
					@playerCard(pl)
				}
			</div>
		}
	}
}

templ playerCard(pl service.PlayerSummary) {
	<a href={ templ.SafeURL("/p/" + pl.ID) } class="group block rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring">
		@card.Card(card.Props{Class: "transition-colors group-hover:bg-muted/50"}) {
			@card.Header() {
				<div class="flex items-center gap-3">
					@avatar.Avatar() {
						@avatar.Image(avatar.ImageProps{Src: pl.AvatarURL, Alt: pl.Name})
						@avatar.Fallback() {
							{ Initials(pl.Name) }
						}
					}
					<div class="min-w-0">
						@card.Title(card.TitleProps{Class: "truncate"}) {
							{ pl.Name }
						}
						@card.Description() {
							{ pl.Country }
						}
					</div>
				</div>
			}
			@card.Content() {
				<div class="flex items-baseline justify-between text-sm">
					<span class="text-muted-foreground">Archived replays</span>
					<span class="font-medium tabular-nums">{ Number(pl.Counts.Archived) }</span>
				</div>
			}
		}
	</a>
}
```

- [x] **Step 8: Write the web handler core** — `internal/web/web.go`

```go
// Package web serves the HTML UI (templ + htmx).
package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/viewer"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
	"github.com/yyewolf/ssarchiver/internal/web/static"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

type StatusSource interface {
	Status() archiver.Status
}

type Deps struct {
	Service *service.Service
	Status  StatusSource
	Viewer  *viewer.Handler
	Config  config.Config
}

type Handler struct {
	svc       *service.Service
	status    StatusSource
	viewer    *viewer.Handler
	cfg       config.Config
	logins    *loginLimiter
	setupDone atomic.Bool
}

func New(d Deps) *Handler {
	return &Handler{svc: d.Service, status: d.Status, viewer: d.Viewer, cfg: d.Config, logins: newLoginLimiter()}
}

// Routes registers every UI route. Later tasks add lines here.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /static/", static.Handler())
	mux.Handle("/viewer/", h.viewer)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /{$}", h.home)
}

// Middleware wraps the whole mux (UI and API).
func (h *Handler) Middleware(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if h.cfg.BaseURL != "" {
		if err := cop.AddTrustedOrigin(h.cfg.BaseURL); err != nil {
			slog.Warn("invalid trusted origin", "url", h.cfg.BaseURL, "err", err)
		}
	}
	var hd http.Handler = cop.Handler(next)
	hd = h.requireSetup(hd)
	hd = h.loadSession(hd)
	hd = httpx.BaseURL(h.cfg.BaseURL, h.cfg.TrustProxy)(hd)
	hd = httpx.SecurityHeaders(hd)
	hd = httpx.Logging(hd)
	return httpx.Recover(hd)
}

func (h *Handler) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(httpx.SessionCookie); err == nil && c.Value != "" {
			if u, err := h.svc.UserForSession(r.Context(), c.Value); err == nil {
				r = r.WithContext(httpx.WithUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func exemptFromSetup(path string) bool {
	return path == "/setup" || path == "/healthz" || strings.HasPrefix(path, "/static/")
}

func (h *Handler) requireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.setupDone.Load() || exemptFromSetup(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		need, err := h.svc.NeedsSetup(r.Context())
		if err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if !need {
			h.setupDone.Store(true)
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"title":"Service Unavailable","status":503,"detail":"setup required: open /setup in a browser"}`))
			return
		}
		redirect(w, r, "/setup")
	})
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// redirect sends a 303, or an HX-Redirect for htmx requests.
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		slog.Error("render failed", "path", r.URL.Path, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (h *Handler) page(r *http.Request, title string) views.Page {
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		st = service.DefaultSettings
	}
	return views.Page{Title: title, Instance: st.InstanceTitle, Path: r.URL.Path, Version: buildinfo.Version, User: httpx.UserFrom(r.Context())}
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, msg string) {
	render(w, r, http.StatusNotFound, views.NotFound(h.page(r, "Not found"), msg))
}

func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "path", r.URL.Path, "err", err)
	if isHTMX(r) {
		h.toastOnly(w, r, toast.TypeError, "Something went wrong", "The error was logged.")
		return
	}
	render(w, r, http.StatusInternalServerError, views.ServerError(h.page(r, "Error")))
}

// toastOnly answers an htmx request with just a toast and no swap.
func (h *Handler) toastOnly(w http.ResponseWriter, r *http.Request, t toast.Type, title, desc string) {
	w.Header().Set("HX-Reswap", "none")
	render(w, r, http.StatusOK, views.ToastOOB(t, title, desc))
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

func isNotFound(err error) bool { return errors.Is(err, service.ErrNotFound) }
```

`internal/web/pages.go`:

```go
package web

import (
	"net/http"

	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	players, err := h.svc.ListPlayers(r.Context(), false)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.Home(h.page(r, ""), players))
}
```

`web.New` needs the login limiter used by Task 12; add it now in `internal/web/ratelimit.go`:

```go
package web

import (
	"sync"
	"time"
)

// loginLimiter allows `limit` attempts per key per `window`.
type loginLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{hits: map[string][]time.Time{}, limit: 5, window: time.Minute}
}

func (l *loginLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 10000 { // bound memory under abuse
		l.hits = map[string][]time.Time{}
	}
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}
```

- [x] **Step 9: Add the test env and failing page tests**

Append to `internal/testutil/fakes.go` (add imports `bytes`, `compress/gzip`):

```go
// Gzip compresses s (for fake viewer bundles).
func Gzip(s string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return buf.Bytes()
}
```

`internal/web/env_test.go`:

```go
package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
	"github.com/yyewolf/ssarchiver/internal/viewer"
	"github.com/yyewolf/ssarchiver/internal/web"
)

const adminPW = "correct horse battery"

type statusStub struct{ st archiver.Status }

func (s *statusStub) Status() archiver.Status { return s.st }

type testEnv struct {
	t      *testing.T
	svc    *service.Service
	clk    *testutil.Clock
	status *statusStub
	h      http.Handler
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	svc, _, clk := testutil.NewService(t)
	vh := viewer.NewHandler(fstest.MapFS{"index.html.gz": {Data: testutil.Gzip("<html>viewer</html>")}}, "test")
	st := &statusStub{st: archiver.Status{State: archiver.StateIdle, Since: testutil.T0}}
	h := web.New(web.Deps{Service: svc, Status: st, Viewer: vh, Config: config.Config{HourlyBudget: 300, BaseURL: "https://replays.example.com"}})
	mux := http.NewServeMux()
	h.Routes(mux)
	return &testEnv{t: t, svc: svc, clk: clk, status: st, h: h.Middleware(mux)}
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withHeader(k, v string) reqOpt   { return func(r *http.Request) { r.Header.Set(k, v) } }
func htmx(target string) reqOpt {
	return func(r *http.Request) {
		r.Header.Set("HX-Request", "true")
		if target != "" {
			r.Header.Set("HX-Target", target)
		}
	}
}

func (e *testEnv) do(method, path string, form url.Values, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) setup() {
	e.t.Helper()
	if _, err := e.svc.Setup(context.Background(), "admin", adminPW); err != nil {
		e.t.Fatal(err)
	}
}

// login completes setup and returns a valid session cookie.
func (e *testEnv) login() *http.Cookie {
	e.t.Helper()
	e.setup()
	token, err := e.svc.Login(context.Background(), "admin", adminPW)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: httpx.SessionCookie, Value: token}
}

// seed tracks Alice (1001) with three scores: 1 archived (file on disk), 2 pending, 3 without replay.
func (e *testEnv) seed() {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.svc.AddPlayer(ctx, "1001"); err != nil {
		e.t.Fatal(err)
	}
	items := []scoresaber.ScoreItem{
		testutil.Item("1001", 1, 501, testutil.T0.Add(3*time.Minute), true),
		testutil.Item("1001", 2, 502, testutil.T0.Add(2*time.Minute), true),
		testutil.Item("1001", 3, 503, testutil.T0.Add(time.Minute), false),
	}
	items[0].Leaderboard.Map.SongName = "Hell of a time"
	items[0].Leaderboard.Realm.LeaderboardStatus = "RANKED"
	if _, err := e.svc.UpsertScores(ctx, "1001", items); err != nil {
		e.t.Fatal(err)
	}
	size, sum, err := e.svc.Store().Put("1001", 1, strings.NewReader("ScoreSaber Replay bytes"))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.MarkReplayArchived(ctx, 1, size, sum); err != nil {
		e.t.Fatal(err)
	}
}

func contains(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Fatalf("body does not contain %q:\n%s", p, body)
		}
	}
}
```

`internal/web/pages_test.go`:

```go
package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/healthz", nil)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestRedirectsToSetupUntilDone(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	api := e.do(http.MethodGet, "/api/v1/players", nil)
	if api.Code != http.StatusServiceUnavailable {
		t.Fatalf("api before setup = %d", api.Code)
	}
	e.setup()
	if rec := e.do(http.MethodGet, "/", nil); rec.Code != 200 {
		t.Fatalf("after setup code = %d", rec.Code)
	}
}

func TestHomeListsPlayersWithSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	e.setup()
	empty := e.do(http.MethodGet, "/", nil)
	contains(t, empty.Body.String(), "No players yet")
	e.seed()
	rec := e.do(http.MethodGet, "/", nil)
	body := rec.Body.String()
	contains(t, body, "Alice", `href="/p/1001"`, "/static/css/app.css?v=", "data-theme-toggle")
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "'nonce-") {
		t.Fatalf("csp = %q", csp)
	}
	nonce := strings.SplitN(strings.SplitN(csp, "'nonce-", 2)[1], "'", 2)[0]
	contains(t, body, `nonce="`+nonce+`"`) // components.Scripts() carries the nonce
}

func TestStaticCaching(t *testing.T) {
	e := newEnv(t)
	versioned := e.do(http.MethodGet, "/static/js/app.js?v=abc", nil)
	if versioned.Code != 200 || !strings.Contains(versioned.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("versioned = %d %q", versioned.Code, versioned.Header().Get("Cache-Control"))
	}
	if dir := e.do(http.MethodGet, "/static/js/", nil); dir.Code != 404 {
		t.Fatalf("directory listing must be disabled, got %d", dir.Code)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec := e.do(http.MethodPost, "/logout", url.Values{}, withHeader("Sec-Fetch-Site", "cross-site"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rec.Code)
	}
}
```

- [x] **Step 10: Generate, run and fix**

```bash
go tool templ generate
make gen-css
go test ./internal/web/... ./internal/httpx/...
```

Expected: `ok`. The API and `/logout` routes do not exist yet, but both assertions hold already: `requireSetup` answers `/api/*` with 503 before setup, and `CrossOriginProtection` (wrapping the mux directly) rejects the cross-site POST with 403 before routing. If either fails, the middleware order in `Middleware` is wrong.

- [x] **Step 11: Commit**

```bash
make generate && make lint
git add components.json scripts internal/web internal/httpx internal/testutil Makefile go.mod go.sum
git commit -m "feat: add web foundation with shadcn-templ layout, security headers and home page"
```

---

## Task 12: Web auth — setup, login, logout

**Files:**
- Create: `internal/web/auth.go`, `internal/web/views/auth.templ`, `internal/web/auth_test.go`
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: Task 7 `NeedsSetup`, `Setup`, `Login`, `Logout`, `SessionTTL`, errors; Task 11 helpers, `loginLimiter`.
- Produces:
  - Routes `GET/POST /setup`, `GET/POST /login`, `POST /logout`
  - `(*Handler).requireAdmin(http.HandlerFunc) http.HandlerFunc` — unauthenticated: 303 to `/login?next=<escaped RequestURI>`; for htmx requests 401 + `HX-Redirect`
  - `safeNext(string) string` (only same-site absolute paths; default `/admin`)
  - `views.AuthForm{Username, Next, Error string}`, templ `views.Setup(Page, AuthForm)`, `views.Login(Page, AuthForm)`

- [ ] **Step 1: Write the failing tests** — `internal/web/auth_test.go`

```go
package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/httpx"
)

func sessionCookie(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == httpx.SessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func TestSetupFlow(t *testing.T) {
	e := newEnv(t)
	contains(t, e.do(http.MethodGet, "/setup", nil).Body.String(), "Create admin account")

	bad := e.do(http.MethodPost, "/setup", url.Values{"username": {"admin"}, "password": {adminPW}, "confirm": {"different password"}})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatch code = %d", bad.Code)
	}
	contains(t, bad.Body.String(), "Passwords do not match")

	weak := e.do(http.MethodPost, "/setup", url.Values{"username": {"admin"}, "password": {"short"}, "confirm": {"short"}})
	if weak.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak code = %d", weak.Code)
	}
	contains(t, weak.Body.String(), "at least 10 characters")

	ok := e.do(http.MethodPost, "/setup", url.Values{"username": {"admin"}, "password": {adminPW}, "confirm": {adminPW}})
	if ok.Code != http.StatusSeeOther || ok.Header().Get("Location") != "/admin" {
		t.Fatalf("setup code=%d location=%q", ok.Code, ok.Header().Get("Location"))
	}
	c := sessionCookie(t, ok.Result())
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie flags = %+v", c)
	}
	if u, err := e.svc.UserForSession(context.Background(), c.Value); err != nil || u.Username != "admin" {
		t.Fatalf("session invalid: %v", err)
	}
	if again := e.do(http.MethodGet, "/setup", nil); again.Code != http.StatusNotFound {
		t.Fatalf("setup after completion = %d, want 404", again.Code)
	}
	if again := e.do(http.MethodPost, "/setup", url.Values{"username": {"x"}, "password": {adminPW}, "confirm": {adminPW}}); again.Code != http.StatusNotFound {
		t.Fatalf("second setup POST = %d, want 404", again.Code)
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	contains(t, e.do(http.MethodGet, "/login?next=/p/1001", nil).Body.String(), `value="/p/1001"`)

	wrong := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"wrong password"}})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password code = %d", wrong.Code)
	}
	contains(t, wrong.Body.String(), "Invalid username or password")

	ok := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPW}, "next": {"/p/1001"}})
	if ok.Code != http.StatusSeeOther || ok.Header().Get("Location") != "/p/1001" {
		t.Fatalf("login code=%d location=%q", ok.Code, ok.Header().Get("Location"))
	}
	evil := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPW}, "next": {"//evil.example"}})
	if evil.Header().Get("Location") != "/admin" {
		t.Fatalf("open redirect: %q", evil.Header().Get("Location"))
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for i := range 5 {
		if rec := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {"nope nope nope"}}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d code = %d", i+1, rec.Code)
		}
	}
	rec := e.do(http.MethodPost, "/login", url.Values{"username": {"admin"}, "password": {adminPW}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt code = %d, want 429", rec.Code)
	}
	contains(t, rec.Body.String(), "Too many attempts")
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	rec := e.do(http.MethodPost, "/logout", url.Values{}, withCookie(c))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("logout code=%d", rec.Code)
	}
	cleared := false
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == httpx.SessionCookie && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout must clear the cookie")
	}
	if _, err := e.svc.UserForSession(context.Background(), c.Value); err == nil {
		t.Fatal("logout must delete the session")
	}
	home := e.do(http.MethodGet, "/", nil, withCookie(c))
	if strings.Contains(home.Body.String(), "Log out") {
		t.Fatal("stale cookie still treated as logged in")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/web/ -run 'Setup|Login|Logout'`
Expected: FAIL (404s for `/setup`, `/login`).

- [ ] **Step 3: Write the views** — `internal/web/views/auth.templ`

```templ
package views

import (
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/card"
	"github.com/yyewolf/ssarchiver/internal/web/components/field"
	"github.com/yyewolf/ssarchiver/internal/web/components/input"
)

type AuthForm struct {
	Username string
	Next     string
	Error    string
}

templ authCard(title, desc string) {
	<div class="mx-auto flex w-full max-w-sm flex-col pt-8">
		@card.Card() {
			@card.Header() {
				@card.Title() {
					{ title }
				}
				@card.Description() {
					{ desc }
				}
			}
			@card.Content() {
				{ children... }
			}
		}
	</div>
}

templ Setup(p Page, f AuthForm) {
	@Layout(p) {
		@authCard("Set up SSArchiver", "Create the admin account. You can add players right after.") {
			<form method="post" action="/setup" class="flex flex-col gap-4">
				@FormError(f.Error)
				@field.Field() {
					@field.Label(field.LabelProps{For: "username"}) {
						Username
					}
					@input.Input(input.Props{ID: "username", Name: "username", Value: f.Username, Required: true, Attributes: templ.Attributes{"autocomplete": "username", "autofocus": true}})
				}
				@field.Field() {
					@field.Label(field.LabelProps{For: "password"}) {
						Password
					}
					@input.Input(input.Props{ID: "password", Name: "password", Type: "password", Required: true, Attributes: templ.Attributes{"autocomplete": "new-password", "minlength": "10"}})
					@field.Description() {
						At least 10 characters.
					}
				}
				@field.Field() {
					@field.Label(field.LabelProps{For: "confirm"}) {
						Confirm password
					}
					@input.Input(input.Props{ID: "confirm", Name: "confirm", Type: "password", Required: true, Attributes: templ.Attributes{"autocomplete": "new-password"}})
				}
				@button.Button(button.Props{Type: button.TypeSubmit, Class: "w-full"}) {
					Create admin account
				}
			</form>
		}
	}
}

templ Login(p Page, f AuthForm) {
	@Layout(p) {
		@authCard("Log in", "Administration of this archive.") {
			<form method="post" action="/login" class="flex flex-col gap-4">
				@FormError(f.Error)
				<input type="hidden" name="next" value={ f.Next }/>
				@field.Field() {
					@field.Label(field.LabelProps{For: "username"}) {
						Username
					}
					@input.Input(input.Props{ID: "username", Name: "username", Value: f.Username, Required: true, Attributes: templ.Attributes{"autocomplete": "username", "autofocus": true}})
				}
				@field.Field() {
					@field.Label(field.LabelProps{For: "password"}) {
						Password
					}
					@input.Input(input.Props{ID: "password", Name: "password", Type: "password", Required: true, Attributes: templ.Attributes{"autocomplete": "current-password"}})
				}
				@button.Button(button.Props{Type: button.TypeSubmit, Class: "w-full"}) {
					Log in
				}
			</form>
		}
	}
}
```

- [ ] **Step 4: Implement handlers** — `internal/web/auth.go`

```go
package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

const maxFormBytes = 64 << 10

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func safeNext(s string) string {
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "/\\") {
		return s
	}
	return "/admin"
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: httpx.SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: httpx.IsHTTPS(r, h.cfg.BaseURL, h.cfg.TrustProxy), MaxAge: int(service.SessionTTL / time.Second),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: httpx.SessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: httpx.IsHTTPS(r, h.cfg.BaseURL, h.cfg.TrustProxy), MaxAge: -1,
	})
}

func (h *Handler) setupForm(w http.ResponseWriter, r *http.Request) {
	if need, err := h.svc.NeedsSetup(r.Context()); err != nil || !need {
		h.notFound(w, r, "Setup has already been completed.")
		return
	}
	render(w, r, http.StatusOK, views.Setup(h.page(r, "Set up"), views.AuthForm{}))
}

func (h *Handler) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	username, pw := r.PostFormValue("username"), r.PostFormValue("password")
	form := views.AuthForm{Username: username}
	fail := func(msg string) {
		form.Error = msg
		render(w, r, http.StatusUnprocessableEntity, views.Setup(h.page(r, "Set up"), form))
	}
	if pw != r.PostFormValue("confirm") {
		fail("Passwords do not match.")
		return
	}
	_, err := h.svc.Setup(r.Context(), username, pw)
	switch {
	case errors.Is(err, service.ErrAlreadySetup):
		h.notFound(w, r, "Setup has already been completed.")
		return
	case errors.Is(err, service.ErrWeakPassword), errors.Is(err, service.ErrInvalidUsername):
		fail(strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + ".")
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.setupDone.Store(true)
	token, err := h.svc.Login(r.Context(), username, pw)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.setSessionCookie(w, r, token)
	redirect(w, r, "/admin")
}

func (h *Handler) loginForm(w http.ResponseWriter, r *http.Request) {
	if httpx.UserFrom(r.Context()) != nil {
		redirect(w, r, safeNext(r.URL.Query().Get("next")))
		return
	}
	render(w, r, http.StatusOK, views.Login(h.page(r, "Log in"), views.AuthForm{Next: safeNext(r.URL.Query().Get("next"))}))
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	form := views.AuthForm{Username: r.PostFormValue("username"), Next: safeNext(r.PostFormValue("next"))}
	if !h.logins.Allow(httpx.ClientIP(r, h.cfg.TrustProxy), h.svc.Now()) {
		form.Error = "Too many attempts. Try again in a minute."
		render(w, r, http.StatusTooManyRequests, views.Login(h.page(r, "Log in"), form))
		return
	}
	token, err := h.svc.Login(r.Context(), form.Username, r.PostFormValue("password"))
	if errors.Is(err, service.ErrInvalidCredentials) {
		form.Error = "Invalid username or password."
		render(w, r, http.StatusUnauthorized, views.Login(h.page(r, "Log in"), form))
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.setSessionCookie(w, r, token)
	redirect(w, r, form.Next)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(httpx.SessionCookie); err == nil && c.Value != "" {
		_ = h.svc.Logout(r.Context(), c.Value)
	}
	h.clearSessionCookie(w, r)
	redirect(w, r, "/")
}

// requireAdmin guards admin handlers.
func (h *Handler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if httpx.UserFrom(r.Context()) != nil {
			next(w, r)
			return
		}
		target := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
		if isHTMX(r) {
			w.Header().Set("HX-Redirect", target)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}
```

Register routes in `Routes` (append):

```go
	mux.HandleFunc("GET /setup", h.setupForm)
	mux.HandleFunc("POST /setup", h.setupSubmit)
	mux.HandleFunc("GET /login", h.loginForm)
	mux.HandleFunc("POST /login", h.loginSubmit)
	mux.HandleFunc("POST /logout", h.logout)
```

- [ ] **Step 5: Generate and run tests**

```bash
go tool templ generate && go test ./internal/web/...
```

Expected: `ok`. Note `TestSetupFlow` expects `Secure` cookies because the test config's base URL is https.

- [ ] **Step 6: Commit**

```bash
make generate && make lint
git add internal/web
git commit -m "feat: add first-run setup, login and logout"
```

---
## Task 13: Public player & score pages

**Files:**
- Create: `internal/web/public.go`, `internal/web/views/urls.go`, `internal/web/views/urls_test.go`, `internal/web/views/player.templ`, `internal/web/views/score.templ`, `internal/web/public_test.go`
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: `service.GetPlayer`, `PlayerCounts`, `ListScores`, `GetScore`, `ScoreFilter`, `FilterWithReplay`, `FilterArchived`; `httpx.BaseURLFrom`; Task 11 helpers; `(*viewer.Handler).Available()`.
- Produces:
  - Routes `GET /p/{id}`, `GET /s/{id}`
  - `views.PlayerView{Player *model.Player; Counts service.Counts; Scores service.ScoreList; Filter service.ScoreFilter; Now time.Time}`; templ `views.PlayerPage(Page, PlayerView)`, `views.ScoreTable(PlayerView)` (root element `<div id="scores">`), `views.ReplayBadge(state string)`, `views.DifficultyBadge(*model.Score)`
  - `views.ScoreView{Score *model.Score; ViewerAvailable bool; ReplayURL, EmbedURL, EmbedCode string; Now time.Time}`; templ `views.ScorePage(Page, ScoreView)`
  - URL/text helpers (`views/urls.go`): `PlayerScoresURL(playerID string, f service.ScoreFilter, page int) string`, `EmbedSnippet(embedURL string) string`, `ViewerSrc(base string, id int64, autoplay, loop, hideUI bool) string`, `SongTitle(*model.Score) string`, `SongAuthor(*model.Score) string`, `Mapper(*model.Score) string`, `PlayerName(*model.Score) string`, `CoverURL(*model.Score) string`, `ScoreSummary(*model.Score) string`
  - Behaviour: an htmx request with `HX-Target: scores` to `/p/{id}` gets only `ScoreTable`.

- [ ] **Step 1: Write the failing helper tests** — `internal/web/views/urls_test.go`

```go
package views

import (
	"testing"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func TestURLs(t *testing.T) {
	f := service.ScoreFilter{Search: "ghost rule", State: service.FilterArchived, RankedOnly: true}
	if got := PlayerScoresURL("1001", f, 3); got != "/p/1001?page=3&q=ghost+rule&ranked=1&state=archived" {
		t.Errorf("PlayerScoresURL = %s", got)
	}
	if got := PlayerScoresURL("1001", service.ScoreFilter{}, 1); got != "/p/1001" {
		t.Errorf("PlayerScoresURL(empty) = %s", got)
	}
	src := ViewerSrc("https://r.example.com", 42, true, false, true)
	if src != "/viewer/?autoPlay=true&noProxy=true&replayURL=https%3A%2F%2Fr.example.com%2Fr%2F42.dat&uiOff=true" {
		t.Errorf("ViewerSrc = %s", src)
	}
	if got := EmbedSnippet("https://r.example.com/embed/42"); got != `<iframe src="https://r.example.com/embed/42" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>` {
		t.Errorf("EmbedSnippet = %s", got)
	}
}

func TestScoreText(t *testing.T) {
	s := &model.Score{
		ID: 1, Rank: 3, Accuracy: 0.9728, FullCombo: true, PP: 0,
		Leaderboard: &model.Leaderboard{SongName: "Hell of a time", SongSubName: "(Live)", SongAuthor: "Quadeca", Mapper: "oermergeesh", CoverURL: "https://cdn.scoresaber.com/covers/x.png"},
		Player:      &model.Player{Name: "oermer"},
	}
	if SongTitle(s) != "Hell of a time (Live)" || SongAuthor(s) != "Quadeca" || Mapper(s) != "oermergeesh" || PlayerName(s) != "oermer" || CoverURL(s) == "" {
		t.Fatal("text helpers wrong")
	}
	if got := ScoreSummary(s); got != "97.28% · #3 · FC" {
		t.Errorf("ScoreSummary = %q", got)
	}
	s.FullCombo, s.MissedNotes, s.BadCuts, s.PP = false, 2, 1, 312.5
	if got := ScoreSummary(s); got != "97.28% · #3 · 3 mistakes · 312.50pp" {
		t.Errorf("ScoreSummary = %q", got)
	}
	bare := &model.Score{LeaderboardID: 7, PlayerID: "9"}
	if SongTitle(bare) != "Leaderboard 7" || PlayerName(bare) != "9" || CoverURL(bare) != "" {
		t.Fatal("helpers must tolerate missing preloads")
	}
}
```

- [ ] **Step 2: Implement the helpers** — `internal/web/views/urls.go`

```go
package views

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func PlayerScoresURL(playerID string, f service.ScoreFilter, page int) string {
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
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	u := "/p/" + url.PathEscape(playerID)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

func EmbedSnippet(embedURL string) string {
	return fmt.Sprintf(`<iframe src="%s" width="960" height="540" allow="fullscreen" loading="lazy" style="border:0"></iframe>`, embedURL)
}

// ViewerSrc is the same-origin ArcViewer URL that loads one archived replay.
func ViewerSrc(base string, id int64, autoplay, loop, hideUI bool) string {
	v := url.Values{}
	v.Set("replayURL", base+ReplayPath(id))
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
	return "/viewer/?" + v.Encode()
}

func SongTitle(s *model.Score) string {
	if s.Leaderboard == nil {
		return fmt.Sprintf("Leaderboard %d", s.LeaderboardID)
	}
	if s.Leaderboard.SongSubName != "" {
		return s.Leaderboard.SongName + " " + s.Leaderboard.SongSubName
	}
	return s.Leaderboard.SongName
}

func SongAuthor(s *model.Score) string {
	if s.Leaderboard == nil {
		return ""
	}
	return s.Leaderboard.SongAuthor
}

func Mapper(s *model.Score) string {
	if s.Leaderboard == nil {
		return ""
	}
	return s.Leaderboard.Mapper
}

func PlayerName(s *model.Score) string {
	if s.Player == nil {
		return s.PlayerID
	}
	return s.Player.Name
}

func CoverURL(s *model.Score) string {
	if s.Leaderboard == nil {
		return ""
	}
	return s.Leaderboard.CoverURL
}

func ScoreSummary(s *model.Score) string {
	parts := []string{Percent(s.Accuracy), "#" + strconv.Itoa(s.Rank)}
	if s.FullCombo {
		parts = append(parts, "FC")
	} else {
		parts = append(parts, fmt.Sprintf("%d mistakes", s.MissedNotes+s.BadCuts))
	}
	if s.PP > 0 {
		parts = append(parts, PP(s.PP))
	}
	return strings.Join(parts, " · ")
}
```

Run: `go test ./internal/web/views/` → `ok`.

- [ ] **Step 3: Write the failing page tests** — `internal/web/public_test.go`

```go
package web_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestPlayerPage(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	rec := e.do(http.MethodGet, "/p/1001", nil)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	contains(t, rec.Body.String(), "Alice", "Hell of a time", "Song 502", "Archived", "Pending",
		`property="og:title"`, `href="/s/1"`, `id="score-filters"`, "Archiving replays")
}

func TestPlayerPageHTMXPartialAndFilters(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	rec := e.do(http.MethodGet, "/p/1001?state=archived", nil, htmx("scores"))
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), `<div id="scores"`) || strings.Contains(body, "<html") {
		t.Fatalf("expected a bare #scores partial, got:\n%s", body)
	}
	if !strings.Contains(body, "Hell of a time") || strings.Contains(body, "Song 502") {
		t.Fatalf("state filter not applied:\n%s", body)
	}
	search := e.do(http.MethodGet, "/p/1001?q=hell", nil, htmx("scores")).Body.String()
	if !strings.Contains(search, "Hell of a time") || strings.Contains(search, "Song 503") {
		t.Fatal("search filter not applied")
	}
	ranked := e.do(http.MethodGet, "/p/1001?ranked=1", nil, htmx("scores")).Body.String()
	if !strings.Contains(ranked, "Hell of a time") || strings.Contains(ranked, "Song 502") {
		t.Fatal("ranked filter not applied")
	}
	none := e.do(http.MethodGet, "/p/1001?q=zzzz", nil, htmx("scores")).Body.String()
	contains(t, none, "No scores match")
}

func TestPlayerNotFound(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec := e.do(http.MethodGet, "/p/424242", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
	contains(t, rec.Body.String(), "not archived here")
}

func TestScorePageArchived(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	rec := e.do(http.MethodGet, "/s/1", nil)
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	contains(t, rec.Body.String(),
		"Hell of a time", `src="/embed/1"`, `href="/r/1.dat"`, "Download .dat",
		"replays.example.com/embed/1", `data-copy="#embed-code"`, "sha256",
		`property="og:image" content="https://cdn.scoresaber.com/covers/x.png"`, "Ranked")
}

func TestScorePageStates(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	pending := e.do(http.MethodGet, "/s/2", nil).Body.String()
	contains(t, pending, "queued for archiving")
	if strings.Contains(pending, "/embed/2") || strings.Contains(pending, "/r/2.dat") {
		t.Fatal("pending score must not offer viewer or download")
	}
	contains(t, e.do(http.MethodGet, "/s/3", nil).Body.String(), "has no replay")
	for _, p := range []string{"/s/abc", "/s/999", "/s/-1"} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s code = %d", p, rec.Code)
		}
	}
}
```

Run: `go test ./internal/web/ -run 'Player|Score'` → FAIL (404 everywhere).

- [ ] **Step 4: Write the player view** — `internal/web/views/player.templ`

```templ
package views

import (
	"fmt"
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
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
	Player *model.Player
	Counts service.Counts
	Scores service.ScoreList
	Filter service.ScoreFilter
	Now    time.Time
}

templ PlayerPage(p Page, v PlayerView) {
	@Layout(p) {
		<div class="mb-8 flex flex-wrap items-center gap-4">
			@avatar.Avatar(avatar.Props{Class: "size-14"}) {
				@avatar.Image(avatar.ImageProps{Src: v.Player.AvatarURL, Alt: v.Player.Name})
				@avatar.Fallback() {
					{ Initials(v.Player.Name) }
				}
			}
			<div class="min-w-0 flex-1">
				<h1 class="truncate text-2xl font-semibold tracking-tight">{ v.Player.Name }</h1>
				<p class="text-sm text-muted-foreground">
					{ v.Player.Country } ·
					<a class="underline-offset-4 hover:underline" href={ templ.SafeURL("https://scoresaber.com/u/" + v.Player.ID) } target="_blank" rel="noopener">ScoreSaber profile</a>
				</p>
			</div>
			<dl class="flex gap-8 text-sm">
				@stat("Scores", Number(v.Counts.Scores))
				@stat("Archived", Number(v.Counts.Archived))
				@stat("Pending", Number(v.Counts.Pending))
			</dl>
		</div>
		if v.Player.BackfillState != model.BackfillDone || v.Counts.Pending > 0 {
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

templ archiveProgress(v PlayerView) {
	<div class="mb-6 flex flex-col gap-2 rounded-lg border p-4">
		@progress.Progress(progress.Props{Value: int(v.Counts.Archived), Max: max(int(v.Counts.Replays()), 1)}) {
			@progress.Label() {
				Archiving replays
			}
		}
		<p class="text-xs text-muted-foreground">
			{ Number(v.Counts.Archived) } of { Number(v.Counts.Replays()) } replays archived
			if v.Player.BackfillState != model.BackfillDone {
				· still listing older scores (page { strconv.Itoa(v.Player.BackfillPage) } of { strconv.Itoa(max(v.Player.BackfillTotalPages, v.Player.BackfillPage)) })
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
		<noscript>
			@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantOutline}) {
				Apply
			}
		</noscript>
	</form>
}

templ ScoreTable(v PlayerView) {
	<div id="scores">
		if len(v.Scores.Items) == 0 {
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
							@table.Head(table.HeadProps{Class: "text-right"}) {
								Rank
							}
							@table.Head(table.HeadProps{Class: "text-right"}) {
								Accuracy
							}
							@table.Head(table.HeadProps{Class: "hidden text-right sm:table-cell"}) {
								PP
							}
							@table.Head(table.HeadProps{Class: "hidden lg:table-cell"}) {
								Set
							}
							@table.Head() {
								Replay
							}
						}
					}
					@table.Body() {
						for _, s := range v.Scores.Items {
							@scoreRow(s, v.Now)
						}
					}
				}
			</div>
			@pagination(v)
		}
	</div>
}

templ scoreRow(s *model.Score, now time.Time) {
	@table.Row() {
		@table.Cell() {
			<a href={ templ.SafeURL(ScoreURL(s.ID)) } class="flex items-center gap-3 underline-offset-4 hover:underline">
				if cover := CoverURL(s); cover != "" {
					<img src={ cover } alt="" loading="lazy" class="size-10 shrink-0 rounded-md border object-cover"/>
				}
				<span class="min-w-0">
					<span class="block max-w-64 truncate font-medium">{ SongTitle(s) }</span>
					<span class="block max-w-64 truncate text-xs text-muted-foreground">{ SongAuthor(s) } · { Mapper(s) }</span>
				</span>
			</a>
		}
		@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
			@DifficultyBadge(s)
		}
		@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
			#{ strconv.Itoa(s.Rank) }
		}
		@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
			{ Percent(s.Accuracy) }
		}
		@table.Cell(table.CellProps{Class: "hidden text-right tabular-nums sm:table-cell"}) {
			{ PP(s.PP) }
		}
		@table.Cell(table.CellProps{Class: "hidden text-muted-foreground lg:table-cell"}) {
			{ TimeAgo(s.SetAt, now) }
		}
		@table.Cell() {
			@ReplayBadge(s.ReplayState)
		}
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
	if v.Scores.Pages > 1 {
		<nav class="mt-4 flex items-center justify-between text-sm" aria-label="Pagination">
			<span class="text-muted-foreground">Page { strconv.Itoa(v.Scores.Page) } of { strconv.Itoa(v.Scores.Pages) } · { fmt.Sprintf("%s scores", Number(v.Scores.Total)) }</span>
			<div class="flex gap-2">
				@pageButton(v, v.Scores.Page-1, "Previous", v.Scores.Page <= 1)
				@pageButton(v, v.Scores.Page+1, "Next", v.Scores.Page >= v.Scores.Pages)
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

- [ ] **Step 5: Write the score view** — `internal/web/views/score.templ`

```templ
package views

import (
	"fmt"
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/web/components/alert"
	"github.com/yyewolf/ssarchiver/internal/web/components/badge"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/field"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
	"github.com/yyewolf/ssarchiver/internal/web/components/textarea"
)

type ScoreView struct {
	Score           *model.Score
	ViewerAvailable bool
	ReplayURL       string
	EmbedURL        string
	EmbedCode       string
	Now             time.Time
}

templ ScorePage(p Page, v ScoreView) {
	@Layout(p) {
		<nav class="mb-6 text-sm text-muted-foreground" aria-label="Breadcrumb">
			<a href="/" class="hover:text-foreground">Players</a>
			<span class="mx-1">/</span>
			<a href={ templ.SafeURL("/p/" + v.Score.PlayerID) } class="hover:text-foreground">{ PlayerName(v.Score) }</a>
		</nav>
		<div class="mb-8 flex flex-col gap-6 sm:flex-row sm:items-start">
			if cover := CoverURL(v.Score); cover != "" {
				<img src={ cover } alt="" class="size-28 shrink-0 rounded-lg border object-cover"/>
			}
			<div class="min-w-0 flex-1">
				<h1 class="text-2xl font-semibold tracking-tight">{ SongTitle(v.Score) }</h1>
				<p class="text-sm text-muted-foreground">{ SongAuthor(v.Score) } · mapped by { Mapper(v.Score) }</p>
				<div class="mt-3 flex flex-wrap gap-2">
					@DifficultyBadge(v.Score)
					if v.Score.Leaderboard != nil && v.Score.Leaderboard.Status == "RANKED" {
						@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
							{ fmt.Sprintf("Ranked · %.2f★", v.Score.Leaderboard.Stars) }
						}
					}
					if v.Score.FullCombo {
						@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
							Full combo
						}
					}
					@ReplayBadge(v.Score.ReplayState)
				</div>
			</div>
		</div>
		<dl class="mb-8 grid grid-cols-2 gap-4 rounded-lg border p-4 sm:grid-cols-4">
			@stat("Rank", "#"+strconv.Itoa(v.Score.Rank))
			@stat("Accuracy", Percent(v.Score.Accuracy))
			@stat("Score", Number(v.Score.ModifiedScore))
			@stat("PP", PP(v.Score.PP))
			@stat("Misses", strconv.Itoa(v.Score.MissedNotes))
			@stat("Bad cuts", strconv.Itoa(v.Score.BadCuts))
			@stat("Max combo", Number(v.Score.MaxCombo))
			@stat("Set", TimeAgo(v.Score.SetAt, v.Now))
		</dl>
		<section class="flex flex-col gap-4">
			<h2 class="text-lg font-semibold tracking-tight">Replay</h2>
			@replaySection(v)
		</section>
	}
}

templ replaySection(v ScoreView) {
	switch v.Score.ReplayState {
		case model.ReplayArchived:
			if v.ViewerAvailable {
				<div class="aspect-video w-full overflow-hidden rounded-lg border bg-muted">
					<iframe src={ EmbedPath(v.Score.ID) } title="Replay viewer" class="size-full border-0" allow="fullscreen; autoplay" allowfullscreen loading="lazy"></iframe>
				</div>
			} else {
				@stateAlert("Viewer unavailable", "The 3D viewer is not bundled with this build. You can still download the replay.", false)
			}
			<div class="flex flex-wrap gap-2">
				@button.Button(button.Props{Href: ReplayPath(v.Score.ID), Attributes: templ.Attributes{"download": ""}}) {
					@icon.Download()
					Download .dat
				}
				if v.ViewerAvailable {
					@button.Button(button.Props{Variant: button.VariantOutline, Href: EmbedPath(v.Score.ID), Target: "_blank"}) {
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
			@stateAlert("Replay pruned", "ScoreSaber pruned this replay before it could be archived.", false)
		default:
			@stateAlert("No replay", "ScoreSaber has no replay for this score.", false)
	}
}

templ stateAlert(title, desc string, destructive bool) {
	@alert.Alert(alert.Props{Variant: alertVariant(destructive)}) {
		@icon.CircleAlert()
		@alert.Title() {
			{ title }
		}
		@alert.Description() {
			{ desc }
		}
	}
}

func alertVariant(destructive bool) alert.Variant {
	if destructive {
		return alert.VariantDestructive
	}
	return alert.VariantDefault
}
```

- [ ] **Step 6: Implement handlers** — `internal/web/public.go`

```go
package web

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func (h *Handler) player(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	pl, err := h.svc.GetPlayer(ctx, id)
	if isNotFound(err) {
		h.notFound(w, r, "This player is not archived here.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	f := service.ScoreFilter{PlayerID: id, Search: q.Get("q"), RankedOnly: q.Get("ranked") == "1", Page: page, PerPage: 50}
	if s := q.Get("state"); s == service.FilterWithReplay || s == service.FilterArchived {
		f.State = s
	}
	list, err := h.svc.ListScores(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	counts, err := h.svc.PlayerCounts(ctx, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	v := views.PlayerView{Player: pl, Counts: counts, Scores: list, Filter: f, Now: h.svc.Now()}
	if isHTMX(r) && r.Header.Get("HX-Target") == "scores" {
		render(w, r, http.StatusOK, views.ScoreTable(v))
		return
	}
	p := h.page(r, pl.Name)
	p.OG = &views.OpenGraph{
		Title:       pl.Name + " · ScoreSaber replays",
		Description: fmt.Sprintf("%s archived replays", views.Number(counts.Archived)),
		Image:       pl.AvatarURL,
		URL:         httpx.BaseURLFrom(ctx) + "/p/" + pl.ID,
	}
	render(w, r, http.StatusOK, views.PlayerPage(p, v))
}

func (h *Handler) score(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		h.notFound(w, r, "No such score.")
		return
	}
	sc, err := h.svc.GetScore(ctx, id)
	if isNotFound(err) {
		h.notFound(w, r, "No such score.")
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	base := httpx.BaseURLFrom(ctx)
	v := views.ScoreView{
		Score: sc, ViewerAvailable: h.viewer.Available(), Now: h.svc.Now(),
		ReplayURL: base + views.ReplayPath(id), EmbedURL: base + views.EmbedPath(id),
	}
	v.EmbedCode = views.EmbedSnippet(v.EmbedURL)
	title := fmt.Sprintf("%s by %s", views.SongTitle(sc), views.PlayerName(sc))
	p := h.page(r, title)
	p.OG = &views.OpenGraph{Title: title, Description: views.ScoreSummary(sc), Image: views.CoverURL(sc), URL: base + views.ScoreURL(id)}
	render(w, r, http.StatusOK, views.ScorePage(p, v))
}
```

Register in `Routes`:

```go
	mux.HandleFunc("GET /p/{id}", h.player)
	mux.HandleFunc("GET /s/{id}", h.score)
```

- [ ] **Step 7: Generate, test, look at it**

```bash
go tool templ generate && make gen-css && go test ./internal/web/...
```

Expected: `ok`.

- [ ] **Step 8: Commit**

```bash
make generate && make lint
git add internal/web
git commit -m "feat: add public player and score pages"
```

---

## Task 14: Replay download & embed endpoints

**Files:**
- Create: `internal/web/replay.go`, `internal/web/views/embed.templ`, `internal/web/replay_test.go`
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: `service.GetScore`, `(*Service).Store().Open`; `views.ViewerSrc`, `views.SongTitle`; `httpx.EmbedCSP`.
- Produces: routes `GET /r/{file}` (also answers HEAD), `OPTIONS /r/{file}`, `GET /embed/{id}`; templ `views.Embed(title, src string)`, `views.EmbedUnavailable()`.

- [ ] **Step 1: Write the failing tests** — `internal/web/replay_test.go`

```go
package web_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/httpx"
)

const replayBody = "ScoreSaber Replay bytes"

func TestReplayDownload(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	rec := e.do(http.MethodGet, "/r/1.dat", nil)
	if rec.Code != 200 || rec.Body.String() != replayBody {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if h.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(h.Get("Content-Disposition"), `filename="1.dat"`) ||
		!strings.Contains(h.Get("Cache-Control"), "immutable") || h.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("headers = %v", h)
	}
	etag := h.Get("ETag")
	if len(etag) != 66 {
		t.Fatalf("ETag must be the quoted sha256, got %q", etag)
	}
	if rec := e.do(http.MethodGet, "/r/1.dat", nil, withHeader("If-None-Match", etag)); rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d", rec.Code)
	}
	part := e.do(http.MethodGet, "/r/1.dat", nil, withHeader("Range", "bytes=0-9"))
	if part.Code != http.StatusPartialContent || part.Body.String() != replayBody[:10] || part.Header().Get("Content-Range") != "bytes 0-9/23" {
		t.Fatalf("range = %d %q %q", part.Code, part.Body.String(), part.Header().Get("Content-Range"))
	}
	head := e.do(http.MethodHead, "/r/1.dat", nil)
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d with %d bytes", head.Code, head.Body.Len())
	}
	pre := e.do(http.MethodOptions, "/r/1.dat", nil, withHeader("Origin", "https://portfolio.example"), withHeader("Access-Control-Request-Headers", "range"))
	if pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(pre.Header().Get("Access-Control-Allow-Headers"), "Range") {
		t.Fatalf("preflight = %d %v", pre.Code, pre.Header())
	}
}

func TestReplayNotServed(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	for _, p := range []string{"/r/2.dat", "/r/3.dat", "/r/999.dat", "/r/abc.dat", "/r/1", "/r/0.dat"} {
		if rec := e.do(http.MethodGet, p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
	if err := os.Remove(e.svc.Store().Path("1001", 1)); err != nil {
		t.Fatal(err)
	}
	if rec := e.do(http.MethodGet, "/r/1.dat", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing file = %d, want 404", rec.Code)
	}
}

func TestEmbed(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.seed()
	rec := e.do(http.MethodGet, "/embed/1?autoplay=1&loop=1&ui=0", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != httpx.EmbedCSP {
		t.Fatalf("code=%d csp=%q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	contains(t, rec.Body.String(), "/viewer/?autoPlay=true", "loop=true", "noProxy=true",
		"replayURL=https%3A%2F%2Freplays.example.com%2Fr%2F1.dat", "uiOff=true")
	plain := e.do(http.MethodGet, "/embed/1", nil).Body.String()
	if strings.Contains(plain, "autoPlay") || strings.Contains(plain, "uiOff") {
		t.Fatal("options must be off by default")
	}
	if rec := e.do(http.MethodGet, "/embed/2", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("embed of pending replay = %d", rec.Code)
	}
	viewer := e.do(http.MethodGet, "/viewer/", nil, withHeader("Accept-Encoding", "identity"))
	if viewer.Code != 200 || !strings.Contains(viewer.Header().Get("Content-Security-Policy"), "frame-ancestors *") {
		t.Fatalf("viewer = %d %v", viewer.Code, viewer.Header())
	}
}
```

Run: `go test ./internal/web/ -run 'Replay|Embed'` → FAIL.

- [ ] **Step 2: Write the embed views** — `internal/web/views/embed.templ`

```templ
package views

// Embed is a chrome-less page meant to be iframed anywhere. The background
// matches ArcViewer's canvas colour to avoid a flash while it loads.
templ Embed(title, src string) {
	<!DOCTYPE html>
	<html lang="en">
		<head>
			<meta charset="utf-8"/>
			<meta name="viewport" content="width=device-width, initial-scale=1"/>
			<meta name="robots" content="noindex"/>
			<title>{ title }</title>
			<style>
				html, body { margin: 0; height: 100%; background: #101116; }
				iframe { display: block; width: 100%; height: 100%; border: 0; }
			</style>
		</head>
		<body>
			<iframe src={ src } title={ title } allow="fullscreen; autoplay" allowfullscreen></iframe>
		</body>
	</html>
}

templ EmbedUnavailable() {
	<!DOCTYPE html>
	<html lang="en">
		<head>
			<meta charset="utf-8"/>
			<meta name="robots" content="noindex"/>
			<title>Replay unavailable</title>
			<style>
				html, body { margin: 0; height: 100%; background: #101116; color: #a1a1aa; font: 14px system-ui, sans-serif; }
				body { display: flex; align-items: center; justify-content: center; }
			</style>
		</head>
		<body>This replay is not available.</body>
	</html>
}
```

- [ ] **Step 3: Implement handlers** — `internal/web/replay.go`

```go
package web

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Content-Disposition, ETag")
}

// replayFile serves /r/{id}.dat for archived replays only.
func (h *Handler) replayFile(w http.ResponseWriter, r *http.Request) {
	setCORS(w.Header())
	idStr, ok := strings.CutSuffix(r.PathValue("file"), ".dat")
	id, okID := parseID(idStr)
	if !ok || !okID {
		http.NotFound(w, r)
		return
	}
	sc, err := h.svc.GetScore(r.Context(), id)
	if err != nil || sc.ReplayState != model.ReplayArchived {
		http.NotFound(w, r)
		return
	}
	f, err := h.svc.Store().Open(sc.PlayerID, sc.ID)
	if err != nil {
		slog.Error("archived replay file missing", "score", sc.ID, "err", err)
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="`+strconv.FormatInt(sc.ID, 10)+`.dat"`)
	hd.Set("ETag", `"`+sc.ReplaySHA256+`"`)
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (h *Handler) replayPreflight(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	setCORS(hd)
	hd.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	hd.Set("Access-Control-Allow-Headers", "Range")
	hd.Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// embed serves the iframe-able wrapper around the same-origin viewer.
func (h *Handler) embed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", httpx.EmbedCSP)
	id, ok := parseID(r.PathValue("id"))
	if !ok || !h.viewer.Available() {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	sc, err := h.svc.GetScore(r.Context(), id)
	if err != nil || sc.ReplayState != model.ReplayArchived {
		render(w, r, http.StatusNotFound, views.EmbedUnavailable())
		return
	}
	q := r.URL.Query()
	src := views.ViewerSrc(httpx.BaseURLFrom(r.Context()), id, q.Get("autoplay") == "1", q.Get("loop") == "1", q.Get("ui") == "0")
	render(w, r, http.StatusOK, views.Embed(views.SongTitle(sc)+" · "+views.PlayerName(sc), src))
}
```

Register in `Routes`:

```go
	mux.HandleFunc("GET /r/{file}", h.replayFile)
	mux.HandleFunc("OPTIONS /r/{file}", h.replayPreflight)
	mux.HandleFunc("GET /embed/{id}", h.embed)
```

- [ ] **Step 4: Generate and test**

```bash
go tool templ generate && go test ./internal/web/...
```

Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
make generate && make lint
git add internal/web
git commit -m "feat: serve replay downloads with CORS/Range and embeddable viewer page"
```

---

## Task 15: Admin — players & settings

**Files:**
- Create: `internal/web/admin.go`, `internal/web/views/admin.templ`, `internal/web/views/settings.templ`, `internal/web/admin_test.go`
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: `service.ResolvePlayer`, `AddPlayer`, `GetPlayer`, `PlayerCounts`, `ListPlayers`, `SetPlayerEnabled`, `RequestPoll`, `DeletePlayer`, `Settings`, `UpdateSettings`, `ChangePassword`, `PollIntervals`, errors; `(*Handler).requireAdmin`, `toastOnly`, `clearSessionCookie` (Tasks 11–12).
- Produces:
  - Routes (all behind `requireAdmin`): `GET /admin`, `POST /admin/players/lookup`, `POST /admin/players`, `POST /admin/players/{id}/enabled`, `POST /admin/players/{id}/poll`, `POST /admin/players/{id}/delete`, `GET /admin/settings`, `POST /admin/settings`, `POST /admin/password`
  - `views.AdminPlayersView{Players []service.PlayerSummary; Now time.Time}`; templ `AdminPlayersPage`, `AdminPlayersTable` (root `<div id="admin-players">`), `AdminPlayerRow(service.PlayerSummary, time.Time)` (root `<tr id="player-<id>">`), `PlayerPreview(scoresaber.Player, tracked bool)`, `PlayerAdded(AdminPlayersView, name string)`, `PlayerRowToast(service.PlayerSummary, time.Time, title string)`
  - `views.SettingsView{Settings service.Settings; Intervals []time.Duration; Error string}`, `views.PasswordView{Error string}`; templ `SettingsPage`, `GeneralSettings` (root `#settings-general`), `GeneralSettingsSaved`, `PasswordSettings` (root `#settings-password`)
  - `sentence(err error) string` helper (capitalise + full stop)
- htmx convention: validation problems are answered **200** (htmx does not swap 4xx by default) — either a re-rendered form with an error, or `toastOnly`.

- [ ] **Step 1: Write the failing tests** — `internal/web/admin_test.go`

```go
package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAdminRequiresLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	rec := e.do(http.MethodGet, "/admin", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fadmin" {
		t.Fatalf("code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	hx := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"1001"}}, htmx(""))
	if hx.Code != http.StatusUnauthorized || !strings.HasPrefix(hx.Header().Get("HX-Redirect"), "/login") {
		t.Fatalf("htmx code=%d redirect=%q", hx.Code, hx.Header().Get("HX-Redirect"))
	}
}

func TestLookupPlayer(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ok := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"https://scoresaber.com/u/1001?page=2"}}, withCookie(c), htmx("lookup-result"))
	contains(t, ok.Body.String(), "Alice", "Track player", `value="1001"`)
	bad := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"not a link"}}, withCookie(c), htmx("lookup-result"))
	contains(t, bad.Body.String(), "Enter a ScoreSaber player ID")
	missing := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"9999"}}, withCookie(c), htmx("lookup-result"))
	contains(t, missing.Body.String(), "No ScoreSaber player found")
	e.seed()
	tracked := e.do(http.MethodPost, "/admin/players/lookup", url.Values{"ref": {"1001"}}, withCookie(c), htmx("lookup-result"))
	contains(t, tracked.Body.String(), "Already tracked")
}

func TestAddPlayer(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	rec := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"1002"}}, withCookie(c), htmx("admin-players"))
	body := rec.Body.String()
	contains(t, body, `id="admin-players"`, "Bob", "data-templ-toast", "Now tracking Bob", `id="lookup-result" hx-swap-oob`)
	if _, err := e.svc.GetPlayer(context.Background(), "1002"); err != nil {
		t.Fatal("player not stored")
	}
	dup := e.do(http.MethodPost, "/admin/players", url.Values{"ref": {"1002"}}, withCookie(c), htmx("admin-players"))
	if dup.Header().Get("HX-Reswap") != "none" {
		t.Fatal("duplicate add must not swap the table")
	}
	contains(t, dup.Body.String(), "Already tracked")
}

func TestPlayerRowActions(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	ctx := context.Background()
	_ = e.svc.MarkPolled(ctx, "1001")

	off := e.do(http.MethodPost, "/admin/players/1001/enabled", url.Values{"enabled": {"false"}}, withCookie(c), htmx("player-1001"))
	contains(t, off.Body.String(), `id="player-1001"`, "Disabled", "Tracking paused")
	if p, _ := e.svc.GetPlayer(ctx, "1001"); p.Enabled {
		t.Fatal("player still enabled")
	}

	poll := e.do(http.MethodPost, "/admin/players/1001/poll", url.Values{}, withCookie(c), htmx(""))
	contains(t, poll.Body.String(), "Poll queued")
	if p, _ := e.svc.GetPlayer(ctx, "1001"); p.LastPolledAt != nil {
		t.Fatal("poll not requested")
	}

	del := e.do(http.MethodPost, "/admin/players/1001/delete", url.Values{"delete_files": {"on"}}, withCookie(c), htmx("player-1001"))
	contains(t, del.Body.String(), "Stopped tracking Alice")
	if strings.Contains(del.Body.String(), `id="player-1001"`) {
		t.Fatal("deleted row must be replaced with nothing")
	}
	if _, err := e.svc.Store().Open("1001", 1); err == nil {
		t.Fatal("replay files not deleted")
	}
	missing := e.do(http.MethodPost, "/admin/players/1001/poll", url.Values{}, withCookie(c), htmx(""))
	contains(t, missing.Body.String(), "not found")
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	contains(t, e.do(http.MethodGet, "/admin/settings", nil, withCookie(c)).Body.String(), "Instance title", "Poll every", "Change password")

	bad := e.do(http.MethodPost, "/admin/settings", url.Values{"title": {""}, "poll_interval": {"10m0s"}}, withCookie(c), htmx("settings-general"))
	contains(t, bad.Body.String(), `id="settings-general"`, "Title must be 1-64 characters")

	ok := e.do(http.MethodPost, "/admin/settings", url.Values{"title": {"Oermer replays"}, "poll_interval": {"15m0s"}}, withCookie(c), htmx("settings-general"))
	contains(t, ok.Body.String(), "Settings saved")
	if st, _ := e.svc.Settings(ctx); st.InstanceTitle != "Oermer replays" || st.PollInterval != 15*time.Minute {
		t.Fatalf("settings = %+v", st)
	}
	contains(t, e.do(http.MethodGet, "/", nil).Body.String(), "<title>Oermer replays</title>")

	wrong := e.do(http.MethodPost, "/admin/password", url.Values{"current": {"nope nope nope"}, "new": {"brand new password"}, "confirm": {"brand new password"}}, withCookie(c), htmx("settings-password"))
	contains(t, wrong.Body.String(), "Current password is incorrect")
	mismatch := e.do(http.MethodPost, "/admin/password", url.Values{"current": {adminPW}, "new": {"brand new password"}, "confirm": {"other"}}, withCookie(c), htmx("settings-password"))
	contains(t, mismatch.Body.String(), "Passwords do not match")
	done := e.do(http.MethodPost, "/admin/password", url.Values{"current": {adminPW}, "new": {"brand new password"}, "confirm": {"brand new password"}}, withCookie(c), htmx("settings-password"))
	if done.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("password change must redirect to login, headers = %v", done.Header())
	}
	if _, err := e.svc.Login(ctx, "admin", "brand new password"); err != nil {
		t.Fatal("new password not active")
	}
}
```

Run: `go test ./internal/web/ -run 'Admin|Lookup|AddPlayer|RowActions|Settings'` → FAIL.

- [ ] **Step 2: Write the admin views** — `internal/web/views/admin.templ`

```templ
package views

import (
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/avatar"
	"github.com/yyewolf/ssarchiver/internal/web/components/badge"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/card"
	"github.com/yyewolf/ssarchiver/internal/web/components/empty"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
	"github.com/yyewolf/ssarchiver/internal/web/components/input"
	"github.com/yyewolf/ssarchiver/internal/web/components/table"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
)

type AdminPlayersView struct {
	Players []service.PlayerSummary
	Now     time.Time
}

func lastPoll(p model.Player, now time.Time) string {
	if p.LastPolledAt == nil {
		return "never"
	}
	return TimeAgo(*p.LastPolledAt, now)
}

func backfillLabel(p model.Player) string {
	switch p.BackfillState {
	case model.BackfillDone:
		return "Complete"
	case model.BackfillRunning:
		return "Page " + strconv.Itoa(p.BackfillPage) + " of " + strconv.Itoa(max(p.BackfillTotalPages, p.BackfillPage))
	}
	return "Waiting"
}

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
					Paste a ScoreSaber profile URL or a player ID.
				}
			}
			@card.Content() {
				<form hx-post="/admin/players/lookup" hx-target="#lookup-result" hx-swap="innerHTML" class="flex flex-col gap-2 sm:flex-row">
					@input.Input(input.Props{Name: "ref", Placeholder: "https://scoresaber.com/u/76561198…", Required: true, Class: "sm:flex-1", Attributes: templ.Attributes{"aria-label": "ScoreSaber profile URL or player ID"}})
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

templ PlayerPreview(sp scoresaber.Player, tracked bool) {
	<div class="flex items-center gap-3 rounded-lg border p-3">
		@avatar.Avatar() {
			@avatar.Image(avatar.ImageProps{Src: sp.Avatar, Alt: sp.Name})
			@avatar.Fallback() {
				{ Initials(sp.Name) }
			}
		}
		<div class="min-w-0 flex-1">
			<p class="truncate font-medium">{ sp.Name }</p>
			<p class="font-mono text-xs text-muted-foreground">{ sp.ID } · { sp.Country }</p>
		</div>
		if tracked {
			@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
				Already tracked
			}
		} else {
			<form hx-post="/admin/players" hx-target="#admin-players" hx-swap="outerHTML">
				<input type="hidden" name="ref" value={ sp.ID }/>
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
							@AdminPlayerRow(pl, v.Now)
						}
					}
				}
			</div>
		}
	</div>
}

templ AdminPlayerRow(pl service.PlayerSummary, now time.Time) {
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
			if pl.Enabled {
				@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
					Enabled
				}
			} else {
				@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
					Disabled
				}
			}
			if pl.LastError != "" {
				<p class="mt-1 max-w-56 truncate text-xs text-destructive" title={ pl.LastError }>{ pl.LastError }</p>
			}
		}
		@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
			{ backfillLabel(pl.Player) }
		}
		@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
			{ Number(pl.Counts.Archived) } / { Number(pl.Counts.Replays()) }
		}
		@table.Cell(table.CellProps{Class: "hidden text-muted-foreground md:table-cell"}) {
			{ lastPoll(pl.Player, now) }
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

templ PlayerAdded(v AdminPlayersView, name string) {
	@AdminPlayersTable(v)
	<div id="lookup-result" hx-swap-oob="innerHTML"></div>
	@ToastOOB(toast.TypeSuccess, "Now tracking "+name, "Their history is being archived in the background.")
}

templ PlayerRowToast(pl service.PlayerSummary, now time.Time, title string) {
	@AdminPlayerRow(pl, now)
	@ToastOOB(toast.TypeSuccess, title, pl.Name)
}
```

`internal/web/views/settings.templ`:

```templ
package views

import (
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/card"
	"github.com/yyewolf/ssarchiver/internal/web/components/field"
	"github.com/yyewolf/ssarchiver/internal/web/components/input"
	"github.com/yyewolf/ssarchiver/internal/web/components/nativeselect"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
)

type SettingsView struct {
	Settings  service.Settings
	Intervals []time.Duration
	Error     string
}

type PasswordView struct {
	Error string
}

templ SettingsPage(p Page, v SettingsView) {
	@Layout(p) {
		<div class="mb-8 flex flex-col gap-1">
			<h1 class="text-2xl font-semibold tracking-tight">Settings</h1>
		</div>
		<div class="grid gap-6 lg:grid-cols-2">
			@GeneralSettings(v)
			@PasswordSettings(PasswordView{})
		</div>
	}
}

templ GeneralSettings(v SettingsView) {
	<div id="settings-general">
		@card.Card() {
			@card.Header() {
				@card.Title() {
					General
				}
				@card.Description() {
					How this archive is named and how often players are polled.
				}
			}
			@card.Content() {
				<form hx-post="/admin/settings" hx-target="#settings-general" hx-swap="outerHTML" class="flex flex-col gap-4">
					@FormError(v.Error)
					@field.Field() {
						@field.Label(field.LabelProps{For: "title"}) {
							Instance title
						}
						@input.Input(input.Props{ID: "title", Name: "title", Value: v.Settings.InstanceTitle, Required: true})
					}
					@field.Field() {
						@field.Label(field.LabelProps{For: "poll_interval"}) {
							Poll every
						}
						@nativeselect.NativeSelect(nativeselect.Props{ID: "poll_interval", Name: "poll_interval"}) {
							for _, d := range v.Intervals {
								@nativeselect.Option(nativeselect.OptionProps{Value: d.String(), Selected: d == v.Settings.PollInterval}) {
									{ HumanDuration(d) }
								}
							}
						}
						@field.Description() {
							Each poll costs one ScoreSaber request per player; the rest of the hourly budget goes to replays.
						}
					}
					<div>
						@button.Button(button.Props{Type: button.TypeSubmit}) {
							Save
						}
					</div>
				</form>
			}
		}
	</div>
}

templ GeneralSettingsSaved(v SettingsView) {
	@GeneralSettings(v)
	@ToastOOB(toast.TypeSuccess, "Settings saved", "")
}

templ PasswordSettings(v PasswordView) {
	<div id="settings-password">
		@card.Card() {
			@card.Header() {
				@card.Title() {
					Change password
				}
				@card.Description() {
					You will be logged out everywhere.
				}
			}
			@card.Content() {
				<form hx-post="/admin/password" hx-target="#settings-password" hx-swap="outerHTML" class="flex flex-col gap-4">
					@FormError(v.Error)
					@field.Field() {
						@field.Label(field.LabelProps{For: "current"}) {
							Current password
						}
						@input.Input(input.Props{ID: "current", Name: "current", Type: "password", Required: true, Attributes: templ.Attributes{"autocomplete": "current-password"}})
					}
					@field.Field() {
						@field.Label(field.LabelProps{For: "new"}) {
							New password
						}
						@input.Input(input.Props{ID: "new", Name: "new", Type: "password", Required: true, Attributes: templ.Attributes{"autocomplete": "new-password", "minlength": "10"}})
					}
					@field.Field() {
						@field.Label(field.LabelProps{For: "confirm"}) {
							Confirm new password
						}
						@input.Input(input.Props{ID: "confirm", Name: "confirm", Type: "password", Required: true, Attributes: templ.Attributes{"autocomplete": "new-password"}})
					}
					<div>
						@button.Button(button.Props{Type: button.TypeSubmit, Variant: button.VariantSecondary}) {
							Change password
						}
					</div>
				</form>
			}
		}
	</div>
}
```

- [ ] **Step 3: Implement handlers** — `internal/web/admin.go`

```go
package web

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

// sentence turns an error into a UI sentence: capitalised, ending with a period.
func sentence(err error) string {
	msg := err.Error()
	if msg == "" {
		return ""
	}
	r := []rune(msg)
	r[0] = unicode.ToUpper(r[0])
	msg = string(r)
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

func (h *Handler) adminPlayersView(r *http.Request) (views.AdminPlayersView, error) {
	players, err := h.svc.ListPlayers(r.Context(), true)
	return views.AdminPlayersView{Players: players, Now: h.svc.Now()}, err
}

func (h *Handler) adminPlayers(w http.ResponseWriter, r *http.Request) {
	v, err := h.adminPlayersView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.AdminPlayersPage(h.page(r, "Manage players"), v))
}

func (h *Handler) lookupPlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	sp, err := h.svc.ResolvePlayer(r.Context(), r.PostFormValue("ref"))
	switch {
	case errors.Is(err, service.ErrInvalidPlayerRef):
		render(w, r, http.StatusOK, views.FormError(sentence(err)))
		return
	case isNotFound(err):
		render(w, r, http.StatusOK, views.FormError("No ScoreSaber player found for that ID."))
		return
	case err != nil:
		render(w, r, http.StatusOK, views.FormError("ScoreSaber could not be reached: "+err.Error()))
		return
	}
	_, gerr := h.svc.GetPlayer(r.Context(), sp.ID)
	render(w, r, http.StatusOK, views.PlayerPreview(sp, gerr == nil))
}

func (h *Handler) addPlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	p, err := h.svc.AddPlayer(r.Context(), r.PostFormValue("ref"))
	switch {
	case errors.Is(err, service.ErrPlayerExists):
		h.toastOnly(w, r, toast.TypeWarning, "Already tracked", "This player is already archived here.")
		return
	case errors.Is(err, service.ErrInvalidPlayerRef), isNotFound(err):
		h.toastOnly(w, r, toast.TypeError, "Could not add player", sentence(err))
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
	render(w, r, http.StatusOK, views.PlayerAdded(v, p.Name))
}

func (h *Handler) playerSummary(r *http.Request, id string) (service.PlayerSummary, error) {
	p, err := h.svc.GetPlayer(r.Context(), id)
	if err != nil {
		return service.PlayerSummary{}, err
	}
	c, err := h.svc.PlayerCounts(r.Context(), id)
	return service.PlayerSummary{Player: *p, Counts: c}, err
}

func (h *Handler) setPlayerEnabled(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	enabled := r.PostFormValue("enabled") == "true"
	if err := h.svc.SetPlayerEnabled(r.Context(), id, enabled); err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Player not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	pl, err := h.playerSummary(r, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	title := "Tracking resumed"
	if !enabled {
		title = "Tracking paused"
	}
	render(w, r, http.StatusOK, views.PlayerRowToast(pl, h.svc.Now(), title))
}

func (h *Handler) pollPlayer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.RequestPoll(r.Context(), id); err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Player not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	h.toastOnly(w, r, toast.TypeInfo, "Poll queued", "The worker will poll this player next.")
}

func (h *Handler) deletePlayer(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	id := r.PathValue("id")
	p, err := h.svc.GetPlayer(r.Context(), id)
	if err != nil {
		if isNotFound(err) {
			h.toastOnly(w, r, toast.TypeError, "Player not found", "")
			return
		}
		h.serverError(w, r, err)
		return
	}
	if err := h.svc.DeletePlayer(r.Context(), id, r.PostFormValue("delete_files") == "on"); err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.ToastOOB(toast.TypeSuccess, "Stopped tracking "+p.Name, ""))
}

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Settings(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.SettingsPage(h.page(r, "Settings"), views.SettingsView{Settings: st, Intervals: service.PollIntervals}))
}

func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	d, _ := time.ParseDuration(r.PostFormValue("poll_interval"))
	st := service.Settings{InstanceTitle: r.PostFormValue("title"), PollInterval: d}
	v := views.SettingsView{Settings: st, Intervals: service.PollIntervals}
	if err := h.svc.UpdateSettings(r.Context(), st); err != nil {
		if !errors.Is(err, service.ErrInvalidSettings) {
			h.serverError(w, r, err)
			return
		}
		// "invalid settings: title must be 1-64 characters" → "Title must be 1-64 characters."
		v.Error = sentence(errors.New(strings.TrimPrefix(err.Error(), service.ErrInvalidSettings.Error()+": ")))
		render(w, r, http.StatusOK, views.GeneralSettings(v))
		return
	}
	render(w, r, http.StatusOK, views.GeneralSettingsSaved(v))
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	u := httpx.UserFrom(r.Context())
	fail := func(msg string) { render(w, r, http.StatusOK, views.PasswordSettings(views.PasswordView{Error: msg})) }
	if r.PostFormValue("new") != r.PostFormValue("confirm") {
		fail("Passwords do not match.")
		return
	}
	err := h.svc.ChangePassword(r.Context(), u.ID, r.PostFormValue("current"), r.PostFormValue("new"))
	switch {
	case errors.Is(err, service.ErrInvalidCredentials):
		fail("Current password is incorrect.")
		return
	case errors.Is(err, service.ErrWeakPassword):
		fail(sentence(err))
		return
	case err != nil:
		h.serverError(w, r, err)
		return
	}
	h.clearSessionCookie(w, r)
	w.Header().Set("HX-Redirect", "/login")
	w.WriteHeader(http.StatusOK)
}
```

Register in `Routes`:

```go
	mux.HandleFunc("GET /admin", h.requireAdmin(h.adminPlayers))
	mux.HandleFunc("POST /admin/players/lookup", h.requireAdmin(h.lookupPlayer))
	mux.HandleFunc("POST /admin/players", h.requireAdmin(h.addPlayer))
	mux.HandleFunc("POST /admin/players/{id}/enabled", h.requireAdmin(h.setPlayerEnabled))
	mux.HandleFunc("POST /admin/players/{id}/poll", h.requireAdmin(h.pollPlayer))
	mux.HandleFunc("POST /admin/players/{id}/delete", h.requireAdmin(h.deletePlayer))
	mux.HandleFunc("GET /admin/settings", h.requireAdmin(h.settingsPage))
	mux.HandleFunc("POST /admin/settings", h.requireAdmin(h.saveSettings))
	mux.HandleFunc("POST /admin/password", h.requireAdmin(h.changePassword))
```

- [ ] **Step 4: Generate and test**

```bash
go tool templ generate && make gen-css && go test ./internal/web/...
```

Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
make generate && make lint
git add internal/web
git commit -m "feat: add admin pages for players and settings"
```

---

## Task 16: Admin — sync status page

**Files:**
- Create: `internal/service/stats.go`, `internal/service/stats_test.go`, `internal/web/admin_sync.go`, `internal/web/views/sync.templ`, `internal/web/admin_sync_test.go`
- Modify: `internal/web/web.go` (routes)

**Interfaces:**
- Consumes: `archiver.Status`, `archiver.State*`, `scoresaber.WindowSnapshot`; `service.ListPlayers`, `Settings`, `SetWorkerPaused`, `ListEvents`, `ListFailedReplays`, `RetryFailed`, `EventFilter`; `h.cfg.HourlyBudget`.
- Produces:
  - `service.ReplayRatePerHour(hourlyBudget, enabledPlayers int, pollInterval time.Duration) float64`, `service.ETA(pending int64, perHour float64) time.Duration`
  - Routes (admin): `GET /admin/sync`, `GET /admin/sync/live`, `GET /admin/sync/events`, `GET /admin/sync/failed`, `POST /admin/sync/pause`, `POST /admin/sync/resume`, `POST /admin/sync/retry`
  - `views.SyncView{Status archiver.Status; Paused bool; Queues []QueueRow; PendingTotal, FailedTotal int64; RatePerHour float64; ETA time.Duration; Now time.Time}`, `views.QueueRow{Player service.PlayerSummary; NextPoll time.Time; ETA time.Duration}`, `views.EventsView{Events []*model.SyncEvent; Total int64; Filter service.EventFilter; Players []service.PlayerSummary; Now time.Time}`, `views.FailedView{Items []*model.Score; Total int64; Page, Pages int; Now time.Time}`
  - templ `SyncPage`, `SyncLive` (root `#sync-live`, polls itself every 3 s), `SyncLiveToast`, `EventsTable` (root `#sync-events`), `FailedTable` (root `#failed-replays`), `FailedRetried`

- [ ] **Step 1: Write the failing stats test** — `internal/service/stats_test.go`

```go
package service_test

import (
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
)

func TestReplayRateAndETA(t *testing.T) {
	if r := service.ReplayRatePerHour(300, 10, 10*time.Minute); r != 240 {
		t.Fatalf("rate = %v, want 240 (300 - 10 players × 6 polls)", r)
	}
	if r := service.ReplayRatePerHour(300, 100, 5*time.Minute); r != 1 {
		t.Fatalf("rate floor = %v, want 1", r)
	}
	if d := service.ETA(480, 240); d != 2*time.Hour {
		t.Fatalf("ETA = %v", d)
	}
	if d := service.ETA(0, 240); d != 0 {
		t.Fatalf("ETA with nothing pending = %v", d)
	}
}
```

`internal/service/stats.go`:

```go
package service

import (
	"math"
	"time"
)

// ReplayRatePerHour estimates replay downloads per hour once polling is paid for.
func ReplayRatePerHour(hourlyBudget, enabledPlayers int, pollInterval time.Duration) float64 {
	if pollInterval <= 0 {
		pollInterval = DefaultSettings.PollInterval
	}
	pollCost := float64(enabledPlayers) * float64(time.Hour) / float64(pollInterval)
	return math.Max(float64(hourlyBudget)-pollCost, 1)
}

// ETA estimates how long `pending` downloads take at `perHour`.
func ETA(pending int64, perHour float64) time.Duration {
	if pending <= 0 || perHour <= 0 {
		return 0
	}
	return time.Duration(float64(pending) / perHour * float64(time.Hour))
}
```

Run: `go test ./internal/service/ -run ReplayRate` → `ok`.

- [ ] **Step 2: Write the failing web tests** — `internal/web/admin_sync_test.go`

```go
package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestSyncPage(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	e.status.st = archiver.Status{
		State: archiver.StateRunning, Task: "Downloading replay 42 · Alice · Song", Since: testutil.T0,
		Limiter: scoresaber.LimiterSnapshot{Windows: []scoresaber.WindowSnapshot{
			{Name: "short", Limit: 20, Used: 3, ServerRemaining: -1},
			{Name: "medium", Limit: 60, Used: 9, ServerRemaining: -1},
			{Name: "long", Limit: 300, Used: 120, ServerRemaining: 200},
		}},
	}
	body := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, body, "Running", "Downloading replay 42", "Last hour", "120 / 300", "ScoreSaber reports 200 remaining",
		"Alice", `hx-trigger="every 3s"`, `id="sync-events"`, `id="failed-replays"`)

	live := e.do(http.MethodGet, "/admin/sync/live", nil, withCookie(c), htmx("sync-live")).Body.String()
	if !strings.HasPrefix(strings.TrimSpace(live), `<div id="sync-live"`) || strings.Contains(live, "<html") {
		t.Fatalf("live partial wrong:\n%s", live)
	}
}

func TestSyncPauseResume(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	paused := e.do(http.MethodPost, "/admin/sync/pause", url.Values{}, withCookie(c), htmx("sync-live")).Body.String()
	contains(t, paused, "Resume", "Worker paused")
	if st, _ := e.svc.Settings(ctx); !st.WorkerPaused {
		t.Fatal("worker not paused")
	}
	resumed := e.do(http.MethodPost, "/admin/sync/resume", url.Values{}, withCookie(c), htmx("sync-live")).Body.String()
	contains(t, resumed, "Pause", "Worker resumed")
	if st, _ := e.svc.Settings(ctx); st.WorkerPaused {
		t.Fatal("worker still paused")
	}
}

func TestSyncEventsFilter(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	ctx := context.Background()
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindReplay, Message: "boom happened"})
	e.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindScores, Message: "all fine"})
	body := e.do(http.MethodGet, "/admin/sync/events?level=error", nil, withCookie(c), htmx("sync-events")).Body.String()
	if !strings.Contains(body, "boom happened") || strings.Contains(body, "all fine") {
		t.Fatalf("level filter not applied:\n%s", body)
	}
}

func TestSyncRetryFailed(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	e.seed()
	ctx := context.Background()
	for range service.MaxReplayAttempts {
		if _, _, err := e.svc.MarkReplayAttemptFailed(ctx, 2, errors.New("502 bad gateway")); err != nil {
			t.Fatal(err)
		}
	}
	page := e.do(http.MethodGet, "/admin/sync", nil, withCookie(c)).Body.String()
	contains(t, page, "Retry all", "502 bad gateway")
	rec := e.do(http.MethodPost, "/admin/sync/retry", url.Values{"score_id": {"2"}}, withCookie(c), htmx("failed-replays"))
	contains(t, rec.Body.String(), `id="failed-replays"`, "Re-queued 1 replay", "No failed replays")
	if s, _ := e.svc.GetScore(ctx, 2); s.ReplayState != model.ReplayPending {
		t.Fatalf("state = %s", s.ReplayState)
	}
}

func TestSyncRequiresLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if rec := e.do(http.MethodGet, "/admin/sync", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
}
```

Run: `go test ./internal/web/ -run Sync` → FAIL.

- [ ] **Step 3: Write the sync views** — `internal/web/views/sync.templ`

```templ
package views

import (
	"fmt"
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/components/badge"
	"github.com/yyewolf/ssarchiver/internal/web/components/button"
	"github.com/yyewolf/ssarchiver/internal/web/components/card"
	"github.com/yyewolf/ssarchiver/internal/web/components/icon"
	"github.com/yyewolf/ssarchiver/internal/web/components/nativeselect"
	"github.com/yyewolf/ssarchiver/internal/web/components/progress"
	"github.com/yyewolf/ssarchiver/internal/web/components/table"
	"github.com/yyewolf/ssarchiver/internal/web/components/toast"
)

type QueueRow struct {
	Player   service.PlayerSummary
	NextPoll time.Time
	ETA      time.Duration
}

type SyncView struct {
	Status       archiver.Status
	Paused       bool
	Queues       []QueueRow
	PendingTotal int64
	FailedTotal  int64
	RatePerHour  float64
	ETA          time.Duration
	Now          time.Time
}

type EventsView struct {
	Events  []*model.SyncEvent
	Total   int64
	Filter  service.EventFilter
	Players []service.PlayerSummary
	Now     time.Time
}

type FailedView struct {
	Items []*model.Score
	Total int64
	Page  int
	Pages int
	Now   time.Time
}

func windowLabel(name string) string {
	switch name {
	case "short":
		return "Last 10 seconds"
	case "medium":
		return "Last minute"
	case "long":
		return "Last hour"
	}
	return name
}

func etaText(d time.Duration) string {
	if d <= 0 {
		return "nothing queued"
	}
	return "about " + HumanDuration(d) + " left"
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return Number(n) + " " + word + "s"
}

func (v EventsView) playerName(id *string) string {
	if id == nil {
		return "—"
	}
	for _, p := range v.Players {
		if p.ID == *id {
			return p.Name
		}
	}
	return *id
}

func EventsURL(f service.EventFilter, page int) string {
	return fmt.Sprintf("/admin/sync/events?level=%s&kind=%s&player=%s&page=%d", f.Level, f.Kind, f.PlayerID, page)
}

templ SyncPage(p Page, v SyncView, ev EventsView, fv FailedView) {
	@Layout(p) {
		<div class="mb-8 flex flex-col gap-1">
			<h1 class="text-2xl font-semibold tracking-tight">Sync</h1>
			<p class="text-sm text-muted-foreground">What the archiver is doing right now. Refreshes every few seconds.</p>
		</div>
		@SyncLive(v)
		<section class="mt-10 flex flex-col gap-4">
			<h2 class="text-lg font-semibold tracking-tight">Activity</h2>
			@eventFilters(ev)
			@EventsTable(ev)
		</section>
		<section class="mt-10 flex flex-col gap-4">
			<h2 class="text-lg font-semibold tracking-tight">Failed replays</h2>
			@FailedTable(fv)
		</section>
	}
}

templ SyncLive(v SyncView) {
	<div id="sync-live" hx-get="/admin/sync/live" hx-trigger="every 3s" hx-swap="outerHTML" class="flex flex-col gap-6">
		<div class="grid gap-6 lg:grid-cols-2">
			@workerCard(v)
			@budgetCard(v)
		</div>
		@queueCard(v)
	</div>
}

templ SyncLiveToast(v SyncView, title string) {
	@SyncLive(v)
	@ToastOOB(toast.TypeSuccess, title, "")
}

templ StateBadge(s archiver.State) {
	switch s {
		case archiver.StateRunning:
			@badge.Badge() {
				Running
			}
		case archiver.StatePaused:
			@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
				Paused
			}
		case archiver.StateRateLimited:
			@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
				Rate limited
			}
		case archiver.StateStopped:
			@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
				Stopped
			}
		default:
			@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
				Idle
			}
	}
}

templ workerCard(v SyncView) {
	@card.Card() {
		@card.Header() {
			@card.Title() {
				Worker
			}
			@card.Description() {
				Polls players, walks their history and downloads replays.
			}
			@card.Action() {
				if v.Paused {
					@button.Button(button.Props{Size: button.SizeSm, Attributes: templ.Attributes{"hx-post": "/admin/sync/resume", "hx-target": "#sync-live", "hx-swap": "outerHTML"}}) {
						@icon.Play()
						Resume
					}
				} else {
					@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Attributes: templ.Attributes{"hx-post": "/admin/sync/pause", "hx-target": "#sync-live", "hx-swap": "outerHTML"}}) {
						@icon.Pause()
						Pause
					}
				}
			}
		}
		@card.Content() {
			<div class="flex flex-col gap-3 text-sm">
				<div class="flex items-center gap-2">
					@StateBadge(v.Status.State)
					<span class="text-muted-foreground">since { TimeAgo(v.Status.Since, v.Now) }</span>
				</div>
				if v.Status.Task != "" {
					<p class="truncate">{ v.Status.Task }</p>
				}
				if !v.Status.Limiter.BlockedUntil.IsZero() {
					<p class="text-muted-foreground">ScoreSaber asked us to wait; resuming { Until(v.Status.Limiter.BlockedUntil, time.Now()) }.</p>
				}
				<p class="text-muted-foreground">
					{ plural(v.PendingTotal, "replay") } pending · { fmt.Sprintf("~%.0f replays/hour", v.RatePerHour) } · { etaText(v.ETA) }
				</p>
			</div>
		}
	}
}

templ budgetCard(v SyncView) {
	@card.Card() {
		@card.Header() {
			@card.Title() {
				ScoreSaber budget
			}
			@card.Description() {
				Requests sent by this instance in each ScoreSaber rate-limit window.
			}
		}
		@card.Content() {
			<div class="flex flex-col gap-4">
				for _, w := range v.Status.Limiter.Windows {
					<div class="flex flex-col gap-1.5">
						<div class="flex justify-between text-sm">
							<span>{ windowLabel(w.Name) }</span>
							<span class="tabular-nums text-muted-foreground">{ strconv.Itoa(w.Used) } / { strconv.Itoa(w.Limit) }</span>
						</div>
						@progress.Progress(progress.Props{Value: w.Used, Max: max(w.Limit, 1)})
						if w.ServerRemaining >= 0 {
							<p class="text-xs text-muted-foreground">ScoreSaber reports { strconv.Itoa(w.ServerRemaining) } remaining</p>
						}
					</div>
				}
				if len(v.Status.Limiter.Windows) == 0 {
					<p class="text-sm text-muted-foreground">No requests sent yet.</p>
				}
			</div>
		}
	}
}

templ queueCard(v SyncView) {
	@card.Card() {
		@card.Header() {
			@card.Title() {
				Queue
			}
			@card.Description() {
				Per-player progress. Estimates assume the current budget and poll interval.
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
								@table.Cell(table.CellProps{Class: "text-muted-foreground"}) {
									if q.Player.Enabled {
										{ Until(q.NextPoll, v.Now) }
									} else {
										—
									}
								}
								@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
									{ backfillLabel(q.Player.Player) }
								}
								@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
									{ Number(q.Player.Counts.Archived) }
								}
								@table.Cell(table.CellProps{Class: "text-right tabular-nums"}) {
									{ Number(q.Player.Counts.Pending) }
								}
								@table.Cell(table.CellProps{Class: "hidden text-right tabular-nums sm:table-cell"}) {
									{ Number(q.Player.Counts.Failed) }
								}
								@table.Cell(table.CellProps{Class: "hidden text-right tabular-nums sm:table-cell"}) {
									{ Number(q.Player.Counts.Gone) }
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

templ eventFilters(ev EventsView) {
	<form id="event-filters" class="flex flex-wrap gap-2" hx-get="/admin/sync/events" hx-target="#sync-events" hx-swap="outerHTML" hx-trigger="change">
		@nativeselect.NativeSelect(nativeselect.Props{Name: "level", Attributes: templ.Attributes{"aria-label": "Level"}}) {
			for _, o := range []string{"", model.LevelInfo, model.LevelWarn, model.LevelError} {
				@nativeselect.Option(nativeselect.OptionProps{Value: o, Selected: ev.Filter.Level == o}) {
					if o == "" {
						All levels
					} else {
						{ o }
					}
				}
			}
		}
		@nativeselect.NativeSelect(nativeselect.Props{Name: "kind", Attributes: templ.Attributes{"aria-label": "Kind"}}) {
			for _, o := range []string{"", model.KindPoll, model.KindScores, model.KindReplay, model.KindBackfill, model.KindRateLimit, model.KindWorker} {
				@nativeselect.Option(nativeselect.OptionProps{Value: o, Selected: ev.Filter.Kind == o}) {
					if o == "" {
						All kinds
					} else {
						{ o }
					}
				}
			}
		}
		@nativeselect.NativeSelect(nativeselect.Props{Name: "player", Attributes: templ.Attributes{"aria-label": "Player"}}) {
			@nativeselect.Option(nativeselect.OptionProps{Value: "", Selected: ev.Filter.PlayerID == ""}) {
				All players
			}
			for _, p := range ev.Players {
				@nativeselect.Option(nativeselect.OptionProps{Value: p.ID, Selected: ev.Filter.PlayerID == p.ID}) {
					{ p.Name }
				}
			}
		}
	</form>
}

templ levelBadge(level string) {
	switch level {
		case model.LevelError:
			@badge.Badge(badge.Props{Variant: badge.VariantDestructive}) {
				error
			}
		case model.LevelWarn:
			@badge.Badge(badge.Props{Variant: badge.VariantOutline}) {
				warn
			}
		default:
			@badge.Badge(badge.Props{Variant: badge.VariantSecondary}) {
				info
			}
	}
}

templ EventsTable(ev EventsView) {
	<div id="sync-events">
		if len(ev.Events) == 0 {
			<p class="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">No events.</p>
		} else {
			<div class="rounded-lg border">
				@table.Table() {
					@table.Header() {
						@table.Row() {
							@table.Head() {
								When
							}
							@table.Head() {
								Level
							}
							@table.Head(table.HeadProps{Class: "hidden md:table-cell"}) {
								Kind
							}
							@table.Head(table.HeadProps{Class: "hidden md:table-cell"}) {
								Player
							}
							@table.Head() {
								Message
							}
						}
					}
					@table.Body() {
						for _, e := range ev.Events {
							@table.Row() {
								@table.Cell(table.CellProps{Class: "whitespace-nowrap text-muted-foreground", Attributes: templ.Attributes{"title": e.At.Format(time.RFC3339)}}) {
									{ TimeAgo(e.At, ev.Now) }
								}
								@table.Cell() {
									@levelBadge(e.Level)
								}
								@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
									{ e.Kind }
								}
								@table.Cell(table.CellProps{Class: "hidden md:table-cell"}) {
									{ ev.playerName(e.PlayerID) }
								}
								@table.Cell(table.CellProps{Class: "max-w-md whitespace-normal break-words"}) {
									{ e.Message }
								}
							}
						}
					}
				}
			</div>
			{{ pages := int((ev.Total + int64(max(ev.Filter.PerPage, 1)) - 1) / int64(max(ev.Filter.PerPage, 1))) }}
			if pages > 1 {
				<div class="mt-3 flex items-center justify-between text-sm text-muted-foreground">
					<span>Page { strconv.Itoa(ev.Filter.Page) } of { strconv.Itoa(pages) }</span>
					<div class="flex gap-2">
						if ev.Filter.Page > 1 {
							@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Attributes: templ.Attributes{"hx-get": EventsURL(ev.Filter, ev.Filter.Page-1), "hx-target": "#sync-events", "hx-swap": "outerHTML"}}) {
								Newer
							}
						}
						if ev.Filter.Page < pages {
							@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Attributes: templ.Attributes{"hx-get": EventsURL(ev.Filter, ev.Filter.Page+1), "hx-target": "#sync-events", "hx-swap": "outerHTML"}}) {
								Older
							}
						}
					</div>
				</div>
			}
		}
	</div>
}

templ FailedTable(fv FailedView) {
	<div id="failed-replays">
		if fv.Total == 0 {
			<p class="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">No failed replays.</p>
		} else {
			<div class="mb-3 flex items-center justify-between">
				<span class="text-sm text-muted-foreground">{ plural(fv.Total, "replay") } gave up after { strconv.Itoa(service.MaxReplayAttempts) } attempts</span>
				@button.Button(button.Props{Variant: button.VariantOutline, Size: button.SizeSm, Attributes: templ.Attributes{"hx-post": "/admin/sync/retry", "hx-target": "#failed-replays", "hx-swap": "outerHTML"}}) {
					@icon.RefreshCw()
					Retry all
				}
			</div>
			<div class="rounded-lg border">
				@table.Table() {
					@table.Header() {
						@table.Row() {
							@table.Head() {
								Replay
							}
							@table.Head(table.HeadProps{Class: "hidden md:table-cell"}) {
								Last error
							}
							@table.Head(table.HeadProps{Class: "hidden sm:table-cell"}) {
								Set
							}
							@table.Head(table.HeadProps{Class: "text-right"}) {
								<span class="sr-only">Actions</span>
							}
						}
					}
					@table.Body() {
						for _, s := range fv.Items {
							@table.Row() {
								@table.Cell() {
									<a href={ templ.SafeURL(ScoreURL(s.ID)) } class="underline-offset-4 hover:underline">
										<span class="block font-medium">{ SongTitle(s) }</span>
										<span class="block text-xs text-muted-foreground">{ PlayerName(s) } · #{ strconv.FormatInt(s.ID, 10) }</span>
									</a>
								}
								@table.Cell(table.CellProps{Class: "hidden max-w-sm whitespace-normal break-words text-xs text-muted-foreground md:table-cell"}) {
									{ s.LastError }
								}
								@table.Cell(table.CellProps{Class: "hidden text-muted-foreground sm:table-cell"}) {
									{ TimeAgo(s.SetAt, fv.Now) }
								}
								@table.Cell(table.CellProps{Class: "text-right"}) {
									@button.Button(button.Props{Variant: button.VariantGhost, Size: button.SizeSm, Attributes: templ.Attributes{"hx-post": "/admin/sync/retry", "hx-vals": fmt.Sprintf(`{"score_id":"%d"}`, s.ID), "hx-target": "#failed-replays", "hx-swap": "outerHTML"}}) {
										Retry
									}
								}
							}
						}
					}
				}
			</div>
		}
	</div>
}

templ FailedRetried(fv FailedView, n int64) {
	@FailedTable(fv)
	@ToastOOB(toast.TypeSuccess, "Re-queued "+plural(n, "replay"), "")
}
```

- [ ] **Step 4: Implement handlers** — `internal/web/admin_sync.go`

```go
package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/web/views"
)

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
			if p.Counts.Pending > 0 {
				withPending++
			}
		}
		pending += p.Counts.Pending
		failed += p.Counts.Failed
	}
	rate := service.ReplayRatePerHour(h.cfg.HourlyBudget, enabled, st.PollInterval)
	rows := make([]views.QueueRow, 0, len(players))
	for _, p := range players {
		next := now
		if p.LastPolledAt != nil {
			next = p.LastPolledAt.Add(st.PollInterval)
		}
		var eta time.Duration
		if p.Enabled && withPending > 0 {
			eta = service.ETA(p.Counts.Pending, rate/float64(withPending))
		}
		rows = append(rows, views.QueueRow{Player: p, NextPoll: next, ETA: eta})
	}
	return views.SyncView{
		Status: h.status.Status(), Paused: st.WorkerPaused, Queues: rows,
		PendingTotal: pending, FailedTotal: failed, RatePerHour: rate, ETA: service.ETA(pending, rate), Now: now,
	}, nil
}

func (h *Handler) eventsView(r *http.Request) (views.EventsView, error) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	f := service.EventFilter{Level: q.Get("level"), Kind: q.Get("kind"), PlayerID: q.Get("player"), Page: max(page, 1), PerPage: 50}
	events, total, err := h.svc.ListEvents(r.Context(), f)
	if err != nil {
		return views.EventsView{}, err
	}
	players, err := h.svc.ListPlayers(r.Context(), true)
	return views.EventsView{Events: events, Total: total, Filter: f, Players: players, Now: h.svc.Now()}, err
}

func (h *Handler) failedView(r *http.Request) (views.FailedView, error) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	items, total, err := h.svc.ListFailedReplays(r.Context(), page, 50)
	pages := int((total + 49) / 50)
	return views.FailedView{Items: items, Total: total, Page: page, Pages: max(pages, 1), Now: h.svc.Now()}, err
}

func (h *Handler) syncPage(w http.ResponseWriter, r *http.Request) {
	v, err := h.syncView(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	ev, err := h.eventsView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	fv, err := h.failedView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.SyncPage(h.page(r, "Sync"), v, ev, fv))
}

func (h *Handler) syncLive(w http.ResponseWriter, r *http.Request) {
	v, err := h.syncView(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.SyncLive(v))
}

func (h *Handler) syncEvents(w http.ResponseWriter, r *http.Request) {
	ev, err := h.eventsView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.EventsTable(ev))
}

func (h *Handler) syncFailed(w http.ResponseWriter, r *http.Request) {
	fv, err := h.failedView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.FailedTable(fv))
}

func (h *Handler) setPaused(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h.svc.SetWorkerPaused(r.Context(), paused); err != nil {
			h.serverError(w, r, err)
			return
		}
		v, err := h.syncView(r.Context())
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		title := "Worker resumed"
		if paused {
			title = "Worker paused"
		}
		render(w, r, http.StatusOK, views.SyncLiveToast(v, title))
	}
}

func (h *Handler) syncRetry(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	scoreID, _ := strconv.ParseInt(r.PostFormValue("score_id"), 10, 64)
	n, err := h.svc.RetryFailed(r.Context(), r.PostFormValue("player_id"), scoreID)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	fv, err := h.failedView(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, views.FailedRetried(fv, n))
}
```

Register in `Routes`:

```go
	mux.HandleFunc("GET /admin/sync", h.requireAdmin(h.syncPage))
	mux.HandleFunc("GET /admin/sync/live", h.requireAdmin(h.syncLive))
	mux.HandleFunc("GET /admin/sync/events", h.requireAdmin(h.syncEvents))
	mux.HandleFunc("GET /admin/sync/failed", h.requireAdmin(h.syncFailed))
	mux.HandleFunc("POST /admin/sync/pause", h.requireAdmin(h.setPaused(true)))
	mux.HandleFunc("POST /admin/sync/resume", h.requireAdmin(h.setPaused(false)))
	mux.HandleFunc("POST /admin/sync/retry", h.requireAdmin(h.syncRetry))
```

- [ ] **Step 5: Generate and test**

```bash
go tool templ generate && make gen-css && go test ./internal/web/... ./internal/service/...
```

Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
make generate && make lint
git add internal/web internal/service
git commit -m "feat: add sync status page with budget, queue, activity log and retries"
```

---
## Task 17: huma JSON API

**Files:**
- Create: `internal/api/api.go`, `internal/api/dto.go`, `internal/api/players.go`, `internal/api/scores.go`, `internal/api/sync.go`, `internal/api/api_test.go`

**Interfaces:**
- Consumes: service methods (Tasks 6–7, 16), `archiver.Status`, `httpx.UserFrom`, `httpx.BaseURLFrom`, `httpx.SessionCookie`, `views`-free (the API builds its own URLs).
- Produces:
  - `api.Register(mux *http.ServeMux, svc *service.Service, status StatusSource, version string) huma.API`; `api.StatusSource interface{ Status() archiver.Status }`
  - OpenAPI at `/api/openapi.json` (+ `.yaml`), docs at `/api/docs`
  - Public: `GET /api/v1/players` (`list-players`), `GET /api/v1/players/{id}` (`get-player`), `GET /api/v1/players/{id}/scores` (`list-player-scores`; query `page`, `per_page` ≤ 100, `search`, `state` ∈ {replay, archived}, `ranked`), `GET /api/v1/scores/{id}` (`get-score`)
  - Admin (session cookie, security scheme `session`): `POST /api/v1/players` (`add-player`, body `{"ref": "..."}` → 201), `PATCH /api/v1/players/{id}` (`update-player`, body `{"enabled": bool}`), `DELETE /api/v1/players/{id}?delete_files=bool` (`delete-player` → 204), `POST /api/v1/players/{id}/poll` (`poll-player` → 202), `GET /api/v1/sync` (`get-sync-status`), `POST /api/v1/sync/pause` / `resume` (→ 204), `POST /api/v1/sync/retry` (body `{"player_id"?, "score_id"?}` → `{"requeued": n}`)
  - Error mapping: `ErrNotFound`→404, `ErrPlayerExists`→409, `ErrInvalidPlayerRef`→422, anything else → 500 (logged); missing session → 401.

- [ ] **Step 1: Allow the docs CDN** — huma's default docs page loads its renderer from a CDN. In `internal/httpx/httpx.go` change `DocsCSP` to also allow `https://cdn.jsdelivr.net` in `script-src` and `style-src`:

```go
	DocsCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' https://unpkg.com https://cdn.jsdelivr.net; " +
		"style-src 'self' 'unsafe-inline' https://unpkg.com https://cdn.jsdelivr.net; " +
		"img-src 'self' data: https:; font-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'"
```

- [ ] **Step 2: Write the failing tests** — `internal/api/api_test.go`

```go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/api"
	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

type statusStub struct{}

func (statusStub) Status() archiver.Status { return archiver.Status{State: archiver.StateIdle} }

func newAPI(t *testing.T, admin bool) (*service.Service, http.Handler) {
	t.Helper()
	svc, _, _ := testutil.NewService(t)
	mux := http.NewServeMux()
	api.Register(mux, svc, statusStub{}, "test")
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := httpx.WithBaseURL(r.Context(), "https://replays.example.com")
		if admin {
			ctx = httpx.WithUser(ctx, &model.User{ID: 1, Username: "admin"})
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	return svc, h
}

func call(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any, []any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var obj map[string]any
	var arr []any
	raw := rec.Body.Bytes()
	if len(raw) > 0 && raw[0] == '[' {
		_ = json.Unmarshal(raw, &arr)
	} else {
		_ = json.Unmarshal(raw, &obj)
	}
	return rec.Code, obj, arr
}

func seed(t *testing.T, svc *service.Service) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.AddPlayer(ctx, "1001"); err != nil {
		t.Fatal(err)
	}
	items := []scoresaber.ScoreItem{
		testutil.Item("1001", 1, 501, testutil.T0.Add(2*time.Minute), true),
		testutil.Item("1001", 2, 502, testutil.T0.Add(time.Minute), true),
	}
	if _, err := svc.UpsertScores(ctx, "1001", items); err != nil {
		t.Fatal(err)
	}
	size, sum, _ := svc.Store().Put("1001", 1, strings.NewReader("replay"))
	if err := svc.MarkReplayArchived(ctx, 1, size, sum); err != nil {
		t.Fatal(err)
	}
}

func TestPublicReads(t *testing.T) {
	svc, h := newAPI(t, false)
	seed(t, svc)

	code, _, players := call(t, h, http.MethodGet, "/api/v1/players", nil)
	if code != 200 || len(players) != 1 {
		t.Fatalf("list players = %d %v", code, players)
	}
	p := players[0].(map[string]any)
	if p["name"] != "Alice" || p["replays"].(map[string]any)["archived"].(float64) != 1 || p["url"] != "https://replays.example.com/p/1001" {
		t.Fatalf("player = %v", p)
	}

	code, page, _ := call(t, h, http.MethodGet, "/api/v1/players/1001/scores?state=archived", nil)
	items := page["items"].([]any)
	if code != 200 || page["total"].(float64) != 1 || len(items) != 1 {
		t.Fatalf("scores = %d %v", code, page)
	}
	replay := items[0].(map[string]any)["replay"].(map[string]any)
	if replay["download_url"] != "https://replays.example.com/r/1.dat" || replay["embed_url"] != "https://replays.example.com/embed/1" {
		t.Fatalf("replay = %v", replay)
	}

	code, sc, _ := call(t, h, http.MethodGet, "/api/v1/scores/2", nil)
	r2 := sc["replay"].(map[string]any)
	if code != 200 || r2["state"] != "pending" || r2["download_url"] != nil {
		t.Fatalf("pending score = %d %v", code, sc)
	}
	if lb := sc["leaderboard"].(map[string]any); lb["difficulty"] != "Expert+" || lb["song_name"] != "Song 502" {
		t.Fatalf("leaderboard = %v", lb)
	}

	for path, want := range map[string]int{
		"/api/v1/players/9":                       404,
		"/api/v1/players/9/scores":                404,
		"/api/v1/scores/999":                      404,
		"/api/v1/players/1001/scores?per_page=1000": 422,
		"/api/v1/players/1001/scores?state=bogus":   422,
	} {
		if code, _, _ := call(t, h, http.MethodGet, path, nil); code != want {
			t.Errorf("%s = %d, want %d", path, code, want)
		}
	}
}

func TestAdminRequiresSession(t *testing.T) {
	_, h := newAPI(t, false)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/players"},
		{http.MethodPatch, "/api/v1/players/1001"},
		{http.MethodDelete, "/api/v1/players/1001"},
		{http.MethodPost, "/api/v1/players/1001/poll"},
		{http.MethodGet, "/api/v1/sync"},
		{http.MethodPost, "/api/v1/sync/pause"},
		{http.MethodPost, "/api/v1/sync/retry"},
	} {
		code, _, _ := call(t, h, c.method, c.path, map[string]any{"ref": "1001", "enabled": true})
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, code)
		}
	}
}

func TestAdminWrites(t *testing.T) {
	svc, h := newAPI(t, true)
	ctx := context.Background()

	code, p, _ := call(t, h, http.MethodPost, "/api/v1/players", map[string]any{"ref": "https://scoresaber.com/u/1002"})
	if code != 201 || p["name"] != "Bob" {
		t.Fatalf("add = %d %v", code, p)
	}
	for ref, want := range map[string]int{"1002": 409, "nope": 422, "9999": 404} {
		if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players", map[string]any{"ref": ref}); code != want {
			t.Errorf("add %q = %d, want %d", ref, code, want)
		}
	}
	code, p, _ = call(t, h, http.MethodPatch, "/api/v1/players/1002", map[string]any{"enabled": false})
	if code != 200 || p["enabled"] != false {
		t.Fatalf("patch = %d %v", code, p)
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/players/1002/poll", nil); code != 202 {
		t.Fatalf("poll = %d", code)
	}
	code, st, _ := call(t, h, http.MethodGet, "/api/v1/sync", nil)
	if code != 200 || st["state"] != "idle" {
		t.Fatalf("sync = %d %v", code, st)
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/sync/pause", nil); code != 204 {
		t.Fatalf("pause = %d", code)
	}
	if s, _ := svc.Settings(ctx); !s.WorkerPaused {
		t.Fatal("not paused")
	}
	if code, _, _ := call(t, h, http.MethodPost, "/api/v1/sync/resume", nil); code != 204 {
		t.Fatalf("resume = %d", code)
	}
	code, rr, _ := call(t, h, http.MethodPost, "/api/v1/sync/retry", map[string]any{})
	if code != 200 || rr["requeued"].(float64) != 0 {
		t.Fatalf("retry = %d %v", code, rr)
	}
	if code, _, _ := call(t, h, http.MethodDelete, "/api/v1/players/1002?delete_files=true", nil); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, _, _ := call(t, h, http.MethodDelete, "/api/v1/players/1002", nil); code != 404 {
		t.Fatalf("second delete = %d", code)
	}
}

func TestOpenAPIDocument(t *testing.T) {
	_, h := newAPI(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`"list-players"`, `"add-player"`, `"session"`, `"ssa_session"`} {
		if !strings.Contains(body, want) {
			t.Errorf("openapi missing %s", want)
		}
	}
	docs := httptest.NewRecorder()
	h.ServeHTTP(docs, httptest.NewRequest(http.MethodGet, "/api/docs", nil))
	if docs.Code != 200 {
		t.Errorf("docs = %d", docs.Code)
	}
}
```

Run: `go get github.com/danielgtaylor/huma/v2@v2.39.1 && go test ./internal/api/...` → FAIL (`undefined: api.Register`).

- [ ] **Step 3: Write the DTOs** — `internal/api/dto.go`

```go
package api

import (
	"strconv"
	"strings"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type ReplayCounts struct {
	Scores   int64 `json:"scores" doc:"All stored scores"`
	Archived int64 `json:"archived"`
	Pending  int64 `json:"pending"`
	Failed   int64 `json:"failed"`
	Gone     int64 `json:"gone" doc:"Pruned by ScoreSaber before they could be archived"`
}

type Backfill struct {
	State      string `json:"state" enum:"pending,running,done"`
	NextPage   int    `json:"next_page"`
	TotalPages int    `json:"total_pages"`
}

type Player struct {
	ID           string       `json:"id" example:"76561198059961776"`
	Name         string       `json:"name"`
	AvatarURL    string       `json:"avatar_url"`
	Country      string       `json:"country"`
	Enabled      bool         `json:"enabled"`
	AddedAt      time.Time    `json:"added_at"`
	LastPolledAt *time.Time   `json:"last_polled_at,omitempty"`
	LastError    string       `json:"last_error,omitempty"`
	Backfill     Backfill     `json:"backfill"`
	Replays      ReplayCounts `json:"replays"`
	URL          string       `json:"url"`
}

type Leaderboard struct {
	ID            int64   `json:"id"`
	SongHash      string  `json:"song_hash"`
	SongName      string  `json:"song_name"`
	SongSubName   string  `json:"song_sub_name"`
	SongAuthor    string  `json:"song_author"`
	Mapper        string  `json:"mapper"`
	Difficulty    string  `json:"difficulty" example:"Expert+"`
	DifficultyRaw string  `json:"difficulty_raw"`
	GameMode      string  `json:"game_mode"`
	CoverURL      string  `json:"cover_url"`
	Status        string  `json:"status" example:"RANKED"`
	Stars         float64 `json:"stars"`
}

type Replay struct {
	State       string     `json:"state" enum:"none,pending,archived,gone,failed"`
	Size        int64      `json:"size,omitempty"`
	SHA256      string     `json:"sha256,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	DownloadURL string     `json:"download_url,omitempty"`
	EmbedURL    string     `json:"embed_url,omitempty"`
}

type Score struct {
	ID          int64        `json:"id"`
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

func difficultyName(d int) string {
	switch d {
	case 1:
		return "Easy"
	case 3:
		return "Normal"
	case 5:
		return "Hard"
	case 7:
		return "Expert"
	case 9:
		return "Expert+"
	}
	return "Unknown"
}

func playerDTO(base string, p service.PlayerSummary) Player {
	return Player{
		ID: p.ID, Name: p.Name, AvatarURL: p.AvatarURL, Country: p.Country, Enabled: p.Enabled,
		AddedAt: p.AddedAt, LastPolledAt: p.LastPolledAt, LastError: p.LastError,
		Backfill: Backfill{State: p.BackfillState, NextPage: p.BackfillPage, TotalPages: p.BackfillTotalPages},
		Replays:  ReplayCounts{Scores: p.Counts.Scores, Archived: p.Counts.Archived, Pending: p.Counts.Pending, Failed: p.Counts.Failed, Gone: p.Counts.Gone},
		URL:      base + "/p/" + p.ID,
	}
}

func scoreDTO(base string, s *model.Score) Score {
	id := strconv.FormatInt(s.ID, 10)
	mods := []string{}
	if s.Mods != "" {
		mods = strings.Split(s.Mods, ",")
	}
	out := Score{
		ID: s.ID, PlayerID: s.PlayerID, Rank: s.Rank, Score: s.ModifiedScore, Accuracy: s.Accuracy, PP: s.PP,
		Mods: mods, FullCombo: s.FullCombo, MissedNotes: s.MissedNotes, BadCuts: s.BadCuts, MaxCombo: s.MaxCombo,
		HMD: s.HMD, SetAt: s.SetAt, Replay: Replay{State: s.ReplayState}, URL: base + "/s/" + id,
	}
	if lb := s.Leaderboard; lb != nil {
		out.Leaderboard = &Leaderboard{
			ID: lb.ID, SongHash: lb.SongHash, SongName: lb.SongName, SongSubName: lb.SongSubName, SongAuthor: lb.SongAuthor,
			Mapper: lb.Mapper, Difficulty: difficultyName(lb.Difficulty), DifficultyRaw: lb.DifficultyRaw, GameMode: lb.GameMode,
			CoverURL: lb.CoverURL, Status: lb.Status, Stars: lb.Stars,
		}
	}
	if s.ReplayState == model.ReplayArchived {
		out.Replay.Size, out.Replay.SHA256, out.Replay.ArchivedAt = s.ReplaySize, s.ReplaySHA256, s.ArchivedAt
		out.Replay.DownloadURL = base + "/r/" + id + ".dat"
		out.Replay.EmbedURL = base + "/embed/" + id
	}
	return out
}
```

- [ ] **Step 4: Write registration and helpers** — `internal/api/api.go`

```go
// Package api exposes SSArchiver over a JSON API built with huma.
package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type StatusSource interface {
	Status() archiver.Status
}

type API struct {
	svc    *service.Service
	status StatusSource
	api    huma.API
}

var adminSecurity = []map[string][]string{{"session": {}}}

// Register mounts the API and its docs on mux.
func Register(mux *http.ServeMux, svc *service.Service, status StatusSource, version string) huma.API {
	cfg := huma.DefaultConfig("SSArchiver API", version)
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = "/api/schemas"
	cfg.Info.Description = "Archived ScoreSaber replays. Read endpoints are public; write endpoints need the admin session cookie."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"session": {Type: "apiKey", In: "cookie", Name: httpx.SessionCookie},
	}
	a := &API{svc: svc, status: status}
	a.api = humago.New(mux, cfg)
	a.registerPlayers()
	a.registerScores()
	a.registerSync()
	return a.api
}

func (a *API) requireAdmin(ctx huma.Context, next func(huma.Context)) {
	if httpx.UserFrom(ctx.Context()) == nil {
		_ = huma.WriteErr(a.api, ctx, http.StatusUnauthorized, "admin session required")
		return
	}
	next(ctx)
}

func (a *API) admin(op huma.Operation) huma.Operation {
	op.Security = adminSecurity
	op.Middlewares = huma.Middlewares{a.requireAdmin}
	op.Tags = append(op.Tags, "Admin")
	return op
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, service.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, service.ErrPlayerExists):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, service.ErrInvalidPlayerRef):
		return huma.Error422UnprocessableEntity(err.Error())
	}
	slog.Error("api request failed", "err", err)
	return huma.Error500InternalServerError("internal error")
}
```

- [ ] **Step 5: Players and scores operations** — `internal/api/players.go`

```go
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type PlayerPath struct {
	ID string `path:"id" pattern:"^[0-9]{1,32}$" doc:"ScoreSaber player ID"`
}

type PlayerOutput struct{ Body Player }

type ListPlayersOutput struct{ Body []Player }

type AddPlayerInput struct {
	Body struct {
		Ref string `json:"ref" minLength:"1" maxLength:"200" doc:"ScoreSaber player ID or profile URL"`
	}
}

type UpdatePlayerInput struct {
	PlayerPath
	Body struct {
		Enabled *bool `json:"enabled,omitempty" doc:"Pause or resume tracking"`
	}
}

type DeletePlayerInput struct {
	PlayerPath
	DeleteFiles bool `query:"delete_files" doc:"Also delete archived replay files from disk"`
}

func (a *API) summary(ctx context.Context, id string) (Player, error) {
	p, err := a.svc.GetPlayer(ctx, id)
	if err != nil {
		return Player{}, err
	}
	c, err := a.svc.PlayerCounts(ctx, id)
	if err != nil {
		return Player{}, err
	}
	return playerDTO(httpx.BaseURLFrom(ctx), service.PlayerSummary{Player: *p, Counts: c}), nil
}

func (a *API) registerPlayers() {
	huma.Register(a.api, huma.Operation{
		OperationID: "list-players", Method: http.MethodGet, Path: "/api/v1/players",
		Summary: "List tracked players", Tags: []string{"Players"},
	}, func(ctx context.Context, _ *struct{}) (*ListPlayersOutput, error) {
		ps, err := a.svc.ListPlayers(ctx, false)
		if err != nil {
			return nil, mapErr(err)
		}
		out := &ListPlayersOutput{Body: make([]Player, 0, len(ps))}
		for _, p := range ps {
			out.Body = append(out.Body, playerDTO(httpx.BaseURLFrom(ctx), p))
		}
		return out, nil
	})

	huma.Register(a.api, huma.Operation{
		OperationID: "get-player", Method: http.MethodGet, Path: "/api/v1/players/{id}",
		Summary: "Get a player", Tags: []string{"Players"},
	}, func(ctx context.Context, in *PlayerPath) (*PlayerOutput, error) {
		p, err := a.summary(ctx, in.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: p}, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "add-player", Method: http.MethodPost, Path: "/api/v1/players", DefaultStatus: http.StatusCreated,
		Summary: "Start tracking a player", Tags: []string{"Players"},
	}), func(ctx context.Context, in *AddPlayerInput) (*PlayerOutput, error) {
		p, err := a.svc.AddPlayer(ctx, in.Body.Ref)
		if err != nil {
			return nil, mapErr(err)
		}
		dto, err := a.summary(ctx, p.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: dto}, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "update-player", Method: http.MethodPatch, Path: "/api/v1/players/{id}",
		Summary: "Update a tracked player", Tags: []string{"Players"},
	}), func(ctx context.Context, in *UpdatePlayerInput) (*PlayerOutput, error) {
		if in.Body.Enabled != nil {
			if err := a.svc.SetPlayerEnabled(ctx, in.ID, *in.Body.Enabled); err != nil {
				return nil, mapErr(err)
			}
		}
		dto, err := a.summary(ctx, in.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &PlayerOutput{Body: dto}, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "delete-player", Method: http.MethodDelete, Path: "/api/v1/players/{id}", DefaultStatus: http.StatusNoContent,
		Summary: "Stop tracking a player and remove their scores", Tags: []string{"Players"},
	}), func(ctx context.Context, in *DeletePlayerInput) (*struct{}, error) {
		if err := a.svc.DeletePlayer(ctx, in.ID, in.DeleteFiles); err != nil {
			return nil, mapErr(err)
		}
		return nil, nil
	})

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "poll-player", Method: http.MethodPost, Path: "/api/v1/players/{id}/poll", DefaultStatus: http.StatusAccepted,
		Summary: "Poll a player as soon as possible", Tags: []string{"Players"},
	}), func(ctx context.Context, in *PlayerPath) (*struct{}, error) {
		if err := a.svc.RequestPoll(ctx, in.ID); err != nil {
			return nil, mapErr(err)
		}
		return nil, nil
	})
}
```

`internal/api/scores.go`:

```go
package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/yyewolf/ssarchiver/internal/httpx"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type ListScoresInput struct {
	PlayerPath
	Page    int    `query:"page" minimum:"1" default:"1"`
	PerPage int    `query:"per_page" minimum:"1" maximum:"100" default:"50"`
	Search  string `query:"search" maxLength:"64" doc:"Matches song name, artist or mapper"`
	State   string `query:"state" enum:"replay,archived" doc:"replay: ScoreSaber offered a replay; archived: stored here"`
	Ranked  bool   `query:"ranked" doc:"Only ranked maps"`
}

type ScorePage struct {
	Items   []Score `json:"items"`
	Total   int64   `json:"total"`
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
	Pages   int     `json:"pages"`
}

type ListScoresOutput struct{ Body ScorePage }

type ScorePath struct {
	ID int64 `path:"id" minimum:"1"`
}

type ScoreOutput struct{ Body Score }

func (a *API) registerScores() {
	huma.Register(a.api, huma.Operation{
		OperationID: "list-player-scores", Method: http.MethodGet, Path: "/api/v1/players/{id}/scores",
		Summary: "List a player's scores, newest first", Tags: []string{"Scores"},
	}, func(ctx context.Context, in *ListScoresInput) (*ListScoresOutput, error) {
		if _, err := a.svc.GetPlayer(ctx, in.ID); err != nil {
			return nil, mapErr(err)
		}
		list, err := a.svc.ListScores(ctx, service.ScoreFilter{
			PlayerID: in.ID, Search: in.Search, RankedOnly: in.Ranked, State: in.State, Page: in.Page, PerPage: in.PerPage,
		})
		if err != nil {
			return nil, mapErr(err)
		}
		base := httpx.BaseURLFrom(ctx)
		out := &ListScoresOutput{Body: ScorePage{Items: make([]Score, 0, len(list.Items)), Total: list.Total, Page: list.Page, PerPage: list.PerPage, Pages: list.Pages}}
		for _, s := range list.Items {
			out.Body.Items = append(out.Body.Items, scoreDTO(base, s))
		}
		return out, nil
	})

	huma.Register(a.api, huma.Operation{
		OperationID: "get-score", Method: http.MethodGet, Path: "/api/v1/scores/{id}",
		Summary: "Get a score and its replay status", Tags: []string{"Scores"},
	}, func(ctx context.Context, in *ScorePath) (*ScoreOutput, error) {
		s, err := a.svc.GetScore(ctx, in.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &ScoreOutput{Body: scoreDTO(httpx.BaseURLFrom(ctx), s)}, nil
	})
}
```

`internal/api/sync.go`:

```go
package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

type Window struct {
	Name            string `json:"name" enum:"short,medium,long"`
	Limit           int    `json:"limit"`
	Used            int    `json:"used"`
	ServerRemaining *int   `json:"server_remaining,omitempty"`
}

type SyncStatus struct {
	State      string     `json:"state" enum:"idle,running,paused,ratelimited,stopped"`
	Task       string     `json:"task,omitempty"`
	Since      time.Time  `json:"since"`
	Paused     bool       `json:"paused"`
	BlockedTil *time.Time `json:"blocked_until,omitempty"`
	Windows    []Window   `json:"windows"`
}

type SyncStatusOutput struct{ Body SyncStatus }

type RetryInput struct {
	Body struct {
		PlayerID string `json:"player_id,omitempty" pattern:"^[0-9]{1,32}$"`
		ScoreID  int64  `json:"score_id,omitempty" minimum:"0"`
	}
}

type RetryOutput struct {
	Body struct {
		Requeued int64 `json:"requeued"`
	}
}

func (a *API) registerSync() {
	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "get-sync-status", Method: http.MethodGet, Path: "/api/v1/sync",
		Summary: "Worker status and rate-limit usage", Tags: []string{"Sync"},
	}), func(ctx context.Context, _ *struct{}) (*SyncStatusOutput, error) {
		st := a.status.Status()
		settings, err := a.svc.Settings(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		out := SyncStatus{State: string(st.State), Task: st.Task, Since: st.Since, Paused: settings.WorkerPaused, Windows: []Window{}}
		if !st.Limiter.BlockedUntil.IsZero() {
			out.BlockedTil = &st.Limiter.BlockedUntil
		}
		for _, w := range st.Limiter.Windows {
			win := Window{Name: w.Name, Limit: w.Limit, Used: w.Used}
			if w.ServerRemaining >= 0 {
				rem := w.ServerRemaining
				win.ServerRemaining = &rem
			}
			out.Windows = append(out.Windows, win)
		}
		return &SyncStatusOutput{Body: out}, nil
	})

	for _, c := range []struct {
		id, path, summary string
		paused            bool
	}{
		{"pause-sync", "/api/v1/sync/pause", "Pause the archiver", true},
		{"resume-sync", "/api/v1/sync/resume", "Resume the archiver", false},
	} {
		huma.Register(a.api, a.admin(huma.Operation{
			OperationID: c.id, Method: http.MethodPost, Path: c.path, DefaultStatus: http.StatusNoContent,
			Summary: c.summary, Tags: []string{"Sync"},
		}), func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			if err := a.svc.SetWorkerPaused(ctx, c.paused); err != nil {
				return nil, mapErr(err)
			}
			return nil, nil
		})
	}

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "retry-failed", Method: http.MethodPost, Path: "/api/v1/sync/retry",
		Summary: "Re-queue failed replays (all, one player, or one score)", Tags: []string{"Sync"},
	}), func(ctx context.Context, in *RetryInput) (*RetryOutput, error) {
		n, err := a.svc.RetryFailed(ctx, in.Body.PlayerID, in.Body.ScoreID)
		if err != nil {
			return nil, mapErr(err)
		}
		out := &RetryOutput{}
		out.Body.Requeued = n
		return out, nil
	})
}
```

- [ ] **Step 6: Run tests**

Run: `go mod tidy && go test -race ./internal/api/...`
Expected: `ok`. If `TestAdminRequiresSession` gets 422 instead of 401 for some routes, huma validated the body before middleware: per-operation middlewares run **before** input parsing in huma v2, so a 422 means the middleware was not attached — check `a.admin(...)` wraps every admin operation.

- [ ] **Step 7: Commit**

```bash
make lint
git add internal/api internal/httpx go.mod go.sum
git commit -m "feat: add huma JSON API with OpenAPI docs"
```

---

## Task 18: App wiring, CLI commands, end-to-end test

**Files:**
- Create: `internal/app/app.go`, `internal/app/app_test.go`, `internal/cli/serve.go`, `internal/cli/healthcheck.go`, `internal/cli/migrate.go`, `internal/cli/user.go`, `internal/cli/commands_test.go`
- Modify: `internal/cli/root.go`, `Makefile`, `.gitignore`

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `app.New(cfg config.Config, opts app.Options) (*App, error)`; `app.Options{ScoreSaberURL string}`; `(*App).Serve(ctx, net.Listener) error` (HTTP server + worker; returns nil on ctx cancel after ≤15 s graceful shutdown); `(*App).Close() error`; fields `Service`, `Worker`, `Handler`
  - CLI: `ssarchiver serve`, `ssarchiver healthcheck`, `ssarchiver migrate`, `ssarchiver user reset-password [--username]` (password from stdin; prompts without echo on a TTY), `ssarchiver version`
  - `cli.healthURL(listen string) string`

- [ ] **Step 1: Write the failing end-to-end test** — `internal/app/app_test.go`

```go
package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestEndToEnd(t *testing.T) {
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	ss := fakeScoreSaber(t)
	cfg := config.Config{DataDir: t.TempDir(), Listen: "127.0.0.1:0", HourlyBudget: 300, LogLevel: "error"}
	a, err := app.New(cfg, app.Options{ScoreSaberURL: ss.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 1. first-run setup
	res, err := client.PostForm(base+"/setup", url.Values{"username": {"admin"}, "password": {"correct horse battery"}, "confirm": {"correct horse battery"}})
	if err != nil || res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup: %v %v", res, err)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == httpx.SessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	// 2. add a player through the API
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/players", bytes.NewBufferString(`{"ref":"https://scoresaber.com/u/1001"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	res, err = client.Do(req)
	if err != nil || res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("add player: %v %s", err, body)
	}

	// 3. the worker polls, lists and archives the replay
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := client.Get(base + "/api/v1/scores/777")
		if err == nil && res.StatusCode == 200 {
			var s struct {
				Replay struct {
					State string `json:"state"`
				} `json:"replay"`
			}
			_ = json.NewDecoder(res.Body).Decode(&s)
			res.Body.Close()
			if s.Replay.State == "archived" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("replay was not archived within 10s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 4. download it and load the public pages
	res, err = client.Get(base + "/r/777.dat")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Equal(got, replayBytes) {
		t.Fatalf("downloaded %q", got)
	}
	for _, p := range []string{"/", "/p/1001", "/s/777", "/healthz", "/api/docs"} {
		res, err := client.Get(base + p)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("GET %s: %v %v", p, res, err)
		}
		res.Body.Close()
	}

	// 5. graceful shutdown
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Serve did not stop")
	}
}
```

Run: `go test ./internal/app/...` → FAIL (`undefined: app.New`).

- [ ] **Step 2: Implement the app** — `internal/app/app.go`

```go
// Package app wires SSArchiver's components together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/api"
	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
	"github.com/yyewolf/ssarchiver/internal/viewer"
	"github.com/yyewolf/ssarchiver/internal/web"
)

type Options struct {
	ScoreSaberURL string // tests point this at a fake server
}

type App struct {
	db      *gorm.DB
	Service *service.Service
	Worker  *archiver.Worker
	Handler http.Handler
}

func New(cfg config.Config, opts Options) (*App, error) {
	gdb, err := db.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(gdb); err != nil {
		_ = db.Close(gdb)
		return nil, err
	}
	store, err := storage.New(cfg.ReplayDir())
	if err != nil {
		_ = db.Close(gdb)
		return nil, fmt.Errorf("app: storage: %w", err)
	}
	limiter := scoresaber.NewLimiter(cfg.HourlyBudget)
	var copts []scoresaber.Option
	if opts.ScoreSaberURL != "" {
		copts = append(copts, scoresaber.WithBaseURL(opts.ScoreSaberURL))
	}
	client := scoresaber.NewClient(limiter, copts...)
	svc := service.New(gdb, store, client)
	worker := archiver.New(svc, client, limiter)
	vh := viewer.NewHandler(viewer.Bundle(), viewer.DeploySHA)
	if !vh.Available() {
		slog.Warn("ArcViewer bundle not embedded: replays can be downloaded but not watched", "fix", "go generate ./internal/viewer && rebuild")
	}
	w := web.New(web.Deps{Service: svc, Status: worker, Viewer: vh, Config: cfg})
	mux := http.NewServeMux()
	w.Routes(mux)
	api.Register(mux, svc, worker, buildinfo.Version)
	return &App{db: gdb, Service: svc, Worker: worker, Handler: w.Middleware(mux)}, nil
}

func (a *App) Close() error { return db.Close(a.db) }

// Serve runs the HTTP server and the archiver until ctx is cancelled.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})
	g.Go(func() error { return a.Worker.Run(gctx) })
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	})
	return g.Wait()
}
```

Run: `go get golang.org/x/sync@v0.23.0 && go test ./internal/app/...` → `ok` (the test uses the PLACEHOLDER-only bundle if you have not generated the viewer; `/s/777` still renders, showing the "viewer unavailable" alert).

- [ ] **Step 3: Write the failing CLI tests** — `internal/cli/commands_test.go`

```go
package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexedwards/argon2id"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/healthz",
		"[::]:9000":      "http://127.0.0.1:9000/healthz",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/healthz",
		"localhost:8080": "http://localhost:8080/healthz",
	} {
		if got := healthURL(in); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthcheckCommand(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer ok.Close()
	if out, err := run(t, "", "--listen", strings.TrimPrefix(ok.URL, "http://"), "healthcheck"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("healthy: %q %v", out, err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	if _, err := run(t, "", "--listen", strings.TrimPrefix(bad.URL, "http://"), "healthcheck"); err == nil {
		t.Fatal("unhealthy server must fail the healthcheck")
	}
}

func TestMigrateCommand(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, "", "--data-dir", dir, "migrate"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ssarchiver.db")); err != nil {
		t.Fatal("database not created")
	}
}

func TestResetPasswordCommand(t *testing.T) {
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	dir := t.TempDir()
	gdb, err := db.Open(filepath.Join(dir, "ssarchiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Migrate(gdb)
	svc := service.New(gdb, nil, nil)
	if _, err := svc.Setup(context.Background(), "admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close(gdb)

	out, err := run(t, "brand new password\n", "--data-dir", dir, "user", "reset-password")
	if err != nil || !strings.Contains(out, "password updated") {
		t.Fatalf("reset: %q %v", out, err)
	}
	gdb, _ = db.Open(filepath.Join(dir, "ssarchiver.db"))
	defer db.Close(gdb)
	if _, err := service.New(gdb, nil, nil).Login(context.Background(), "admin", "brand new password"); err != nil {
		t.Fatal("new password not active")
	}
	if _, err := run(t, "short\n", "--data-dir", dir, "user", "reset-password"); err == nil {
		t.Fatal("weak password must be rejected")
	}
}
```

Run: `go test ./internal/cli/...` → FAIL.

- [ ] **Step 4: Implement the commands**

`internal/cli/serve.go`:

```go
package cli

import (
	"log/slog"
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/app"
	"github.com/yyewolf/ssarchiver/internal/buildinfo"
	"github.com/yyewolf/ssarchiver/internal/config"
)

func setupLogging(level string) {
	lvl, _ := config.ParseLogLevel(level)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}

func newServeCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the web server and the archiver",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			setupLogging(cfg.LogLevel)
			a, err := app.New(*cfg, app.Options{})
			if err != nil {
				return err
			}
			defer a.Close()
			ln, err := net.Listen("tcp", cfg.Listen)
			if err != nil {
				return err
			}
			slog.Info("ssarchiver started", "addr", ln.Addr().String(), "data", cfg.DataDir, "version", buildinfo.Version)
			return a.Serve(cmd.Context(), ln)
		},
	}
}
```

`internal/cli/healthcheck.go`:

```go
package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/config"
)

// healthURL maps the listen address to a URL reachable from inside the container.
func healthURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen + "/healthz"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

func newHealthcheckCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit non-zero unless the local server is healthy (for container HEALTHCHECK)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL(cfg.Listen), nil)
			if err != nil {
				return err
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				return fmt.Errorf("unhealthy: status %d", res.StatusCode)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
}
```

`internal/cli/migrate.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/db"
)

func newMigrateCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Create or update the database schema and exit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			gdb, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer db.Close(gdb)
			if err := db.Migrate(gdb); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "migrations applied:", cfg.DBPath())
			return nil
		},
	}
}
```

`internal/cli/user.go`:

```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func newUserCmd(cfg *config.Config) *cobra.Command {
	user := &cobra.Command{Use: "user", Short: "Manage the admin account"}
	user.AddCommand(newResetPasswordCmd(cfg))
	return user
}

func readPassword(in io.Reader, prompt io.Writer) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(prompt, "New password: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt)
		return string(b), err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func newResetPasswordCmd(cfg *config.Config) *cobra.Command {
	var username string
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Set a new admin password (read from stdin) and log out all sessions",
		Example: "  echo 'new long password' | ssarchiver user reset-password\n" +
			"  docker exec -i ssarchiver /ssarchiver user reset-password < pw.txt",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			pw, err := readPassword(cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			gdb, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer db.Close(gdb)
			if err := db.Migrate(gdb); err != nil {
				return err
			}
			if err := service.New(gdb, nil, nil).ResetPassword(cmd.Context(), username, pw); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "password updated; existing sessions were logged out")
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "admin username (default: the only user)")
	return cmd
}
```

In `internal/cli/root.go` replace `root.AddCommand(newVersionCmd())` with:

```go
	root.AddCommand(newVersionCmd(), newServeCmd(&cfg), newHealthcheckCmd(&cfg), newMigrateCmd(&cfg), newUserCmd(&cfg))
```

Then:

```bash
go get golang.org/x/term@latest
go mod tidy
go test -race ./internal/cli/... ./internal/app/...
```

Expected: `ok`.

- [ ] **Step 5: Add run/dev targets** — append to `Makefile`:

```make
.PHONY: run dev
run: build
	./bin/ssarchiver serve --data-dir ./data --log-level debug

dev:
	go tool templ generate --watch --proxy="http://localhost:8080" --cmd="go run ./cmd/ssarchiver serve --data-dir ./data --log-level debug"
```

- [ ] **Step 6: Manual QA in a browser** (record results in the Verification log)

```bash
make build && ./bin/ssarchiver serve --data-dir ./data --log-level debug
```

Then, at `http://localhost:8080`:
1. You are redirected to `/setup`; create the admin; you land on `/admin`.
2. Add a real player you are allowed to archive (e.g. your own ScoreSaber profile URL). The preview shows the right name/avatar; "Track player" adds a row and a toast.
3. `/admin/sync` shows the worker running, budget meters moving, the queue row filling, and activity events — and refreshes every 3 s without flicker.
4. Once a replay is archived, open its score page: the ArcViewer iframe loads the map and plays the replay (this verifies ScoreSaber `.dat` → `/r/{id}.dat` → ArcViewer `replayURL` end to end). Try `/embed/{id}?autoplay=1&ui=0` directly.
5. Paste the embed snippet into a local HTML file opened from another origin (e.g. `python3 -m http.server` in a temp dir): the replay plays.
6. Toggle light/dark; check the home, player, score, admin, sync and settings pages in both themes and at a ~375 px wide viewport. No gradients, no layout overflow.
7. Pause the worker, restart the binary, confirm it stays paused; resume.
8. Stop the binary with Ctrl+C: it exits within a few seconds with no error.

If step 4 fails, capture the browser console output and the `/viewer/` network requests, and log it under **Session hand-off** — do not change the design without asking.

- [ ] **Step 7: Commit**

```bash
make generate && make lint && go test -race ./...
git add internal/app internal/cli Makefile go.mod go.sum
git commit -m "feat: wire app, serve/healthcheck/migrate/user commands and e2e test"
```

---

## Task 19: Packaging — Dockerfile, goreleaser, CI/release workflows, docs

**Files:**
- Create: `Dockerfile`, `docker/data/.keep`, `.goreleaser.yaml`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/dependabot.yml`, `THIRD_PARTY_NOTICES.md`
- Modify: `README.md`, `Makefile`, `.gitignore`

**Interfaces:**
- Consumes: `make generate`, `make build`, `go generate ./internal/viewer`, `ssarchiver healthcheck`, ldflags vars in `internal/buildinfo`.
- Produces: tagged releases (`v*`) publishing archives + checksums + SBOMs + cosign bundles + SLSA provenance, and `ghcr.io/yyewolf/ssarchiver:{version,latest}` multi-arch signed + attested images.

- [ ] **Step 1: Write the image** — `Dockerfile`

```dockerfile
# syntax=docker/dockerfile:1
# Built by goreleaser (dockers_v2). The build context contains
# <os>/<arch>/ssarchiver for each platform and docker/data/.keep.
FROM scratch
ARG TARGETPLATFORM
COPY --chown=65532:65532 docker/data/ /data/
COPY --chmod=0555 $TARGETPLATFORM/ssarchiver /ssarchiver
USER 65532:65532
ENV SSA_DATA_DIR=/data \
    SSA_LISTEN=:8080
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/ssarchiver", "healthcheck"]
ENTRYPOINT ["/ssarchiver"]
CMD ["serve"]
```

`docker/data/.keep`: empty file (`touch docker/data/.keep`).

Append to `Makefile` (local image for testing; CI uses goreleaser):

```make
ARCH := $(shell go env GOARCH)

.PHONY: image
image: viewer
	rm -rf .docker-ctx && mkdir -p .docker-ctx/linux/$(ARCH)
	GOOS=linux GOARCH=$(ARCH) CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o .docker-ctx/linux/$(ARCH)/ssarchiver ./cmd/ssarchiver
	cp -r docker .docker-ctx/
	docker build --platform linux/$(ARCH) -t ssarchiver:dev -f Dockerfile .docker-ctx
```

Add `/.docker-ctx/` to `.gitignore`.

- [ ] **Step 2: Verify the image is minimal, rootless and read-only friendly**

```bash
make image
docker image ls ssarchiver:dev --format '{{.Size}}'                 # expect ~40-45MB
docker inspect ssarchiver:dev --format '{{.Config.User}}'           # expect 65532:65532
docker run -d --name ssa-test --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  -p 18080:8080 -v ssa-test-data:/data ssarchiver:dev
sleep 3
curl -fsS http://localhost:18080/healthz                           # expect: ok
docker exec ssa-test /ssarchiver healthcheck                        # expect: ok
curl -fsS -o /dev/null -w '%{http_code}\n' http://localhost:18080/viewer/   # expect: 200
docker logs ssa-test 2>&1 | tail -5                                 # no errors
docker rm -f ssa-test && docker volume rm ssa-test-data
```

If `podman` is installed, also run: `podman run --rm -d --name ssa-p --read-only -p 18081:8080 -v ssa-p:/data:U localhost/ssarchiver:dev` (after `podman load` / `podman build` the same way) and `curl localhost:18081/healthz`.

Expected: all commands succeed. A permission error writing `/data/ssarchiver.db` means `COPY --chown` did not apply to `/data`; fix the Dockerfile before continuing.

- [ ] **Step 3: goreleaser config** — `.goreleaser.yaml`

```yaml
# yaml-language-server: $schema=https://goreleaser.com/static/schema.json
version: 2
project_name: ssarchiver

before:
  hooks:
    - go mod download
    - go generate ./internal/viewer
    - test -f internal/viewer/dist/index.html.gz

builds:
  - id: ssarchiver
    main: ./cmd/ssarchiver
    binary: ssarchiver
    env: [CGO_ENABLED=0]
    flags: [-trimpath]
    ldflags:
      - -s -w
      - -X github.com/yyewolf/ssarchiver/internal/buildinfo.Version={{ .Version }}
      - -X github.com/yyewolf/ssarchiver/internal/buildinfo.Commit={{ .FullCommit }}
      - -X github.com/yyewolf/ssarchiver/internal/buildinfo.Date={{ .CommitDate }}
    mod_timestamp: "{{ .CommitTimestamp }}"
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]

archives:
  - formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    files: [LICENSE, README.md, THIRD_PARTY_NOTICES.md]

checksum:
  name_template: checksums.txt

sboms:
  - artifacts: archive

signs:
  - cmd: cosign
    artifacts: checksum
    signature: "${artifact}.sigstore.json"
    args: ["sign-blob", "--bundle=${signature}", "${artifact}", "--yes"]

dockers_v2:
  - id: image
    ids: [ssarchiver]
    images: ["ghcr.io/yyewolf/ssarchiver"]
    tags: ["{{ .Version }}", "{{ if not .Prerelease }}latest{{ end }}"]
    platforms: [linux/amd64, linux/arm64]
    extra_files: [docker/data/.keep]
    sbom: true
    labels:
      org.opencontainers.image.title: ssarchiver
      org.opencontainers.image.description: Archive and serve ScoreSaber replays
      org.opencontainers.image.source: https://github.com/yyewolf/SSArchiver
      org.opencontainers.image.version: "{{ .Version }}"
      org.opencontainers.image.revision: "{{ .FullCommit }}"
      org.opencontainers.image.created: "{{ .CommitDate }}"
      org.opencontainers.image.licenses: "BSD-3-Clause AND GPL-3.0-only"
    annotations:
      org.opencontainers.image.description: Archive and serve ScoreSaber replays
      org.opencontainers.image.source: https://github.com/yyewolf/SSArchiver
      org.opencontainers.image.licenses: "BSD-3-Clause AND GPL-3.0-only"

docker_signs:
  - cmd: cosign
    args: ["sign", "${artifact}@${digest}", "--yes"]

release:
  footer: |
    ### Verify

    ```sh
    gh attestation verify ssarchiver_{{ .Version }}_linux_amd64.tar.gz --repo yyewolf/SSArchiver
    gh attestation verify oci://ghcr.io/yyewolf/ssarchiver:{{ .Version }} --repo yyewolf/SSArchiver
    cosign verify ghcr.io/yyewolf/ssarchiver:{{ .Version }} \
      --certificate-identity-regexp '^https://github.com/yyewolf/SSArchiver/' \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com
    ```

    Bundles ArcViewer (GPL-3.0, unmodified) from https://github.com/AllPoland/ArcViewer/tree/c776256497b66f7c91a74162cfcd943b0f45ee2e — see THIRD_PARTY_NOTICES.md.

changelog:
  use: github
  filters:
    exclude: ["^docs:", "^test:", "^chore:", "^ci:"]
```

Verify:

```bash
go run github.com/goreleaser/goreleaser/v2@v2.18.2 check
go run github.com/goreleaser/goreleaser/v2@v2.18.2 release --snapshot --clean --skip=sign,sbom,docker,publish
ls dist/*.tar.gz dist/checksums.txt
tar -tzf dist/ssarchiver_*_linux_amd64.tar.gz
./dist/ssarchiver_linux_amd64_v1/ssarchiver version
```

Expected: `check` passes; six archives + checksums; archive contains the binary, LICENSE, README.md, THIRD_PARTY_NOTICES.md; `version` prints a snapshot version. (The docker/sign/sbom steps run only in CI.) Add `/dist/` is already ignored from Task 1.

- [ ] **Step 4: CI workflow** — `.github/workflows/ci.yml`

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
      - name: Regenerate code
        run: make generate
      - name: Generated code must be committed
        run: git diff --exit-code
      - name: Test
        run: go test -race ./...
      - name: Build with embedded viewer
        run: make build

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
      - uses: golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0
        with:
          version: v2.14.0
```

- [ ] **Step 5: Release workflow** — `.github/workflows/release.yml`

```yaml
name: Release

on:
  push:
    tags: ["v*"]

permissions:
  contents: write      # create the GitHub release
  packages: write      # push to ghcr.io
  id-token: write      # keyless cosign + attestations
  attestations: write  # SLSA provenance

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 0
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
      - uses: sigstore/cosign-installer@6f9f17788090df1f26f669e9d70d6ae9567deba6 # v4.1.2
      - uses: anchore/sbom-action/download-syft@66cbf4bc1f1c0d2edc94016e65bc221b6bb0ad6c # v0.24.3
      - uses: docker/setup-qemu-action@99012661954931238ded8c8b007157a8430204e1 # v4.4.0
      - uses: docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069 # v4.4.1
      - uses: docker/login-action@dbcb813823bdd20940b903addbd779551569679f # v4.6.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94 # v7.2.3
        with:
          distribution: goreleaser
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
      - name: Resolve image digest
        id: image
        run: |
          digest=$(docker buildx imagetools inspect "ghcr.io/yyewolf/ssarchiver:${GITHUB_REF_NAME#v}" --format '{{json .Manifest}}' | jq -r .digest)
          test -n "$digest" && test "$digest" != "null"
          echo "digest=$digest" >> "$GITHUB_OUTPUT"
      - name: Attest archives
        uses: actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8 # v4.2.2
        with:
          subject-checksums: dist/checksums.txt
      - name: Attest image
        uses: actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8 # v4.2.2
        with:
          subject-name: ghcr.io/yyewolf/ssarchiver
          subject-digest: ${{ steps.image.outputs.digest }}
          push-to-registry: true
```

`.github/dependabot.yml`:

```yaml
version: 2
updates:
  - package-ecosystem: gomod
    directory: /
    schedule:
      interval: weekly
    groups:
      go:
        patterns: ["*"]
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
```

Validate syntax locally if `actionlint` is available: `go run github.com/rhysd/actionlint/cmd/actionlint@latest`. Expected: no findings.

- [ ] **Step 6: Docs** — `THIRD_PARTY_NOTICES.md`

```markdown
# Third-party notices

## ArcViewer

SSArchiver binaries and container images include an unmodified WebGL build of
**ArcViewer** by AllPoland, served at `/viewer/`.

- Source: https://github.com/AllPoland/ArcViewer
- Bundled build: `deploy` branch commit `c776256497b66f7c91a74162cfcd943b0f45ee2e` (v0.8.1-beta),
  source at https://github.com/AllPoland/ArcViewer/tree/c776256497b66f7c91a74162cfcd943b0f45ee2e
- License: GNU General Public License v3.0 — full text served at `/viewer/LICENSE`

ArcViewer is a separate program distributed alongside SSArchiver; SSArchiver
itself is licensed under the BSD 3-Clause License (see `LICENSE`).

## Other components

- htmx 2.0.10 — BSD 2-Clause (vendored in `internal/web/static/js/htmx.min.js`)
- shadcn-templ components — MIT (copied into `internal/web/components`)
- Go module dependencies — see `go.mod`; licenses are listed in the release SBOMs.
```

Rewrite `README.md`:

````markdown
# SSArchiver

Self-hosted archive for ScoreSaber replays. Track players, keep every replay
ScoreSaber still offers (it prunes them), and serve them back as pages,
`.dat` downloads and an embeddable 3D viewer (ArcViewer).

## Run it

```sh
docker run -d --name ssarchiver \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  -p 8080:8080 -v ssarchiver-data:/data \
  -e SSA_BASE_URL=https://replays.example.com \
  ghcr.io/yyewolf/ssarchiver:latest
```

Open the URL, create the admin account on first visit, then add players
under **Manage**. The image is `FROM scratch`, runs as UID/GID 65532 and only
writes to `/data`.

Compose:

```yaml
services:
  ssarchiver:
    image: ghcr.io/yyewolf/ssarchiver:latest
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    environment:
      SSA_BASE_URL: https://replays.example.com
    ports: ["8080:8080"]
    volumes: ["ssarchiver-data:/data"]
    restart: unless-stopped
volumes:
  ssarchiver-data:
```

Bind mounts must be writable by UID 65532 (`chown -R 65532:65532 ./data`), or
use Podman's `:U` volume option (`-v ./data:/data:U`).

## Configuration

| Variable | Flag | Default | Meaning |
|---|---|---|---|
| `SSA_DATA_DIR` | `--data-dir` | `./data` (`/data` in the image) | SQLite database and replay files |
| `SSA_LISTEN` | `--listen` | `:8080` | HTTP listen address |
| `SSA_BASE_URL` | `--base-url` | derived from the request | Public URL used in embeds and link previews |
| `SSA_HOURLY_BUDGET` | `--hourly-budget` | `300` | Max ScoreSaber requests per hour (1–360) |
| `SSA_TRUST_PROXY` | `--trust-proxy` | `false` | Trust `X-Forwarded-*` headers |
| `SSA_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

ScoreSaber allows 360 requests per hour per IP. A large history (thousands of
replays) takes hours to backfill; progress is on **Sync**.

## Embedding

Every archived score page has a **Copy embed code** button:

```html
<iframe src="https://replays.example.com/embed/94461650" width="960" height="540"
        allow="fullscreen" loading="lazy" style="border:0"></iframe>
```

Options: `?autoplay=1`, `?loop=1`, `?ui=0`. Raw files are at
`/r/<score id>.dat` with CORS enabled.

## API

JSON API with OpenAPI docs at `/api/docs`. Reads are public; writes use the
admin session cookie.

## Admin password reset

```sh
echo 'a new long password' | docker exec -i ssarchiver /ssarchiver user reset-password
```

## Verify a release

```sh
gh attestation verify oci://ghcr.io/yyewolf/ssarchiver:<version> --repo yyewolf/SSArchiver
cosign verify ghcr.io/yyewolf/ssarchiver:<version> \
  --certificate-identity-regexp '^https://github.com/yyewolf/SSArchiver/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Development

```sh
make generate   # gorm gen, templ, Tailwind
make viewer     # download + gzip the pinned ArcViewer build (~28 MB)
make test lint
make run        # build and serve on :8080 with ./data
make dev        # templ watch + live reload proxy
```

## License

BSD 3-Clause. Bundled ArcViewer is GPL-3.0; see `THIRD_PARTY_NOTICES.md`.
````

- [ ] **Step 7: Final checks and commit**

```bash
make generate && git diff --exit-code   # generated code committed
make lint
go test -race ./...
make build && ./bin/ssarchiver version
git add Dockerfile docker .goreleaser.yaml .github THIRD_PARTY_NOTICES.md README.md Makefile .gitignore
git commit -m "ci: add container image, goreleaser, CI and signed SLSA release workflows"
```

---

## Final verification (whole plan)

Run after Task 19, record each result in the **Verification log**:

- [ ] `make generate && git diff --exit-code` — clean
- [ ] `make lint` — `0 issues.`
- [ ] `go test -race ./...` — all `ok`
- [ ] `make build && ls -lh bin/ssarchiver` — static binary, roughly 45–55 MB with the viewer
- [ ] Task 18 Step 6 manual QA checklist — all 8 items pass
- [ ] Task 19 Step 2 container checks — size, user, read-only run, healthcheck, viewer
- [ ] `goreleaser check` and snapshot release — pass
- [ ] Push a `v0.1.0-rc.1` tag on a fork or after review → release workflow green; `gh attestation verify` and `cosign verify` succeed on the published image (do this only with the repository owner's go-ahead)

## Spec coverage map

| Spec section | Implemented in |
|---|---|
| §1 purpose / success criteria | Tasks 8–9 (archiving), 13–14 (serving/embedding), 11 + 13–16 (UI) |
| §2 external facts | Tasks 3–4 (limits, endpoints), 10 (ArcViewer build + params) |
| §3 decisions | Global Constraints; Tasks 10 (viewer), 6–8 (backfill), 7 + 12 (single admin), 19 (image) |
| §4 architecture, config, SQLite pragmas | Tasks 1, 2, 18 |
| §5 data model | Task 2 (+ `backfill_retry_at` added for §6.4 backfill retries) |
| §6.1 rate limiter | Task 3 |
| §6.2 work selection, poll cap rule | Tasks 8–9 |
| §6.3 downloads, reconciliation | Tasks 5, 9 |
| §6.4 errors | Tasks 8–9 |
| §6.5 status | Tasks 9, 16, 17 |
| §7.1 public routes | Tasks 11, 13, 14, 17 |
| §7.2 admin routes, CLI reset | Tasks 12, 15, 16, 17, 18 |
| §7.3 security | Tasks 7, 11, 12, 14, 17 |
| §7.4 viewer embedding | Tasks 10, 14 |
| §8 UI | Tasks 11–16 |
| §9 build, lint, CI, release; §9.1 image | Tasks 1, 11, 19 |
| §10 testing | every task's test steps; e2e in Task 18 |
