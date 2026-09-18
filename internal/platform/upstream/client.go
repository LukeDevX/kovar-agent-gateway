package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kovar-gateway/internal/platform/httpx"
)

const MaxResponse = 32 << 20

type Client struct {
	base string
	http *http.Client
	log  *slog.Logger
}

func New(base string, timeout time.Duration, log *slog.Logger) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.ResponseHeaderTimeout = timeout
	return &Client{strings.TrimRight(base, "/"), &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, log}
}
func (c *Client) Do(ctx context.Context, method, path, contentType string, body []byte, headers http.Header) (*http.Response, error) {
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("construct upstream request: %w", err)
		}
		req.Header = headers.Clone()
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("X-Request-Id", httpx.RequestID(ctx))
		start := time.Now()
		resp, err := c.http.Do(req)
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		if c.log != nil {
			safePath, _ := url.Parse(path)
			c.log.DebugContext(ctx, "upstream request", "method", method, "path", safePath.Path, "status", status, "duration_ms", time.Since(start).Milliseconds(), "request_id", httpx.RequestID(ctx))
		}
		retry := method == http.MethodGet && attempt < 2 && (err != nil || status == 429 || status == 502 || status == 503 || status == 504)
		if retry && ctx.Err() == nil {
			if resp != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
			}
			d := time.Duration(100*(1<<attempt)+rand.IntN(75)) * time.Millisecond
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var ne interface{ Timeout() bool }
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, &httpx.Error{Status: 504, Code: "UPSTREAM_TIMEOUT", Message: "Kovar request timed out", Cause: err}
			}
			return nil, &httpx.Error{Status: 502, Code: "UPSTREAM_UNAVAILABLE", Message: "Kovar is unavailable", Cause: err}
		}
		if status < 200 || status >= 300 {
			resp.Body.Close()
			code := "UPSTREAM_ERROR"
			out := 502
			if status == 401 || status == 403 {
				code = "KOVAR_AUTH_FAILED"
			}
			if status == 429 {
				code = "UPSTREAM_RATE_LIMITED"
				out = 429
			}
			return nil, httpx.E(out, code, "Kovar rejected the request")
		}
		return resp, nil
	}
	return nil, httpx.E(502, "UPSTREAM_UNAVAILABLE", "Kovar is unavailable")
}
func Read(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxResponse {
		return nil, httpx.E(502, "UPSTREAM_RESPONSE_TOO_LARGE", "Kovar response exceeded the limit")
	}
	return b, nil
}
func JSON(resp *http.Response) (json.RawMessage, error) {
	b, err := Read(resp)
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "Kovar returned invalid JSON")
	}
	return b, nil
}
