package auth

import (
	"context"
	"testing"
	"time"
)

func TestServiceLoginAndLogout(t *testing.T) {
	store := NewMemoryStore()
	service := NewService("secret", time.Hour, store)

	_, err := service.Login(context.Background(), 42, "wrong")
	if err == nil {
		t.Fatalf("expected error on wrong password")
	}

	session, err := service.Login(context.Background(), 42, "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.UserID != 42 {
		t.Fatalf("unexpected user id: %d", session.UserID)
	}

	if !service.IsAuthorized(context.Background(), 42) {
		t.Fatalf("user should be authorized")
	}

	service.Logout(context.Background(), 42)
	if service.IsAuthorized(context.Background(), 42) {
		t.Fatalf("user should be logged out")
	}
}
