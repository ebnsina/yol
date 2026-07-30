package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

const maxRequestBytes = 1 << 20

// errorBody is the wire shape of a failure. It carries no technical detail by design.
type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      Code              `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
}

// JSON writes a success response.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write response body", "error", err)
	}
}

// NoContent writes a successful empty response.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// Fail writes the user-facing error and logs the technical cause separately.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	e := AsError(err)
	reqID := RequestID(r.Context())

	logAt := slog.LevelWarn
	if e.Status >= http.StatusInternalServerError {
		logAt = slog.LevelError
	}
	slog.Log(r.Context(), logAt, "request failed",
		"code", e.Code,
		"status", e.Status,
		"method", r.Method,
		"path", r.URL.Path,
		"requestId", reqID,
		"error", e.Error(),
	)

	JSON(w, e.Status, errorBody{Error: errorPayload{
		Code:      e.Code,
		Message:   e.Message,
		Fields:    e.Fields,
		RequestID: reqID,
	}})
}

// Decode reads a JSON body, translating parse failures into plain-language errors.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if dec.More() {
		return InvalidInput("We could not read that request. Please try again.")
	}
	return nil
}

func decodeError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return InvalidInput("Some details were not in the expected format.").
			WithField(typeErr.Field, "This value is not valid.").
			WithCause(err)
	}
	if errors.Is(err, io.EOF) {
		return InvalidInput("Please fill in the form and try again.").WithCause(err)
	}
	return InvalidInput("We could not read that request. Please try again.").WithCause(err)
}
