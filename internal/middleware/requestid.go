package middleware

import (
	"net/http"

	"github.com/google/uuid"
)

const headerRequestID = "X-Request-ID"

// RequestID проставляет идентификатор запроса, если он не был задан.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get(headerRequestID)
		if reqID == "" {
			reqID = uuid.NewString()
			r.Header.Set(headerRequestID, reqID)
		}
		w.Header().Set(headerRequestID, reqID)
		next.ServeHTTP(w, r)
	})
}
