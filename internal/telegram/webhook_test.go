package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aiadvent/internal/auth"
	"log/slog"
	"os"
	"sync"
)

type stubBot struct {
	mu       sync.Mutex
	msgs     []string
	nextID   int64
	messages map[int64]string
}

func (s *stubBot) SendMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.msgs = append(s.msgs, text)
	messageID := s.nextID
	s.nextID++
	if s.messages == nil {
		s.messages = make(map[int64]string)
	}
	s.messages[messageID] = text
	return messageID, nil
}

func (s *stubBot) EditMessage(ctx context.Context, chatID int64, messageID int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.messages == nil {
		s.messages = make(map[int64]string)
	}
	s.messages[messageID] = text

	// Update the message in msgs slice if it exists
	for i, msg := range s.msgs {
		if s.messages[int64(i)] == msg {
			s.msgs[i] = text
			break
		}
	}

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
	s.messages = nil
	s.nextID = 0
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

	waitForMessages(t, bot, 2, 500*time.Millisecond)
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

func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected []string
	}{
		{
			name:     "short message",
			input:    "Hello world",
			maxLen:   20,
			expected: []string{"Hello world"},
		},
		{
			name:   "long message splits on space",
			input:  "This is a very long message that should be split into multiple parts",
			maxLen: 20,
			expected: []string{
				"This is a very long",
				"message that should",
				"be split into",
				"multiple parts",
			},
		},
		{
			name:   "long message without spaces",
			input:  "Thisisaverylongmessagewithoutanyspacesthatshouldbesplit",
			maxLen: 20,
			expected: []string{
				"Thisisaverylongmessa",
				"gewithoutanyspacesth",
				"atshouldbesplit",
			},
		},
		{
			name:     "empty message",
			input:    "",
			maxLen:   20,
			expected: []string{""},
		},
		{
			name:     "message with newlines",
			input:    "Line 1\nLine 2\nLine 3",
			maxLen:   10,
			expected: []string{"Line 1", "Line 2", "Line 3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitMessage(tt.input, tt.maxLen)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d parts, got %d. Result: %v", len(tt.expected), len(result), result)
			}
			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("part %d: expected %q, got %q", i, expected, result[i])
				}
			}
		})
	}
}

func TestSendMessageWithChunks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "admin",
		SessionTTL:    time.Hour,
	})

	ctx := context.Background()
	chatID := int64(123)

	// Короткое сообщение - должно отправиться одним куском
	shortMsg := "Hello world"
	bot.Reset()
	handler.sendMessageWithChunks(ctx, chatID, shortMsg)
	msgs := bot.Messages()
	if len(msgs) != 1 || msgs[0] != shortMsg {
		t.Errorf("short message: expected 1 message %q, got %v", shortMsg, msgs)
	}

	// Длинное сообщение - должно разбиться на части
	longMsg := ""
	for len(longMsg) < maxMessageLength*2 {
		longMsg += "This is a very long message that should be split into multiple parts because it exceeds the maximum message length limit for Telegram Bot API. "
	}

	bot.Reset()
	handler.sendMessageWithChunks(ctx, chatID, longMsg)
	msgs = bot.Messages()

	// Проверяем, что отправлено несколько сообщений
	if len(msgs) <= 1 {
		t.Errorf("long message: expected multiple messages, got %d: %v", len(msgs), msgs)
	}

	// Проверяем, что все части вместе дают оригинальное сообщение (с нормализацией пробелов)
	var reconstructed string
	for _, msg := range msgs {
		reconstructed += msg + " "
	}
	reconstructed = strings.TrimSpace(reconstructed)

	// Нормализуем пробелы в оригинальном сообщении для сравнения
	normalizedOriginal := strings.Join(strings.Fields(longMsg), " ")
	if reconstructed != normalizedOriginal {
		t.Errorf("reconstructed message doesn't match original.\nOriginal: %q\nReconstructed: %q", normalizedOriginal, reconstructed)
	}

	// Проверяем, что каждая часть не превышает максимальную длину
	for i, msg := range msgs {
		if len(msg) > maxMessageLength {
			t.Errorf("message part %d exceeds max length: %d > %d", i, len(msg), maxMessageLength)
		}
	}
}

func TestSendThinkingAnimation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "admin",
		SessionTTL:    time.Hour,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	chatID := int64(123)
	bot.Reset()

	_, cancelAnimation, err := handler.sendThinkingAnimation(ctx, chatID)
	if err != nil {
		t.Fatalf("sendThinkingAnimation failed: %v", err)
	}
	defer cancelAnimation()

	// Ждем немного, чтобы анимация успела поработать
	time.Sleep(1200 * time.Millisecond)

	cancelAnimation()

	msgs := bot.Messages()
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d: %v", len(msgs), msgs)
	}

	// Проверяем, что сообщение было создано
	if msgs[0] == "" {
		t.Errorf("expected message to be created")
	}
}
