package font

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	service *appfont.Service
}

func NewHandler(service *appfont.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterRoutes(router chi.Router) {
	router.Post("/g/{font}", h.generate)
	router.Get("/css/{font}", h.css)
	router.Get("/list", h.list)
	router.Get("/info/{fontID}", h.info)
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.writeError(w, r, appfont.ErrObjectStorageUnavailable)
		return
	}
	var body generateRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		_ = httpx.WriteProblem(w, r, httpx.BadRequest("invalid request body"))
		return
	}
	response, err := h.service.Generate(r.Context(), appfont.GenerateRequest{
		FontID: chi.URLParam(r, "font"), Words: body.Words, Min: bool(body.Min),
		Weight: string(body.Weight), Format: body.Format,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) css(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.writeError(w, r, appfont.ErrObjectStorageUnavailable)
		return
	}
	fontID := strings.TrimSuffix(chi.URLParam(r, "font"), ".css")
	stylesheet, err := h.service.CSS(r.Context(), appfont.GenerateRequest{
		FontID: fontID,
		Words:  r.URL.Query().Get("words"),
		Min:    parseBool(r.URL.Query().Get("min")),
		Weight: r.URL.Query().Get("weight"),
		Format: r.URL.Query().Get("format"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(stylesheet))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.writeError(w, r, appfont.ErrObjectStorageUnavailable)
		return
	}
	fonts, err := h.service.List(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, fonts)
}

func (h *Handler) info(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.writeError(w, r, appfont.ErrObjectStorageUnavailable)
		return
	}
	info, err := h.service.Info(r.Context(), chi.URLParam(r, "fontID"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, info)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, appfont.ErrInvalidInput):
		_ = httpx.WriteProblem(w, r, httpx.UnprocessableEntity("invalid font request", httpx.ErrorDetail{
			Code: httpx.FieldCodeInvalidValue, Message: err.Error(), Location: "request",
		}))
	case errors.Is(err, appfont.ErrFontNotFound):
		_ = httpx.WriteProblem(w, r, httpx.NotFoundCode(httpx.CodeFontNotFound, "font not found"))
	case errors.Is(err, appfont.ErrFontSourceNotFound):
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeFontSourceNotFound, "font source is unavailable"))
	case errors.Is(err, appfont.ErrBuildNotReady), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		w.Header().Set("Retry-After", "1")
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeFontBuildNotReady, "font build is already in progress"))
	case errors.Is(err, appfont.ErrObjectStorageUnavailable):
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeObjectStorageUnavailable, "font object storage is unavailable"))
	case errors.Is(err, appfont.ErrBuildFailed):
		_ = httpx.WriteProblem(w, r, httpx.InternalServerErrorCode(httpx.CodeFontBuildFailed, "font build failed"))
	default:
		_ = httpx.WriteProblem(w, r, httpx.InternalServerError("font operation failed"))
	}
}

type generateRequest struct {
	Words  string         `json:"words"`
	Min    flexibleBool   `json:"min"`
	Weight flexibleString `json:"weight"`
	Format string         `json:"format"`
}

type flexibleString string

func (value *flexibleString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || len(data) == 0 {
		*value = ""
		return nil
	}
	if data[0] == '"' {
		var decoded string
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		*value = flexibleString(decoded)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return fmt.Errorf("must be a string or number: %w", err)
	}
	*value = flexibleString(number.String())
	return nil
}

type flexibleBool bool

func (value *flexibleBool) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || len(data) == 0 {
		*value = false
		return nil
	}
	if data[0] == '"' {
		var decoded string
		if err := json.Unmarshal(data, &decoded); err != nil {
			return err
		}
		parsed, err := strconv.ParseBool(decoded)
		if err != nil {
			return err
		}
		*value = flexibleBool(parsed)
		return nil
	}
	var decoded bool
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*value = flexibleBool(decoded)
	return nil
}

func parseBool(value string) bool {
	parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
	return parsed
}
