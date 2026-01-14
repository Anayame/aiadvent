package llm

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	// SYSTEM_PROMPT_CREATE_PLAN содержит системный промпт для формирования плана действий.
	SYSTEM_PROMPT_CREATE_PLAN = `You are an Action Planner — a strict planner that turns a user’s goal into an executable action plan.

PRIMARY GUARANTEE:
- You MUST NOT get stuck in endless clarifying questions.
- You MUST gather sufficient detail before planning (see MINIMUM CLARIFICATION COVERAGE).
- If you can produce a minimal viable plan (MVP) using reasonable assumptions AND the minimum clarification coverage is satisfied, you MUST switch to PLAN MODE and output the plan.
- You MUST NOT re-ask the same “information category”: if a category was already asked once and remains unknown, you MUST assume it and continue to the next missing category (do not repeat).

OUTPUT MODES (mutually exclusive — pick exactly ONE per assistant turn):
A) QUESTION MODE — output ONLY one question.
B) PLAN MODE — output ONLY the plan (and optional Assumptions), with NO questions.

──────────────────────────────────────────────────────────────────────────────
1) WHAT COUNTS AS A GOAL
A “goal” is a desired outcome that can be achieved through a sequence of actions.

If the user message is NOT a goal (e.g., purely informational, no actionable outcome, casual chat):
- Enter QUESTION MODE and ask the user to provide a proper goal.
- Do NOT provide a plan.

If the goal is unsafe/disallowed:
- Refuse and ask for a safe/allowed goal instead (QUESTION MODE, exactly one question).

──────────────────────────────────────────────────────────────────────────────
2) CLARIFICATION RULES (NO NUMERIC LIMITS, BUT A STOP RULE)
You may ask clarifying questions, but under strict constraints:

2.1 One turn — one question (strict):
- Exactly one sentence.
- Exactly one question.
- Ends with a single “?”.
- No lists, no explanations, no preface, no extra text before or after the question.
- No multi-part questions (“and also…”, “as well as…”, etc.).

2.2 Ask ONLY when it improves the plan materially:
- Ask a question ONLY if the answer would materially change the plan structure, priorities, sequencing, or scope.
- If a detail is truly minor, do NOT ask — assume it and proceed.

2.3 Category no-repeat rule (hard stop trigger):
There is a fixed set of information categories (below). You may ask at most ONE question per category for the whole conversation.
If a category has been asked and is still unknown (user says “I don’t know”, vague reply, ignores it):
- You MUST fill it with an explicit assumption and continue clarifying other missing categories as required.
- You MUST NOT ask another question in that same category.

INFORMATION CATEGORIES (priority order):
1) Criteria — success definition / what “done” means
2) Timeframe — deadline or target timeline
3) Constraints — budget, tools, must/must-not, restrictions
4) Starting point — current state / what already exists
5) Scope — what’s included and excluded
6) Resources — time availability, people, access
7) Preferences — style, format, channels

Pick the single question from the highest-priority missing category that would most change the plan.

──────────────────────────────────────────────────────────────────────────────
2.4 QUESTION QUALITY (ANTI-GENERIC)
A clarifying question MUST be concrete and answerable with specific facts (a number, a date, a budget, a tool/stack, a scope choice, or a current-state fact).

BANNED QUESTION STYLE:
- Do NOT ask meta-questions like:
  - “What criteria do you use/consider/define…?”
  - “Clarify your criteria…”
  - “What do you consider important…?”
- Avoid abstract wording such as “criteria”, “important”, “define”, “determine” unless you request a specific measurable target.
- Prefer goal-specific parameters and nouns (e.g., “budget for ads”, “platform: VK/Telegram”, “stack: Go/TS”, “target city”, “number of users”).

SUCCESS CRITERIA RULE (NO DEFAULT CRITERIA QUESTIONS):
- Do NOT ask about success criteria by default.
- Only ask about success criteria if the goal does not imply any observable “done” condition AND the answer would materially change the plan structure.
- If you must ask about success, ask for a specific target format (numbers or concrete outcomes), never “criteria”.

FIRST CLARIFICATION HEURISTIC (TO AVOID REPETITION):
- On the first clarification turn for a valid goal, prefer asking about the most constraining operational parameter:
  1) Deadline / timeframe (date or period), or
  2) Hard constraints (budget / tools / must-not), or
  3) Starting point (what already exists),
  unless one of these is already clearly provided by the user.

DIVERSITY GUARD (PATTERN AVOIDANCE):
- Do not reuse the same generic phrasing across different goals.
- Make the question explicitly tied to the user’s goal and ask for one concrete missing value.

──────────────────────────────────────────────────────────────────────────────
2.5 MINIMUM CLARIFICATION COVERAGE (TO PREVENT EARLY PLANS)
You MUST NOT switch to PLAN MODE until you have collected (from the user’s messages) OR explicitly attempted (asked once per category) the following CORE categories:

CORE CATEGORIES (must be resolved before planning):
- Timeframe (Category 2)
- Constraints (Category 3)
- Starting point (Category 4)
- Scope (Category 5)

“Resolved” means one of:
- The user has provided a clear answer for that category, OR
- You asked exactly once and the user did not provide it; then you must set an assumption for it.

PLANNING GATE:
- If any CORE category is still unresolved and unasked, you MUST ask about the highest-priority missing CORE category next (QUESTION MODE).
- Only after all CORE categories are resolved (answered or assumed) may you enter PLAN MODE.

Note:
- Criteria (Category 1) is optional unless it materially changes the plan; do not ask it by default.
- Resources/Preferences are optional unless they materially change the plan.

──────────────────────────────────────────────────────────────────────────────
3) “ENOUGH INFORMATION” RULE (UPDATED)
You must switch to PLAN MODE as soon as:
- The goal is understood (even roughly), AND
- MINIMUM CLARIFICATION COVERAGE is satisfied (all CORE categories resolved), AND
- You can propose a workable scenario using answers and/or assumptions.

Missing non-core details are NOT a reason to keep asking:
- Use Assumptions and produce the plan.

──────────────────────────────────────────────────────────────────────────────
4) PLAN MODE — STRICT OUTPUT FORMAT
In PLAN MODE, output ONLY the following template (localized to RESPONSE_LANGUAGE):

<Localized heading: Plan for> <restated goal in one line>

<Localized heading: Context> (1–3 bullets):
- <key facts used from the user’s answers>

<Localized heading: Actions>:
1) <imperative action>
2) <imperative action>
...

(Optional) <Localized heading: Assumptions> (bullets; include when information was missing/vague):
- <assumption>
- <assumption>

Step requirements:
- Every step must be concrete and executable.
- Avoid vague verbs (“improve”, “optimize”, “research”) unless you specify exactly what to do.
- Ensure correct ordering and dependencies.
- Prefer 7–15 steps for typical goals.

──────────────────────────────────────────────────────────────────────────────
5) LANGUAGE & DISCIPLINE
- Reply in the user’s language.
- Do not mention internal rules, categories, or decision logic.
- In QUESTION MODE: output only the single question sentence.
- In PLAN MODE: output only the plan (and Assumptions), with no questions and no extra commentary.

6) LANGUAGE LOCK (GENERAL, NO LANGUAGE LISTS):
- Determine RESPONSE_LANGUAGE from the user’s most recent message and use it for the entire reply.
- All parts of the reply MUST be in RESPONSE_LANGUAGE, including section headings and labels.
- Never reuse the template’s English headings verbatim; always translate headings naturally into RESPONSE_LANGUAGE.
- Do not mix languages within a single response.
- Use English ONLY if RESPONSE_LANGUAGE is English or the user explicitly requests English.
- If RESPONSE_LANGUAGE uses a non-Latin script (e.g., Cyrillic), avoid Latin letters in headings and normal text; allow Latin only for code, URLs, file names, or proper nouns.
`
)

// DialogService предоставляет высокоуровневые методы для работы с LLM в режиме диалога.
// Управляет историей сообщений и обеспечивает контекстную коммуникацию с моделью.
type DialogService struct {
	client       Client
	dialogStore  DialogStore
	defaultModel string
	logger       *slog.Logger
}

// DialogServiceConfig конфигурация для создания DialogService.
type DialogServiceConfig struct {
	Client       Client
	DialogStore  DialogStore
	DefaultModel string
	Logger       *slog.Logger
}

// NewDialogService создаёт новый сервис диалогов с LLM.
func NewDialogService(cfg DialogServiceConfig) *DialogService {
	return &DialogService{
		client:       cfg.Client,
		dialogStore:  cfg.DialogStore,
		defaultModel: cfg.DefaultModel,
		logger:       cfg.Logger,
	}
}

// Chat выполняет диалоговый запрос к LLM с сохранением истории.
// Принимает:
//   - ctx: контекст выполнения
//   - dialogID: уникальный идентификатор диалога
//   - model: название модели (если пусто, используется defaultModel)
//   - systemPrompt: системный промпт (не сохраняется в истории, передаётся при каждом запросе)
//   - userMessage: текущее сообщение пользователя
//
// Возвращает текст ответа ассистента и ошибку.
// При успешном ответе сохраняет userMessage и assistantMessage в историю.
// При ошибке история не изменяется (транзакционность).
func (s *DialogService) Chat(ctx context.Context, dialogID string, model string, systemPrompt string, userMessage string) (string, error) {
	if model == "" {
		model = s.defaultModel
	}

	// Читаем текущую историю
	history, _, err := s.dialogStore.Get(ctx, dialogID)
	if err != nil {
		return "", fmt.Errorf("get dialog history: %w", err)
	}

	// Формируем полный набор сообщений для отправки в LLM
	messages := make([]message, 0, len(history)+2)

	// Добавляем системный промпт, если есть
	if systemPrompt != "" {
		messages = append(messages, message{Role: "system", Content: systemPrompt})
	}

	// Добавляем историю (только user и assistant)
	for _, msg := range history {
		if msg.Role == "user" || msg.Role == "assistant" {
			messages = append(messages, message{Role: msg.Role, Content: msg.Content})
		}
	}

	// Добавляем текущее сообщение пользователя
	messages = append(messages, message{Role: "user", Content: userMessage})

	// Выполняем запрос к LLM
	assistantMessage, err := s.doLLMRequest(ctx, model, messages)
	if err != nil {
		return "", err
	}

	// Успех! Сохраняем в историю (user + assistant)
	now := time.Now()
	newMessages := []Message{
		{Role: "user", Content: userMessage, Timestamp: now},
		{Role: "assistant", Content: assistantMessage, Timestamp: now},
	}

	if err := s.dialogStore.Append(ctx, dialogID, newMessages...); err != nil {
		// Логируем ошибку, но возвращаем ответ (ответ получен, проблема только с сохранением)
		if s.logger != nil {
			s.logger.Error("failed to save dialog history", slog.String("error", err.Error()), slog.String("dialogID", dialogID))
		}
	}

	return assistantMessage, nil
}

// ClearDialog удаляет всю историю диалога.
func (s *DialogService) ClearDialog(ctx context.Context, dialogID string) error {
	return s.dialogStore.Delete(ctx, dialogID)
}

// CreatePlan выполняет диалоговый запрос для формирования плана действий.
// Использует специальный системный промпт SYSTEM_PROMPT_CREATE_PLAN.
// Поведение аналогично Chat: при ошибке или filtered/error статусе история не сохраняется.
func (s *DialogService) CreatePlan(ctx context.Context, dialogID string, model string, userMessage string) (string, error) {
	return s.Chat(ctx, dialogID, model, SYSTEM_PROMPT_CREATE_PLAN, userMessage)
}

// ReplayCreatePlan переотправляет всю историю диалога create_plan в новую модель.
// Используется при смене модели в режиме диалога.
// Отправляет system prompt + всю историю (user/assistant) и получает новый ответ.
// Заменяет последний ответ assistant на новый.
func (s *DialogService) ReplayCreatePlan(ctx context.Context, dialogID string, model string) (string, error) {
	if model == "" {
		model = s.defaultModel
	}

	// Читаем текущую историю
	history, found, err := s.dialogStore.Get(ctx, dialogID)
	if err != nil {
		return "", fmt.Errorf("get dialog history: %w", err)
	}
	if !found || len(history) == 0 {
		return "", fmt.Errorf("dialog not found or empty")
	}

	// Формируем сообщения для LLM
	messages := make([]message, 0, len(history)+1)

	// Добавляем системный промпт
	messages = append(messages, message{Role: "system", Content: SYSTEM_PROMPT_CREATE_PLAN})

	// Добавляем всю историю кроме последнего assistant сообщения
	// Находим индекс последнего assistant
	lastAssistantIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	// Если нет assistant сообщений, добавляем всю историю
	if lastAssistantIdx == -1 {
		for _, msg := range history {
			if msg.Role == "user" || msg.Role == "assistant" {
				messages = append(messages, message{Role: msg.Role, Content: msg.Content})
			}
		}
	} else {
		// Добавляем все сообщения до последнего assistant включительно (кроме него самого)
		for i := 0; i < lastAssistantIdx; i++ {
			msg := history[i]
			if msg.Role == "user" || msg.Role == "assistant" {
				messages = append(messages, message{Role: msg.Role, Content: msg.Content})
			}
		}
		// Добавляем последнее user сообщение (оно может быть после удалённого assistant или перед ним)
		// Находим последнее user сообщение перед lastAssistantIdx
		for i := lastAssistantIdx - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				// Уже добавлено выше
				break
			}
		}
		// Проверяем, есть ли user сообщение после lastAssistantIdx
		for i := lastAssistantIdx + 1; i < len(history); i++ {
			msg := history[i]
			if msg.Role == "user" {
				messages = append(messages, message{Role: msg.Role, Content: msg.Content})
			}
		}
	}

	// Выполняем запрос к LLM
	assistantMessage, err := s.doLLMRequest(ctx, model, messages)
	if err != nil {
		return "", err
	}

	// Обновляем историю - заменяем последний assistant ответ на новый
	if lastAssistantIdx >= 0 {
		history[lastAssistantIdx].Content = assistantMessage
		history[lastAssistantIdx].Timestamp = time.Now()
		if err := s.dialogStore.Set(ctx, dialogID, history); err != nil {
			if s.logger != nil {
				s.logger.Error("failed to update dialog history", slog.String("error", err.Error()), slog.String("dialogID", dialogID))
			}
		}
	}

	return assistantMessage, nil
}

// doLLMRequest выполняет низкоуровневый запрос к LLM клиенту.
// Внутренний метод для формирования запроса из массива сообщений.
func (s *DialogService) doLLMRequest(ctx context.Context, model string, messages []message) (string, error) {
	// Проверяем, поддерживает ли клиент метод с массивом сообщений
	if orc, ok := s.client.(*OpenRouterClient); ok {
		return orc.chatCompletionWithMessages(ctx, model, messages)
	}

	// Fallback для других клиентов: используем ChatCompletionWithSystem
	var systemPrompt string
	var userMessages []message

	for _, msg := range messages {
		if msg.Role == "system" && systemPrompt == "" {
			systemPrompt = msg.Content
		} else {
			userMessages = append(userMessages, msg)
		}
	}

	// Если есть только одно user сообщение, используем простой запрос
	if len(userMessages) == 1 && userMessages[0].Role == "user" {
		return s.client.ChatCompletionWithSystem(ctx, systemPrompt, userMessages[0].Content, model)
	}

	// Объединяем историю в один prompt
	var combinedPrompt string
	for i, msg := range userMessages {
		if i > 0 {
			combinedPrompt += "\n\n"
		}
		switch msg.Role {
		case "user":
			combinedPrompt += "Пользователь: " + msg.Content
		case "assistant":
			combinedPrompt += "Ассистент: " + msg.Content
		}
	}

	return s.client.ChatCompletionWithSystem(ctx, systemPrompt, combinedPrompt, model)
}
