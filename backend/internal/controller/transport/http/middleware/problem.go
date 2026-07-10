package middleware

import (
	"net/http"

	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
)

func WriteProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	_ = httpx.WriteError(w, r, status, detail)
}

func WriteProblemCode(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	_ = httpx.WriteProblem(w, r, httpx.NewErrorCode(status, code, detail))
}
