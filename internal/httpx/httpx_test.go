package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/yyewolf/ssarchiver/internal/httpx"
)

func TestSecurityHeadersNonce(t *testing.T) {
	var nonce string
	h := httpx.SecurityHeaders("https://cdn.scoresaber.com")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce = templ.GetNonce(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if nonce == "" || !strings.Contains(csp, "'nonce-"+nonce+"'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("nonce=%q csp=%q", nonce, csp)
	}
	if !strings.Contains(csp, "img-src 'self' data: https://cdn.scoresaber.com;") {
		t.Fatalf("img-src must list the given hosts: %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") == "" {
		t.Fatalf("headers = %v", rec.Header())
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if rec2.Header().Get("Content-Security-Policy") == csp {
		t.Fatal("nonce must differ per request")
	}
}

func TestDocsCSP(t *testing.T) {
	h := httpx.SecurityHeaders("https://cdn.scoresaber.com")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/docs", nil))
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
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/x", nil)
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if ip := httpx.ClientIP(req, false); ip != "10.0.0.1" {
		t.Fatalf("untrusted ip = %s", ip)
	}
	if ip := httpx.ClientIP(req, true); ip != "10.0.0.1" {
		t.Fatalf("trusted ip = %s, want rightmost XFF entry", ip)
	}

	spoofed := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	spoofed.RemoteAddr = "10.0.0.1:1234"
	spoofed.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if ip := httpx.ClientIP(spoofed, true); ip != "5.6.7.8" {
		t.Fatalf("trusted ip = %s, want rightmost 5.6.7.8 over spoofed leftmost 1.2.3.4", ip)
	}

	noXFF := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	noXFF.RemoteAddr = "10.0.0.1:1234"
	if ip := httpx.ClientIP(noXFF, true); ip != "10.0.0.1" {
		t.Fatalf("trusted ip without XFF = %s, want RemoteAddr host", ip)
	}
}

func TestRecover(t *testing.T) {
	h := httpx.Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
}
