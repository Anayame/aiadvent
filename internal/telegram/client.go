package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"aiadvent/internal/config"
)

type BotClient interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

type HTTPBotClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

func NewClient(cfg config.TelegramConfig, httpClient *http.Client) BotClient {
	return &HTTPBotClient{
		token:      cfg.BotToken,
		baseURL:    cfg.APIBaseURL,
		httpClient: httpClient,
	}
}

func (c *HTTPBotClient) SendMessage(ctx context.Context, chatID int64, text string) error {
	payload := sendMessageRequest{
		ChatID: chatID,
		Text:   text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram request: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", c.baseURL, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute telegram request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram api status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type sendMessageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}
