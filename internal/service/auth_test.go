package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

const goodPW = "correct horse battery"

func TestSetupOnce(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	if need, _ := svc.NeedsSetup(ctx); !need {
		t.Fatal("fresh instance needs setup")
	}
	if _, err := svc.Setup(ctx, "admin", "short"); !errors.Is(err, service.ErrWeakPassword) {
		t.Fatalf("weak password err = %v", err)
	}
	if _, err := svc.Setup(ctx, " ", goodPW); !errors.Is(err, service.ErrInvalidUsername) {
		t.Fatalf("blank username err = %v", err)
	}
	u, err := svc.Setup(ctx, "  admin ", goodPW)
	if err != nil || u.Username != "admin" {
		t.Fatalf("setup = %+v, %v", u, err)
	}
	if need, _ := svc.NeedsSetup(ctx); need {
		t.Fatal("setup done")
	}
	if _, err := svc.Setup(ctx, "other", goodPW); !errors.Is(err, service.ErrAlreadySetup) {
		t.Fatalf("second setup err = %v", err)
	}
}

func TestSetupConcurrentOnlyOneWins(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	var wins atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Setup(ctx, fmt.Sprintf("admin%d", i), goodPW)
			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, service.ErrAlreadySetup):
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}
	if wins.Load() != 1 {
		t.Fatalf("%d setups succeeded, want exactly 1", wins.Load())
	}
}

func TestLoginAndSessions(t *testing.T) {
	svc, _, clk := testutil.NewService(t)
	ctx := context.Background()
	_, _ = svc.Setup(ctx, "admin", goodPW)
	if _, err := svc.Login(ctx, "admin", "wrong password!"); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("bad password err = %v", err)
	}
	if _, err := svc.Login(ctx, "ghost", goodPW); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("unknown user err = %v", err)
	}
	token, err := svc.Login(ctx, "admin", goodPW)
	if err != nil || len(token) < 40 {
		t.Fatalf("login = %q, %v", token, err)
	}
	if u, err := svc.UserForSession(ctx, token); err != nil || u.Username != "admin" {
		t.Fatalf("session user = %+v, %v", u, err)
	}
	if _, err := svc.UserForSession(ctx, "forged"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("forged token err = %v", err)
	}
	clk.Advance(29 * 24 * time.Hour) // sliding: still valid, gets extended
	if _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("session should slide: %v", err)
	}
	clk.Advance(29 * 24 * time.Hour)
	if _, err := svc.UserForSession(ctx, token); err != nil {
		t.Fatalf("extended session expired early: %v", err)
	}
	clk.Advance(31 * 24 * time.Hour)
	if _, err := svc.UserForSession(ctx, token); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("idle session must expire, err = %v", err)
	}
	token, _ = svc.Login(ctx, "admin", goodPW)
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UserForSession(ctx, token); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("logout must invalidate the session")
	}
}

func TestChangeAndResetPassword(t *testing.T) {
	svc, _, _ := testutil.NewService(t)
	ctx := context.Background()
	u, _ := svc.Setup(ctx, "admin", goodPW)
	token, _ := svc.Login(ctx, "admin", goodPW)
	if err := svc.ChangePassword(ctx, u.ID, "nope nope nope", "another long password"); !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("wrong old password err = %v", err)
	}
	if err := svc.ChangePassword(ctx, u.ID, goodPW, "short"); !errors.Is(err, service.ErrWeakPassword) {
		t.Fatalf("weak new password err = %v", err)
	}
	if err := svc.ChangePassword(ctx, u.ID, goodPW, "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UserForSession(ctx, token); !errors.Is(err, service.ErrNotFound) {
		t.Fatal("changing the password must end existing sessions")
	}
	if _, err := svc.Login(ctx, "admin", "another long password"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, "", "reset long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "admin", "reset long password"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, "nobody", "reset long password"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("reset unknown user err = %v", err)
	}
}
