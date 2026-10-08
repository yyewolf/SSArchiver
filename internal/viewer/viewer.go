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
