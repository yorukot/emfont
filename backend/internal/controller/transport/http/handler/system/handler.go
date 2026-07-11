package system

import (
	"context"
	"errors"
	"net/http"

	appsystem "github.com/emfont/emfont/backend/internal/controller/application/system"
	"github.com/emfont/emfont/backend/internal/controller/logger"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type Handler struct {
	service *appsystem.Service
}

func NewHandler(service *appsystem.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterRoutes(router chi.Router) {
	router.Get("/system", h.get)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeSystemServiceUnavailable, "system service unavailable"))
		return
	}

	dto, err := h.service.Get(r.Context(), appsystem.GetRequest{
		ID: r.URL.Query().Get("id"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, dto)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	log := logger.FromContext(r.Context())
	switch {
	case errors.Is(err, appsystem.ErrInvalidInput):
		_ = httpx.WriteProblem(w, r, httpx.UnprocessableEntity("invalid system input"))
	case errors.Is(err, appsystem.ErrNotFound):
		_ = httpx.WriteProblem(w, r, httpx.NotFoundCode(httpx.CodeSystemNotFound, "system metadata not found"))
	case errors.Is(err, context.DeadlineExceeded):
		log.Warn("system operation timed out", zap.Error(err))
		_ = httpx.WriteProblem(w, r, httpx.NewErrorCode(http.StatusGatewayTimeout, httpx.CodeGatewayTimeout, "system operation timed out"))
	case errors.Is(err, context.Canceled):
		log.Info("system operation canceled", zap.Error(err))
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeSystemServiceUnavailable, "system service unavailable"))
	default:
		log.Error("system operation failed", zap.Error(err))
		_ = httpx.WriteProblem(w, r, httpx.InternalServerError("system operation failed"))
	}
}
