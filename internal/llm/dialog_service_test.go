package llm

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// mockClient реализует интерфейс Client для тестов.
type mockClient struct {
	chatCompletionFunc           func(ctx context.Context, prompt string, model string) (string, error)
	chatCompletionWithSystemFunc func(ctx context.Context, systemPrompt string, prompt string, model string) (string, error)
}

func (m *mockClient) ChatCompletion(ctx context.Context, prompt string, model string) (string, error) {
	if m.chatCompletionFunc != nil {
		return m.chatCompletionFunc(ctx, prompt, model)
	}
	return "", errors.New("not implemented")
}

func (m *mockClient) ChatCompletionWithSystem(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
	if m.chatCompletionWithSystemFunc != nil {
		return m.chatCompletionWithSystemFunc(ctx, systemPrompt, prompt, model)
	}
	return "", errors.New("not implemented")
}

func TestDialogService_Chat_NewDialog(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	client := &mockClient{
		chatCompletionWithSystemFunc: func(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
			if prompt != "Hello" {
				t.Errorf("expected prompt 'Hello', got: %s", prompt)
			}
			return "Hi there!", nil
		},
	}

	service := NewDialogService(DialogServiceConfig{
		Client:       client,
		DialogStore:  store,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	ctx := context.Background()
	answer, err := service.Chat(ctx, "dialog1", "", "You are helpful", "Hello")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if answer != "Hi there!" {
		t.Errorf("expected 'Hi there!', got: %s", answer)
	}

	// Проверяем, что история сохранилась
	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected dialog to be saved")
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got: %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "Hello" {
		t.Errorf("expected first message to be user 'Hello', got: %v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "Hi there!" {
		t.Errorf("expected second message to be assistant 'Hi there!', got: %v", messages[1])
	}
}

func TestDialogService_Chat_ContinueDialog(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	// Предзаполним историю
	err := store.Append(ctx, "dialog1",
		Message{Role: "user", Content: "First", Timestamp: time.Now()},
		Message{Role: "assistant", Content: "First reply", Timestamp: time.Now()},
	)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Мок проверяет, что в prompt включена история
	client := &mockClient{
		chatCompletionWithSystemFunc: func(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
			// В fallback режиме история объединяется в один prompt
			if !strings.Contains(prompt, "First") {
				t.Errorf("expected history to be included in prompt, got: %s", prompt)
			}
			return "Second reply", nil
		},
	}

	service := NewDialogService(DialogServiceConfig{
		Client:       client,
		DialogStore:  store,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	answer, err := service.Chat(ctx, "dialog1", "", "You are helpful", "Second")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	if answer != "Second reply" {
		t.Errorf("expected 'Second reply', got: %s", answer)
	}

	// Проверяем, что история увеличилась
	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected dialog to be found")
	}
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got: %d", len(messages))
	}
}

func TestDialogService_Chat_ErrorDoesNotSaveHistory(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	client := &mockClient{
		chatCompletionWithSystemFunc: func(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
			return "", errors.New("LLM error")
		},
	}

	service := NewDialogService(DialogServiceConfig{
		Client:       client,
		DialogStore:  store,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	ctx := context.Background()
	_, err := service.Chat(ctx, "dialog1", "", "You are helpful", "Hello")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	// Проверяем, что история НЕ сохранилась
	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found {
		t.Fatalf("expected dialog not to be saved on error")
	}
	if messages != nil {
		t.Fatalf("expected nil messages on error, got: %v", messages)
	}
}

func TestDialogService_Chat_PreservesHistoryOnError(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	// Предзаполним историю
	err := store.Append(ctx, "dialog1",
		Message{Role: "user", Content: "First", Timestamp: time.Now()},
		Message{Role: "assistant", Content: "First reply", Timestamp: time.Now()},
	)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	client := &mockClient{
		chatCompletionWithSystemFunc: func(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
			return "", errors.New("LLM error")
		},
	}

	service := NewDialogService(DialogServiceConfig{
		Client:       client,
		DialogStore:  store,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	_, err = service.Chat(ctx, "dialog1", "", "You are helpful", "Second")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	// Проверяем, что история осталась прежней (2 сообщения)
	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected dialog to be found")
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (history preserved), got: %d", len(messages))
	}
}

func TestDialogService_ClearDialog(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	// Создаём диалог
	err := store.Append(ctx, "dialog1",
		Message{Role: "user", Content: "Test", Timestamp: time.Now()},
	)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	client := &mockClient{}
	service := NewDialogService(DialogServiceConfig{
		Client:       client,
		DialogStore:  store,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	// Очищаем диалог
	err = service.ClearDialog(ctx, "dialog1")
	if err != nil {
		t.Fatalf("ClearDialog failed: %v", err)
	}

	// Проверяем, что диалог удалён
	_, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found {
		t.Fatalf("expected dialog to be deleted")
	}
}

func TestDialogService_CreatePlan(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	client := &mockClient{
		chatCompletionWithSystemFunc: func(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
			// Проверяем, что используется правильный системный промпт
			if !strings.Contains(systemPrompt, "Action Planner") {
				t.Errorf("expected CreatePlan system prompt, got: %s", systemPrompt)
			}
			return "Что именно вы хотите сделать?", nil
		},
	}

	service := NewDialogService(DialogServiceConfig{
		Client:       client,
		DialogStore:  store,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	ctx := context.Background()
	answer, err := service.CreatePlan(ctx, "plan-dialog", "", "Нужен сайт")
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}

	if !strings.Contains(answer, "сделать") && !strings.Contains(answer, "хотите") {
		t.Errorf("unexpected answer: %s", answer)
	}

	// Проверяем, что история сохранилась
	messages, found, err := store.Get(ctx, "plan-dialog")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected dialog to be saved")
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got: %d", len(messages))
	}
}

func TestDialogService_CreatePlan_ErrorDoesNotSaveHistory(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	client := &mockClient{
		chatCompletionWithSystemFunc: func(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
			return "", errors.New("LLM error")
		},
	}

	service := NewDialogService(DialogServiceConfig{
		Client:       client,
		DialogStore:  store,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})

	ctx := context.Background()
	_, err := service.CreatePlan(ctx, "plan-dialog", "", "Нужен сайт")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	// Проверяем, что история НЕ сохранилась
	_, found, err := store.Get(ctx, "plan-dialog")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found {
		t.Fatalf("expected dialog not to be saved on error")
	}
}
