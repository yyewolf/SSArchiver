package archiver_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yyewolf/ssarchiver/internal/archiver"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
	"github.com/yyewolf/ssarchiver/internal/testutil"
)

type fakeClient struct {
	mu           sync.Mutex
	perPage      int
	scores       map[string][]scoresaber.ScoreItem // newest first
	scoresErr    map[string]error
	replays      map[int64][]byte
	replayErr    map[int64]error
	replayReader map[int64]func() io.ReadCloser
	scoreCalls   []string // "player:page"
	replayCalls  []int64
}

func newFake() *fakeClient {
	return &fakeClient{
		perPage: 2, scores: map[string][]scoresaber.ScoreItem{}, scoresErr: map[string]error{},
		replays: map[int64][]byte{}, replayErr: map[int64]error{}, replayReader: map[int64]func() io.ReadCloser{},
	}
}

func (f *fakeClient) Scores(_ context.Context, playerID string, page int) (scoresaber.ScorePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scoreCalls = append(f.scoreCalls, fmt.Sprintf("%s:%d", playerID, page))
	if err := f.scoresErr[playerID]; err != nil {
		return scoresaber.ScorePage{}, err
	}
	all := f.scores[playerID]
	total := (len(all) + f.perPage - 1) / f.perPage
	var data []scoresaber.ScoreItem
	if start := (page - 1) * f.perPage; start < len(all) {
		data = all[start:min(start+f.perPage, len(all))]
	}
	return scoresaber.ScorePage{Data: data, Metadata: scoresaber.PageMeta{Page: page, ItemsPerPage: f.perPage, TotalItems: len(all), TotalPages: total}}, nil
}

func (f *fakeClient) Replay(_ context.Context, id int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replayCalls = append(f.replayCalls, id)
	if err := f.replayErr[id]; err != nil {
		return nil, err
	}
	if fn := f.replayReader[id]; fn != nil {
		return fn(), nil
	}
	if b, ok := f.replays[id]; ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return nil, fmt.Errorf("%w: replay %d", scoresaber.ErrNotFound, id)
}

func (f *fakeClient) Player(_ context.Context, id string) (scoresaber.Player, error) {
	if p, ok := testutil.DefaultPlayers()[id]; ok {
		return p, nil
	}
	return scoresaber.Player{ID: id, Name: "Player " + id}, nil
}

func (f *fakeClient) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.scoreCalls...)
}

func (f *fakeClient) replaysCalled() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.replayCalls...)
}

func (f *fakeClient) reset() {
	f.mu.Lock()
	f.scoreCalls, f.replayCalls = nil, nil
	f.mu.Unlock()
}

// history returns n scores (newest first) ending `newest`, one minute apart,
// with ids start, start+1, …; all have replays and fake replay bytes.
func (f *fakeClient) history(playerID string, start int64, n int, newest time.Time) []scoresaber.ScoreItem {
	var out []scoresaber.ScoreItem
	for i := range n {
		id := start + int64(i)
		out = append(out, testutil.Item(playerID, id, id+100000, newest.Add(-time.Duration(i)*time.Minute), true))
		f.replays[id] = []byte(fmt.Sprintf("replay-%d", id))
	}
	return out
}

type env struct {
	svc *service.Service
	clk *testutil.Clock
	fc  *fakeClient
	w   *archiver.Worker
}

func newEnv(t *testing.T) *env {
	t.Helper()
	fc := newFake()
	svc, _, clk := testutil.NewServiceWith(t, scoresaber.NewPlatform(fc, nil))
	return &env{svc: svc, clk: clk, fc: fc, w: archiver.New(svc)}
}

func (e *env) add(t *testing.T, id string) string {
	t.Helper()
	return testutil.AddPlayer(t, e.svc, id)
}

func (e *env) step(t *testing.T) bool {
	t.Helper()
	did, err := e.w.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return did
}

// drain runs Step until it reports no work (at most max times).
func (e *env) drain(t *testing.T, max int) {
	t.Helper()
	for range max {
		if !e.step(t) {
			return
		}
	}
	t.Fatalf("worker still busy after %d steps", max)
}
