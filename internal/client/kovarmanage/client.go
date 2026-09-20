// Package kovarmanage implements only allowlisted paths from new-kovar-manage-api.json.
package kovarmanage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/platform/upstream"
)

type KovarManageClient struct{ http *upstream.Client }

func New(base string, timeout time.Duration, log *slog.Logger) *KovarManageClient {
	return &KovarManageClient{upstream.New(base, timeout, log)}
}

type Credential struct {
	UserID      int64  `json:"user_id"`
	Session     string `json:"session,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
}

func (c Credential) headers() http.Header {
	h := http.Header{}
	if c.UserID > 0 {
		h.Set("New-Api-User", strconv.FormatInt(c.UserID, 10))
	}
	if c.Session != "" {
		h.Set("Cookie", (&http.Cookie{Name: "session", Value: c.Session}).String())
	}
	if c.AccessToken != "" {
		h.Set("Authorization", "Bearer "+c.AccessToken)
	}
	return h
}

type Envelope struct {
	Success *bool           `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}
type User struct {
	ID           int64  `json:"id"`
	Quota        *int64 `json:"quota"`
	UsedQuota    *int64 `json:"used_quota"`
	RequestCount *int64 `json:"request_count"`
}
type Token struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	Status      *int   `json:"status"`
	ExpiredTime *int64 `json:"expired_time"`
	RemainQuota *int64 `json:"remain_quota"`
	Unlimited   bool   `json:"unlimited_quota"`
}
type RegisterRequest struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	Email            string `json:"email,omitempty"`
	VerificationCode string `json:"verification_code,omitempty"`
	AffCode          string `json:"aff_code,omitempty"`
}
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type CreateTokenRequest struct {
	Name        string `json:"name"`
	RemainQuota int64  `json:"remain_quota"`
	ExpiredTime int64  `json:"expired_time"`
	Unlimited   bool   `json:"unlimited_quota"`
}

// request performs the HTTP round-trip and returns the decoded JSON body plus
// any session cookie. Envelope validation is left to callers because a few
// endpoints (token usage) use a different success field.
func (c *KovarManageClient) request(ctx context.Context, method, path string, cred Credential, in any) (json.RawMessage, string, error) {
	var b []byte
	var err error
	if in != nil {
		b, err = json.Marshal(in)
		if err != nil {
			return nil, "", err
		}
	}
	defer clear(b)
	resp, err := c.http.Do(ctx, method, path, "application/json", b, cred.headers())
	if err != nil {
		var status *upstream.HTTPError
		if errors.As(err, &status) {
			codes := map[int]string{400: "KOVAR_INVALID_REQUEST", 401: "KOVAR_AUTH_FAILED", 403: "KOVAR_FORBIDDEN", 404: "KOVAR_NOT_FOUND", 409: "KOVAR_CONFLICT", 429: "UPSTREAM_RATE_LIMITED"}
			if code, ok := codes[status.StatusCode]; ok {
				return nil, "", httpx.E(status.StatusCode, code, "Kovar rejected the request")
			}
		}
		return nil, "", err
	}
	session := ""
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "session" && len(cookie.Value) <= 8192 {
			session = cookie.Value
		}
	}
	raw, err := upstream.JSON(resp)
	if err != nil {
		return nil, "", err
	}
	return raw, session, nil
}

func (c *KovarManageClient) call(ctx context.Context, method, path string, cred Credential, in any) (Envelope, string, error) {
	raw, session, err := c.request(ctx, method, path, cred, in)
	if err != nil {
		return Envelope{}, "", err
	}
	var e Envelope
	if json.Unmarshal(raw, &e) != nil {
		return Envelope{}, "", httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "invalid Kovar management response")
	}
	// Some payment controllers return message:error without a success field.
	// Never return the upstream message: it can contain credentials/provider errors.
	if (e.Success != nil && !*e.Success) || e.Message == "error" {
		if strings.HasPrefix(path, "/api/user/topup/status?") && e.Message == "topup order not found" {
			return Envelope{}, "", httpx.E(404, "KOVAR_NOT_FOUND", "topup order not found")
		}
		return Envelope{}, "", httpx.E(502, "KOVAR_REQUEST_REJECTED", "Kovar management request was rejected")
	}
	if e.Success == nil {
		return e, "", httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Kovar management response lacks ApiResponse.success")
	}
	return e, session, nil
}
func (c *KovarManageClient) Register(ctx context.Context, in RegisterRequest) error {
	_, _, err := c.call(ctx, "POST", "/api/user/register", Credential{}, in)
	return err
}
func (c *KovarManageClient) Login(ctx context.Context, in LoginRequest) (User, Credential, error) {
	e, session, err := c.call(ctx, "POST", "/api/user/login", Credential{}, in)
	if err != nil {
		return User{}, Credential{}, err
	}
	var u User
	if len(e.Data) > 0 && string(e.Data) != "null" {
		if err = json.Unmarshal(e.Data, &u); err != nil {
			return u, Credential{}, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "login data does not match documented User schema")
		}
	}
	return u, Credential{UserID: u.ID, Session: session}, nil
}
func (c *KovarManageClient) TwoFA(ctx context.Context, cred Credential, code string) (User, Credential, error) {
	e, session, err := c.call(ctx, "POST", "/api/user/login/2fa", cred, map[string]string{"code": code})
	if err != nil {
		return User{}, Credential{}, err
	}
	var u User
	if json.Unmarshal(e.Data, &u) != nil || u.ID <= 0 {
		return u, Credential{}, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "2FA response lacks documented User.id")
	}
	cred.UserID = u.ID
	if session != "" {
		cred.Session = session
	}
	return u, cred, nil
}
func (c *KovarManageClient) Self(ctx context.Context, cred Credential) (User, error) {
	e, _, err := c.call(ctx, "GET", "/api/user/self", cred, nil)
	if err != nil {
		return User{}, err
	}
	var u User
	if json.Unmarshal(e.Data, &u) != nil || u.ID <= 0 || u.ID != cred.UserID {
		return u, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "self response lacks matching User.id")
	}
	return u, nil
}
func (c *KovarManageClient) CreateToken(ctx context.Context, cred Credential, in CreateTokenRequest) (Token, error) {
	e, _, err := c.call(ctx, "POST", "/api/token/", cred, in)
	if err != nil {
		return Token{}, err
	}
	var t Token
	if len(e.Data) > 0 && string(e.Data) != "null" {
		_ = json.Unmarshal(e.Data, &t)
	}
	// ApiResponse.data is unspecified for token creation. Only the documented
	// Token/PageInfo shapes are accepted; missing key retrieval stays explicit.
	if t.ID == 0 || t.Key == "" || strings.Contains(t.Key, "*") {
		list, e := c.SearchTokens(ctx, cred, in.Name)
		if e != nil {
			return t, e
		}
		matches := []Token{}
		for _, v := range list {
			if v.Name == in.Name {
				matches = append(matches, v)
			}
		}
		if len(matches) != 1 {
			return t, httpx.E(502, "KOVAR_TOKEN_RECONCILIATION_REQUIRED", "created token could not be identified unambiguously")
		}
		t = matches[0]
	}
	if t.ID <= 0 || t.UserID != cred.UserID || t.Name != in.Name || t.Status == nil || t.ExpiredTime == nil || t.RemainQuota == nil {
		return t, httpx.E(502, "KOVAR_TOKEN_RECONCILIATION_REQUIRED", "token response lacks documented Token fields or retrievable key")
	}
	// Search/get return masked keys in the new API. Fetch only the token just
	// identified for this agent, never an arbitrary caller-supplied token ID.
	e, _, err = c.call(ctx, "POST", fmt.Sprintf("/api/token/%d/key", t.ID), cred, nil)
	if err != nil {
		return Token{}, err
	}
	var secret struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(e.Data, &secret) != nil || strings.TrimSpace(secret.Key) == "" || strings.ContainsAny(secret.Key, "* \t\r\n") {
		return Token{}, httpx.E(502, "KOVAR_TOKEN_RECONCILIATION_REQUIRED", "full token key is unavailable")
	}
	t.Key = secret.Key
	return t, nil
}
func (c *KovarManageClient) SearchTokens(ctx context.Context, cred Credential, name string) ([]Token, error) {
	e, _, err := c.call(ctx, "GET", "/api/token/search?p=1&page_size=100&keyword="+url.QueryEscape(name), cred, nil)
	if err != nil {
		return nil, err
	}
	var out []Token
	if json.Unmarshal(e.Data, &out) == nil {
		return out, nil
	}
	var page struct {
		Items []Token `json:"items"`
		Total *int64  `json:"total"`
	}
	if json.Unmarshal(e.Data, &page) != nil || page.Items == nil {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "token list does not match Token array or PageInfo.items")
	}
	if page.Total == nil || *page.Total != int64(len(page.Items)) {
		return nil, httpx.E(502, "KOVAR_TOKEN_RECONCILIATION_REQUIRED", "token search is incomplete; creation will not be retried")
	}
	return page.Items, nil
}
func (c *KovarManageClient) Token(ctx context.Context, cred Credential, id int64) (Token, error) {
	e, _, err := c.call(ctx, "GET", fmt.Sprintf("/api/token/%d", id), cred, nil)
	if err != nil {
		return Token{}, err
	}
	var t Token
	if json.Unmarshal(e.Data, &t) != nil || t.ID != id || t.UserID != cred.UserID {
		return t, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "token ownership response invalid")
	}
	return t, nil
}
func (c *KovarManageClient) DeleteToken(ctx context.Context, cred Credential, id int64) error {
	_, _, err := c.call(ctx, "DELETE", fmt.Sprintf("/api/token/%d", id), cred, nil)
	return err
}
func (c *KovarManageClient) Usage(ctx context.Context, key string) (json.RawMessage, error) {
	raw, _, err := c.request(ctx, "GET", "/api/usage/token/", Credential{AccessToken: key}, nil)
	if err != nil {
		return nil, err
	}
	// GetTokenUsage is inconsistent with the rest of the management API: its
	// success response uses "code" instead of "success", while its error path
	// (ApiErrorI18n) still returns "success":false. Accept both here.
	var e struct {
		Success *bool           `json:"success"`
		Code    *bool           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "invalid Kovar usage response")
	}
	if e.Success != nil && !*e.Success {
		return nil, httpx.E(502, "KOVAR_REQUEST_REJECTED", "Kovar usage request was rejected")
	}
	if e.Code == nil {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Kovar usage response lacks code")
	}
	if !*e.Code {
		return nil, httpx.E(502, "KOVAR_REQUEST_REJECTED", "Kovar usage request was rejected")
	}
	return e.Data, nil
}
func (c *KovarManageClient) Pricing(ctx context.Context, cred Credential) (json.RawMessage, error) {
	e, _, err := c.call(ctx, "GET", "/api/pricing", cred, nil)
	return e.Data, err
}
func (c *KovarManageClient) Ratios(ctx context.Context) (json.RawMessage, error) {
	e, _, err := c.call(ctx, "GET", "/api/ratio_config", Credential{}, nil)
	return e.Data, err
}
func (c *KovarManageClient) Read(ctx context.Context, cred Credential, operation string) (json.RawMessage, error) {
	paths := map[string]string{"data": "/api/data/self", "models": "/api/user/models", "topup_info": "/api/user/topup/info", "topups": "/api/user/topup/self", "logs": "/api/log/self", "logs_stat": "/api/log/self/stat", "tasks": "/api/task/self"}
	p, ok := paths[operation]
	if !ok {
		return nil, httpx.NotSupported("unsupported management operation")
	}
	e, _, err := c.call(ctx, "GET", p, cred, nil)
	return e.Data, err
}

func (c *KovarManageClient) Topup(ctx context.Context, cred Credential, provider string, payload json.RawMessage) (json.RawMessage, error) {
	switch provider {
	case "axone":
		return c.axoneOrder(ctx, cred, payload)
	case "epay", "stripe", "creem":
		return nil, httpx.NotSupported("payment provider adapter is not enabled")
	default:
		return nil, httpx.NotSupported("payment provider is not documented")
	}
}
