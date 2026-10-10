// Package service holds SSArchiver's business logic. It is the only package
// (besides internal/db) that talks to GORM.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/platform"
	"github.com/yyewolf/ssarchiver/internal/storage"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrPlayerExists     = errors.New("player is already tracked")
	ErrInvalidPlayerRef = errors.New("enter a ScoreSaber player ID or profile URL (https://scoresaber.com/u/<id>)")
)

type Service struct {
	db    *gorm.DB
	q     *query.Query
	store *storage.Store
	reg   *platform.Registry

	clockMu sync.RWMutex
	now     func() time.Time
	newID   func() string

	wake chan struct{}
}

func New(gdb *gorm.DB, store *storage.Store, reg *platform.Registry) *Service {
	return &Service{
		db: gdb, q: query.Use(gdb), store: store, reg: reg,
		now:   func() time.Time { return time.Now().UTC() },
		newID: platform.NewPlayerID,
		wake:  make(chan struct{}, 1),
	}
}

// Platforms is the registry of platforms this instance archives from.
func (s *Service) Platforms() *platform.Registry { return s.reg }

func (s *Service) Now() time.Time {
	s.clockMu.RLock()
	defer s.clockMu.RUnlock()
	return s.now().UTC()
}

// SetClock replaces the time source (tests).
func (s *Service) SetClock(now func() time.Time) {
	s.clockMu.Lock()
	s.now = now
	s.clockMu.Unlock()
}

// SetIDGenerator replaces the player ID generator (tests).
func (s *Service) SetIDGenerator(gen func() string) {
	s.clockMu.Lock()
	s.newID = gen
	s.clockMu.Unlock()
}

func (s *Service) nextID() string {
	s.clockMu.RLock()
	defer s.clockMu.RUnlock()
	return s.newID()
}

func (s *Service) Store() *storage.Store { return s.store }

// Wake nudges the archiver worker; it never blocks.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) WakeC() <-chan struct{} { return s.wake }

func (s *Service) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func Ptr[T any](v T) *T { return &v }

func notFound(err error, what string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return err
}

// pickAfter returns the first id greater than last (ids sorted ascending), wrapping around.
func pickAfter(ids []string, last string) string {
	for _, id := range ids {
		if id > last {
			return id
		}
	}
	return ids[0]
}
