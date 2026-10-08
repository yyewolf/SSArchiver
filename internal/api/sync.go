package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

type Window struct {
	Name            string `json:"name" enum:"short,medium,long"`
	Limit           int    `json:"limit"`
	Used            int    `json:"used"`
	ServerRemaining *int   `json:"server_remaining,omitempty"`
}

type SyncStatus struct {
	State      string     `json:"state" enum:"idle,running,paused,ratelimited,stopped"`
	Task       string     `json:"task,omitempty"`
	Since      time.Time  `json:"since"`
	Paused     bool       `json:"paused"`
	BlockedTil *time.Time `json:"blocked_until,omitempty"`
	Windows    []Window   `json:"windows"`
}

type SyncStatusOutput struct{ Body SyncStatus }

type RetryInput struct {
	Body struct {
		PlayerID string `json:"player_id,omitempty" pattern:"^[0-9]{1,32}$"`
		ScoreID  int64  `json:"score_id,omitempty" minimum:"0"`
	}
}

type RetryOutput struct {
	Body struct {
		Requeued int64 `json:"requeued"`
	}
}

func (a *API) registerSync() {
	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "get-sync-status", Method: http.MethodGet, Path: "/api/v1/sync",
		Summary: "Worker status and rate-limit usage", Tags: []string{"Sync"},
	}), func(ctx context.Context, _ *struct{}) (*SyncStatusOutput, error) {
		st := a.status.Status()
		settings, err := a.svc.Settings(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		out := SyncStatus{State: string(st.State), Task: st.Task, Since: st.Since, Paused: settings.WorkerPaused, Windows: []Window{}}
		if !st.Limiter.BlockedUntil.IsZero() {
			out.BlockedTil = &st.Limiter.BlockedUntil
		}
		for _, w := range st.Limiter.Windows {
			win := Window{Name: w.Name, Limit: w.Limit, Used: w.Used}
			if w.ServerRemaining >= 0 {
				rem := w.ServerRemaining
				win.ServerRemaining = &rem
			}
			out.Windows = append(out.Windows, win)
		}
		return &SyncStatusOutput{Body: out}, nil
	})

	for _, c := range []struct {
		id, path, summary string
		paused            bool
	}{
		{"pause-sync", "/api/v1/sync/pause", "Pause the archiver", true},
		{"resume-sync", "/api/v1/sync/resume", "Resume the archiver", false},
	} {
		huma.Register(a.api, a.admin(huma.Operation{
			OperationID: c.id, Method: http.MethodPost, Path: c.path, DefaultStatus: http.StatusNoContent,
			Summary: c.summary, Tags: []string{"Sync"},
		}), func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			if err := a.svc.SetWorkerPaused(ctx, c.paused); err != nil {
				return nil, mapErr(err)
			}
			return nil, nil
		})
	}

	huma.Register(a.api, a.admin(huma.Operation{
		OperationID: "retry-failed", Method: http.MethodPost, Path: "/api/v1/sync/retry",
		Summary: "Re-queue failed replays (all, one player, or one score)", Tags: []string{"Sync"},
	}), func(ctx context.Context, in *RetryInput) (*RetryOutput, error) {
		n, err := a.svc.RetryFailed(ctx, in.Body.PlayerID, in.Body.ScoreID)
		if err != nil {
			return nil, mapErr(err)
		}
		out := &RetryOutput{}
		out.Body.Requeued = n
		return out, nil
	})
}
