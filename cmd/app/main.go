package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aiadvent/internal/auth"
	"aiadvent/internal/config"
	"aiadvent/internal/httpserver"
	"aiadvent/internal/llm"
	"aiadvent/internal/telegram"
	"aiadvent/internal/transport"
	"log/slog"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	logger := newLogger(cfg.LogLevel)

	httpClient := transport.NewHTTPClient(cfg.RequestTimeout)
	llmClient := llm.NewOpenRouterClient(cfg.OpenRouter, httpClient, logger)

	var store auth.Store
	switch strings.ToLower(cfg.AuthStoreType) {
	case "memory":
		store = auth.NewMemoryStore()
	default:
		fileStore, err := auth.NewFileStore(cfg.AuthStorePath)
		if err != nil {
			log.Fatalf("failed to init file store: %v", err)
		}
		store = fileStore
	}
	authService := auth.NewService(cfg.AdminPassword, cfg.SessionTTL, store)

	telegramClient := telegram.NewClient(cfg.Telegram, httpClient)
	webhookHandler := telegram.NewWebhookHandler(telegram.WebhookDeps{
		Auth:          authService,
		LLM:           llmClient,
		Bot:           telegramClient,
		Logger:        logger,
		AdminPassword: cfg.AdminPassword,
		SessionTTL:    cfg.SessionTTL,
		WebhookSecret: cfg.Telegram.WebhookSecret,
	})

	router := httpserver.NewRouter(httpserver.RouterDeps{
		Logger:          logger,
		TelegramHandler: webhookHandler,
	})

	server := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server starting", slog.String("addr", cfg.HTTPAddr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", slog.String("error", err.Error()))
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown initiated")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.String("error", err.Error()))
	}

	logger.Info("server stopped")
}

func newLogger(level string) *slog.Logger {
	slogLevel := slog.LevelInfo
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))
}
