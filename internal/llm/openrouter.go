package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"aiadvent/internal/config"
	"aiadvent/internal/retry"
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
	retryPolicy  retry.Policy
	logger       *slog.Logger
}

func NewOpenRouterClient(cfg config.OpenRouterConfig, httpClient *http.Client, logger *slog.Logger) Client {
	return &OpenRouterClient{
		apiKey:       cfg.APIKey,
		baseURL:      cfg.BaseURL,
		defaultModel: cfg.DefaultModel,
		httpClient:   httpClient,
		retryPolicy:  retry.DefaultPolicy(),
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

	resp, bodyBytes, err := retry.DoHTTP(ctx, c.retryPolicy, c.logger, func(ctx context.Context) (*http.Response, []byte, error) {
		return c.doRequest(ctx, requestBody)
	})
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, snippet(bodyBytes, 200))
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

func (c *OpenRouterClient) doRequest(ctx context.Context, body openRouterRequest) (*http.Response, []byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/chat/completions", c.baseURL), bytes.NewReader(buf))
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("read response: %w", err)
	}

	return resp, bodyBytes, nil
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

func snippet(body []byte, limit int) string {
	if len(body) == 0 || limit <= 0 {
		return ""
	}
	if len(body) <= limit {
		return string(body)
	}
	return string(body[:limit])
}
