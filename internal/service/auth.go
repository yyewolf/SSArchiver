package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
	"gorm.io/gorm"

	"github.com/yyewolf/ssarchiver/internal/db/query"
	"github.com/yyewolf/ssarchiver/internal/model"
)

var (
	ErrAlreadySetup       = errors.New("setup has already been completed")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrWeakPassword       = errors.New("password must be at least 10 characters")
	ErrInvalidUsername    = errors.New("username must be 1-64 characters")
)

const (
	SessionTTL        = 30 * 24 * time.Hour
	MinPasswordLength = 10
)

// PasswordParams are the argon2id parameters for new hashes (tests lower them).
var PasswordParams = &argon2id.Params{Memory: 64 * 1024, Iterations: 1, Parallelism: 2, SaltLength: 16, KeyLength: 32}

var (
	dummyOnce sync.Once
	dummyHash string
)

// burnHash spends roughly the same time as a real verify, so unknown
// usernames are not distinguishable by timing.
func burnHash(password string) {
	dummyOnce.Do(func() { dummyHash, _ = argon2id.CreateHash("ssarchiver-dummy", PasswordParams) })
	_, _ = argon2id.ComparePasswordAndHash(password, dummyHash)
}

func validateCredentials(username, password string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" || utf8.RuneCountInString(username) > 64 {
		return "", ErrInvalidUsername
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return "", ErrWeakPassword
	}
	return username, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.q.User.WithContext(ctx).Count()
	if err != nil {
		return false, fmt.Errorf("service: count users: %w", err)
	}
	return n == 0, nil
}

// Setup creates the single admin. The count check and insert run in one
// IMMEDIATE transaction, so concurrent setups serialise and only one wins.
func (s *Service) Setup(ctx context.Context, username, password string) (*model.User, error) {
	username, err := validateCredentials(username, password)
	if err != nil {
		return nil, err
	}
	hash, err := argon2id.CreateHash(password, PasswordParams)
	if err != nil {
		return nil, fmt.Errorf("service: hash password: %w", err)
	}
	u := &model.User{Username: username, PasswordHash: hash, CreatedAt: s.Now()}
	err = s.q.Transaction(func(tx *query.Query) error {
		n, err := tx.User.WithContext(ctx).Count()
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadySetup
		}
		return tx.User.WithContext(ctx).Create(u)
	})
	if err != nil {
		if errors.Is(err, ErrAlreadySetup) {
			return nil, ErrAlreadySetup
		}
		return nil, fmt.Errorf("service: setup: %w", err)
	}
	return u, nil
}

func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	u, err := s.q.User.WithContext(ctx).Where(s.q.User.Username.Eq(strings.TrimSpace(username))).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		burnHash(password)
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("service: login: %w", err)
	}
	ok, err := argon2id.ComparePasswordAndHash(password, u.PasswordHash)
	if err != nil || !ok {
		return "", ErrInvalidCredentials
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("service: token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.Now()
	sess := &model.Session{TokenHash: hashToken(token), UserID: u.ID, ExpiresAt: now.Add(SessionTTL), CreatedAt: now}
	if err := s.q.Session.WithContext(ctx).Create(sess); err != nil {
		return "", fmt.Errorf("service: create session: %w", err)
	}
	return token, nil
}

// UserForSession resolves a cookie token. Sessions slide: when less than
// SessionTTL-24h remains, expiry is pushed back to now+SessionTTL.
func (s *Service) UserForSession(ctx context.Context, token string) (*model.User, error) {
	if token == "" {
		return nil, fmt.Errorf("%w: session", ErrNotFound)
	}
	q := s.q.Session
	sess, err := q.WithContext(ctx).Preload(q.User).Where(q.TokenHash.Eq(hashToken(token))).First()
	if err != nil {
		return nil, notFound(err, "session")
	}
	now := s.Now()
	if !sess.ExpiresAt.After(now) {
		_, _ = q.WithContext(ctx).Where(q.TokenHash.Eq(sess.TokenHash)).Delete()
		return nil, fmt.Errorf("%w: session expired", ErrNotFound)
	}
	if sess.ExpiresAt.Sub(now) < SessionTTL-24*time.Hour {
		_, _ = q.WithContext(ctx).Where(q.TokenHash.Eq(sess.TokenHash)).Update(q.ExpiresAt, now.Add(SessionTTL))
	}
	if sess.User == nil {
		return nil, fmt.Errorf("%w: session user", ErrNotFound)
	}
	return sess.User, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.q.Session.WithContext(ctx).Where(s.q.Session.TokenHash.Eq(hashToken(token))).Delete()
	return err
}

func (s *Service) setPassword(ctx context.Context, userID uint, password string) error {
	hash, err := argon2id.CreateHash(password, PasswordParams)
	if err != nil {
		return fmt.Errorf("service: hash password: %w", err)
	}
	return s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.User.WithContext(ctx).Where(tx.User.ID.Eq(userID)).Update(tx.User.PasswordHash, hash); err != nil {
			return err
		}
		_, err := tx.Session.WithContext(ctx).Where(tx.Session.UserID.Eq(userID)).Delete()
		return err
	})
}

func (s *Service) ChangePassword(ctx context.Context, userID uint, oldPW, newPW string) error {
	u, err := s.q.User.WithContext(ctx).Where(s.q.User.ID.Eq(userID)).First()
	if err != nil {
		return notFound(err, "user")
	}
	if ok, err := argon2id.ComparePasswordAndHash(oldPW, u.PasswordHash); err != nil || !ok {
		return ErrInvalidCredentials
	}
	if _, err := validateCredentials(u.Username, newPW); err != nil {
		return err
	}
	return s.setPassword(ctx, u.ID, newPW)
}

// ResetPassword is used by the CLI; an empty username targets the only user.
func (s *Service) ResetPassword(ctx context.Context, username, newPW string) error {
	do := s.q.User.WithContext(ctx)
	if username != "" {
		do = do.Where(s.q.User.Username.Eq(username))
	}
	u, err := do.Order(s.q.User.ID).First()
	if err != nil {
		return notFound(err, "user "+username)
	}
	if _, err := validateCredentials(u.Username, newPW); err != nil {
		return err
	}
	return s.setPassword(ctx, u.ID, newPW)
}
