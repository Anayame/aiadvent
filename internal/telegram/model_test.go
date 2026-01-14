package telegram

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"aiadvent/internal/auth"
	"aiadvent/internal/llm"
)

// stubLLMWithModel сохраняет информацию о переданной модели
type stubLLMWithModel struct {
	answer     string
	usedModel  string
	usedPrompt string
}

func (s *stubLLMWithModel) ChatCompletion(ctx context.Context, prompt string, model string) (string, error) {
	s.usedModel = model
	s.usedPrompt = prompt
	return s.answer, nil
}

func (s *stubLLMWithModel) ChatCompletionWithSystem(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
	s.usedModel = model
	s.usedPrompt = prompt
	return s.answer, nil
}

func TestModelCommand_ShowsKeyboard(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	msg := &Message{
		From: &User{ID: 123},
		Chat: Chat{ID: 123},
		Text: "/model",
	}

	handler.dispatch(ctx, msg, "/model")
	waitForMessages(t, bot, 1, 500*time.Millisecond)

	msgs := bot.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}

	response := msgs[0]
	// Проверяем, что ответ содержит информацию о текущей модели
	if !strings.Contains(response, "Текущая модель") {
		t.Errorf("expected response to contain current model info, got: %s", response)
	}
	if !strings.Contains(response, "Выберите модель") {
		t.Errorf("expected response to contain selection prompt, got: %s", response)
	}
}

func TestModelCallback_SelectModel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)

	// Симулируем нажатие на кнопку выбора модели (индекс 1 = вторая модель)
	cb := &CallbackQuery{
		ID:   "test_callback_id",
		From: &User{ID: userID},
		Message: &Message{
			MessageID: 100,
			Chat:      Chat{ID: 123},
		},
		Data: "model:1",
	}

	handler.handleCallbackQuery(ctx, cb)

	// Проверяем, что модель сохранилась
	selectedModel := handler.getSelectedModel(userID)
	expectedModel := llm.AvailableModels[1].ID
	if selectedModel != expectedModel {
		t.Errorf("expected model %s, got %s", expectedModel, selectedModel)
	}
}

func TestModelCallback_InvalidIndex(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)

	// Симулируем нажатие с неверным индексом
	cb := &CallbackQuery{
		ID:   "test_callback_id",
		From: &User{ID: userID},
		Message: &Message{
			MessageID: 100,
			Chat:      Chat{ID: 123},
		},
		Data: "model:99",
	}

	handler.handleCallbackQuery(ctx, cb)

	// Модель не должна измениться
	selectedModel := handler.getSelectedModel(userID)
	if selectedModel != "" {
		t.Errorf("expected no model selected, got %s", selectedModel)
	}
}

func TestModelCallback_RequiresAuth(t *testing.T) {
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

	ctx := context.Background()

	// Без авторизации
	cb := &CallbackQuery{
		ID:   "test_callback_id",
		From: &User{ID: 999},
		Message: &Message{
			MessageID: 100,
			Chat:      Chat{ID: 999},
		},
		Data: "model:0",
	}

	handler.handleCallbackQuery(ctx, cb)

	// Модель не должна быть установлена
	selectedModel := handler.getSelectedModel(999)
	if selectedModel != "" {
		t.Errorf("expected no model for unauthorized user, got %s", selectedModel)
	}
}

func TestAskWithSelectedModel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	llmStub := &stubLLMWithModel{answer: "test answer"}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           llmStub,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)

	// Устанавливаем модель напрямую
	targetModel := llm.AvailableModels[2].ID
	handler.setSelectedModel(userID, targetModel)

	// Включаем режим ask
	handler.setAskMode(userID, true)

	// Отправляем вопрос
	msg := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: 123},
		Text: "test question",
	}

	handler.dispatch(ctx, msg, "test question")
	waitForMessages(t, bot, 1, 500*time.Millisecond)

	// Проверяем, что модель была передана в LLM
	if llmStub.usedModel != targetModel {
		t.Errorf("expected model %s, got %s", targetModel, llmStub.usedModel)
	}
}

func TestModelCallback_ResendsLastQuestion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	llmStub := &stubLLMWithModel{answer: "test answer"}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           llmStub,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)

	// Включаем режим ask
	handler.setAskMode(userID, true)

	// Сохраняем последний вопрос напрямую
	lastQuestion := "Мой предыдущий вопрос"
	handler.setLastQuestion(userID, lastQuestion)

	// Меняем модель через callback
	cb := &CallbackQuery{
		ID:   "test_callback_id",
		From: &User{ID: userID},
		Message: &Message{
			MessageID: 100,
			Chat:      Chat{ID: 123},
		},
		Data: "model:2", // Третья модель (индекс 2)
	}

	handler.handleCallbackQuery(ctx, cb)
	waitForMessages(t, bot, 1, 1000*time.Millisecond)

	// Проверяем, что последний вопрос был переотправлен
	if llmStub.usedPrompt != lastQuestion {
		t.Errorf("expected prompt %q, got %q", lastQuestion, llmStub.usedPrompt)
	}

	// Проверяем, что использовалась новая модель
	expectedModel := llm.AvailableModels[2].ID
	if llmStub.usedModel != expectedModel {
		t.Errorf("expected model %s, got %s", expectedModel, llmStub.usedModel)
	}
}

func TestModelChange_ShowsModelNameOnce(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	llmStub := &stubLLMWithModel{answer: "test answer"}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           llmStub,
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)

	// Устанавливаем модель и флаг showModelName
	targetModel := llm.AvailableModels[0].ID
	handler.setSelectedModel(userID, targetModel)
	handler.setShowModelName(userID, true)
	handler.setAskMode(userID, true)

	// Первый вопрос - должен показать название модели
	msg1 := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: 123},
		Text: "вопрос 1",
	}
	handler.dispatch(ctx, msg1, "вопрос 1")
	waitForMessages(t, bot, 1, 500*time.Millisecond)

	// Проверяем, что флаг сброшен
	if handler.getAndClearShowModelName(userID) {
		t.Error("expected showModelName to be cleared after first answer")
	}

	// Второй вопрос - не должен показывать название модели
	bot.Reset()
	msg2 := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: 123},
		Text: "вопрос 2",
	}
	handler.dispatch(ctx, msg2, "вопрос 2")
	waitForMessages(t, bot, 1, 500*time.Millisecond)
}

func TestLastQuestion_SavedOnAsk(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)

	// Включаем режим ask
	handler.setAskMode(userID, true)

	question := "Мой тестовый вопрос"
	msg := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: 123},
		Text: question,
	}

	handler.dispatch(ctx, msg, question)
	waitForMessages(t, bot, 1, 500*time.Millisecond)

	// Проверяем, что вопрос сохранён
	savedQuestion := handler.getLastQuestion(userID)
	if savedQuestion != question {
		t.Errorf("expected last question %q, got %q", question, savedQuestion)
	}
}

func TestModelCallback_AlreadySelected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	handler := NewWebhookHandler(WebhookDeps{
		Auth:          authService,
		LLM:           &stubLLM{answer: "ok"},
		Bot:           bot,
		Logger:        logger,
		AdminPassword: "pass",
	})

	ctx := context.Background()
	userID := int64(123)

	// Устанавливаем модель
	targetModel := llm.AvailableModels[0].ID
	handler.setSelectedModel(userID, targetModel)

	// Пытаемся выбрать ту же модель снова через callback
	cb := &CallbackQuery{
		ID:   "test_callback_id",
		From: &User{ID: userID},
		Message: &Message{
			MessageID: 100,
			Chat:      Chat{ID: 123},
		},
		Data: "model:0",
	}

	handler.handleCallbackQuery(ctx, cb)

	// Модель должна остаться той же
	selectedModel := handler.getSelectedModel(userID)
	if selectedModel != targetModel {
		t.Errorf("expected model %s, got %s", targetModel, selectedModel)
	}
}

func TestBuildModelKeyboard(t *testing.T) {
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

	// Тест без выбранной модели
	keyboard := handler.buildModelKeyboard("")
	if keyboard == nil {
		t.Fatal("expected keyboard to be created")
	}
	if len(keyboard.InlineKeyboard) != len(llm.AvailableModels) {
		t.Errorf("expected %d rows, got %d", len(llm.AvailableModels), len(keyboard.InlineKeyboard))
	}

	// Тест с выбранной моделью
	selectedModel := llm.AvailableModels[1].ID
	keyboard = handler.buildModelKeyboard(selectedModel)

	// Проверяем, что у выбранной модели есть галочка
	for i, row := range keyboard.InlineKeyboard {
		if len(row) != 1 {
			t.Errorf("expected 1 button in row %d, got %d", i, len(row))
			continue
		}
		button := row[0]
		if llm.AvailableModels[i].ID == selectedModel {
			if !strings.HasPrefix(button.Text, "✓") {
				t.Errorf("expected selected model to have checkmark, got: %s", button.Text)
			}
		} else {
			if strings.HasPrefix(button.Text, "✓") {
				t.Errorf("expected non-selected model to not have checkmark, got: %s", button.Text)
			}
		}
	}
}

func TestDialogModeWithSelectedModel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	bot := &stubBot{}
	authService := auth.NewService("pass", time.Hour, auth.NewMemoryStore())
	if _, err := authService.Login(context.Background(), 123, "pass"); err != nil {
		t.Fatalf("failed to login test user: %v", err)
	}

	var usedModel string
	dialogService := &stubDialogService{
		createPlanFunc: func(ctx context.Context, dialogID string, model string, userMessage string) (string, error) {
			usedModel = model
			return "План создан", nil
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

	// Устанавливаем выбранную модель
	targetModel := llm.AvailableModels[2].ID
	handler.setSelectedModel(userID, targetModel)

	// Включаем режим create_plan
	handler.setDialogMode(userID, dialogModeCreatePlan, "test-dialog-id")

	// Отправляем сообщение
	msg := &Message{
		From: &User{ID: userID},
		Chat: Chat{ID: 123},
		Text: "Создай план",
	}

	handler.dispatch(ctx, msg, "Создай план")
	waitForMessages(t, bot, 1, 500*time.Millisecond)

	// Проверяем, что выбранная модель была использована
	if usedModel != targetModel {
		t.Errorf("expected model %s, got %s", targetModel, usedModel)
	}
}
