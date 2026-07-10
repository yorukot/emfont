package middleware

import (
	"net/http"
	"time"

	"github.com/emfont/emfont/backend/internal/controller/logger"
	"go.uber.org/zap"
)

func Logging(base *zap.Logger) func(http.Handler) http.Handler {
	if base == nil {
		base = zap.NewNop()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			requestID := RequestIDFromContext(r.Context())
			requestLog := base.With(
				zap.String("request_id", requestID),
				zap.String("http.method", r.Method),
				zap.String("http.path", r.URL.Path),
				zap.String("http.remote_addr", r.RemoteAddr),
				zap.String("http.user_agent", r.UserAgent()),
			)

			ww := NewResponseWriter(w)
			next.ServeHTTP(ww, r.WithContext(logger.IntoContext(r.Context(), requestLog)))

			requestLog.Info("http request",
				zap.Int("http.status", ww.Status()),
				zap.Int("http.response_bytes", ww.Bytes()),
				zap.Duration("http.duration", time.Since(started)),
			)
		})
	}
}
