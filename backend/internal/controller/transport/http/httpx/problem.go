package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
)

const ContentTypeProblem = "application/problem+json; charset=utf-8"

const (
	CodeBadRequest         = "BAD_REQUEST"
	CodeUnauthorized       = "UNAUTHORIZED"
	CodeForbidden          = "FORBIDDEN"
	CodeNotFound           = "NOT_FOUND"
	CodeConflict           = "CONFLICT"
	CodeRateLimited        = "RATE_LIMITED"
	CodeValidationFailed   = "VALIDATION_FAILED"
	CodeInternalError      = "INTERNAL_ERROR"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	CodeMethodNotAllowed   = "METHOD_NOT_ALLOWED"
)

const (
	FieldCodeInvalidValue  = "INVALID_VALUE"
	FieldCodeInvalidFormat = "INVALID_FORMAT"
	FieldCodeInvalidEnum   = "INVALID_ENUM"
	FieldCodeRequired      = "REQUIRED"
	FieldCodeValueTooShort = "VALUE_TOO_SHORT"
	FieldCodeValueTooLong  = "VALUE_TOO_LONG"
	FieldCodeValueTooSmall = "VALUE_TOO_SMALL"
	FieldCodeValueTooLarge = "VALUE_TOO_LARGE"
)

const (
	CodeRouteNotFound            = "ROUTE_NOT_FOUND"
	CodeSystemNotFound           = "SYSTEM_NOT_FOUND"
	CodeSystemServiceUnavailable = "SYSTEM_SERVICE_UNAVAILABLE"
	CodeFontNotFound             = "FONT_NOT_FOUND"
	CodeFontWeightNotFound       = "FONT_WEIGHT_NOT_FOUND"
	CodeFontSourceNotFound       = "FONT_SOURCE_NOT_FOUND"
	CodeFontBuildNotReady        = "FONT_BUILD_NOT_READY"
	CodeFontBuildFailed          = "FONT_BUILD_FAILED"
	CodeArtifactNotFound         = "ARTIFACT_NOT_FOUND"
	CodeObjectStorageUnavailable = "OBJECT_STORAGE_UNAVAILABLE"
)

type Error struct {
	Status  int
	Code    string
	Detail  string
	Details []ErrorDetail
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Detail
}

type ErrorDetail struct {
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty"`
	Value    any    `json:"value,omitempty"`
}

type Problem struct {
	Type      string        `json:"type,omitempty"`
	Code      string        `json:"code,omitempty"`
	Title     string        `json:"title,omitempty"`
	Status    int           `json:"status,omitempty"`
	Detail    string        `json:"detail,omitempty"`
	Instance  string        `json:"instance,omitempty"`
	RequestID string        `json:"requestId,omitempty"`
	Errors    []ErrorDetail `json:"errors,omitempty"`
}

func (p Problem) Error() string {
	return p.Detail
}

func BadRequest(detail string) error {
	return NewError(http.StatusBadRequest, detail)
}

func BadRequestCode(code, detail string) error {
	return NewErrorCode(http.StatusBadRequest, code, detail)
}

func Unauthorized(detail string) error {
	return NewError(http.StatusUnauthorized, detail)
}

func UnauthorizedCode(code, detail string) error {
	return NewErrorCode(http.StatusUnauthorized, code, detail)
}

func Forbidden(detail string) error {
	return NewError(http.StatusForbidden, detail)
}

func ForbiddenCode(code, detail string) error {
	return NewErrorCode(http.StatusForbidden, code, detail)
}

func NotFound(detail string) error {
	return NewError(http.StatusNotFound, detail)
}

func NotFoundCode(code, detail string) error {
	return NewErrorCode(http.StatusNotFound, code, detail)
}

func Conflict(detail string) error {
	return NewError(http.StatusConflict, detail)
}

func ConflictCode(code, detail string) error {
	return NewErrorCode(http.StatusConflict, code, detail)
}

func UnprocessableEntity(detail string, details ...ErrorDetail) error {
	return UnprocessableEntityCode(CodeValidationFailed, detail, details...)
}

func UnprocessableEntityCode(code, detail string, details ...ErrorDetail) error {
	return &Error{
		Status:  http.StatusUnprocessableEntity,
		Code:    codeOrDefault(code, http.StatusUnprocessableEntity),
		Detail:  detail,
		Details: details,
	}
}

func InternalServerError(detail string) error {
	return NewError(http.StatusInternalServerError, detail)
}

func InternalServerErrorCode(code, detail string) error {
	return NewErrorCode(http.StatusInternalServerError, code, detail)
}

func ServiceUnavailable(detail string) error {
	return NewError(http.StatusServiceUnavailable, detail)
}

func ServiceUnavailableCode(code, detail string) error {
	return NewErrorCode(http.StatusServiceUnavailable, code, detail)
}

func NewError(status int, detail string) error {
	return NewErrorCode(status, "", detail)
}

func NewErrorCode(status int, code, detail string) error {
	return &Error{Status: status, Code: codeOrDefault(code, status), Detail: detail}
}

func NewProblem(status int, detail string) Problem {
	code := codeOrDefault("", status)
	return Problem{
		Code:   code,
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}
}

func codeOrDefault(code string, status int) string {
	if code != "" {
		return code
	}
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusMethodNotAllowed:
		return CodeMethodNotAllowed
	case http.StatusConflict:
		return CodeConflict
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusUnprocessableEntity:
		return CodeValidationFailed
	case http.StatusServiceUnavailable:
		return CodeServiceUnavailable
	default:
		if status >= http.StatusInternalServerError {
			return CodeInternalError
		}
		return CodeBadRequest
	}
}

func WriteProblem(w http.ResponseWriter, r *http.Request, err error) error {
	status := http.StatusInternalServerError
	code := CodeInternalError
	detail := http.StatusText(status)
	var details []ErrorDetail

	var httpErr *Error
	if errors.As(err, &httpErr) {
		status = httpErr.Status
		code = codeOrDefault(httpErr.Code, status)
		detail = httpErr.Detail
		details = httpErr.Details
	} else if problem, ok := err.(Problem); ok {
		status = problem.Status
		code = codeOrDefault(problem.Code, status)
		detail = problem.Detail
		details = problem.Errors
	}
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if detail == "" {
		detail = http.StatusText(status)
	}

	requestID := requestIDFromHeaders(w, r)
	if requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	w.Header().Set("Content-Type", ContentTypeProblem)
	w.WriteHeader(status)

	body := Problem{
		Code:      code,
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    detail,
		RequestID: requestID,
		Errors:    details,
	}
	return json.NewEncoder(w).Encode(body)
}

func WriteError(w http.ResponseWriter, r *http.Request, status int, detail string) error {
	return WriteProblem(w, r, NewError(status, detail))
}

func requestIDFromHeaders(w http.ResponseWriter, r *http.Request) string {
	if w != nil {
		if requestID := w.Header().Get("X-Request-ID"); requestID != "" {
			return requestID
		}
	}
	if r != nil {
		if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
			return requestID
		}
	}
	return ""
}
