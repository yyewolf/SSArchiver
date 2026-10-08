package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/yyewolf/ssarchiver/internal/model"
)

var ErrInvalidSettings = errors.New("invalid settings")

type Settings struct {
	InstanceTitle string
	PollInterval  time.Duration
	WorkerPaused  bool
}

var DefaultSettings = Settings{InstanceTitle: "SSArchiver", PollInterval: 10 * time.Minute}

// PollIntervals are the choices offered in the UI and accepted by UpdateSettings.
var PollIntervals = []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

const (
	keyTitle  = "instance_title"
	keyPoll   = "poll_interval"
	keyPaused = "worker_paused"
)

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	rows, err := s.q.Setting.WithContext(ctx).Find()
	if err != nil {
		return Settings{}, fmt.Errorf("service: load settings: %w", err)
	}
	st := DefaultSettings
	for _, r := range rows {
		switch r.Key {
		case keyTitle:
			if r.Value != "" {
				st.InstanceTitle = r.Value
			}
		case keyPoll:
			if d, err := time.ParseDuration(r.Value); err == nil && slices.Contains(PollIntervals, d) {
				st.PollInterval = d
			}
		case keyPaused:
			st.WorkerPaused, _ = strconv.ParseBool(r.Value)
		}
	}
	return st, nil
}

func (s *Service) putSettings(ctx context.Context, kv map[string]string) error {
	rows := make([]*model.Setting, 0, len(kv))
	for k, v := range kv {
		rows = append(rows, &model.Setting{Key: k, Value: v})
	}
	return s.q.Setting.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(rows...)
}

// UpdateSettings saves title and poll interval (WorkerPaused is ignored; use SetWorkerPaused).
func (s *Service) UpdateSettings(ctx context.Context, st Settings) error {
	title := strings.TrimSpace(st.InstanceTitle)
	if title == "" || utf8.RuneCountInString(title) > 64 {
		return fmt.Errorf("%w: title must be 1-64 characters", ErrInvalidSettings)
	}
	if !slices.Contains(PollIntervals, st.PollInterval) {
		return fmt.Errorf("%w: unsupported poll interval %s", ErrInvalidSettings, st.PollInterval)
	}
	if err := s.putSettings(ctx, map[string]string{keyTitle: title, keyPoll: st.PollInterval.String()}); err != nil {
		return fmt.Errorf("service: save settings: %w", err)
	}
	s.Wake()
	return nil
}

func (s *Service) SetWorkerPaused(ctx context.Context, paused bool) error {
	if err := s.putSettings(ctx, map[string]string{keyPaused: strconv.FormatBool(paused)}); err != nil {
		return fmt.Errorf("service: save pause: %w", err)
	}
	s.Wake()
	return nil
}
