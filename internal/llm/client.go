package llm

import "context"

// Client минимальный публичный интерфейс LLM клиента.
type Client interface {
	ChatCompletion(ctx context.Context, prompt string, model string) (string, error)
	ChatCompletionWithSystem(ctx context.Context, systemPrompt string, prompt string, model string) (string, error)
}
