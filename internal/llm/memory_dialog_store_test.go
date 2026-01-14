package llm

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMemoryDialogStore_GetEmpty(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found {
		t.Fatalf("expected not found, but found")
	}
	if messages != nil {
		t.Fatalf("expected nil messages, got: %v", messages)
	}
}

func TestMemoryDialogStore_AppendAndGet(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	msg1 := Message{Role: "user", Content: "Hello", Timestamp: time.Now()}
	msg2 := Message{Role: "assistant", Content: "Hi", Timestamp: time.Now()}

	if err := store.Append(ctx, "dialog1", msg1); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found, but not found")
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got: %d", len(messages))
	}
	if messages[0].Content != "Hello" {
		t.Fatalf("expected 'Hello', got: %s", messages[0].Content)
	}

	// Добавляем ещё одно сообщение
	if err := store.Append(ctx, "dialog1", msg2); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	messages, found, err = store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found, but not found")
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got: %d", len(messages))
	}
}

func TestMemoryDialogStore_Set(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	msg1 := Message{Role: "user", Content: "First", Timestamp: time.Now()}
	msg2 := Message{Role: "assistant", Content: "Second", Timestamp: time.Now()}

	if err := store.Append(ctx, "dialog1", msg1); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Заменяем всю историю
	newMessages := []Message{msg2}
	if err := store.Set(ctx, "dialog1", newMessages); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found, but not found")
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message after Set, got: %d", len(messages))
	}
	if messages[0].Content != "Second" {
		t.Fatalf("expected 'Second', got: %s", messages[0].Content)
	}
}

func TestMemoryDialogStore_Delete(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	msg := Message{Role: "user", Content: "Test", Timestamp: time.Now()}
	if err := store.Append(ctx, "dialog1", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if err := store.Delete(ctx, "dialog1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found {
		t.Fatalf("expected not found after delete, but found")
	}
	if messages != nil {
		t.Fatalf("expected nil messages after delete, got: %v", messages)
	}
}

func TestMemoryDialogStore_TTL_Expired(t *testing.T) {
	ttl := 100 * time.Millisecond
	store := NewMemoryDialogStore(ttl)
	ctx := context.Background()

	msg := Message{Role: "user", Content: "Test", Timestamp: time.Now()}
	if err := store.Append(ctx, "dialog1", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Ждём истечения TTL
	time.Sleep(ttl + 50*time.Millisecond)

	// Ленивая очистка при Get
	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if found {
		t.Fatalf("expected not found (expired), but found")
	}
	if messages != nil {
		t.Fatalf("expected nil messages (expired), got: %v", messages)
	}
}

func TestMemoryDialogStore_TTL_NotExpired(t *testing.T) {
	ttl := 1 * time.Second
	store := NewMemoryDialogStore(ttl)
	ctx := context.Background()

	msg := Message{Role: "user", Content: "Test", Timestamp: time.Now()}
	if err := store.Append(ctx, "dialog1", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Не ждём истечения TTL
	time.Sleep(100 * time.Millisecond)

	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found (not expired), but not found")
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got: %d", len(messages))
	}
}

func TestMemoryDialogStore_TTL_ZeroNeverExpires(t *testing.T) {
	store := NewMemoryDialogStore(0)
	ctx := context.Background()

	msg := Message{Role: "user", Content: "Test", Timestamp: time.Now()}
	if err := store.Append(ctx, "dialog1", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Даже с давним временем диалог не должен истечь
	farFuture := time.Now().Add(100 * time.Hour)
	deleted, err := store.ClearExpired(ctx, farFuture)
	if err != nil {
		t.Fatalf("ClearExpired failed: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deleted (ttl=0), got: %d", deleted)
	}

	messages, found, err := store.Get(ctx, "dialog1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !found {
		t.Fatalf("expected found (ttl=0), but not found")
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got: %d", len(messages))
	}
}

func TestMemoryDialogStore_ClearExpired(t *testing.T) {
	ttl := 1 * time.Second
	store := NewMemoryDialogStore(ttl)
	ctx := context.Background()

	msg := Message{Role: "user", Content: "Test", Timestamp: time.Now()}

	// Создаём три диалога
	if err := store.Append(ctx, "dialog1", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if err := store.Append(ctx, "dialog2", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if err := store.Append(ctx, "dialog3", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Ждём истечения TTL
	time.Sleep(ttl + 100*time.Millisecond)

	// Обновляем dialog3, чтобы он не истёк
	if err := store.Append(ctx, "dialog3", msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Очищаем истёкшие
	now := time.Now()
	deleted, err := store.ClearExpired(ctx, now)
	if err != nil {
		t.Fatalf("ClearExpired failed: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 deleted, got: %d", deleted)
	}

	// Проверяем, что dialog1 и dialog2 удалены
	_, found1, _ := store.Get(ctx, "dialog1")
	if found1 {
		t.Fatalf("expected dialog1 not found")
	}
	_, found2, _ := store.Get(ctx, "dialog2")
	if found2 {
		t.Fatalf("expected dialog2 not found")
	}

	// Проверяем, что dialog3 остался
	_, found3, _ := store.Get(ctx, "dialog3")
	if !found3 {
		t.Fatalf("expected dialog3 found")
	}
}

func TestMemoryDialogStore_Concurrency(t *testing.T) {
	store := NewMemoryDialogStore(24 * time.Hour)
	ctx := context.Background()

	var wg sync.WaitGroup
	iterations := 100

	// Параллельные записи в разные диалоги
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dialogID := string(rune('A' + id))
			for j := 0; j < iterations; j++ {
				msg := Message{
					Role:      "user",
					Content:   "msg",
					Timestamp: time.Now(),
				}
				if err := store.Append(ctx, dialogID, msg); err != nil {
					t.Errorf("Append failed: %v", err)
				}
			}
		}(i)
	}

	// Параллельные чтения
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dialogID := string(rune('A' + id))
			for j := 0; j < iterations; j++ {
				_, _, err := store.Get(ctx, dialogID)
				if err != nil {
					t.Errorf("Get failed: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Проверяем, что все диалоги записались
	for i := 0; i < 10; i++ {
		dialogID := string(rune('A' + i))
		messages, found, err := store.Get(ctx, dialogID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if !found {
			t.Fatalf("expected dialog %s found", dialogID)
		}
		if len(messages) != iterations {
			t.Fatalf("expected %d messages for dialog %s, got: %d", iterations, dialogID, len(messages))
		}
	}
}
