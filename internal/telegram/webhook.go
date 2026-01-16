package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aiadvent/internal/auth"
	"aiadvent/internal/httpserver"
	"aiadvent/internal/llm"
	"aiadvent/internal/llmcontracts"
	"aiadvent/internal/retry"
	"log/slog"
)

const (
	defaultProcessingTimeout = 60 * time.Second
	defaultAcquireTimeout    = 200 * time.Millisecond
	defaultMaxWorkers        = 10
	// Максимальная длина сообщения Telegram Bot API
	maxMessageLength = 4096
	// Минимальная задержка между частями сообщения
	messagePartDelay = 100 * time.Millisecond
)

// BotCommand описывает команду бота с её описанием и требованием авторизации.
type BotCommand struct {
	Command     string
	Description string
	RequireAuth bool
	DayNumber   int // Номер дня (0 = без привязки к дню)
}

// botCommands список всех доступных команд бота.
var botCommands = []BotCommand{
	{Command: "/start", Description: "Показать список команд", RequireAuth: false, DayNumber: 0},
	{Command: "/login", Description: "Войти в систему (пароль следующим сообщением)", RequireAuth: false, DayNumber: 0},
	//{Command: "/logout", Description: "Выйти из системы", RequireAuth: true},
	//{Command: "/me", Description: "Показать информацию о текущем пользователе", RequireAuth: false},
	{Command: "/ask", Description: "Режим обычных вопросов к LLM", RequireAuth: true, DayNumber: 1},
	{Command: "/ask_json", Description: "Режим JSON-ответов с контрактом", RequireAuth: true, DayNumber: 2},
	{Command: "/create_plan", Description: "Режим создания плана действий", RequireAuth: true, DayNumber: 3},
	{Command: "/solve", Description: "Варианты решения задачи. 1 - прямой ответ, 2 - пошаговое решение, 3 - промт, 4 - группа экспертов.", RequireAuth: true, DayNumber: 4},
	{Command: "/model", Description: "Изменить модель LLM", RequireAuth: true, DayNumber: 3},
	{Command: "/end", Description: "Выйти из текущего режима", RequireAuth: false, DayNumber: 0},
}

func formatCommandList() []string {
	var messages []string

	// Разделяем команды на публичные и требующие авторизации
	var publicCommands []BotCommand
	var authCommands []BotCommand
	for _, cmd := range botCommands {
		if cmd.RequireAuth {
			authCommands = append(authCommands, cmd)
		} else {
			publicCommands = append(publicCommands, cmd)
		}
	}

	// Функция для получения отсортированных номеров дней
	getSortedDays := func(commands []BotCommand) []int {
		dayNumbers := make(map[int]bool)
		for _, cmd := range commands {
			if cmd.DayNumber > 0 {
				dayNumbers[cmd.DayNumber] = true
			}
		}
		var sortedDays []int
		for day := range dayNumbers {
			sortedDays = append(sortedDays, day)
		}
		// Простая сортировка вставками
		for i := 1; i < len(sortedDays); i++ {
			for j := i; j > 0 && sortedDays[j-1] > sortedDays[j]; j-- {
				sortedDays[j], sortedDays[j-1] = sortedDays[j-1], sortedDays[j]
			}
		}
		return sortedDays
	}

	// Функция для формирования сообщения с командами
	formatCommands := func(commands []BotCommand, dayNumber int) string {
		var b strings.Builder
		for _, cmd := range commands {
			if cmd.DayNumber == dayNumber {
				b.WriteString(fmt.Sprintf("%s — %s\n", cmd.Command, cmd.Description))
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}

	// 1. Сообщение с общими командами
	if len(publicCommands) > 0 {
		var b strings.Builder
		b.WriteString("📋 Доступные команды:\n\n👥 Общие:\n\n")

		// Сначала команды по дням
		sortedDays := getSortedDays(publicCommands)
		for _, day := range sortedDays {
			b.WriteString(fmt.Sprintf("📅 День %d\n\n", day))
			b.WriteString(formatCommands(publicCommands, day))
		}

		// Затем команды без дня
		noDayCommands := formatCommands(publicCommands, 0)
		if noDayCommands != "" {
			if len(sortedDays) > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(noDayCommands)
		}

		messages = append(messages, b.String())
	}

	// 2. Заголовок "Требуют авторизации"
	if len(authCommands) > 0 {
		messages = append(messages, "🔐 Требуют авторизации:")

		// 3. Каждый день - отдельное сообщение
		sortedDays := getSortedDays(authCommands)
		for _, day := range sortedDays {
			var b strings.Builder
			b.WriteString(fmt.Sprintf("📅 День %d\n\n", day))
			b.WriteString(formatCommands(authCommands, day))
			messages = append(messages, b.String())
		}

		// 4. Команды без дня - отдельное сообщение
		noDayCommands := formatCommands(authCommands, 0)
		if noDayCommands != "" {
			messages = append(messages, noDayCommands)
		}
	}

	// 5. Финальное сообщение
	messages = append(messages, "💡 Для начала работы используйте /login")

	return messages
}

type pendingCommand string

const (
	pendingCommandLogin pendingCommand = "login"
)

type dialogMode string

const (
	dialogModeCreatePlan dialogMode = "create_plan"
)

type userState struct {
	pending         pendingCommand
	askMode         bool
	askJSONMode     bool
	askJSONContract string
	dialogMode      dialogMode
	dialogID        string
	// Выбранная модель для режимов вопросов (пустая строка = модель по умолчанию)
	selectedModel string
	// Последний вопрос пользователя для повторной отправки при смене модели
	lastQuestion string
	// Последнее сообщение в режиме диалога для повторной отправки
	lastDialogMessage string
	// Флаг для отображения названия модели при следующем ответе
	showModelName bool
	// Режим ожидания задачи для /solve (после выбора модели)
	solveMode bool
	// Выбранная модель для /solve
	solveModel string
	// Задача для /solve (для retry)
	solveTask string
	// Message ID для каждого этапа solve (для редактирования при retry)
	solveStepMessages map[int]int64
	// Модель использованная для каждого этапа (для retry с другой моделью)
	solveStepModels map[int]string
}

type AuthService interface {
	Login(ctx context.Context, userID int64, password string) (auth.Session, error)
	Logout(ctx context.Context, userID int64)
	IsAuthorized(ctx context.Context, userID int64) bool
}

type DialogService interface {
	Chat(ctx context.Context, dialogID string, model string, systemPrompt string, userMessage string) (string, error)
	ClearDialog(ctx context.Context, dialogID string) error
	CreatePlan(ctx context.Context, dialogID string, model string, userMessage string) (string, error)
	ReplayCreatePlan(ctx context.Context, dialogID string, model string) (string, error)
	GetSolutionVariantsParallel(ctx context.Context, model string, taskPrompt string, attemptNotifiers map[int]*llm.AttemptNotifier, callback llm.SolutionStepCallback)
	ExecuteSolutionStep(ctx context.Context, step int, model string, taskPrompt string) llm.SolutionStepResult
}

type WebhookDeps struct {
	Auth          AuthService
	LLM           llm.Client
	DialogService DialogService
	Bot           BotClient
	Logger        *slog.Logger
	AdminPassword string
	SessionTTL    time.Duration
	WebhookSecret string
	DefaultModel  string
	// Необязательные настройки параллельной обработки.
	ProcessingTimeout time.Duration
	AcquireTimeout    time.Duration
	MaxWorkers        int
}

type WebhookHandler struct {
	auth          AuthService
	llm           llm.Client
	dialogService DialogService
	bot           BotClient
	logger        *slog.Logger
	adminPassword string
	webhookSecret string
	defaultModel  string
	sem           chan struct{}
	processingTTL time.Duration
	acquireTTL    time.Duration
	stateMu       sync.Mutex
	state         map[int64]userState
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
		dialogService: deps.DialogService,
		bot:           deps.Bot,
		logger:        deps.Logger,
		adminPassword: deps.AdminPassword,
		webhookSecret: deps.WebhookSecret,
		defaultModel:  deps.DefaultModel,
		sem:           make(chan struct{}, maxWorkers),
		processingTTL: processingTTL,
		acquireTTL:    acquireTTL,
		state:         make(map[int64]userState),
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

	// Быстро отвечаем Telegram, основную обработку переносим в фон.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))

	// Обработка callback query от inline кнопок
	if upd.CallbackQuery != nil && upd.CallbackQuery.From != nil {
		h.processCallbackAsync(upd.CallbackQuery)
		return
	}

	// Обработка обычных сообщений
	if upd.Message == nil || upd.Message.From == nil {
		return
	}

	text := strings.TrimSpace(upd.Message.Text)
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
		messages := formatCommandList()
		for _, message := range messages {
			h.reply(ctx, msg.Chat.ID, message)
		}
	case "/login":
		if arg == "" {
			h.setPending(msg.From.ID, pendingCommandLogin)
			h.reply(ctx, msg.Chat.ID, "Введите пароль следующим сообщением")
			return
		}
		h.handleLogin(ctx, msg, arg)
	case "/logout":
		h.auth.Logout(ctx, msg.From.ID)
		h.setAskMode(msg.From.ID, false)
		h.setAskJSONMode(msg.From.ID, false, "")
		h.clearPending(msg.From.ID)
		h.reply(ctx, msg.Chat.ID, "Вы вышли")
	case "/me":
		authStatus := "не авторизован"
		if h.auth.IsAuthorized(ctx, msg.From.ID) {
			authStatus = "авторизован"
		}
		h.reply(ctx, msg.Chat.ID, fmt.Sprintf("Ваш id: %d, статус: %s", msg.From.ID, authStatus))
	case "/ask":
		if !h.auth.IsAuthorized(ctx, msg.From.ID) {
			h.reply(ctx, msg.Chat.ID, "Требуется авторизация. Отправьте /login, затем пароль отдельным сообщением.")
			return
		}
		h.setAskMode(msg.From.ID, true)
		h.setAskJSONMode(msg.From.ID, false, "")
		h.reply(ctx, msg.Chat.ID, "Режим вопросов включен. Отправляйте сообщения — я буду отвечать. Команда /end выключит режим.")
		if arg != "" {
			h.handleAsk(ctx, msg, arg)
		}
	case "/ask_json":
		if !h.auth.IsAuthorized(ctx, msg.From.ID) {
			h.reply(ctx, msg.Chat.ID, "Требуется авторизация. Отправьте /login, затем пароль отдельным сообщением.")
			return
		}
		contractName := llmcontracts.DefaultContract()
		if arg != "" {
			contractName = arg
		}
		if !llmcontracts.HasContract(contractName) {
			h.reply(ctx, msg.Chat.ID, fmt.Sprintf("Неизвестный контракт \"%s\". Доступные: %s", contractName, strings.Join(llmcontracts.AvailableContracts(), ", ")))
			return
		}
		h.setAskMode(msg.From.ID, false)
		h.setAskJSONMode(msg.From.ID, true, contractName)
		h.reply(ctx, msg.Chat.ID, fmt.Sprintf("Режим JSON-вопросов включен (контракт: %s). Отправляйте сообщения. /end выключит режим.", contractName))
	case "/create_plan":
		if !h.auth.IsAuthorized(ctx, msg.From.ID) {
			h.reply(ctx, msg.Chat.ID, "Требуется авторизация. Отправьте /login, затем пароль отдельным сообщением.")
			return
		}
		h.handleCreatePlanCommand(ctx, msg)
	case "/solve":
		if !h.auth.IsAuthorized(ctx, msg.From.ID) {
			h.reply(ctx, msg.Chat.ID, "Требуется авторизация. Отправьте /login, затем пароль отдельным сообщением.")
			return
		}
		h.handleSolveCommand(ctx, msg, arg)
	case "/model":
		if !h.auth.IsAuthorized(ctx, msg.From.ID) {
			h.reply(ctx, msg.Chat.ID, "Требуется авторизация. Отправьте /login, затем пароль отдельным сообщением.")
			return
		}
		h.handleModelCommand(ctx, msg, arg)
	case "/end":
		ask := h.isAskMode(msg.From.ID)
		askJSON, _ := h.askJSONState(msg.From.ID)
		dialogActive := h.isDialogMode(msg.From.ID)
		solveActive := h.isSolveMode(msg.From.ID)
		if ask || askJSON || dialogActive || solveActive {
			h.setAskMode(msg.From.ID, false)
			h.setAskJSONMode(msg.From.ID, false, "")
			h.setSolveMode(msg.From.ID, false)
			if dialogActive {
				h.handleEndDialog(ctx, msg)
			} else {
				h.reply(ctx, msg.Chat.ID, "Режим вопросов выключен.")
			}
		} else {
			h.reply(ctx, msg.Chat.ID, "Вы не в режиме вопросов. Отправьте /ask, /ask_json, /create_plan или /solve, чтобы начать.")
		}
	default:
		h.reply(ctx, msg.Chat.ID, "❌ Неизвестная команда.")
		messages := formatCommandList()
		for _, message := range messages {
			h.reply(ctx, msg.Chat.ID, message)
		}
	}
}

func (h *WebhookHandler) handleText(ctx context.Context, msg *Message, text string) {
	if !h.auth.IsAuthorized(ctx, msg.From.ID) {
		h.reply(ctx, msg.Chat.ID, "Нужно войти: отправьте /login и затем пароль отдельным сообщением")
		return
	}

	// Проверяем режим solve
	if h.isSolveMode(msg.From.ID) {
		h.handleSolveTask(ctx, msg, text)
		return
	}

	// Проверяем режим диалога
	if dialogMode, dialogID := h.getDialogState(msg.From.ID); dialogMode != "" {
		h.handleDialogMessage(ctx, msg, text, dialogMode, dialogID)
		return
	}

	if askJSON, contract := h.askJSONState(msg.From.ID); askJSON {
		h.handleAskJSON(ctx, msg, text, contract)
		return
	}
	if h.isAskMode(msg.From.ID) {
		h.handleAsk(ctx, msg, text)
		return
	}

	h.reply(ctx, msg.Chat.ID, "Чтобы задать вопрос, включите режим /ask, /ask_json, /create_plan или /solve. Команда /end выключает режим.")
}

func (h *WebhookHandler) handleLogin(ctx context.Context, msg *Message, password string) {
	if password == "" {
		h.setPending(msg.From.ID, pendingCommandLogin)
		h.reply(ctx, msg.Chat.ID, "Введите пароль следующим сообщением")
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
		h.reply(ctx, msg.Chat.ID, "Нужно задать вопрос. Отправьте текст следующим сообщением")
		return
	}
	if !h.auth.IsAuthorized(ctx, msg.From.ID) {
		h.reply(ctx, msg.Chat.ID, "Требуется авторизация. Отправьте /login, затем пароль отдельным сообщением.")
		return
	}

	// Сохраняем последний вопрос для возможной переотправки при смене модели
	h.setLastQuestion(msg.From.ID, question)

	// Получаем выбранную модель пользователя
	selectedModel := h.getSelectedModel(msg.From.ID)

	thinkingMessageID, cancelAnimation, err := h.sendThinkingAnimation(ctx, msg.Chat.ID)
	if err != nil {
		h.logger.Error("send thinking animation failed", slog.String("error", err.Error()))
		h.reply(ctx, msg.Chat.ID, "Ошибка отправки сообщения.")
		return
	}
	defer cancelAnimation()

	answer, err := h.llm.ChatCompletion(ctx, question, selectedModel)
	if err != nil {
		cancelAnimation()
		h.logger.Error("llm error", slog.String("error", err.Error()))
		if h.handleRetryableLLMError(ctx, msg, thinkingMessageID, err, "retry:ask") {
			return
		}
		h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, "Ошибка LLM. Попробуйте позже.")
		return
	}

	// Добавляем название модели к ответу, если это первый ответ после смены модели
	if h.getAndClearShowModelName(msg.From.ID) && selectedModel != "" {
		modelName := llm.GetModelName(selectedModel)
		answer = fmt.Sprintf("*[%s]*\n\n%s", modelName, answer)
	}

	cancelAnimation()
	h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, answer)
}

func (h *WebhookHandler) handleAskJSON(ctx context.Context, msg *Message, question string, contractName string) {
	if question == "" {
		h.reply(ctx, msg.Chat.ID, "Нужно задать вопрос. Отправьте текст следующим сообщением")
		return
	}

	// Сохраняем последний вопрос для возможной переотправки при смене модели
	h.setLastQuestion(msg.From.ID, question)

	// Получаем выбранную модель пользователя
	selectedModel := h.getSelectedModel(msg.From.ID)

	thinkingMessageID, cancelAnimation, err := h.sendThinkingAnimation(ctx, msg.Chat.ID)
	if err != nil {
		h.logger.Error("send thinking animation failed", slog.String("error", err.Error()))
		h.reply(ctx, msg.Chat.ID, "Ошибка отправки сообщения.")
		return
	}
	defer cancelAnimation()

	systemPrompt, err := llmcontracts.SystemPrompt(contractName)
	if err != nil {
		cancelAnimation()
		h.logger.Error("system prompt error", slog.String("error", err.Error()))
		h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, "Не удалось получить контракт LLM.")
		return
	}

	answer, err := h.llm.ChatCompletionWithSystem(ctx, systemPrompt, question, selectedModel)
	if err != nil {
		cancelAnimation()
		h.logger.Error("llm error", slog.String("error", err.Error()))
		if h.handleRetryableLLMError(ctx, msg, thinkingMessageID, err, "retry:ask_json") {
			return
		}
		h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, "Ошибка LLM. Попробуйте позже.")
		return
	}

	// Добавляем название модели к ответу, если это первый ответ после смены модели
	if h.getAndClearShowModelName(msg.From.ID) && selectedModel != "" {
		modelName := llm.GetModelName(selectedModel)
		answer = fmt.Sprintf("*[%s]*\n\n%s", modelName, answer)
	}

	cancelAnimation()
	h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, answer)

	validation, err := llmcontracts.Validate(contractName, answer)
	if err != nil {
		h.logger.Error("validate error", slog.String("error", err.Error()))
		h.reply(ctx, msg.Chat.ID, fmt.Sprintf("Ошибка валидации: %v", err))
		return
	}

	if validation.IsValid {
		h.reply(ctx, msg.Chat.ID, fmt.Sprintf("✅ Ответ валиден для контракта %s", contractName))
		return
	}

	errors := validation.Errors
	if len(errors) == 0 {
		errors = []string{"неизвестная ошибка валидации"}
	}
	maxErrs := 10
	if len(errors) > maxErrs {
		errors = append(errors[:maxErrs], "…")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("❌ Ответ НЕ валиден для контракта %s:\n", contractName))
	for _, e := range errors {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	h.reply(ctx, msg.Chat.ID, strings.TrimRight(b.String(), "\n"))
}

// splitMessage разбивает длинное сообщение на части, не превышающие maxMessageLength
func splitMessage(text string, maxLength int) []string {
	if len(text) <= maxLength {
		return []string{text}
	}

	var parts []string
	remaining := text

	for len(remaining) > maxLength {
		// Ищем последний пробел перед лимитом в широком диапазоне
		cutIndex := maxLength
		for i := maxLength - 1; i >= 0; i-- {
			if remaining[i] == ' ' || remaining[i] == '\n' {
				cutIndex = i
				break
			}
		}

		// Если пробел найден, используем его
		part := remaining[:cutIndex]
		parts = append(parts, strings.TrimSpace(part))

		// Оставшаяся часть без начальных пробелов
		remaining = strings.TrimLeft(remaining[cutIndex:], " \n")
	}

	if remaining != "" {
		parts = append(parts, remaining)
	}

	return parts
}

// sendMessageWithChunks отправляет сообщение, разбивая его на части если необходимо
func (h *WebhookHandler) sendMessageWithChunks(ctx context.Context, chatID int64, text string) {
	parts := splitMessage(text, maxMessageLength)

	for i, part := range parts {
		if i > 0 {
			// Добавляем небольшую задержку между частями
			time.Sleep(messagePartDelay)
		}

		if _, err := h.bot.SendMessage(ctx, chatID, part); err != nil {
			h.logger.Error("send message failed", slog.String("error", err.Error()))
			return
		}
	}
}

func (h *WebhookHandler) reply(ctx context.Context, chatID int64, text string) {
	h.sendMessageWithChunks(ctx, chatID, text)
}

func (h *WebhookHandler) handleRetryableLLMError(ctx context.Context, msg *Message, messageID int64, err error, retryAction string) bool {
	text, ok := retryErrorMessage(err)
	if !ok {
		return false
	}
	keyboard := retryKeyboard(retryAction)
	if editErr := h.bot.EditMessageKeyboard(ctx, msg.Chat.ID, messageID, text, keyboard); editErr != nil {
		h.reply(ctx, msg.Chat.ID, text)
	}
	return true
}

func retryErrorMessage(err error) (string, bool) {
	var exhausted *retry.ExhaustedError
	if !errors.As(err, &exhausted) {
		return "", false
	}
	reason := humanRetryReason(exhausted.Cause)
	if reason == "" {
		reason = "Временная ошибка LLM."
	}
	return fmt.Sprintf("%s Я попробовал %d раз, но ответ не получен. Нажмите «Повторить запрос», чтобы попробовать снова.", reason, exhausted.Attempts), true
}

func humanRetryReason(err error) string {
	var statusErr *retry.HTTPStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusTooManyRequests:
			return "Сервис временно ограничил частоту запросов (429)."
		case http.StatusRequestTimeout:
			return "Истекло время ожидания ответа (408)."
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return "Провайдер LLM временно недоступен (5xx)."
		default:
			return fmt.Sprintf("Временная ошибка сервиса (HTTP %d).", statusErr.StatusCode)
		}
	}
	if isTransientNetError(err) {
		return "Временная ошибка сети при обращении к LLM."
	}
	return ""
}

func isTransientNetError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "connection reset")
}

func retryKeyboard(action string) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🔁 Повторить запрос", CallbackData: action},
			},
		},
	}
}

// sendThinkingAnimation отправляет анимированное сообщение "Думаю" с бегающими точками
func (h *WebhookHandler) sendThinkingAnimation(ctx context.Context, chatID int64) (int64, context.CancelFunc, error) {
	messageID, err := h.bot.SendMessage(ctx, chatID, "Думаю")
	if err != nil {
		return 0, nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	go func() {
		states := []string{"Думаю", "Думаю.", "Думаю..", "Думаю..."}
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				i = (i + 1) % len(states)
				if err := h.bot.EditMessage(ctx, chatID, messageID, states[i]); err != nil {
					h.logger.Error("edit thinking message failed", slog.String("error", err.Error()))
					return
				}
			}
		}
	}()

	return messageID, cancel, nil
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

func (h *WebhookHandler) processCallbackAsync(cb *CallbackQuery) {
	if !h.acquireSlot() {
		return
	}

	go func(cb *CallbackQuery) {
		defer h.releaseSlot()
		defer func() {
			if r := recover(); r != nil {
				h.logger.Error("callback goroutine panic recovered", slog.Any("panic", r))
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), h.processingTTL)
		defer cancel()

		h.handleCallbackQuery(ctx, cb)
	}(cb)
}

func (h *WebhookHandler) handleCallbackQuery(ctx context.Context, cb *CallbackQuery) {
	// Проверяем авторизацию
	if !h.auth.IsAuthorized(ctx, cb.From.ID) {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Требуется авторизация")
		return
	}

	// Парсим callback data (формат: action:data)
	parts := strings.SplitN(cb.Data, ":", 2)
	if len(parts) < 2 {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Неверный формат данных")
		return
	}

	action := parts[0]
	data := parts[1]

	switch action {
	case "model":
		h.handleModelCallback(ctx, cb, data)
	case "solve_model":
		h.handleSolveModelCallback(ctx, cb, data)
	case "solve_retry":
		h.handleSolveRetryCallback(ctx, cb, data)
	case "retry":
		h.handleRetryCallback(ctx, cb, data)
	default:
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Неизвестное действие")
	}
}

func (h *WebhookHandler) dispatch(ctx context.Context, msg *Message, text string) {
	if text == "" {
		h.reply(ctx, msg.Chat.ID, "Пустое сообщение. Используйте /start.")
		return
	}

	if strings.HasPrefix(text, "/") {
		h.clearPending(msg.From.ID)
		h.handleCommand(ctx, msg, text)
		return
	}

	if cmd, ok := h.popPending(msg.From.ID); ok {
		h.handlePending(ctx, msg, cmd, text)
		return
	}

	h.handleText(ctx, msg, text)
}

func (h *WebhookHandler) handlePending(ctx context.Context, msg *Message, cmd pendingCommand, text string) {
	switch cmd {
	case pendingCommandLogin:
		h.handleLogin(ctx, msg, text)
	default:
		h.reply(ctx, msg.Chat.ID, "Неизвестное состояние. Попробуйте снова отправить команду.")
	}
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

func (h *WebhookHandler) setPending(userID int64, cmd pendingCommand) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.pending = cmd
	h.state[userID] = state
}

func (h *WebhookHandler) popPending(userID int64) (pendingCommand, bool) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok || state.pending == "" {
		return "", false
	}
	cmd := state.pending
	state.pending = ""
	h.state[userID] = state
	return cmd, true
}

func (h *WebhookHandler) clearPending(userID int64) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.pending = ""
	h.state[userID] = state
}

func (h *WebhookHandler) setAskMode(userID int64, enabled bool) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.askMode = enabled
	h.state[userID] = state
}

func (h *WebhookHandler) isAskMode(userID int64) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	return ok && state.askMode
}

func (h *WebhookHandler) setAskJSONMode(userID int64, enabled bool, contract string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.askJSONMode = enabled
	if enabled {
		state.askJSONContract = contract
	} else {
		state.askJSONContract = ""
	}
	h.state[userID] = state
}

func (h *WebhookHandler) askJSONState(userID int64) (bool, string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok || !state.askJSONMode {
		return false, ""
	}
	return true, state.askJSONContract
}

func (h *WebhookHandler) setDialogMode(userID int64, mode dialogMode, dialogID string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.dialogMode = mode
	state.dialogID = dialogID
	state.lastDialogMessage = ""
	h.state[userID] = state
}

func (h *WebhookHandler) getDialogState(userID int64) (dialogMode, string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok || state.dialogMode == "" {
		return "", ""
	}
	return state.dialogMode, state.dialogID
}

func (h *WebhookHandler) isDialogMode(userID int64) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	return ok && state.dialogMode != ""
}

func (h *WebhookHandler) clearDialogMode(userID int64) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.dialogMode = ""
	state.dialogID = ""
	state.lastDialogMessage = ""
	h.state[userID] = state
}

func (h *WebhookHandler) setSelectedModel(userID int64, model string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.selectedModel = model
	h.state[userID] = state
}

func (h *WebhookHandler) getSelectedModel(userID int64) string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok {
		return ""
	}
	return state.selectedModel
}

func (h *WebhookHandler) setLastQuestion(userID int64, question string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.lastQuestion = question
	h.state[userID] = state
}

func (h *WebhookHandler) getLastQuestion(userID int64) string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok {
		return ""
	}
	return state.lastQuestion
}

func (h *WebhookHandler) setLastDialogMessage(userID int64, message string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.lastDialogMessage = message
	h.state[userID] = state
}

func (h *WebhookHandler) getLastDialogMessage(userID int64) string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok {
		return ""
	}
	return state.lastDialogMessage
}

func (h *WebhookHandler) setShowModelName(userID int64, show bool) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.showModelName = show
	h.state[userID] = state
}

func (h *WebhookHandler) getAndClearShowModelName(userID int64) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok || !state.showModelName {
		return false
	}
	state.showModelName = false
	h.state[userID] = state
	return true
}

func (h *WebhookHandler) generateDialogID(userID int64) string {
	return fmt.Sprintf("%d:%d", userID, time.Now().UnixNano())
}

func (h *WebhookHandler) handleCreatePlanCommand(ctx context.Context, msg *Message) {
	// Если у пользователя уже был активный диалог создания плана - удаляем его
	if mode, dialogID := h.getDialogState(msg.From.ID); mode == dialogModeCreatePlan && dialogID != "" {
		if h.dialogService != nil {
			if err := h.dialogService.ClearDialog(ctx, dialogID); err != nil {
				h.logger.Error("failed to clear old dialog", slog.String("error", err.Error()))
			}
		}
	}

	// Генерируем новый dialogID
	dialogID := h.generateDialogID(msg.From.ID)

	// Устанавливаем режим диалога
	h.setDialogMode(msg.From.ID, dialogModeCreatePlan, dialogID)

	h.reply(ctx, msg.Chat.ID, "Режим создания плана действий включен.\n\nОпишите, что вы хотите сделать. Я буду задавать уточняющие вопросы, чтобы собрать требования и сформировать план.\n\nЧтобы прервать диалог, отправьте /end")
}

func (h *WebhookHandler) handleEndDialog(ctx context.Context, msg *Message) {
	mode, dialogID := h.getDialogState(msg.From.ID)
	if mode == "" {
		h.reply(ctx, msg.Chat.ID, "Вы не в режиме диалога.")
		return
	}

	// Удаляем историю диалога
	if h.dialogService != nil && dialogID != "" {
		if err := h.dialogService.ClearDialog(ctx, dialogID); err != nil {
			h.logger.Error("failed to clear dialog", slog.String("error", err.Error()))
		}
	}

	// Очищаем состояние
	h.clearDialogMode(msg.From.ID)

	h.reply(ctx, msg.Chat.ID, "Режим диалога завершён. История удалена.")
}

func (h *WebhookHandler) handleModelCommand(ctx context.Context, msg *Message, arg string) {
	currentModel := h.getSelectedModel(msg.From.ID)
	displayModel := currentModel
	if displayModel == "" {
		displayModel = h.defaultModel
	}
	currentModelName := "по умолчанию"
	if displayModel != "" {
		currentModelName = llm.GetModelName(displayModel)
	}

	// Формируем текст сообщения
	text := fmt.Sprintf("🤖 *Текущая модель:* %s\n\n*Выберите модель:*", currentModelName)

	// Создаём inline клавиатуру с кнопками моделей
	keyboard := h.buildModelKeyboard(displayModel)

	h.bot.SendMessageWithKeyboard(ctx, msg.Chat.ID, text, keyboard)
}

// buildModelKeyboard создаёт inline клавиатуру с кнопками выбора модели.
func (h *WebhookHandler) buildModelKeyboard(currentModel string) *InlineKeyboardMarkup {
	var rows [][]InlineKeyboardButton

	for i, m := range llm.AvailableModels {
		buttonText := m.Name
		if m.ID == currentModel {
			buttonText = "✓ " + buttonText
		}

		rows = append(rows, []InlineKeyboardButton{
			{
				Text:         buttonText,
				CallbackData: fmt.Sprintf("model:%d", i),
			},
		})
	}

	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// handleModelCallback обрабатывает нажатие на кнопку выбора модели.
func (h *WebhookHandler) handleModelCallback(ctx context.Context, cb *CallbackQuery, data string) {
	// Парсим индекс модели
	var modelIndex int
	if _, err := fmt.Sscanf(data, "%d", &modelIndex); err != nil || modelIndex < 0 || modelIndex >= len(llm.AvailableModels) {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "❌ Неверная модель")
		return
	}

	selectedModel := llm.AvailableModels[modelIndex]
	currentModel := h.getSelectedModel(cb.From.ID)

	// Проверяем, не выбрана ли уже эта модель
	if currentModel == selectedModel.ID {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, fmt.Sprintf("Модель %s уже выбрана", selectedModel.Name))
		return
	}

	// Устанавливаем новую модель
	h.setSelectedModel(cb.From.ID, selectedModel.ID)
	h.setShowModelName(cb.From.ID, true)

	// Обновляем сообщение с новой клавиатурой
	newText := fmt.Sprintf("🤖 *Текущая модель:* %s\n\n*Выберите модель:*", selectedModel.Name)
	newKeyboard := h.buildModelKeyboard(selectedModel.ID)

	if cb.Message != nil {
		h.bot.EditMessageKeyboard(ctx, cb.Message.Chat.ID, cb.Message.MessageID, newText, newKeyboard)
	}

	// Проверяем, находимся ли мы в режиме вопросов или диалога
	inAskMode := h.isAskMode(cb.From.ID)
	askJSON, contract := h.askJSONState(cb.From.ID)
	dialogMode, dialogID := h.getDialogState(cb.From.ID)

	// Режим диалога create_plan - переотправляем всю историю
	if dialogMode == dialogModeCreatePlan && dialogID != "" {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, fmt.Sprintf("✅ %s. Переотправляю диалог...", selectedModel.Name))
		h.handleDialogModelChange(ctx, cb, selectedModel, dialogID)
		return
	}

	// Режим вопросов - переотправляем последний вопрос
	if inAskMode || askJSON {
		lastQuestion := h.getLastQuestion(cb.From.ID)
		if lastQuestion != "" {
			h.bot.AnswerCallbackQuery(ctx, cb.ID, fmt.Sprintf("✅ %s. Переотправляю запрос...", selectedModel.Name))
			// Создаём виртуальное сообщение для обработки
			msg := &Message{
				From: cb.From,
				Chat: cb.Message.Chat,
			}
			if askJSON {
				h.handleAskJSON(ctx, msg, lastQuestion, contract)
			} else {
				h.handleAsk(ctx, msg, lastQuestion)
			}
			return
		}
	}

	h.bot.AnswerCallbackQuery(ctx, cb.ID, fmt.Sprintf("✅ Модель: %s", selectedModel.Name))
}

func (h *WebhookHandler) handleRetryCallback(ctx context.Context, cb *CallbackQuery, data string) {
	if cb.Message == nil {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Нет сообщения для повтора")
		return
	}
	lastQuestion := h.getLastQuestion(cb.From.ID)
	switch data {
	case "ask":
		if lastQuestion == "" {
			h.bot.AnswerCallbackQuery(ctx, cb.ID, "Нет запроса для повтора")
			return
		}
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Повторяю запрос...")
		msg := &Message{From: cb.From, Chat: cb.Message.Chat}
		h.handleAsk(ctx, msg, lastQuestion)
	case "ask_json":
		askJSON, contract := h.askJSONState(cb.From.ID)
		if !askJSON || contract == "" {
			h.bot.AnswerCallbackQuery(ctx, cb.ID, "Режим JSON выключен")
			return
		}
		if lastQuestion == "" {
			h.bot.AnswerCallbackQuery(ctx, cb.ID, "Нет запроса для повтора")
			return
		}
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Повторяю запрос...")
		msg := &Message{From: cb.From, Chat: cb.Message.Chat}
		h.handleAskJSON(ctx, msg, lastQuestion, contract)
	case "dialog":
		mode, dialogID := h.getDialogState(cb.From.ID)
		lastDialogMessage := h.getLastDialogMessage(cb.From.ID)
		if mode == "" || dialogID == "" || lastDialogMessage == "" {
			h.bot.AnswerCallbackQuery(ctx, cb.ID, "Нет активного диалога для повтора")
			return
		}
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Повторяю запрос...")
		msg := &Message{From: cb.From, Chat: cb.Message.Chat}
		h.handleDialogMessage(ctx, msg, lastDialogMessage, mode, dialogID)
	default:
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "Неизвестное действие")
	}
}

// handleDialogModelChange обрабатывает смену модели в режиме диалога.
func (h *WebhookHandler) handleDialogModelChange(ctx context.Context, cb *CallbackQuery, model llm.ModelInfo, dialogID string) {
	if h.dialogService == nil {
		h.reply(ctx, cb.Message.Chat.ID, "Сервис диалогов недоступен.")
		return
	}

	thinkingMessageID, cancelAnimation, err := h.sendThinkingAnimation(ctx, cb.Message.Chat.ID)
	if err != nil {
		h.logger.Error("send thinking animation failed", slog.String("error", err.Error()))
		h.reply(ctx, cb.Message.Chat.ID, "Ошибка отправки сообщения.")
		return
	}
	defer cancelAnimation()

	// Переотправляем диалог с новой моделью
	answer, err := h.dialogService.ReplayCreatePlan(ctx, dialogID, model.ID)
	if err != nil {
		cancelAnimation()
		h.logger.Error("replay dialog error", slog.String("error", err.Error()))
		h.bot.EditMessage(ctx, cb.Message.Chat.ID, thinkingMessageID, "Ошибка LLM. Попробуйте позже или завершите режим командой /end")
		return
	}

	// Добавляем название модели к ответу
	answer = fmt.Sprintf("*[%s]*\n\n%s", model.Name, answer)

	cancelAnimation()
	h.bot.EditMessage(ctx, cb.Message.Chat.ID, thinkingMessageID, answer)
}

func (h *WebhookHandler) handleDialogMessage(ctx context.Context, msg *Message, text string, mode dialogMode, dialogID string) {
	if h.dialogService == nil {
		h.reply(ctx, msg.Chat.ID, "Сервис диалогов недоступен.")
		return
	}

	h.setLastDialogMessage(msg.From.ID, text)

	// Получаем выбранную модель пользователя
	selectedModel := h.getSelectedModel(msg.From.ID)

	thinkingMessageID, cancelAnimation, err := h.sendThinkingAnimation(ctx, msg.Chat.ID)
	if err != nil {
		h.logger.Error("send thinking animation failed", slog.String("error", err.Error()))
		h.reply(ctx, msg.Chat.ID, "Ошибка отправки сообщения.")
		return
	}
	defer cancelAnimation()

	var answer string
	switch mode {
	case dialogModeCreatePlan:
		answer, err = h.dialogService.CreatePlan(ctx, dialogID, selectedModel, text)
	default:
		cancelAnimation()
		h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, "Неизвестный режим диалога.")
		return
	}

	if err != nil {
		cancelAnimation()
		h.logger.Error("dialog llm error", slog.String("error", err.Error()), slog.String("mode", string(mode)))
		if h.handleRetryableLLMError(ctx, msg, thinkingMessageID, err, "retry:dialog") {
			return
		}
		h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, "Ошибка LLM. Попробуйте позже или завершите режим командой /end")
		return
	}

	// Добавляем название модели к ответу, если это первый ответ после смены модели
	if h.getAndClearShowModelName(msg.From.ID) && selectedModel != "" {
		modelName := llm.GetModelName(selectedModel)
		answer = fmt.Sprintf("*[%s]*\n\n%s", modelName, answer)
	}

	cancelAnimation()
	h.bot.EditMessage(ctx, msg.Chat.ID, thinkingMessageID, answer)
}

// handleSolveCommand обрабатывает команду /solve - варианты решения задачи.
func (h *WebhookHandler) handleSolveCommand(ctx context.Context, msg *Message, arg string) {
	// Показываем выбор модели
	h.showSolveModelSelection(ctx, msg)
}

// showSolveModelSelection показывает выбор модели для /solve.
func (h *WebhookHandler) showSolveModelSelection(ctx context.Context, msg *Message) {
	currentModel := h.getSelectedModel(msg.From.ID)
	displayModel := currentModel
	if displayModel == "" {
		displayModel = h.defaultModel
	}
	currentModelName := "по умолчанию"
	if displayModel != "" {
		currentModelName = llm.GetModelName(displayModel)
	}

	text := fmt.Sprintf("📝 *Варианты решения задачи*\n\n🤖 Выберите модель:\n\nТекущая: %s\n\n_После выбора модели введите текст задачи._", currentModelName)
	keyboard := h.buildSolveModelKeyboard(displayModel)

	h.bot.SendMessageWithKeyboard(ctx, msg.Chat.ID, text, keyboard)
}

// buildSolveModelKeyboard создаёт inline клавиатуру для выбора модели в /solve.
func (h *WebhookHandler) buildSolveModelKeyboard(currentModel string) *InlineKeyboardMarkup {
	var rows [][]InlineKeyboardButton

	for i, m := range llm.AvailableModels {
		buttonText := m.Name
		if m.ID == currentModel {
			buttonText = "✓ " + buttonText
		}

		rows = append(rows, []InlineKeyboardButton{
			{
				Text:         buttonText,
				CallbackData: fmt.Sprintf("solve_model:%d", i),
			},
		})
	}

	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// handleSolveModelCallback обрабатывает выбор модели для /solve.
func (h *WebhookHandler) handleSolveModelCallback(ctx context.Context, cb *CallbackQuery, data string) {
	// Парсим индекс модели
	var modelIndex int
	if _, err := fmt.Sscanf(data, "%d", &modelIndex); err != nil || modelIndex < 0 || modelIndex >= len(llm.AvailableModels) {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "❌ Неверная модель")
		return
	}

	selectedModel := llm.AvailableModels[modelIndex]

	// Сохраняем выбранную модель и включаем режим ожидания задачи
	h.setSolveModel(cb.From.ID, selectedModel.ID)
	h.setSolveMode(cb.From.ID, true)

	h.bot.AnswerCallbackQuery(ctx, cb.ID, fmt.Sprintf("✅ Модель: %s", selectedModel.Name))

	// Обновляем сообщение с выбранной моделью
	if cb.Message != nil {
		h.bot.EditMessage(ctx, cb.Message.Chat.ID, cb.Message.MessageID, fmt.Sprintf("📝 *Варианты решения задачи*\n\n🤖 Модель: *%s*\n\n_Будет сделано 4 запроса с разными промтами._", selectedModel.Name))
	}

	// Отправляем приглашение ввести задачу
	h.reply(ctx, cb.Message.Chat.ID, "Опишите задачу, для которой нужно получить варианты решения:\n\n_Команда /end отменит режим._")
}

// handleSolveTask обрабатывает текст задачи в режиме solve.
func (h *WebhookHandler) handleSolveTask(ctx context.Context, msg *Message, text string) {
	// Получаем выбранную модель
	model := h.getSolveModel(msg.From.ID)

	// Выключаем режим solve (но не очищаем данные - они нужны для retry)
	h.setSolveMode(msg.From.ID, false)

	// Выполняем запрос
	h.executeSolveTask(ctx, msg.Chat.ID, msg.From.ID, model, text)
}

// executeSolveTask выполняет запрос вариантов решения задачи параллельно.
func (h *WebhookHandler) executeSolveTask(ctx context.Context, chatID int64, userID int64, model string, task string) {
	if h.dialogService == nil {
		h.reply(ctx, chatID, "Сервис диалогов недоступен.")
		return
	}

	// Сохраняем задачу для retry
	h.setSolveTaskData(userID, task, model)

	// Заголовок
	header := fmt.Sprintf("📋 *Варианты решения задачи*\n\n📝 *Задача:*\n%s\n\n_Запросы выполняются параллельно..._", task)
	h.reply(ctx, chatID, header)

	// Создаём контекст с большим таймаутом (15 минут для 4 этапов с возможными retry)
	solveCtx, cancelSolve := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancelSolve()

	stepIcons := map[int]string{1: "🎯", 2: "📊", 3: "💡", 4: "👥"}
	stepLabels := map[int]string{1: "Прямой ответ", 2: "Пошаговое решение", 3: "Сгенерированный промт", 4: "Группа экспертов"}

	// Определяем количество этапов
	totalSteps := 3
	if llm.SYSTEM_PROMPT_GROUP_EXPERT != "" {
		totalSteps = 4
	}

	// Каналы для остановки анимации каждого этапа
	stopAnimations := make(map[int]chan struct{})
	startTimes := make(map[int]time.Time)
	attemptNotifiers := make(map[int]*llm.AttemptNotifier)

	// Отправляем сообщения и запускаем анимацию для каждого этапа
	for step := 1; step <= totalSteps; step++ {
		msgCtx, msgCancel := context.WithTimeout(context.Background(), 10*time.Second)
		msgID, err := h.bot.SendMessage(msgCtx, chatID, fmt.Sprintf("%s *%d. %s*\n\n⏳ Думаю...", stepIcons[step], step, stepLabels[step]))
		msgCancel()
		if err != nil {
			h.logger.Error("send thinking message failed", slog.String("error", err.Error()))
			continue
		}
		h.setSolveStepMessage(userID, step, msgID)

		// Запускаем анимацию для этого этапа
		stopCh := make(chan struct{})
		stopAnimations[step] = stopCh
		startTimes[step] = time.Now()

		// Создаём notifier для отслеживания попыток
		notifier := &llm.AttemptNotifier{}
		attemptNotifiers[step] = notifier

		go h.runSolveStepAnimation(chatID, msgID, step, stepIcons[step], stepLabels[step], startTimes[step], notifier, stopCh)
	}

	// Callback для обработки результатов
	callback := func(result llm.SolutionStepResult) {
		h.logger.Info("solve step callback", slog.Int("step", result.Step), slog.Bool("has_error", result.Err != nil))

		// Останавливаем анимацию для этого этапа
		if stopCh, ok := stopAnimations[result.Step]; ok {
			close(stopCh)
		}

		msgCtx, msgCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer msgCancel()

		msgID := h.getSolveStepMessage(userID, result.Step)
		h.setSolveStepModel(userID, result.Step, result.Variant.ModelID)

		keyboard := h.buildSolveRetryKeyboard(result.Step)

		// Вычисляем время выполнения
		elapsed := ""
		if startTime, ok := startTimes[result.Step]; ok {
			elapsed = fmt.Sprintf(" ⏱ %s", formatDuration(time.Since(startTime)))
		}

		if result.Err != nil {
			h.logger.Error("solve step error", slog.Int("step", result.Step), slog.String("error", result.Err.Error()))

			// Формируем сообщение об ошибке с кнопками
			errMsg := fmt.Sprintf("%s *%d. %s*%s\n\n❌ Ошибка: %v",
				stepIcons[result.Step], result.Step, result.Variant.Label, elapsed, result.Err)

			if msgID != 0 {
				h.bot.EditMessageKeyboard(msgCtx, chatID, msgID, errMsg, keyboard)
			}
		} else {
			msg := fmt.Sprintf("%s *%d. %s*%s\n🤖 Модель: _%s_\n\n%s",
				stepIcons[result.Step], result.Step, result.Variant.Label, elapsed, result.Variant.Model, result.Variant.Response)
			if msgID != 0 {
				h.bot.EditMessageKeyboard(msgCtx, chatID, msgID, msg, keyboard)
			}
		}
	}

	// Запускаем параллельное выполнение (блокируется до завершения всех)
	h.dialogService.GetSolutionVariantsParallel(solveCtx, model, task, attemptNotifiers, callback)

	// Финальное сообщение
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finishCancel()
	h.reply(finishCtx, chatID, "━━━━━━━━━━━━━━━━━━━━\n\n✨ Все запросы завершены!\n\nИспользуйте /solve для новой задачи.")
}

// runSolveStepAnimation запускает анимацию "Думаю..." с таймером для одного этапа.
// attemptInfo: указатель на [2]int{currentAttempt, maxAttempts} для отображения номера попытки
func (h *WebhookHandler) runSolveStepAnimation(chatID int64, msgID int64, step int, icon string, label string, startTime time.Time, attemptInfo *[2]int32, stopCh chan struct{}) {
	states := []string{"⏳ Думаю", "⏳ Думаю.", "⏳ Думаю..", "⏳ Думаю..."}
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	i := 0
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			i = (i + 1) % len(states)
			elapsed := formatDuration(time.Since(startTime))

			// Формируем текст с номером попытки если есть
			attemptText := ""
			if attemptInfo != nil {
				attempt := atomic.LoadInt32(&attemptInfo[0])
				maxAttempts := atomic.LoadInt32(&attemptInfo[1])
				if attempt > 1 {
					attemptText = fmt.Sprintf(" 🔄 %d/%d", attempt, maxAttempts)
				}
			}

			text := fmt.Sprintf("%s *%d. %s*\n\n⏱ %s%s %s", icon, step, label, elapsed, attemptText, states[i])

			editCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// Игнорируем ошибки редактирования - продолжаем анимацию
			_ = h.bot.EditMessage(editCtx, chatID, msgID, text)
			cancel()
		}
	}
}

// formatDuration форматирует длительность в читаемый вид.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	m := d / time.Minute
	s := (d % time.Minute) / time.Second
	if m > 0 {
		return fmt.Sprintf("%dм %dс", m, s)
	}
	return fmt.Sprintf("%dс", s)
}

// buildSolveRetryKeyboard создаёт клавиатуру с кнопками retry для solve.
func (h *WebhookHandler) buildSolveRetryKeyboard(step int) *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🔁 Повторить", CallbackData: fmt.Sprintf("solve_retry:%d:same", step)},
				{Text: "🔄 Другая модель", CallbackData: fmt.Sprintf("solve_retry:%d:other", step)},
			},
		},
	}
}

// handleSolveRetryCallback обрабатывает нажатие кнопки retry для solve.
func (h *WebhookHandler) handleSolveRetryCallback(ctx context.Context, cb *CallbackQuery, data string) {
	// Парсим данные: step:action (например "1:same" или "2:other")
	parts := strings.Split(data, ":")
	if len(parts) != 2 {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "❌ Неверный формат")
		return
	}

	var step int
	if _, err := fmt.Sscanf(parts[0], "%d", &step); err != nil {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "❌ Неверный этап")
		return
	}

	action := parts[1]

	// Получаем сохранённые данные
	task := h.getSolveTask(cb.From.ID)
	if task == "" {
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "❌ Задача не найдена. Используйте /solve")
		return
	}

	// Определяем модель
	var model string
	previousModel := h.getSolveStepModel(cb.From.ID, step)

	if action == "same" {
		// Используем последнюю модель для этого этапа, если она была
		model = previousModel
		if model == "" {
			// Если этап ещё не выполнялся, используем первоначальную модель
			model = h.getSolveModel(cb.From.ID)
		}
		h.bot.AnswerCallbackQuery(ctx, cb.ID, "🔁 Повторяю запрос...")
	} else {
		model = llm.GetRandomModelExcept(previousModel)
		h.bot.AnswerCallbackQuery(ctx, cb.ID, fmt.Sprintf("🔄 Пробую %s...", llm.GetModelName(model)))
	}

	// Обновляем сообщение на "Думаю..."
	stepIcons := map[int]string{1: "🎯", 2: "📊", 3: "💡", 4: "👥"}
	stepLabels := map[int]string{1: "Прямой ответ", 2: "Пошаговое решение", 3: "Сгенерированный промт", 4: "Группа экспертов"}

	if cb.Message != nil {
		h.bot.EditMessage(ctx, cb.Message.Chat.ID, cb.Message.MessageID,
			fmt.Sprintf("%s *%d. %s*\n\n⏳ Думаю... (%s)", stepIcons[step], step, stepLabels[step], llm.GetModelName(model)))
	}

	// Выполняем запрос в горутине с анимацией
	go func() {
		startTime := time.Now()

		// Создаём notifier для отслеживания попыток
		attemptInfo := &[2]int32{}

		// Запускаем анимацию
		stopAnimation := make(chan struct{})
		if cb.Message != nil {
			go h.runSolveStepAnimationWithModel(cb.Message.Chat.ID, cb.Message.MessageID, step, stepIcons[step], stepLabels[step], llm.GetModelName(model), startTime, attemptInfo, stopAnimation)
		}

		reqCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		// Добавляем callback для обновления attemptInfo
		reqCtx = retry.WithAttemptCallback(reqCtx, func(attempt, maxAttempts int) {
			atomic.StoreInt32(&attemptInfo[0], int32(attempt))
			atomic.StoreInt32(&attemptInfo[1], int32(maxAttempts))
		})

		result := h.dialogService.ExecuteSolutionStep(reqCtx, step, model, task)

		// Останавливаем анимацию
		close(stopAnimation)

		msgCtx, msgCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer msgCancel()

		h.setSolveStepModel(cb.From.ID, step, model)

		keyboard := h.buildSolveRetryKeyboard(step)
		elapsed := fmt.Sprintf(" ⏱ %s", formatDuration(time.Since(startTime)))

		if result.Err != nil {
			h.logger.Error("solve retry error", slog.Int("step", step), slog.String("error", result.Err.Error()))
			errMsg := fmt.Sprintf("%s *%d. %s*%s\n\n❌ Ошибка: %v",
				stepIcons[step], step, stepLabels[step], elapsed, result.Err)
			if cb.Message != nil {
				h.bot.EditMessageKeyboard(msgCtx, cb.Message.Chat.ID, cb.Message.MessageID, errMsg, keyboard)
			}
		} else {
			msg := fmt.Sprintf("%s *%d. %s*%s\n🤖 Модель: _%s_\n\n%s",
				stepIcons[step], step, result.Variant.Label, elapsed, result.Variant.Model, result.Variant.Response)
			if cb.Message != nil {
				h.bot.EditMessageKeyboard(msgCtx, cb.Message.Chat.ID, cb.Message.MessageID, msg, keyboard)
			}
		}
	}()
}

// runSolveStepAnimationWithModel запускает анимацию с указанием модели.
func (h *WebhookHandler) runSolveStepAnimationWithModel(chatID int64, msgID int64, step int, icon string, label string, modelName string, startTime time.Time, attemptInfo *[2]int32, stopCh chan struct{}) {
	states := []string{"⏳ Думаю", "⏳ Думаю.", "⏳ Думаю..", "⏳ Думаю..."}
	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()

	i := 0
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			i = (i + 1) % len(states)
			elapsed := formatDuration(time.Since(startTime))

			// Формируем текст с номером попытки если есть
			attemptText := ""
			if attemptInfo != nil {
				attempt := atomic.LoadInt32(&attemptInfo[0])
				maxAttempts := atomic.LoadInt32(&attemptInfo[1])
				if attempt > 1 {
					attemptText = fmt.Sprintf(" 🔄 %d/%d", attempt, maxAttempts)
				}
			}

			text := fmt.Sprintf("%s *%d. %s*\n\n⏱ %s%s %s\n🤖 _%s_", icon, step, label, elapsed, attemptText, states[i], modelName)

			editCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// Игнорируем ошибки редактирования - продолжаем анимацию
			_ = h.bot.EditMessage(editCtx, chatID, msgID, text)
			cancel()
		}
	}
}

// setSolveMode устанавливает режим ожидания задачи для /solve.
func (h *WebhookHandler) setSolveMode(userID int64, enabled bool) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.solveMode = enabled
	h.state[userID] = state
}

// isSolveMode проверяет, находится ли пользователь в режиме solve.
func (h *WebhookHandler) isSolveMode(userID int64) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	return ok && state.solveMode
}

// setSolveModel сохраняет выбранную модель для /solve.
func (h *WebhookHandler) setSolveModel(userID int64, model string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.solveModel = model
	h.state[userID] = state
}

// getSolveModel возвращает выбранную модель для /solve.
func (h *WebhookHandler) getSolveModel(userID int64) string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok {
		return ""
	}
	return state.solveModel
}

// setSolveTaskData сохраняет данные задачи для /solve (для retry).
func (h *WebhookHandler) setSolveTaskData(userID int64, task string, model string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.solveTask = task
	state.solveModel = model
	state.solveStepMessages = make(map[int]int64)
	state.solveStepModels = make(map[int]string)
	h.state[userID] = state
}

// getSolveTask возвращает сохранённую задачу для /solve.
func (h *WebhookHandler) getSolveTask(userID int64) string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok {
		return ""
	}
	return state.solveTask
}

// setSolveStepMessage сохраняет ID сообщения для этапа solve.
func (h *WebhookHandler) setSolveStepMessage(userID int64, step int, msgID int64) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	if state.solveStepMessages == nil {
		state.solveStepMessages = make(map[int]int64)
	}
	state.solveStepMessages[step] = msgID
	h.state[userID] = state
}

// getSolveStepMessage возвращает ID сообщения для этапа solve.
func (h *WebhookHandler) getSolveStepMessage(userID int64, step int) int64 {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok || state.solveStepMessages == nil {
		return 0
	}
	return state.solveStepMessages[step]
}

// setSolveStepModel сохраняет модель использованную для этапа solve.
func (h *WebhookHandler) setSolveStepModel(userID int64, step int, model string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	if state.solveStepModels == nil {
		state.solveStepModels = make(map[int]string)
	}
	state.solveStepModels[step] = model
	h.state[userID] = state
}

// getSolveStepModel возвращает модель использованную для этапа solve.
func (h *WebhookHandler) getSolveStepModel(userID int64, step int) string {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state, ok := h.state[userID]
	if !ok || state.solveStepModels == nil {
		return ""
	}
	return state.solveStepModels[step]
}

// clearSolveState очищает состояние /solve.
func (h *WebhookHandler) clearSolveState(userID int64) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	state := h.state[userID]
	state.solveMode = false
	state.solveModel = ""
	state.solveTask = ""
	state.solveStepMessages = nil
	state.solveStepModels = nil
	h.state[userID] = state
}
