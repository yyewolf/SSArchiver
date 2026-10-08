package testutil

import (
	"path/filepath"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

// NewService returns a Service over a temp DB/store with a fake clock and resolver.
func NewService(t testing.TB) (*service.Service, *Resolver, *Clock) {
	t.Helper()
	gdb := OpenDB(t)
	store, err := storage.New(filepath.Join(t.TempDir(), "replays"))
	if err != nil {
		t.Fatal(err)
	}
	res := &Resolver{Players: map[string]scoresaber.Player{
		"1001": {ID: "1001", Name: "Alice", Country: "FR", Avatar: "https://cdn.scoresaber.com/avatars/1001.jpg"},
		"1002": {ID: "1002", Name: "Bob", Country: "US", Avatar: "https://cdn.scoresaber.com/avatars/1002.jpg"},
	}}
	svc := service.New(gdb, store, res)
	clk := NewClock(T0)
	svc.SetClock(clk.Now)
	return svc, res, clk
}
