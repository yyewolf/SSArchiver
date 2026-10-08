package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/config"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

func TestParseID(t *testing.T) {
	if id, ok := parseID("42"); !ok || id != 42 {
		t.Fatalf("parseID(42) = %d %v", id, ok)
	}
	if _, ok := parseID("0"); ok {
		t.Fatal("0 must not parse")
	}
	if _, ok := parseID("-3"); ok {
		t.Fatal("negative must not parse")
	}
	if _, ok := parseID("abc"); ok {
		t.Fatal("non-numeric must not parse")
	}
}

func TestIsNotFound(t *testing.T) {
	if !isNotFound(fmt.Errorf("x: %w", service.ErrNotFound)) {
		t.Fatal("wrapped ErrNotFound must match")
	}
	if isNotFound(errors.New("other")) {
		t.Fatal("other errors must not match")
	}
}

func TestNotFoundRenders404(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	h := New(Deps{Service: svc, Config: config.Config{BaseURL: "https://replays.example.com"}})
	rec := httptest.NewRecorder()
	h.notFound(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), "no such replay")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no such replay") {
		t.Fatal("message missing")
	}
}
