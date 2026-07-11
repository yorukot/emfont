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
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/controller/logger"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type Handler struct {
	service         *appfont.Service
	routeMiddleware []func(http.Handler) http.Handler
}

type operation string

const (
	operationBuild operation = "build"
	operationList  operation = "list"
	operationInfo  operation = "info"
)

func NewHandler(service *appfont.Service, routeMiddleware ...func(http.Handler) http.Handler) *Handler {
	filtered := make([]func(http.Handler) http.Handler, 0, len(routeMiddleware))
	for _, candidate := range routeMiddleware {
		if candidate != nil {
			filtered = append(filtered, candidate)
		}
	}
	return &Handler{service: service, routeMiddleware: filtered}
}

func (h *Handler) RegisterRoutes(router chi.Router) {
	fonts := router.With(h.routeMiddleware...)
	fonts.Post("/g/{font}", h.generate)
	fonts.Get("/css/{font}", h.css)
	fonts.Get("/list", h.list)
	fonts.Get("/info/{fontID}", h.info)
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	if err := httpx.RequireJSONContentType(r); err != nil {
		_ = httpx.WriteProblem(w, r, httpx.NewError(http.StatusUnsupportedMediaType, "content type must be application/json"))
		return
	}
	var body generateRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			_ = httpx.WriteProblem(w, r, httpx.NewError(http.StatusRequestEntityTooLarge, "request body is too large"))
			return
		}
		_ = httpx.WriteProblem(w, r, httpx.BadRequest("invalid request body"))
		return
	}
	if h.service == nil {
		h.writeOperationError(w, r, operationBuild, appfont.ErrObjectStorageUnavailable)
		return
	}
	response, err := h.service.Generate(r.Context(), appfont.GenerateRequest{
		FontID: chi.URLParam(r, "font"), Words: body.Words, Min: bool(body.Min),
		Weight: string(body.Weight), Format: body.Format,
	})
	if err != nil {
		h.writeOperationError(w, r, operationBuild, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	_ = httpx.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) css(w http.ResponseWriter, r *http.Request) {
	min, err := parseOptionalBool(r.URL.Query()["min"])
	if err != nil {
		_ = httpx.WriteProblem(w, r, httpx.UnprocessableEntity("invalid font request", httpx.ErrorDetail{
			Code: httpx.FieldCodeInvalidValue, Message: "min must be a boolean", Location: "query.min", Value: r.URL.Query().Get("min"),
		}))
		return
	}
	if h.service == nil {
		h.writeOperationError(w, r, operationBuild, appfont.ErrObjectStorageUnavailable)
		return
	}
	fontID := strings.TrimSuffix(chi.URLParam(r, "font"), ".css")
	stylesheet, err := h.service.CSS(r.Context(), appfont.GenerateRequest{
		FontID: fontID,
		Words:  r.URL.Query().Get("words"),
		Min:    min,
		Weight: r.URL.Query().Get("weight"),
		Format: r.URL.Query().Get("format"),
	})
	if err != nil {
		h.writeOperationError(w, r, operationBuild, err)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(stylesheet))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			_ = httpx.WriteProblem(w, r, httpx.UnprocessableEntity("invalid font list request", httpx.ErrorDetail{
				Code: httpx.FieldCodeInvalidValue, Message: "limit must be an integer", Location: "query.limit", Value: rawLimit,
			}))
			return
		}
		limit = parsed
	}
	if h.service == nil {
		h.writeOperationError(w, r, operationList, appfont.ErrObjectStorageUnavailable)
		return
	}
	result, err := h.service.List(r.Context(), appfont.ListRequest{
		Search: r.URL.Query().Get("q"), Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.writeOperationError(w, r, operationList, err)
		return
	}
	if result.NextCursor != "" {
		w.Header().Set("X-Next-Cursor", result.NextCursor)
		nextURL := *r.URL
		query := nextURL.Query()
		query.Set("cursor", result.NextCursor)
		nextURL.RawQuery = query.Encode()
		w.Header().Set("Link", "<"+nextURL.String()+">; rel=\"next\"")
	}
	_ = httpx.WriteJSON(w, http.StatusOK, result.Items)
}

func (h *Handler) info(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		h.writeOperationError(w, r, operationInfo, appfont.ErrObjectStorageUnavailable)
		return
	}
	info, err := h.service.Info(r.Context(), chi.URLParam(r, "fontID"))
	if err != nil {
		h.writeOperationError(w, r, operationInfo, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, info)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	h.writeOperationError(w, r, operationBuild, err)
}

func (h *Handler) writeOperationError(w http.ResponseWriter, r *http.Request, operation operation, err error) {
	log := logger.FromContext(r.Context())
	fields := []zap.Field{
		zap.String("font_operation", string(operation)),
		zap.String("font_id", fontIDFromRequest(r)),
	}
	switch {
	case errors.Is(err, appfont.ErrFontSourceNotFound):
		log.Warn("font source unavailable", append(fields, zap.Error(err))...)
	case errors.Is(err, appfont.ErrObjectStorageUnavailable), errors.Is(err, appfont.ErrBuildFailed):
		log.Error("font operation failed", append(fields, zap.Error(err))...)
	case errors.Is(err, context.DeadlineExceeded):
		log.Warn("font operation timed out", append(fields, zap.Error(err))...)
	case errors.Is(err, context.Canceled):
		log.Info("font operation canceled", append(fields, zap.Error(err))...)
	case !errors.Is(err, appfont.ErrInvalidInput) &&
		!errors.Is(err, appfont.ErrFontNotFound) &&
		!errors.Is(err, appfont.ErrArtifactCapacity) &&
		!errors.Is(err, appfont.ErrBuildQueueFull) &&
		!errors.Is(err, appfont.ErrBuildNotReady) &&
		!errors.Is(err, appfont.ErrUnsupportedCodepoints) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded):
		log.Error("unexpected font operation failure", append(fields, zap.Error(err))...)
	}
	switch {
	case errors.Is(err, appfont.ErrInvalidInput):
		_ = httpx.WriteProblem(w, r, httpx.UnprocessableEntity("invalid font request", httpx.ErrorDetail{
			Code: httpx.FieldCodeInvalidValue, Message: err.Error(), Location: "request",
		}))
	case errors.Is(err, appfont.ErrFontNotFound):
		_ = httpx.WriteProblem(w, r, httpx.NotFoundCode(httpx.CodeFontNotFound, "font not found"))
	case errors.Is(err, appfont.ErrFontSourceNotFound):
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeFontSourceNotFound, "font source is unavailable"))
	case errors.Is(err, appfont.ErrUnsupportedCodepoints):
		_ = httpx.WriteProblem(w, r, httpx.UnprocessableEntityCode(
			httpx.CodeFontUnsupportedCodepoints,
			"the selected font does not support every requested character",
			httpx.ErrorDetail{Code: httpx.FieldCodeInvalidValue, Message: "one or more characters are unsupported", Location: "body.words"},
		))
	case errors.Is(err, appfont.ErrArtifactCapacity):
		w.Header().Set("Retry-After", retryAfterHeader(appfont.RetryAfterDuration(err)))
		_ = httpx.WriteProblem(w, r, httpx.NewErrorCode(http.StatusTooManyRequests, httpx.CodeFontArtifactCapacity, "font artifact capacity is temporarily unavailable"))
	case errors.Is(err, appfont.ErrBuildQueueFull):
		w.Header().Set("Retry-After", "1")
		_ = httpx.WriteProblem(w, r, httpx.NewErrorCode(http.StatusTooManyRequests, httpx.CodeFontBuildQueueFull, "font build capacity is temporarily unavailable"))
	case errors.Is(err, appfont.ErrBuildNotReady):
		w.Header().Set("Retry-After", retryAfterHeader(appfont.RetryAfterDuration(err)))
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeFontBuildNotReady, "font build is already in progress"))
	case errors.Is(err, context.DeadlineExceeded):
		_ = httpx.WriteProblem(w, r, httpx.NewErrorCode(http.StatusGatewayTimeout, httpx.CodeGatewayTimeout, "font operation timed out"))
	case errors.Is(err, context.Canceled):
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeServiceUnavailable, "font service is unavailable"))
	case errors.Is(err, appfont.ErrObjectStorageUnavailable):
		_ = httpx.WriteProblem(w, r, httpx.ServiceUnavailableCode(httpx.CodeObjectStorageUnavailable, "font object storage is unavailable"))
	case errors.Is(err, appfont.ErrBuildFailed):
		_ = httpx.WriteProblem(w, r, httpx.InternalServerErrorCode(httpx.CodeFontBuildFailed, "font build failed"))
	default:
		_ = httpx.WriteProblem(w, r, httpx.InternalServerError("font operation failed"))
	}
}

func fontIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if fontID := chi.URLParam(r, "font"); fontID != "" {
		return fontID
	}
	return chi.URLParam(r, "fontID")
}

func retryAfterHeader(delay time.Duration) string {
	seconds := delay / time.Second
	if delay%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(int64(seconds), 10)
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

func parseOptionalBool(values []string) (bool, error) {
	if values == nil {
		return false, nil
	}
	if len(values) != 1 {
		return false, errors.New("must have one value")
	}
	switch values[0] {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("must be a boolean")
	}
}
