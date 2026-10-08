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

// maxViewerFile bounds on-the-fly decompression (gosec G110); the largest
// manifest file is ~53 MB raw.
const maxViewerFile = 256 << 20

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
	defer func() { _ = f.Close() }()
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
	defer func() { _ = zr.Close() }()
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, io.LimitReader(zr, maxViewerFile))
}
