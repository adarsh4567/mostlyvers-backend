package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

type Problem struct {
	Error ErrorBody `json:"error"`
}
type ErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId"`
	Details   map[string]any `json:"details,omitempty"`
}
type APIError struct {
	Status        int
	Code, Message string
	Details       map[string]any
}

func (e *APIError) Error() string { return e.Message }
func NewError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func Decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		WriteError(w, r, &APIError{Status: 422, Code: "VALIDATION_ERROR", Message: "The request body is invalid."})
		return false
	}
	return true
}
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = &APIError{Status: 500, Code: "INTERNAL_ERROR", Message: "The request could not be completed."}
	}
	requestID, _ := r.Context().Value(RequestIDKey).(string)
	attributes := []any{"requestId", requestID, "method", r.Method, "path", r.URL.Path, "status", apiErr.Status, "code", apiErr.Code}
	if apiErr.Code == "INTERNAL_ERROR" {
		attributes = append(attributes, "cause", err)
	}
	slog.Warn("api request failed", attributes...)
	JSON(w, apiErr.Status, Problem{Error: ErrorBody{Code: apiErr.Code, Message: apiErr.Message, RequestID: requestID, Details: apiErr.Details}})
}

type contextKey string

const RequestIDKey contextKey = "request-id"
const PrincipalKey contextKey = "principal"

func Bearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}
