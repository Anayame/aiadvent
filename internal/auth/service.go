package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrUnauthorized = errors.New("unauthorized")

type Session struct {
	UserID    int64
	Token     string
	ExpiresAt time.Time
}

type Store interface {
	Save(session Session) error
	Get(userID int64) (Session, bool)
	Delete(userID int64)
}

type Service struct {
	password string
	ttl      time.Duration
	store    Store
}

func NewService(password string, ttl time.Duration, store Store) *Service {
	return &Service{
		password: password,
		ttl:      ttl,
		store:    store,
	}
}

// Login проверяет пароль и создает сессию.
func (s *Service) Login(ctx context.Context, userID int64, password string) (Session, error) {
	if s.password != "" && s.password != password {
		return Session{}, ErrUnauthorized
	}

	expiresAt := time.Time{}
	if s.ttl > 0 {
		expiresAt = time.Now().Add(s.ttl)
	}

	session := Session{
		UserID:    userID,
		Token:     fmt.Sprintf("tok_%d_%d", userID, time.Now().UnixNano()),
		ExpiresAt: expiresAt,
	}
	if err := s.store.Save(session); err != nil {
		return Session{}, fmt.Errorf("save session: %w", err)
	}
	return session, nil
}

func (s *Service) Logout(ctx context.Context, userID int64) {
	s.store.Delete(userID)
}

func (s *Service) IsAuthorized(ctx context.Context, userID int64) bool {
	session, ok := s.store.Get(userID)
	if !ok {
		return false
	}

	// TTL == 0 означает, что сессии вечные и не истекают по времени.
	if s.ttl <= 0 {
		return true
	}
	if session.ExpiresAt.IsZero() || time.Now().After(session.ExpiresAt) {
		s.store.Delete(userID)
		return false
	}
	return true
}
