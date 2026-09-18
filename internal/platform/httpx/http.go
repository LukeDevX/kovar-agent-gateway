package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
	Cause   error  `json:"-"`
}

func (e *Error) Error() string { return e.Code }
func (e *Error) Unwrap() error { return e.Cause }
func E(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, Status: status}
}
func Invalid(message string) *Error        { return E(400, "INVALID_REQUEST", message) }
func Conflict(code, message string) *Error { return E(409, code, message) }
func NotSupported(message string) *Error   { return E(501, "NOT_SUPPORTED", message) }
func Normalize(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return E(504, "REQUEST_TIMEOUT", "request timed out")
	}
	if errors.Is(err, context.Canceled) {
		return E(499, "REQUEST_CANCELLED", "request cancelled")
	}
	return &Error{Code: "INTERNAL_ERROR", Message: "internal server error", Status: 500, Cause: err}
}

type contextKey int

const requestKey contextKey = 0

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestKey, id)
}
func RequestID(ctx context.Context) string { s, _ := ctx.Value(requestKey).(string); return s }
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	e := Normalize(err)
	JSON(w, e.Status, map[string]any{"code": e.Code, "message": e.Message, "request_id": RequestID(r.Context())})
}
func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return Invalid("invalid JSON request")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return Invalid("request must contain one JSON value")
	}
	return nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func Identifier(s string) bool { return identifier.MatchString(s) }
func Page(r *http.Request) (int, int, error) {
	p, n := 1, 20
	var err error
	if x := r.URL.Query().Get("page"); x != "" {
		p, err = strconv.Atoi(x)
		if err != nil || p < 1 || p > 1000000 {
			return 0, 0, Invalid("invalid page")
		}
	}
	if x := r.URL.Query().Get("page_size"); x != "" {
		n, err = strconv.Atoi(x)
		if err != nil || n < 1 || n > 100 {
			return 0, 0, Invalid("page_size must be 1..100")
		}
	}
	return p, n, nil
}
