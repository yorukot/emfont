package system

import (
	"errors"
	"net/http"

	appsystem "github.com/emfont/emfont/backend/internal/controller/application/system"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"github.com/go-chi/chi/v5"
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

func (h *Handler) upsert(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeSystemServiceUnavailable, "system service unavailable"))
		return
	}

	var req upsertRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		_ = httpx.WriteProblem(w, r, httpx.BadRequest("invalid request body"))
		return
	}

	dto, err := h.service.Upsert(r.Context(), appsystem.UpsertRequest{
		ID:          req.ID,
		Name:        req.Name,
		Environment: req.Environment,
		Version:     req.Version,
		Revision:    req.Revision,
		Status:      req.Status,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, dto)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, appsystem.ErrInvalidInput):
		_ = httpx.WriteProblem(w, r, httpx.UnprocessableEntity("invalid system input"))
	case errors.Is(err, appsystem.ErrNotFound):
		_ = httpx.WriteProblem(w, r, httpx.NotFoundCode(httpx.CodeSystemNotFound, "system metadata not found"))
	default:
		_ = httpx.WriteProblem(w, r, httpx.InternalServerError("system operation failed"))
	}
}

type upsertRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	Status      string `json:"status"`
}
