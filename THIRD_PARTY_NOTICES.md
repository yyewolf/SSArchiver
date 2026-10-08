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
