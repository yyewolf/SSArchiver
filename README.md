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

### Upgrading to multi-platform (from versions before platform support)

The first start of this version migrates the database in place:

- A copy of the database is saved first as `ssarchiver.pre-platforms.db` in the data directory. Keep it until you are happy with the upgrade; to roll back, stop the new version and put it back as `ssarchiver.db`.
- The migration checks that no player, score or leaderboard row was lost and refuses to start otherwise (the error names the backup).
- Existing players, URLs (`/p/{id}`, `/s/{id}`, `/embed/{id}`, `/r/{id}.dat`) and replay files are unchanged.
- New players get an opaque ID (e.g. `/p/k7m2q9x4c1ab`). Readable links by account work for everyone: `/p/ss/{scoresaberID}` redirects to the player.
- API: players gain `identities[]` (one per linked platform account, with their sync `feeds[]`); the top-level `last_polled_at`, `last_error` and `backfill` fields are deprecated. `GET /api/v1/players/by/{platform}/{id}` finds a player by account. `GET /api/v1/sync` gains `limiters[]`.
- A ScoreSaber "player not found" now disables that account instead of the player; enabling the player again re-enables its accounts.

## Configuration

| Variable | Flag | Default | Meaning |
|---|---|---|---|
| `SSA_DATA_DIR` | `--data-dir` | `./data` (`/data` in the image) | SQLite database and replay files |
| `SSA_LISTEN` | `--listen` | `:8080` | HTTP listen address |
| `SSA_BASE_URL` | `--base-url` | derived from the request | Public URL used in embeds and link previews |
| `SSA_HOURLY_BUDGET` | `--hourly-budget` | `300` | Max ScoreSaber requests per hour (1–360) |
| `SSA_TRUST_PROXY` | `--trust-proxy` | `false` | Trust `X-Forwarded-*` headers; the proxy must set or append `X-Forwarded-For`, and the rightmost entry is treated as the client IP |
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
admin session cookie. Public read endpoints send `Access-Control-Allow-Origin: *`
(preflight `OPTIONS` answered for `GET, HEAD, OPTIONS`); the admin `/api/v1/sync*`
surface stays same-origin only, and cross-site writes are rejected outright.

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

### Adding a platform

Platforms are registered in `internal/app/app.go`. A platform is a client package (like `internal/scoresaber`) that returns a `platform.Platform` descriptor: name/slug, replay extension, profile URL parsing, feeds, and an `Adapter` that resolves players, lists feed pages as `platform.Play`s, downloads replays and exposes its rate limiters. No migration is needed: platform names are plain strings in the database. `internal/testutil.FakePlatform` is a complete minimal example, exercised end to end by `internal/archiver/generic_test.go`.

## Automation

Renovate keeps dependencies updated (install the [Renovate GitHub App](https://developer.mend.io/github) to activate it): patch and minor updates auto-merge once CI is green after a 3-day release age, grouped per ecosystem; major updates wait for approval on the dependency dashboard.

Every push to `main` is auto-tagged from Conventional Commits — `feat:` bumps the minor version, everything else the patch, `!` or a `BREAKING CHANGE` footer the major — and the tag triggers the signed release workflow (archives, SBOMs, cosign bundles, SLSA attestations, `ghcr.io` image). A prerelease tag such as `v0.1.0-rc.1` pushed manually is treated as its base version for the next bump.

## License

BSD 3-Clause. Bundled ArcViewer is GPL-3.0; see `THIRD_PARTY_NOTICES.md`.
