// Package kovarmanage implements only management paths from kovar-manage-api.json.
package kovarmanage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
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

func (c *KovarManageClient) call(ctx context.Context, method, path string, cred Credential, in any) (Envelope, string, error) {
	var b []byte
	var err error
	if in != nil {
		b, err = json.Marshal(in)
		if err != nil {
			return Envelope{}, "", err
		}
	}
	defer clear(b)
	resp, err := c.http.Do(ctx, method, path, "application/json", b, cred.headers())
	if err != nil {
		return Envelope{}, "", err
	}
	session := ""
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "session" && len(cookie.Value) <= 8192 {
			session = cookie.Value
		}
	}
	raw, err := upstream.JSON(resp)
	if err != nil {
		return Envelope{}, "", err
	}
	var e Envelope
	if json.Unmarshal(raw, &e) != nil || e.Success == nil {
		return e, "", httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Kovar management response lacks ApiResponse.success")
	}
	if !*e.Success {
		return e, "", httpx.E(502, "KOVAR_REQUEST_REJECTED", "Kovar management request was rejected")
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
	if t.ID == 0 || t.Key == "" {
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
		if t.Key == "" {
			t, err = c.Token(ctx, cred, t.ID)
			if err != nil {
				return t, err
			}
		}
	}
	if t.ID <= 0 || t.UserID != cred.UserID || t.Key == "" || t.Status == nil || t.ExpiredTime == nil || t.RemainQuota == nil {
		return t, httpx.E(502, "KOVAR_TOKEN_RECONCILIATION_REQUIRED", "token response lacks documented Token fields or retrievable key")
	}
	return t, nil
}
func (c *KovarManageClient) SearchTokens(ctx context.Context, cred Credential, name string) ([]Token, error) {
	e, _, err := c.call(ctx, "GET", "/api/token/search?keyword="+url.QueryEscape(name), cred, nil)
	if err != nil {
		return nil, err
	}
	var out []Token
	if json.Unmarshal(e.Data, &out) == nil {
		return out, nil
	}
	var page struct {
		Items []Token `json:"items"`
	}
	if json.Unmarshal(e.Data, &page) != nil || page.Items == nil {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "token list does not match Token array or PageInfo.items")
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
	e, _, err := c.call(ctx, "GET", "/api/usage/token/", Credential{AccessToken: key}, nil)
	return e.Data, err
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

// Payment paths are present, but none of their request/response fields are
// specified in the authoritative management document. Never guess a charge.
func (c *KovarManageClient) Topup(_ context.Context, _ Credential, provider string, _ json.RawMessage) (json.RawMessage, error) {
	switch provider {
	case "epay", "stripe", "creem":
		return nil, httpx.NotSupported("management API documents the payment path but omits its request and response schema")
	default:
		return nil, httpx.NotSupported("payment provider is not documented")
	}
}
