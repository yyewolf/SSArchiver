package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexedwards/argon2id"

	"github.com/yyewolf/ssarchiver/internal/db"
	"github.com/yyewolf/ssarchiver/internal/service"
)

func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/healthz",
		"[::]:9000":      "http://127.0.0.1:9000/healthz",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/healthz",
		"localhost:8080": "http://localhost:8080/healthz",
	} {
		if got := healthURL(in); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthcheckCommand(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer ok.Close()
	if out, err := run(t, "", "--listen", strings.TrimPrefix(ok.URL, "http://"), "healthcheck"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("healthy: %q %v", out, err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	if _, err := run(t, "", "--listen", strings.TrimPrefix(bad.URL, "http://"), "healthcheck"); err == nil {
		t.Fatal("unhealthy server must fail the healthcheck")
	}
}

func TestMigrateCommand(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, "", "--data-dir", dir, "migrate"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ssarchiver.db")); err != nil {
		t.Fatal("database not created")
	}
}

func TestResetPasswordCommand(t *testing.T) {
	service.PasswordParams = &argon2id.Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	dir := t.TempDir()
	gdb, err := db.Open(filepath.Join(dir, "ssarchiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Migrate(gdb)
	svc := service.New(gdb, nil, nil)
	if _, err := svc.Setup(context.Background(), "admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close(gdb)

	out, err := run(t, "brand new password\n", "--data-dir", dir, "user", "reset-password")
	if err != nil || !strings.Contains(out, "password updated") {
		t.Fatalf("reset: %q %v", out, err)
	}
	gdb, _ = db.Open(filepath.Join(dir, "ssarchiver.db"))
	defer db.Close(gdb)
	if _, err := service.New(gdb, nil, nil).Login(context.Background(), "admin", "brand new password"); err != nil {
		t.Fatal("new password not active")
	}
	if _, err := run(t, "short\n", "--data-dir", dir, "user", "reset-password"); err == nil {
		t.Fatal("weak password must be rejected")
	}
}
