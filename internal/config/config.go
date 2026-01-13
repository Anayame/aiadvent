package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr       string
	LogLevel       string
	AdminPassword  string
	SessionTTL     time.Duration
	RequestTimeout time.Duration
	OpenRouter     OpenRouterConfig
	Telegram       TelegramConfig
}

type OpenRouterConfig struct {
	APIKey       string
	BaseURL      string
	DefaultModel string
}

type TelegramConfig struct {
	BotToken      string
	APIBaseURL    string
	WebhookSecret string
}

func Load() (Config, error) {
	var cfg Config

	cfg.HTTPAddr = getEnv("HTTP_ADDR", ":8080")
	cfg.LogLevel = getEnv("LOG_LEVEL", "info")
	cfg.AdminPassword = getEnv("ADMIN_PASSWORD", "")

	sessionTTL, err := parseDuration(getEnv("SESSION_TTL", "2h"))
	if err != nil {
		return Config{}, fmt.Errorf("parse SESSION_TTL: %w", err)
	}
	cfg.SessionTTL = sessionTTL

	reqTimeout, err := parseDuration(getEnv("HTTP_CLIENT_TIMEOUT", "15s"))
	if err != nil {
		return Config{}, fmt.Errorf("parse HTTP_CLIENT_TIMEOUT: %w", err)
	}
	cfg.RequestTimeout = reqTimeout

	cfg.OpenRouter = OpenRouterConfig{
		APIKey:       getEnv("OPENROUTER_API_KEY", ""),
		BaseURL:      getEnv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
		DefaultModel: getEnv("OPENROUTER_DEFAULT_MODEL", ""),
	}

	cfg.Telegram = TelegramConfig{
		BotToken:      getEnv("TELEGRAM_BOT_TOKEN", ""),
		APIBaseURL:    getEnv("TELEGRAM_API_BASE_URL", "https://api.telegram.org"),
		WebhookSecret: getEnv("TELEGRAM_WEBHOOK_SECRET", ""),
	}

	return cfg, nil
}

func parseDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, fmt.Errorf("duration is empty")
	}
	return time.ParseDuration(value)
}

func getEnv(key, def string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return def
}

// parseBoolDefault parses optional boolean with default value.
func parseBoolDefault(value string, def bool) (bool, error) {
	if value == "" {
		return def, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, err
	}
	return parsed, nil
}
