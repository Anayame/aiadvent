package httpserver

import (
	"net/http"

	"aiadvent/internal/middleware"

	"log/slog"

	"github.com/go-chi/chi/v5"
)

type RouterDeps struct {
	Logger          *slog.Logger
	TelegramHandler http.Handler
}

// NewRouter собирает chi-роутер с общими middleware.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recover(deps.Logger))
	r.Use(middleware.Logging(deps.Logger))

	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("pong"))
	})

	r.Post("/telegram/webhook", deps.TelegramHandler.ServeHTTP)

	return r
}
