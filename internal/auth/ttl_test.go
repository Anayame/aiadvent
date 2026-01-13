package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTTLZeroDoesNotExpire(t *testing.T) {
	store := NewMemoryStore()
	service := NewService("", 0, store)

	expired := Session{
		UserID:    1,
		Token:     "tok",
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	if err := store.Save(expired); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if !service.IsAuthorized(context.Background(), expired.UserID) {
		t.Fatalf("session with ttl=0 should stay authorized")
	}

	if _, ok := store.Get(expired.UserID); !ok {
		t.Fatalf("session should not be deleted when ttl=0")
	}
}

func TestTTLRemovesExpiredSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth_sessions.json")

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("new filestore: %v", err)
	}

	expired := Session{
		UserID:    42,
		Token:     "tok",
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := store.Save(expired); err != nil {
		t.Fatalf("save expired: %v", err)
	}

	service := NewService("", time.Minute, store)

	if service.IsAuthorized(context.Background(), expired.UserID) {
		t.Fatalf("expected authorization to fail for expired session")
	}
	if _, ok := store.Get(expired.UserID); ok {
		t.Fatalf("expired session should be removed from store")
	}

	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload filestore: %v", err)
	}
	if _, ok := reloaded.Get(expired.UserID); ok {
		t.Fatalf("expired session should be removed from persisted file")
	}
}
