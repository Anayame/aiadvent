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
	"sync"
)

type stubBot struct {
	mu   sync.Mutex
	msgs []string
}

func (s *stubBot) SendMessage(ctx context.Context, chatID int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, text)
	return nil
}

func (s *stubBot) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]string, len(s.msgs))
	copy(result, s.msgs)
	return result
}

func (s *stubBot) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = nil
}

type stubLLM struct {
	answer string
}

func (s *stubLLM) ChatCompletion(ctx context.Context, prompt string, model string) (string, error) {
	return s.answer, nil
}

func (s *stubLLM) ChatCompletionWithSystem(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
	return s.ChatCompletion(ctx, prompt, model)
}

type slowLLM struct {
	delay  time.Duration
	answer string
}

func (s *slowLLM) ChatCompletion(ctx context.Context, prompt string, model string) (string, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return s.answer, nil
}

func (s *slowLLM) ChatCompletionWithSystem(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
	return s.ChatCompletion(ctx, prompt, model)
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
	waitForMessages(t, bot, 1, 500*time.Millisecond)
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
	updateAsk := Update{Message: &Message{Text: "/ask", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyAsk, _ := json.Marshal(updateAsk)
	reqAsk := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyAsk))
	rrAsk := httptest.NewRecorder()
	handler.ServeHTTP(rrAsk, reqAsk)
	waitForMessages(t, bot, 1, 500*time.Millisecond)

	// login
	bot.Reset()
	updateLogin := Update{Message: &Message{Text: "/login", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyLogin, _ := json.Marshal(updateLogin)
	reqLogin := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyLogin))
	rrLogin := httptest.NewRecorder()
	handler.ServeHTTP(rrLogin, reqLogin)
	waitForMessages(t, bot, 1, 500*time.Millisecond)

	updatePassword := Update{Message: &Message{Text: "pass", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyPassword, _ := json.Marshal(updatePassword)
	reqPassword := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyPassword))
	rrPassword := httptest.NewRecorder()
	handler.ServeHTTP(rrPassword, reqPassword)
	waitForMessages(t, bot, 2, 500*time.Millisecond)

	// ask снова
	bot.Reset()
	updateAsk2 := Update{Message: &Message{Text: "/ask", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyAsk2, _ := json.Marshal(updateAsk2)
	reqAsk2 := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyAsk2))
	rrAsk2 := httptest.NewRecorder()
	handler.ServeHTTP(rrAsk2, reqAsk2)

	updateQuestion := Update{Message: &Message{Text: "hi", Chat: Chat{ID: 1}, From: &User{ID: 7}}}
	bodyQuestion, _ := json.Marshal(updateQuestion)
	reqQuestion := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(bodyQuestion))
	rrQuestion := httptest.NewRecorder()
	handler.ServeHTTP(rrQuestion, reqQuestion)

	waitForMessages(t, bot, 3, 500*time.Millisecond)
}

func TestWebhookRespondsFastWithSlowLLM(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 42, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &slowLLM{delay: 2 * time.Second, answer: "ok"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	update := Update{Message: &Message{Text: "/ask hi", Chat: Chat{ID: 1}, From: &User{ID: 42}}}
	body, _ := json.Marshal(update)

	req := httptest.NewRequest("POST", "/telegram/webhook", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	start := time.Now()
	handler.ServeHTTP(rr, req)
	duration := time.Since(start)

	if rr.Code != 200 {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if duration > 500*time.Millisecond {
		t.Fatalf("expected fast response, got %v", duration)
	}
}

func waitForMessages(t *testing.T, bot *stubBot, min int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(bot.Messages()) >= min {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected at least %d messages, got %d", min, len(bot.Messages()))
}
