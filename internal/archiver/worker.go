// Package archiver is the background worker that polls every platform's
// feeds, walks player history and downloads replays (spec §6).
package archiver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yyewolf/ssarchiver/internal/model"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/service"
)

type State string

const (
	StateIdle        State = "idle"
	StateRunning     State = "running"
	StatePaused      State = "paused"
	StateRateLimited State = "ratelimited"
	StateStopped     State = "stopped"
)

// LimiterStatus is one platform limiter's state, for the status page and API.
type LimiterStatus struct {
	Platform string // display name
	Name     string
	Snapshot platform.LimiterSnapshot
}

type Status struct {
	State    State
	Task     string
	Since    time.Time
	Limiter  platform.LimiterSnapshot // the legacy platform's limiter (API windows/blocked_until)
	Limiters []LimiterStatus
}

const (
	MaxPollPages = 5
	maxIdle      = time.Minute
	pruneEvery   = time.Hour
)

type Worker struct {
	svc *service.Service

	mu     sync.RWMutex
	status Status

	lastReplayPlayer string
	lastBackfillFeed string
	lastPrune        time.Time
}

func New(svc *service.Service) *Worker {
	return &Worker{svc: svc, status: Status{State: StateIdle, Since: svc.Now()}}
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
	reg := w.svc.Platforms()
	if reg == nil {
		return st
	}
	legacySet := false
	for _, p := range reg.All() {
		for _, l := range p.Adapter.Limiters() {
			snap := l.Snapshot()
			st.Limiters = append(st.Limiters, LimiterStatus{Platform: p.DisplayName, Name: l.Name(), Snapshot: snap})
			if p.Legacy && !legacySet {
				st.Limiter, legacySet = snap, true
			}
			if st.State == StateRunning && snap.Waiting {
				st.State = StateRateLimited
			}
		}
	}
	return st
}

func (w *Worker) platform(name string) (platform.Platform, error) {
	p, ok := w.svc.Platforms().Get(name)
	if !ok {
		return platform.Platform{}, fmt.Errorf("archiver: unknown platform %q", name)
	}
	return p, nil
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

	feedBusy, replayBusy, _ := w.busy()
	if did, err := w.nextProbe(ctx, feedBusy); did || err != nil {
		return did, err
	}
	wf, err := w.svc.DueFeed(ctx, st.PollInterval, feedBusy)
	if err != nil {
		return false, err
	}
	if wf != nil {
		return true, w.poll(ctx, wf)
	}
	if did, err := w.nextReplay(ctx, service.TierNew, replayBusy); did || err != nil {
		return did, err
	}
	if did, err := w.nextBackfill(ctx, true, feedBusy); did || err != nil {
		return did, err
	}
	if did, err := w.nextReplay(ctx, service.TierBackfill, replayBusy); did || err != nil {
		return did, err
	}
	if did, err := w.nextBackfill(ctx, false, feedBusy); did || err != nil {
		return did, err
	}
	return w.nextReplay(ctx, service.TierBackfillOther, replayBusy)
}

// nextProbe checks the access of one feed that needs it (spec §5.1, tier 1).
func (w *Worker) nextProbe(ctx context.Context, busy service.Busy) (bool, error) {
	wf, err := w.svc.DueProbe(ctx, busy)
	if err != nil || wf == nil {
		return false, err
	}
	w.setStatus(StateRunning, "Checking access · "+wf.PlayerName)
	if _, err := w.svc.CheckFeedAccess(ctx, wf.Key()); err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		msg := "access check failed: " + err.Error()
		if errors.Is(err, platform.ErrRateLimited) {
			msg = "rate limited during an access check; retrying when the limit resets"
		}
		w.svc.Log(ctx, feedEvent(wf, model.LevelWarn, model.KindPoll, msg))
	}
	return true, nil
}

// busy lists, per (platform, kind), the feeds and replay downloads whose
// limiter is not ready, and the earliest time one becomes ready (spec §5.1).
func (w *Worker) busy() (feeds, replays service.Busy, wake time.Time) {
	reg := w.svc.Platforms()
	if reg == nil {
		return nil, nil, time.Time{}
	}
	now := w.svc.Now()
	feeds, replays = service.Busy{}, service.Busy{}
	for _, p := range reg.All() {
		notReady := map[string]bool{} // limiter name → not ready; unknown and "" names are ready
		for _, l := range p.Adapter.Limiters() {
			if ok, at := l.Ready(now); !ok {
				notReady[l.Name()] = true
				if wake.IsZero() || at.Before(wake) {
					wake = at
				}
			}
		}
		for _, f := range p.Feeds {
			pk := service.PlatformKind{Platform: p.Name, Kind: f.Kind}
			if notReady[p.Adapter.FeedLimiter(f.Kind)] {
				feeds[pk] = true
			}
			if notReady[p.Adapter.ReplayLimiter(f.Kind)] {
				replays[pk] = true
			}
		}
	}
	return feeds, replays, wake
}

func (w *Worker) nextReplay(ctx context.Context, tier service.ReplayTier, busy service.Busy) (bool, error) {
	sc, err := w.svc.NextReplay(ctx, tier, w.lastReplayPlayer, busy)
	if err != nil || sc == nil {
		return false, err
	}
	w.lastReplayPlayer = sc.PlayerID
	return true, w.download(ctx, sc)
}

func (w *Worker) nextBackfill(ctx context.Context, scores bool, busy service.Busy) (bool, error) {
	wf, err := w.svc.NextBackfillFeed(ctx, w.lastBackfillFeed, scores, busy)
	if err != nil || wf == nil {
		return false, err
	}
	w.lastBackfillFeed = wf.Key().String()
	return true, w.backfill(ctx, wf)
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
	feedBusy, _, wake := w.busy()
	if st, err := w.svc.Settings(ctx); err == nil {
		consider(w.svc.NextPollAt(ctx, st.PollInterval, feedBusy))
	}
	consider(w.svc.NextRetryAt(ctx))
	consider(wake, !wake.IsZero(), nil)
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
