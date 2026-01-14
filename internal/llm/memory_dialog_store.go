package llm

import (
	"context"
	"sync"
	"time"
)

// dialogData содержит историю диалога и метаданные для TTL.
type dialogData struct {
	messages    []Message
	createdAt   time.Time
	lastTouched time.Time
}

// MemoryDialogStore потокобезопасное in-memory хранилище диалогов с поддержкой TTL.
type MemoryDialogStore struct {
	mu      sync.RWMutex
	dialogs map[string]dialogData
	ttl     time.Duration
}

// NewMemoryDialogStore создаёт новое in-memory хранилище диалогов.
// ttl определяет, как долго диалог хранится без активности.
// Если ttl == 0, диалоги никогда не истекают.
func NewMemoryDialogStore(ttl time.Duration) *MemoryDialogStore {
	return &MemoryDialogStore{
		dialogs: make(map[string]dialogData),
		ttl:     ttl,
	}
}

// Get возвращает историю сообщений для диалога.
// Ленивая очистка: если диалог истёк, он удаляется и возвращается false.
func (s *MemoryDialogStore) Get(ctx context.Context, dialogID string) ([]Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, ok := s.dialogs[dialogID]
	if !ok {
		return nil, false, nil
	}

	// Проверяем TTL (ленивая очистка)
	if s.ttl > 0 && time.Since(data.lastTouched) > s.ttl {
		delete(s.dialogs, dialogID)
		return nil, false, nil
	}

	// Возвращаем копию, чтобы избежать изменений снаружи
	messages := make([]Message, len(data.messages))
	copy(messages, data.messages)
	return messages, true, nil
}

// Append добавляет новые сообщения к диалогу.
// Если диалога не существует, он создаётся.
// Обновляет lastTouched.
func (s *MemoryDialogStore) Append(ctx context.Context, dialogID string, messages ...Message) error {
	if len(messages) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	data, ok := s.dialogs[dialogID]
	if !ok {
		data = dialogData{
			messages:    make([]Message, 0, len(messages)),
			createdAt:   now,
			lastTouched: now,
		}
	}

	data.messages = append(data.messages, messages...)
	data.lastTouched = now
	s.dialogs[dialogID] = data

	return nil
}

// Set заменяет всю историю диалога новыми сообщениями.
// Обновляет lastTouched.
func (s *MemoryDialogStore) Set(ctx context.Context, dialogID string, messages []Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	data, ok := s.dialogs[dialogID]
	if !ok {
		data = dialogData{
			createdAt:   now,
			lastTouched: now,
		}
	}

	// Копируем сообщения, чтобы избежать изменений снаружи
	data.messages = make([]Message, len(messages))
	copy(data.messages, messages)
	data.lastTouched = now
	s.dialogs[dialogID] = data

	return nil
}

// Delete удаляет диалог.
func (s *MemoryDialogStore) Delete(ctx context.Context, dialogID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.dialogs, dialogID)
	return nil
}

// ClearExpired удаляет все диалоги, у которых истёк TTL относительно переданного времени now.
// Возвращает количество удалённых диалогов.
func (s *MemoryDialogStore) ClearExpired(ctx context.Context, now time.Time) (int, error) {
	if s.ttl == 0 {
		return 0, nil // TTL не установлен, ничего не удаляем
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var deleted int
	for dialogID, data := range s.dialogs {
		if now.Sub(data.lastTouched) > s.ttl {
			delete(s.dialogs, dialogID)
			deleted++
		}
	}

	return deleted, nil
}
