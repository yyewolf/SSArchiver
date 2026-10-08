// Package archiver is the background worker that polls ScoreSaber, walks
// player history and downloads replays (spec §6).
package archiver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/scoresaber"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type Client interface {
	Scores(ctx context.Context, playerID string, page int) (scoresaber.ScorePage, error)
	Replay(ctx context.Context, scoreID int64) (io.ReadCloser, error)
}

type LimiterSource interface {
	Snapshot() scoresaber.LimiterSnapshot
}

type State string

const (
	StateIdle        State = "idle"
	StateRunning     State = "running"
	StatePaused      State = "paused"
	StateRateLimited State = "ratelimited"
	StateStopped     State = "stopped"
)

type Status struct {
	State   State
	Task    string
	Since   time.Time
	Limiter scoresaber.LimiterSnapshot
}

const (
	MaxPollPages = 5
	maxIdle      = time.Minute
	pruneEvery   = time.Hour
)

type Worker struct {
	svc     *service.Service
	client  Client
	limiter LimiterSource

	mu     sync.RWMutex
	status Status

	lastReplayPlayer   string
	lastBackfillPlayer string
	lastPrune          time.Time
}

func New(svc *service.Service, c Client, l LimiterSource) *Worker {
	return &Worker{svc: svc, client: c, limiter: l, status: Status{State: StateIdle, Since: svc.Now()}}
}

func (w *Worker) setStatus(state State, task string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status.State != state || w.status.Task != task {
		w.status = Status{State: state, Task: task, Since: w.svc.Now()}
	}
}

// Status returns a snapshot for the UI/API.
func (w *Worker) Status() Status {
	w.mu.RLock()
	st := w.status
	w.mu.RUnlock()
	if w.limiter != nil {
		st.Limiter = w.limiter.Snapshot()
		if st.State == StateRunning && st.Limiter.Waiting {
			st.State = StateRateLimited
		}
	}
	return st
}

// Run loops until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if res, err := w.svc.ReconcileStorage(ctx); err != nil {
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindWorker, Message: "storage reconciliation failed: " + err.Error()})
	} else if res.Changed() {
		w.svc.Log(ctx, model.SyncEvent{Level: model.LevelInfo, Kind: model.KindWorker, Message: fmt.Sprintf(
			"storage reconciled: %d adopted, %d re-queued, %d temp files removed, %d orphan files kept", res.Adopted, res.Requeued, res.RemovedTmp, res.Orphans)})
	}
	for {
		if ctx.Err() != nil {
			w.setStatus(StateStopped, "")
			return nil
		}
		did, err := w.Step(ctx)
		if err != nil && ctx.Err() == nil {
			w.svc.Log(ctx, model.SyncEvent{Level: model.LevelError, Kind: model.KindWorker, Message: "worker step failed: " + err.Error()})
			did = false
		}
		if !did {
			w.idle(ctx)
		}
	}
}

// Step performs at most one unit of work. did=false means nothing was due.
func (w *Worker) Step(ctx context.Context) (did bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			did, err = false, fmt.Errorf("panic: %v", r)
		}
	}()
	st, err := w.svc.Settings(ctx)
	if err != nil {
		return false, err
	}
	if st.WorkerPaused {
		w.setStatus(StatePaused, "")
		return false, nil
	}
	w.maybePrune(ctx)

	p, err := w.svc.DuePlayer(ctx, st.PollInterval)
	if err != nil {
		return false, err
	}
	if p != nil {
		return true, w.poll(ctx, p)
	}
	if sc, err := w.svc.NextReplay(ctx, service.TierNew, w.lastReplayPlayer); err != nil {
		return false, err
	} else if sc != nil {
		w.lastReplayPlayer = sc.PlayerID
		return true, w.download(ctx, sc)
	}
	bp, err := w.svc.NextBackfillPlayer(ctx, w.lastBackfillPlayer)
	if err != nil {
		return false, err
	}
	if bp != nil {
		w.lastBackfillPlayer = bp.ID
		return true, w.backfill(ctx, bp)
	}
	if sc, err := w.svc.NextReplay(ctx, service.TierBackfill, w.lastReplayPlayer); err != nil {
		return false, err
	} else if sc != nil {
		w.lastReplayPlayer = sc.PlayerID
		return true, w.download(ctx, sc)
	}
	return false, nil
}

func (w *Worker) idle(ctx context.Context) {
	w.mu.RLock()
	paused := w.status.State == StatePaused
	w.mu.RUnlock()
	if st, err := w.svc.Settings(ctx); err == nil {
		paused = st.WorkerPaused
	}
	if !paused {
		w.setStatus(StateIdle, "")
	}
	t := time.NewTimer(w.idleFor(ctx))
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-w.svc.WakeC():
	}
}

func (w *Worker) idleFor(ctx context.Context) time.Duration {
	d := maxIdle
	now := w.svc.Now()
	consider := func(at time.Time, ok bool, err error) {
		if err == nil && ok && at.Sub(now) < d {
			d = at.Sub(now)
		}
	}
	if st, err := w.svc.Settings(ctx); err == nil {
		consider(w.svc.NextPollAt(ctx, st.PollInterval))
	}
	consider(w.svc.NextRetryAt(ctx))
	return max(d, time.Second)
}

func (w *Worker) maybePrune(ctx context.Context) {
	now := w.svc.Now()
	if now.Sub(w.lastPrune) < pruneEvery {
		return
	}
	w.lastPrune = now
	if n, err := w.svc.PruneEvents(ctx); err != nil {
		slog.Warn("prune events", "err", err)
	} else if n > 0 {
		slog.Debug("pruned sync events", "count", n)
	}
}
