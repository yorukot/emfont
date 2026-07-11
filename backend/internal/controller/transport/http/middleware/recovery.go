package middleware

import (
	"net/http"

	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"go.uber.org/zap"
)

func Recovery(base *zap.Logger) func(http.Handler) http.Handler {
	if base == nil {
		base = zap.NewNop()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := NewResponseWriter(w)
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				log := base.With(
					zap.String("request_id", RequestIDFromContext(r.Context())),
					zap.String("http.method", r.Method),
					zap.String("http.path", r.URL.Path),
				)
				log.Error("panic recovered",
					zap.Any("panic", recovered),
					zap.Stack("stack"),
				)

				if ww.Written() {
					return
				}
				_ = httpx.WriteProblem(ww, r, httpx.InternalServerError("internal server error"))
			}()

			next.ServeHTTP(ww, r)
		})
	}
}
