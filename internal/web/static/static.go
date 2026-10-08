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
