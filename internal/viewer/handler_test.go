package viewer_test

import (
	"bytes"
	"compress/gzip"
	"context"
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/viewer/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD code=%d body=%d bytes", rec.Code, rec.Body.Len())
	}
}
