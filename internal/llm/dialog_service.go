package llm

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"aiadvent/internal/retry"
)

// Re-export для удобства использования
var WithAttemptCallback = retry.WithAttemptCallback

// SolutionVariantResult содержит результат одного варианта решения задачи.
type SolutionVariantResult struct {
	ModelID    string // ID модели для API
	Model      string // Название модели для отображения
	Label      string // Тип решения (прямой ответ, пошаговое решение, промт, группа экспертов)
	Response   string // Ответ от модели
	SystemUsed string // Использованный системный промт
}

// SolutionVariantsResult содержит все варианты решения задачи.
type SolutionVariantsResult struct {
	TaskPrompt      string                // Исходный промт задачи
	DirectAnswer    SolutionVariantResult // Прямой ответ
	StepByStep      SolutionVariantResult // Пошаговое решение
	GeneratedPrompt SolutionVariantResult // Сгенерированный промт
	GroupExpert     SolutionVariantResult // Ответ группы экспертов
}

const (
	// SYSTEM_PROMPT_DIRECT_ANSWER содержит системный промпт для получения прямого ответа.
	SYSTEM_PROMPT_DIRECT_ANSWER = "Дай только ответ напрямую, без дополнительных рассуждений и информации, только ответ."

	// SYSTEM_PROMPT_STEP_BY_STEP содержит системный промпт для получения пошагового решения.
	SYSTEM_PROMPT_STEP_BY_STEP = `You are a precise problem-solving assistant.

TASK
The user will provide a problem or task. Produce a solution as a numbered, step-by-step procedure.

MANDATORY LANGUAGE REQUIREMENT
- The ENTIRE response MUST be written in Russian.
- Do NOT use any other language in the response (including headings or short phrases), except universally recognized code keywords inside code blocks if absolutely necessary.

STRICT OUTPUT RULES
- Output ONLY the step-by-step solution.
- Number every step consecutively starting from 1 (1., 2., 3., ...).
- Do NOT add any extra commentary, explanations, background, or "thought process".
- Do NOT include sections like "Assumptions", "Notes", "Summary", "Conclusion", or "Final answer" unless the task explicitly requires it.
- Do NOT ask clarifying questions. If information is missing, make the minimal reasonable assumptions needed to proceed and incorporate them implicitly into the steps (without labeling them as assumptions).
- Keep each step short and action-oriented.
- If calculations are needed, show the operations inside the relevant step, but do not add narrative around them.

FORMAT REQUIREMENTS
- Use plain text.
- Each step must be on its own line.
- No bullet points, no tables, no headings.

SAFETY
Follow all applicable safety and policy constraints. If the request is disallowed, respond with a brief refusal.

Now solve the user's task following the rules above.
`

	// SYSTEM_PROMPT_CREATE_PROMPT содержит системный промпт для создания промта для решения задачи.
	SYSTEM_PROMPT_CREATE_PROMPT = "Составь промт для llm модели для решения задачи."

	// SYSTEM_PROMPT_GROUP_EXPERT содержит системный промпт для группы экспертов.
	SYSTEM_PROMPT_GROUP_EXPERT = `You are an Expert Panel Orchestrator.

GOAL
When the user provides a task prompt, you must:
1) Create a panel of experts (2-3 roles) relevant to the task.
2) Each expert independently produces their best solution.
3) Provide a final synthesis that compares approaches and recommends a path forward.

MANDATORY LANGUAGE REQUIREMENT
- The ENTIRE response text MUST be written in Russian.
- Do NOT use any other language (including expert titles, headings, or inline phrases), except universally recognized code keywords inside code blocks if absolutely necessary.

CORE RULES
- Do NOT ask clarifying questions unless the task is impossible without critical missing info.
- If details are missing, make reasonable assumptions and list them explicitly.
- Experts must be diverse (different disciplines, viewpoints, risk tolerance).
- Experts should not copy each other: each must use a distinct approach or emphasis.
- Prefer actionable outputs (steps, examples, checklists, pseudo-code) over generic advice.
- Follow all safety and policy constraints.

PROCESS (always follow)
A) Parse the user task: restate it in 1–2 sentences.
B) Choose experts:
   - Output a short roster: {name, role, focus, success criteria}.
C) Expert round:
   - For each expert:
     - Assumptions (if any)
     - Solution (structured, concise, actionable)
     - Risks / trade-offs
     - What to verify / next steps
D) Synthesis:
   - Compare key differences (table-like text is OK).
   - Recommend: best overall approach + when to choose alternatives.
   - Provide a minimal “next actions” checklist.

OUTPUT FORMAT (strict)
1. Task Summary
2. Expert Roster
3. Expert Solutions
   3.1 Expert 1: ...
   3.2 Expert 2: ...
   ...
4. Synthesis & Recommendation
5. Next Actions Checklist

STYLE
- Write in Russian only.
- No emojis.
- Be direct and concrete.
`

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

// SolutionStepResult передаётся в callback при завершении этапа.
type SolutionStepResult struct {
	Step    int
	Variant SolutionVariantResult
	Err     error
}

// SolutionStepCallback вызывается после завершения каждого этапа (параллельно).
type SolutionStepCallback func(result SolutionStepResult)

// SolutionStepConfig описывает конфигурацию одного этапа решения.
type SolutionStepConfig struct {
	Step         int
	Label        string
	SystemPrompt string
	Model        string // ID модели
}

// GetSolutionStepConfigs возвращает конфигурации всех этапов решения.
func GetSolutionStepConfigs(model string) []SolutionStepConfig {
	configs := []SolutionStepConfig{
		{Step: 1, Label: "Прямой ответ", SystemPrompt: SYSTEM_PROMPT_DIRECT_ANSWER, Model: model},
		{Step: 2, Label: "Пошаговое решение", SystemPrompt: SYSTEM_PROMPT_STEP_BY_STEP, Model: model},
		{Step: 3, Label: "Сгенерированный промт", SystemPrompt: SYSTEM_PROMPT_CREATE_PROMPT, Model: AvailableModels[rand.Intn(len(AvailableModels))].ID},
	}
	if SYSTEM_PROMPT_GROUP_EXPERT != "" {
		configs = append(configs, SolutionStepConfig{Step: 4, Label: "Группа экспертов", SystemPrompt: SYSTEM_PROMPT_GROUP_EXPERT, Model: model})
	}
	return configs
}

// AttemptNotifier хранит информацию о попытках для отображения в UI.
// [0] - текущая попытка, [1] - максимум попыток
type AttemptNotifier = [2]int32

// GetSolutionVariantsParallel выполняет все запросы параллельно.
// Вызывает callback по мере завершения каждого этапа.
// attemptNotifiers - map[step]*AttemptNotifier для обновления информации о попытках (опционально).
// Блокируется до завершения всех запросов.
func (s *DialogService) GetSolutionVariantsParallel(ctx context.Context, model string, taskPrompt string, attemptNotifiers map[int]*AttemptNotifier, callback SolutionStepCallback) {
	if model == "" {
		model = s.defaultModel
	}

	configs := GetSolutionStepConfigs(model)

	var wg sync.WaitGroup
	for _, cfg := range configs {
		wg.Add(1)
		go func(cfg SolutionStepConfig) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					// При панике возвращаем ошибку через callback
					callback(SolutionStepResult{
						Step: cfg.Step,
						Variant: SolutionVariantResult{
							ModelID:    cfg.Model,
							Model:      GetModelName(cfg.Model),
							Label:      cfg.Label,
							SystemUsed: cfg.SystemPrompt,
						},
						Err: fmt.Errorf("panic recovered: %v", r),
					})
				}
			}()

			// Создаём контекст с callback для уведомления о попытках
			reqCtx := ctx
			if attemptNotifiers != nil {
				if notifier, ok := attemptNotifiers[cfg.Step]; ok && notifier != nil {
					reqCtx = withAttemptNotifier(ctx, notifier)
				}
			}

			response, err := s.client.ChatCompletionWithSystem(reqCtx, cfg.SystemPrompt, taskPrompt, cfg.Model)
			callback(SolutionStepResult{
				Step: cfg.Step,
				Variant: SolutionVariantResult{
					ModelID:    cfg.Model,
					Model:      GetModelName(cfg.Model),
					Label:      cfg.Label,
					Response:   response,
					SystemUsed: cfg.SystemPrompt,
				},
				Err: err,
			})
		}(cfg)
	}
	wg.Wait()
}

// withAttemptNotifier создаёт контекст с callback'ом для обновления AttemptNotifier.
func withAttemptNotifier(ctx context.Context, notifier *AttemptNotifier) context.Context {
	return WithAttemptCallback(ctx, func(attempt, maxAttempts int) {
		atomic.StoreInt32(&notifier[0], int32(attempt))
		atomic.StoreInt32(&notifier[1], int32(maxAttempts))
	})
}

// ExecuteSolutionStep выполняет один этап решения задачи.
// Используется для повторных запросов при ошибке.
func (s *DialogService) ExecuteSolutionStep(ctx context.Context, step int, model string, taskPrompt string) SolutionStepResult {
	if model == "" {
		model = s.defaultModel
	}

	var systemPrompt, label string
	switch step {
	case 1:
		systemPrompt = SYSTEM_PROMPT_DIRECT_ANSWER
		label = "Прямой ответ"
	case 2:
		systemPrompt = SYSTEM_PROMPT_STEP_BY_STEP
		label = "Пошаговое решение"
	case 3:
		systemPrompt = SYSTEM_PROMPT_CREATE_PROMPT
		label = "Сгенерированный промт"
	case 4:
		systemPrompt = SYSTEM_PROMPT_GROUP_EXPERT
		label = "Группа экспертов"
	default:
		return SolutionStepResult{Step: step, Err: fmt.Errorf("unknown step: %d", step)}
	}

	response, err := s.client.ChatCompletionWithSystem(ctx, systemPrompt, taskPrompt, model)
	return SolutionStepResult{
		Step: step,
		Variant: SolutionVariantResult{
			ModelID:    model,
			Model:      GetModelName(model),
			Label:      label,
			Response:   response,
			SystemUsed: systemPrompt,
		},
		Err: err,
	}
}

// GetRandomModelExcept возвращает случайную модель, отличную от excludeModel.
func GetRandomModelExcept(excludeModel string) string {
	if len(AvailableModels) <= 1 {
		return AvailableModels[0].ID
	}
	for {
		m := AvailableModels[rand.Intn(len(AvailableModels))]
		if m.ID != excludeModel {
			return m.ID
		}
	}
}
