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
	SendMessage(ctx context.Context, chatID int64, text string) (int64, error)
	SendMessageWithKeyboard(ctx context.Context, chatID int64, text string, keyboard *InlineKeyboardMarkup) (int64, error)
	EditMessage(ctx context.Context, chatID int64, messageID int64, text string) error
	EditMessageKeyboard(ctx context.Context, chatID int64, messageID int64, text string, keyboard *InlineKeyboardMarkup) error
	AnswerCallbackQuery(ctx context.Context, callbackQueryID string, text string) error
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

func (c *HTTPBotClient) SendMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	payload := sendMessageRequest{
		ChatID: chatID,
		Text:   text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal telegram request: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", c.baseURL, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("execute telegram request: %w", err)
	}
	defer resp.Body.Close()

	var response SendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("decode telegram response: %w", err)
	}

	if !response.Ok {
		return 0, fmt.Errorf("telegram api error")
	}

	return response.Result.MessageID, nil
}

type sendMessageRequest struct {
	ChatID      int64                 `json:"chat_id"`
	Text        string                `json:"text"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type editMessageRequest struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Text        string                `json:"text"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

type answerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}

func (c *HTTPBotClient) SendMessageWithKeyboard(ctx context.Context, chatID int64, text string, keyboard *InlineKeyboardMarkup) (int64, error) {
	payload := sendMessageRequest{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: keyboard,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal telegram request: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", c.baseURL, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("execute telegram request: %w", err)
	}
	defer resp.Body.Close()

	var response SendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return 0, fmt.Errorf("decode telegram response: %w", err)
	}

	if !response.Ok {
		return 0, fmt.Errorf("telegram api error")
	}

	return response.Result.MessageID, nil
}

func (c *HTTPBotClient) EditMessage(ctx context.Context, chatID int64, messageID int64, text string) error {
	return c.EditMessageKeyboard(ctx, chatID, messageID, text, nil)
}

func (c *HTTPBotClient) EditMessageKeyboard(ctx context.Context, chatID int64, messageID int64, text string, keyboard *InlineKeyboardMarkup) error {
	payload := editMessageRequest{
		ChatID:      chatID,
		MessageID:   messageID,
		Text:        text,
		ReplyMarkup: keyboard,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram request: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/editMessageText", c.baseURL, c.token)
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

func (c *HTTPBotClient) AnswerCallbackQuery(ctx context.Context, callbackQueryID string, text string) error {
	payload := answerCallbackQueryRequest{
		CallbackQueryID: callbackQueryID,
		Text:            text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram request: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/answerCallbackQuery", c.baseURL, c.token)
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
