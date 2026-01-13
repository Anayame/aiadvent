package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"aiadvent/internal/auth"
	"aiadvent/internal/httpserver"
	"aiadvent/internal/llm"
	"log/slog"
)

type AuthService interface {
	Login(ctx context.Context, userID int64, password string) (auth.Session, error)
	Logout(ctx context.Context, userID int64)
	IsAuthorized(ctx context.Context, userID int64) bool
}

type WebhookDeps struct {
	Auth          AuthService
	LLM           llm.Client
	Bot           BotClient
	Logger        *slog.Logger
	AdminPassword string
	SessionTTL    time.Duration
	WebhookSecret string
}

type WebhookHandler struct {
	auth          AuthService
	llm           llm.Client
	bot           BotClient
	logger        *slog.Logger
	adminPassword string
	webhookSecret string
}

func NewWebhookHandler(deps WebhookDeps) *WebhookHandler {
	return &WebhookHandler{
		auth:          deps.Auth,
		llm:           deps.LLM,
		bot:           deps.Bot,
		logger:        deps.Logger,
		adminPassword: deps.AdminPassword,
		webhookSecret: deps.WebhookSecret,
	}
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.webhookSecret != "" {
		if secret := r.Header.Get("X-Telegram-Bot-Api-Secret-Token"); secret != h.webhookSecret {
			httpserver.WriteJSONError(w, http.StatusForbidden, "forbidden", "invalid webhook secret")
			return
		}
	}

	var upd Update
	if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
		httpserver.WriteJSONError(w, http.StatusBadRequest, "bad_request", "cannot parse update")
		return
	}
	if upd.Message == nil || upd.Message.From == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.Context()
	text := strings.TrimSpace(upd.Message.Text)

	if text == "" {
		h.reply(ctx, upd.Message.Chat.ID, "Пустое сообщение. Используйте /start.")
		w.WriteHeader(http.StatusOK)
		return
	}

	if strings.HasPrefix(text, "/") {
		h.handleCommand(ctx, upd.Message, text)
	} else {
		h.handleText(ctx, upd.Message, text)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (h *WebhookHandler) handleCommand(ctx context.Context, msg *Message, text string) {
	parts := strings.SplitN(text, " ", 2)
	cmd := parts[0]
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "/start":
		h.reply(ctx, msg.Chat.ID, "Привет! Доступные команды: /login <пароль>, /ask <текст>, /logout, /me")
	case "/login":
		h.handleLogin(ctx, msg, arg)
	case "/logout":
		h.auth.Logout(ctx, msg.From.ID)
		h.reply(ctx, msg.Chat.ID, "Вы вышли")
	case "/me":
		authStatus := "не авторизован"
		if h.auth.IsAuthorized(ctx, msg.From.ID) {
			authStatus = "авторизован"
		}
		h.reply(ctx, msg.Chat.ID, fmt.Sprintf("Ваш id: %d, статус: %s", msg.From.ID, authStatus))
	case "/ask":
		h.handleAsk(ctx, msg, arg)
	default:
		h.reply(ctx, msg.Chat.ID, "Неизвестная команда. Попробуйте /start")
	}
}

func (h *WebhookHandler) handleText(ctx context.Context, msg *Message, text string) {
	if !h.auth.IsAuthorized(ctx, msg.From.ID) {
		h.reply(ctx, msg.Chat.ID, "Нужно войти: /login <пароль>")
		return
	}
	h.handleAsk(ctx, msg, text)
}

func (h *WebhookHandler) handleLogin(ctx context.Context, msg *Message, password string) {
	if password == "" {
		h.reply(ctx, msg.Chat.ID, "Введите пароль: /login <пароль>")
		return
	}
	_, err := h.auth.Login(ctx, msg.From.ID, password)
	if err != nil {
		h.reply(ctx, msg.Chat.ID, "Ошибка авторизации")
		return
	}
	h.reply(ctx, msg.Chat.ID, "Вы успешно вошли")
}

func (h *WebhookHandler) handleAsk(ctx context.Context, msg *Message, question string) {
	if question == "" {
		h.reply(ctx, msg.Chat.ID, "Нужно задать вопрос после /ask")
		return
	}
	if !h.auth.IsAuthorized(ctx, msg.From.ID) {
		h.reply(ctx, msg.Chat.ID, "Требуется авторизация. Используйте /login <пароль>")
		return
	}

	answer, err := h.llm.ChatCompletion(ctx, question, "")
	if err != nil {
		h.logger.Error("llm error", slog.String("error", err.Error()))
		h.reply(ctx, msg.Chat.ID, "Ошибка LLM. Попробуйте позже.")
		return
	}
	h.reply(ctx, msg.Chat.ID, answer)
}

func (h *WebhookHandler) reply(ctx context.Context, chatID int64, text string) {
	if err := h.bot.SendMessage(ctx, chatID, text); err != nil {
		h.logger.Error("send message failed", slog.String("error", err.Error()))
	}
}
