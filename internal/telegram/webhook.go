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

const (
	defaultProcessingTimeout = 60 * time.Second
	defaultAcquireTimeout    = 200 * time.Millisecond
	defaultMaxWorkers        = 10
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
	// Необязательные настройки параллельной обработки.
	ProcessingTimeout time.Duration
	AcquireTimeout    time.Duration
	MaxWorkers        int
}

type WebhookHandler struct {
	auth          AuthService
	llm           llm.Client
	bot           BotClient
	logger        *slog.Logger
	adminPassword string
	webhookSecret string
	sem           chan struct{}
	processingTTL time.Duration
	acquireTTL    time.Duration
}

func NewWebhookHandler(deps WebhookDeps) *WebhookHandler {
	maxWorkers := deps.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	processingTTL := deps.ProcessingTimeout
	if processingTTL <= 0 {
		processingTTL = defaultProcessingTimeout
	}
	acquireTTL := deps.AcquireTimeout
	if acquireTTL <= 0 {
		acquireTTL = defaultAcquireTimeout
	}

	return &WebhookHandler{
		auth:          deps.Auth,
		llm:           deps.LLM,
		bot:           deps.Bot,
		logger:        deps.Logger,
		adminPassword: deps.AdminPassword,
		webhookSecret: deps.WebhookSecret,
		sem:           make(chan struct{}, maxWorkers),
		processingTTL: processingTTL,
		acquireTTL:    acquireTTL,
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

	text := strings.TrimSpace(upd.Message.Text)

	// Быстро отвечаем Telegram, основную обработку переносим в фон.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))

	h.processAsync(upd.Message, text)
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

	h.reply(ctx, msg.Chat.ID, "Думаю...")

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

func (h *WebhookHandler) processAsync(msg *Message, text string) {
	if !h.acquireSlot() {
		return
	}

	go func(msg *Message, text string) {
		defer h.releaseSlot()
		defer func() {
			if r := recover(); r != nil {
				h.logger.Error("webhook goroutine panic recovered", slog.Any("panic", r))
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), h.processingTTL)
		defer cancel()

		h.dispatch(ctx, msg, text)
	}(msg, text)
}

func (h *WebhookHandler) dispatch(ctx context.Context, msg *Message, text string) {
	if text == "" {
		h.reply(ctx, msg.Chat.ID, "Пустое сообщение. Используйте /start.")
		return
	}

	if strings.HasPrefix(text, "/") {
		h.handleCommand(ctx, msg, text)
		return
	}

	h.handleText(ctx, msg, text)
}

func (h *WebhookHandler) acquireSlot() bool {
	if h.sem == nil {
		return true
	}

	select {
	case h.sem <- struct{}{}:
		return true
	case <-time.After(h.acquireTTL):
		h.logger.Warn("webhook update dropped: workers are busy")
		return false
	}
}

func (h *WebhookHandler) releaseSlot() {
	if h.sem == nil {
		return
	}

	select {
	case <-h.sem:
	default:
	}
}
