package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"rapig/internal/platform/apperr"
)

type errorPayload struct {
	Code      apperr.Code `json:"code"`
	Message   string      `json:"message"`
	RequestID string      `json:"request_id"`
	Reason    string      `json:"reason,omitempty"`
}

type errorBody struct {
	Error errorPayload `json:"error"`
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	e, ok := apperr.From(err)
	if !ok {
		e = apperr.ErrInternal.WithCause(err)
	}
	status := e.HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if status >= 500 {
		s.log.Error("request failed",
			"err", err,
			"code", string(e.Code),
			"method", r.Method,
			"path", r.URL.Path,
			"request_id", requestID(r),
		)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorPayload{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: requestID(r),
		Reason:    e.Reason,
	}})
}

// writePlainError is used before a Server is available (panic recovery).
func writePlainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorPayload{
		Code:    apperr.CodeInternal,
		Message: message,
	}})
}

// decodeJSON decodes a bounded JSON body. Unknown fields are rejected so
// typos fail loudly instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.ErrBadRequest.WithCause(errors.New("empty body"))
		}
		return apperr.ErrBadRequest.WithCause(err)
	}
	if dec.More() {
		return apperr.ErrBadRequest.WithCause(errors.New("trailing data after JSON body"))
	}
	return nil
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("write json failed", "err", err)
	}
}
