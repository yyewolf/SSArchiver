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
