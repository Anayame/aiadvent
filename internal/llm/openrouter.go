package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"aiadvent/internal/config"
	"log/slog"
)

var (
	ErrInvalidModel = errors.New("model is required")
)

type OpenRouterClient struct {
	apiKey       string
	baseURL      string
	defaultModel string
	httpClient   *http.Client
	retryCount   int
	backoff      time.Duration
	logger       *slog.Logger
}

func NewOpenRouterClient(cfg config.OpenRouterConfig, httpClient *http.Client, logger *slog.Logger) Client {
	return &OpenRouterClient{
		apiKey:       cfg.APIKey,
		baseURL:      cfg.BaseURL,
		defaultModel: cfg.DefaultModel,
		httpClient:   httpClient,
		retryCount:   2,
		backoff:      500 * time.Millisecond,
		logger:       logger,
	}
}

func (c *OpenRouterClient) ChatCompletion(ctx context.Context, prompt string, model string) (string, error) {
	return c.ChatCompletionWithSystem(ctx, "", prompt, model)
}

func (c *OpenRouterClient) ChatCompletionWithSystem(ctx context.Context, systemPrompt string, prompt string, model string) (string, error) {
	if model == "" {
		model = c.defaultModel
	}
	if model == "" {
		return "", ErrInvalidModel
	}

	messages := make([]message, 0, 2)
	if systemPrompt != "" {
		messages = append(messages, message{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, message{Role: "user", Content: prompt})

	return c.chatCompletionWithMessages(ctx, model, messages)
}

// chatCompletionWithMessages выполняет запрос к LLM с произвольным набором сообщений.
func (c *OpenRouterClient) chatCompletionWithMessages(ctx context.Context, model string, messages []message) (string, error) {
	if model == "" {
		model = c.defaultModel
	}
	if model == "" {
		return "", ErrInvalidModel
	}

	requestBody := openRouterRequest{
		Model:    model,
		Messages: messages,
	}

	var lastErr error
	for attempt := 0; attempt <= c.retryCount; attempt++ {
		answer, err := c.doRequest(ctx, requestBody)
		if err == nil {
			return answer, nil
		}
		if !shouldRetry(err) || attempt == c.retryCount {
			return "", err
		}
		lastErr = err
		if c.logger != nil {
			c.logger.Warn("openrouter retry",
				slog.Int("attempt", attempt+1),
				slog.String("error", err.Error()))
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(c.backoff * time.Duration(attempt+1)):
		}
	}
	return "", fmt.Errorf("openrouter request failed: %w", lastErr)
}

func (c *OpenRouterClient) doRequest(ctx context.Context, body openRouterRequest) (string, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/chat/completions", c.baseURL), bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return "", &transientError{status: resp.StatusCode, body: string(bodyBytes)}
	}

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var parsed openRouterResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", errors.New("empty response from model")
	}
	return parsed.Choices[0].Message.Content, nil
}

func shouldRetry(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var te *transientError
	return errors.As(err, &te)
}

type openRouterRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
}

type transientError struct {
	status int
	body   string
}

func (e *transientError) Error() string {
	return fmt.Sprintf("transient status %d: %s", e.status, e.body)
}
