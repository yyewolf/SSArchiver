// Package httpx holds HTTP helpers shared by the web UI and the JSON API.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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
	// EmbedCSP: the embed page frames the same-origin viewer or BeatLeader's
	// hosted one, and may itself be framed anywhere.
	EmbedCSP = "default-src 'none'; style-src 'unsafe-inline'; frame-src 'self' https://replay.beatleader.com; base-uri 'none'; frame-ancestors *"
	// DocsCSP: huma's docs page loads its renderer from a CDN.
	DocsCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' https://unpkg.com https://cdn.jsdelivr.net; " +
		"style-src 'self' 'unsafe-inline' https://unpkg.com https://cdn.jsdelivr.net; " +
		"img-src 'self' data: https:; font-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'"
)

// DefaultCSP is the policy for every UI page. imgHosts are the platforms'
// image hosts (avatars, covers), from the registry.
func DefaultCSP(nonce string, imgHosts []string) string {
	img := "'self' data:"
	if len(imgHosts) > 0 {
		img += " " + strings.Join(imgHosts, " ")
	}
	return "default-src 'self'; script-src 'self' 'nonce-" + nonce + "'; style-src 'self' 'unsafe-inline'; " +
		"img-src " + img + "; frame-src 'self'; connect-src 'self'; " +
		"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}

type ctxKey int

const (
	userKey ctxKey = iota
	baseURLKey
)

func WithUser(ctx context.Context, u *model.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

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
func SecurityHeaders(imgHosts ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
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
				h.Set("Content-Security-Policy", DefaultCSP(nonce, imgHosts))
			}
			next.ServeHTTP(w, r.WithContext(templ.WithNonce(r.Context(), nonce)))
		})
	}
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
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
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
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
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

// CORSReads lets browsers elsewhere read public API responses: GET/HEAD carry
// Access-Control-Allow-Origin: * and OPTIONS is answered as a read-only
// preflight. Requests under an exempt prefix (the admin surface) pass through
// untouched. Writes are not covered: CrossOriginProtection rejects cross-site
// unsafe methods before this runs.
func CORSReads(exemptPrefixes ...string) func(http.Handler) http.Handler {
	exempt := func(path string) bool {
		for _, p := range exemptPrefixes {
			if strings.HasPrefix(path, p) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			switch r.Method {
			case http.MethodOptions:
				hd := w.Header()
				hd.Set("Access-Control-Allow-Origin", "*")
				hd.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
				hd.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			case http.MethodGet, http.MethodHead:
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
			next.ServeHTTP(w, r)
		})
	}
}
