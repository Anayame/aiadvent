package llm

import (
	"context"
	"time"
)

// Message представляет одно сообщение в диалоге.
type Message struct {
	Role      string    `json:"role"`      // "system", "user", "assistant"
	Content   string    `json:"content"`   // текст сообщения
	Timestamp time.Time `json:"timestamp"` // время добавления
}

// DialogStore интерфейс для хранения истории диалогов.
type DialogStore interface {
	// Get возвращает историю сообщений для диалога.
	// Второй параметр bool указывает, найден ли диалог.
	Get(ctx context.Context, dialogID string) ([]Message, bool, error)

	// Append добавляет новые сообщения к существующему диалогу.
	// Если диалога не существует, он будет создан.
	Append(ctx context.Context, dialogID string, messages ...Message) error

	// Set устанавливает полную историю сообщений для диалога, заменяя существующую.
	Set(ctx context.Context, dialogID string, messages []Message) error

	// Delete удаляет диалог и всю его историю.
	Delete(ctx context.Context, dialogID string) error

	// ClearExpired удаляет диалоги, у которых истёк TTL.
	// Возвращает количество удалённых диалогов.
	ClearExpired(ctx context.Context, now time.Time) (int, error)
}
