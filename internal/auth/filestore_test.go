package auth

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth_sessions.json")

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("new filestore: %v", err)
	}

	original := Session{
		UserID:    123,
		Token:     "tok_123",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Save(original); err != nil {
		t.Fatalf("save session: %v", err)
	}

	// Пересоздаем store, чтобы убедиться, что данные загружаются с диска.
	loadedStore, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload filestore: %v", err)
	}

	loaded, ok := loadedStore.Get(original.UserID)
	if !ok {
		t.Fatalf("session not found after reload")
	}
	if loaded.Token != original.Token {
		t.Fatalf("token mismatch after reload: got %s want %s", loaded.Token, original.Token)
	}
	if !loaded.ExpiresAt.Equal(original.ExpiresAt) {
		t.Fatalf("expires mismatch after reload: got %v want %v", loaded.ExpiresAt, original.ExpiresAt)
	}
}
