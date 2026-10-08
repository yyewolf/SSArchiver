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

	ViewerShowHeadset  bool
	ViewerHeadsetColor string
	ViewerHeadsetAlpha float64
}

var DefaultSettings = Settings{
	InstanceTitle: "SSArchiver", PollInterval: 10 * time.Minute,
	ViewerHeadsetColor: "#878787", ViewerHeadsetAlpha: 1,
}

// PollIntervals are the choices offered in the UI and accepted by UpdateSettings.
var PollIntervals = []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

const (
	keyTitle  = "instance_title"
	keyPoll   = "poll_interval"
	keyPaused = "worker_paused"

	keyViewerHeadset      = "viewer_show_headset"
	keyViewerHeadsetColor = "viewer_headset_color"
	keyViewerHeadsetAlpha = "viewer_headset_alpha"
)

// ParseHexColor parses #rgb or #rrggbb into 0-1 RGB components.
func ParseHexColor(s string) (r, g, b float64, ok bool) {
	if len(s) == 4 && s[0] == '#' {
		s = "#" + s[1:2] + s[1:2] + s[2:3] + s[2:3] + s[3:4] + s[3:4]
	}
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255, true
}

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
		case keyViewerHeadset:
			st.ViewerShowHeadset, _ = strconv.ParseBool(r.Value)
		case keyViewerHeadsetColor:
			if _, _, _, ok := ParseHexColor(r.Value); ok {
				st.ViewerHeadsetColor = strings.ToLower(r.Value)
			}
		case keyViewerHeadsetAlpha:
			if a, err := strconv.ParseFloat(r.Value, 64); err == nil && a >= 0 && a <= 1 {
				st.ViewerHeadsetAlpha = a
			}
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

// UpdateViewerSettings saves the public viewer rendering preferences
// (other fields of st are ignored).
func (s *Service) UpdateViewerSettings(ctx context.Context, st Settings) error {
	r, g, b, ok := ParseHexColor(st.ViewerHeadsetColor)
	if !ok {
		return fmt.Errorf("%w: headset color must be a hex color like #878787", ErrInvalidSettings)
	}
	if !(st.ViewerHeadsetAlpha >= 0 && st.ViewerHeadsetAlpha <= 1) {
		return fmt.Errorf("%w: headset opacity must be between 0 and 1", ErrInvalidSettings)
	}
	color := fmt.Sprintf("#%02x%02x%02x", uint8(r*255+0.5), uint8(g*255+0.5), uint8(b*255+0.5))
	err := s.putSettings(ctx, map[string]string{
		keyViewerHeadset:      strconv.FormatBool(st.ViewerShowHeadset),
		keyViewerHeadsetColor: color,
		keyViewerHeadsetAlpha: strconv.FormatFloat(st.ViewerHeadsetAlpha, 'f', -1, 64),
	})
	if err != nil {
		return fmt.Errorf("service: save viewer settings: %w", err)
	}
	return nil
}
