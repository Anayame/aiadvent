package llm

// AvailableModels содержит список популярных моделей OpenRouter.
// Модели выбраны по популярности и качеству ответов.
var AvailableModels = []ModelInfo{
	{
		ID:          "anthropic/claude-3.5-sonnet",
		Name:        "Claude 3.5 Sonnet",
		Description: "Лучшая модель Anthropic для сложных задач",
	},
	{
		ID:          "openai/gpt-4o",
		Name:        "GPT-4o",
		Description: "Флагманская модель OpenAI",
	},
	{
		ID:          "google/gemini-2.0-flash-exp:free",
		Name:        "Gemini 2.0 Flash free",
		Description: "Быстрая модель Google (бесплатно)",
	},
	{
		ID:          "meta-llama/llama-3.3-70b-instruct",
		Name:        "Llama 3.3 70B",
		Description: "Открытая модель Meta",
	},
	{
		ID:          "deepseek/deepseek-chat",
		Name:        "DeepSeek Chat",
		Description: "Экономичная модель с хорошим качеством",
	},
}

// ModelInfo описывает информацию о модели.
type ModelInfo struct {
	ID          string // Идентификатор модели для API
	Name        string // Короткое название для отображения
	Description string // Описание модели
}

// GetModelByID возвращает информацию о модели по её ID.
// Если модель не найдена, возвращает nil.
func GetModelByID(modelID string) *ModelInfo {
	for _, m := range AvailableModels {
		if m.ID == modelID {
			return &m
		}
	}
	return nil
}

// IsValidModel проверяет, является ли modelID допустимой моделью.
func IsValidModel(modelID string) bool {
	return GetModelByID(modelID) != nil
}

// GetModelName возвращает короткое название модели по её ID.
// Если модель не найдена, возвращает сам ID.
func GetModelName(modelID string) string {
	if info := GetModelByID(modelID); info != nil {
		return info.Name
	}
	return modelID
}
