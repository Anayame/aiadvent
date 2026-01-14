package telegram

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"aiadvent/internal/auth"
)

// stubDialogService реализует интерфейс DialogService для тестов.
type stubDialogService struct {
	chatFunc             func(ctx context.Context, dialogID string, model string, systemPrompt string, userMessage string) (string, error)
	clearDialogFunc      func(ctx context.Context, dialogID string) error
	createPlanFunc       func(ctx context.Context, dialogID string, model string, userMessage string) (string, error)
	replayCreatePlanFunc func(ctx context.Context, dialogID string, model string) (string, error)
}

func (m *stubDialogService) Chat(ctx context.Context, dialogID string, model string, systemPrompt string, userMessage string) (string, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, dialogID, model, systemPrompt, userMessage)
	}
	return "", errors.New("not implemented")
}

func (m *stubDialogService) ClearDialog(ctx context.Context, dialogID string) error {
	if m.clearDialogFunc != nil {
		return m.clearDialogFunc(ctx, dialogID)
	}
	return nil
}

func (m *stubDialogService) CreatePlan(ctx context.Context, dialogID string, model string, userMessage string) (string, error) {
	if m.createPlanFunc != nil {
		return m.createPlanFunc(ctx, dialogID, model, userMessage)
	}
	return "", errors.New("not implemented")
}

func (m *stubDialogService) ReplayCreatePlan(ctx context.Context, dialogID string, model string) (string, error) {
	if m.replayCreatePlanFunc != nil {
		return m.replayCreatePlanFunc(ctx, dialogID, model)
	}
	return "", errors.New("not implemented")
}

func TestDialogMode_CreatePlan(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	dialogService := &stubDialogService{
		createPlanFunc: func(ctx context.Context, dialogID string, model string, userMessage string) (string, error) {
			return "Отлично! Расскажите подробнее о функциональности.", nil
		},
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		DialogService: dialogService,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)
	chatID := int64(123)

	// Отправляем команду /create_plan
	msg := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "/create_plan",
	}
	handler.dispatch(ctx, msg, "/create_plan")

	// Проверяем, что режим диалога установлен
	mode, dialogID := handler.getDialogState(userID)
	if mode != dialogModeCreatePlan {
		t.Errorf("expected mode %s, got: %s", dialogModeCreatePlan, mode)
	}
	if dialogID == "" {
		t.Error("expected dialogID to be set")
	}

	// Отправляем сообщение в режиме диалога
	msg2 := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "Нужен сайт для интернет-магазина",
	}
	handler.dispatch(ctx, msg2, "Нужен сайт для интернет-магазина")

	// Ждём, чтобы асинхронная обработка завершилась
	waitForMessages(t, bot, 2, 500*time.Millisecond)

	// Проверяем, что режим диалога всё ещё активен
	mode2, _ := handler.getDialogState(userID)
	if mode2 != dialogModeCreatePlan {
		t.Errorf("expected mode %s to remain active, got: %s", dialogModeCreatePlan, mode2)
	}
}

func TestDialogMode_End(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	dialogCleared := false
	dialogService := &stubDialogService{
		clearDialogFunc: func(ctx context.Context, dialogID string) error {
			dialogCleared = true
			return nil
		},
		createPlanFunc: func(ctx context.Context, dialogID string, model string, userMessage string) (string, error) {
			return "OK", nil
		},
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		DialogService: dialogService,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)
	chatID := int64(123)

	// Отправляем команду /create_plan
	msg := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "/create_plan",
	}
	handler.dispatch(ctx, msg, "/create_plan")

	// Проверяем, что режим установлен
	mode, dialogID := handler.getDialogState(userID)
	if mode != dialogModeCreatePlan {
		t.Fatalf("expected mode %s, got: %s", dialogModeCreatePlan, mode)
	}
	if dialogID == "" {
		t.Fatal("expected dialogID to be set")
	}

	// Отправляем команду /end
	msg2 := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "/end",
	}
	handler.dispatch(ctx, msg2, "/end")

	// Ждём асинхронной обработки
	waitForMessages(t, bot, 2, 500*time.Millisecond)

	// Проверяем, что режим диалога очищен
	mode2, dialogID2 := handler.getDialogState(userID)
	if mode2 != "" {
		t.Errorf("expected mode to be cleared, got: %s", mode2)
	}
	if dialogID2 != "" {
		t.Errorf("expected dialogID to be cleared, got: %s", dialogID2)
	}

	// Проверяем, что ClearDialog был вызван
	if !dialogCleared {
		t.Error("expected ClearDialog to be called")
	}
}

func TestDialogMode_RestartClearsOld(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	clearedDialogs := []string{}
	dialogService := &stubDialogService{
		clearDialogFunc: func(ctx context.Context, dialogID string) error {
			clearedDialogs = append(clearedDialogs, dialogID)
			return nil
		},
		createPlanFunc: func(ctx context.Context, dialogID string, model string, userMessage string) (string, error) {
			return "OK", nil
		},
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		DialogService: dialogService,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)
	chatID := int64(123)

	// Отправляем команду /create_plan первый раз
	msg := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "/create_plan",
	}
	handler.dispatch(ctx, msg, "/create_plan")

	_, dialogID1 := handler.getDialogState(userID)
	if dialogID1 == "" {
		t.Fatal("expected first dialogID to be set")
	}

	// Небольшая задержка, чтобы timestamp гарантированно отличался
	time.Sleep(10 * time.Millisecond)

	// Отправляем команду /create_plan снова
	msg2 := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "/create_plan",
	}
	handler.dispatch(ctx, msg2, "/create_plan")

	_, dialogID2 := handler.getDialogState(userID)
	if dialogID2 == "" {
		t.Fatal("expected second dialogID to be set")
	}
	if dialogID1 == dialogID2 {
		t.Error("expected new dialogID to be different from old one")
	}

	// Проверяем, что старый диалог был удалён
	if len(clearedDialogs) != 1 {
		t.Errorf("expected 1 dialog to be cleared, got: %d", len(clearedDialogs))
	}
	if len(clearedDialogs) > 0 && clearedDialogs[0] != dialogID1 {
		t.Errorf("expected first dialog %s to be cleared, got: %s", dialogID1, clearedDialogs[0])
	}
}

func TestDialogMode_ErrorDoesNotClearMode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	dialogService := &stubDialogService{
		createPlanFunc: func(ctx context.Context, dialogID string, model string, userMessage string) (string, error) {
			return "", errors.New("LLM error")
		},
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		DialogService: dialogService,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)
	chatID := int64(123)

	// Отправляем команду /create_plan
	msg := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "/create_plan",
	}
	handler.dispatch(ctx, msg, "/create_plan")

	mode, dialogID := handler.getDialogState(userID)
	if mode != dialogModeCreatePlan {
		t.Fatalf("expected mode %s, got: %s", dialogModeCreatePlan, mode)
	}

	// Отправляем сообщение, которое вызовет ошибку
	msg2 := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: chatID},
		Text: "Тестовое сообщение",
	}
	handler.dispatch(ctx, msg2, "Тестовое сообщение")

	// Ждём асинхронной обработки
	waitForMessages(t, bot, 2, 500*time.Millisecond)

	// Проверяем, что режим диалога всё ещё активен (ошибка не должна его сбросить)
	mode2, dialogID2 := handler.getDialogState(userID)
	if mode2 != dialogModeCreatePlan {
		t.Errorf("expected mode %s to remain after error, got: %s", dialogModeCreatePlan, mode2)
	}
	if dialogID2 != dialogID {
		t.Errorf("expected dialogID to remain after error")
	}
}
