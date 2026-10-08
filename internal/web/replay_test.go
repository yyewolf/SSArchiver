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
