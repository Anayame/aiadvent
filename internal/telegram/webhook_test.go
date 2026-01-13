package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"aiadvent/internal/auth"
	"log/slog"
	"os"
)

type stubBot struct {
	msgs []string
}

func (s *stubBot) SendMessage(ctx context.Context, chatID int64, text string) error {
	s.msgs = append(s.msgs, text)
	return nil
}

type stubLLM struct {
	answer string
}

func (s *stubLLM) ChatCompletion(ctx context.Context, prompt string, model string) (string, error) {
	return s.answer, nil
}

func TestPublicCommandDoesNotRequireAuth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	update := Update{Message: &Message{Text: "/start", Chat: Chat{ID: 1}, From: &User{ID: 1}}}
	body, _ := json.Marshal(update)

	req := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if len(bot.msgs) == 0 {
		t.Fatalf("expected bot message")
	}
}

func TestPrivateCommandRequiresAuth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "answer"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	// ask без авторизации
	updateAsk := Update{Message: &Message{Text: "/ask hi", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyAsk, _ := json.Marshal(updateAsk)
	reqAsk := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyAsk))
	rrAsk := httptest.NewRecorder()
	handler.ServeHTTP(rrAsk, reqAsk)
	if len(bot.msgs) == 0 {
		t.Fatalf("expected auth prompt message")
	}

	// login
	bot.msgs = nil
	updateLogin := Update{Message: &Message{Text: "/login pass", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyLogin, _ := json.Marshal(updateLogin)
	reqLogin := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyLogin))
	rrLogin := httptest.NewRecorder()
	handler.ServeHTTP(rrLogin, reqLogin)

	// ask снова
	updateAsk2 := Update{Message: &Message{Text: "/ask hi", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyAsk2, _ := json.Marshal(updateAsk2)
	reqAsk2 := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyAsk2))
	rrAsk2 := httptest.NewRecorder()
	handler.ServeHTTP(rrAsk2, reqAsk2)

	if len(bot.msgs) == 0 {
		t.Fatalf("expected response after login")
	}
}
