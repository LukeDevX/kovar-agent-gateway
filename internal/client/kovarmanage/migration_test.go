package kovarmanage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kovar-gateway/internal/platform/httpx"
)

func TestUserScopedReads(t *testing.T) {
	for _, cred := range []Credential{{UserID: 7, Session: "fixture-session"}, {UserID: 7, AccessToken: "fixture-management"}} {
		t.Run(fmt.Sprint(cred.Session != ""), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("New-Api-User") != "7" || r.Header.Get("Cookie") != cred.headers().Get("Cookie") || r.Header.Get("Authorization") != cred.headers().Get("Authorization") {
					t.Error("wrong user credential")
				}
				switch r.URL.Path {
				case "/api/user/models":
					fmt.Fprint(w, `{"success":true,"data":["exact-model/v2","second"]}`)
				case "/api/pricing":
					fmt.Fprint(w, `{"success":true,"data":{"rate":"0.001"}}`)
				case "/api/user/topup/info":
					fmt.Fprint(w, `{"success":true,"data":{"payment_methods":["axone","stripe"]}}`)
				case "/api/user/topup/self":
					if r.URL.Query().Get("p") != "2" || r.URL.Query().Get("page_size") != "1" || r.URL.Query().Get("keyword") != "a&b" || len(r.URL.Query()) != 3 {
						t.Error("history pagination/filter was not encoded")
					}
					fmt.Fprint(w, `{"success":true,"data":{"items":[{"money":9007199254740993.01}],"page":2,"page_size":1,"total":41}}`)
				case "/api/user/topup/status":
					if r.URL.Query().Get("trade_no") != "trade&other=1" || len(r.URL.Query()) != 1 {
						t.Error("unsafe status query")
					}
					fmt.Fprint(w, `{"success":true,"data":{"trade_no":"trade&other=1","status":"pending","money":9007199254740993.01}}`)
				case "/api/user/axone/chains":
					fmt.Fprint(w, `{"success":true,"data":[]}`)
				default:
					t.Error("unexpected upstream route", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer s.Close()
			c := New(s.URL, time.Second, nil)
			ctx := context.Background()
			ids, err := c.Models(ctx, cred)
			if err != nil || len(ids) != 2 || ids[0] != "exact-model/v2" {
				t.Fatal("user model mapping failed", err)
			}
			if _, err = c.Pricing(ctx, cred); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Read(ctx, cred, "topup_info"); err != nil {
				t.Fatal(err)
			}
			page, err := c.Topups(ctx, cred, 2, 1, "a&b")
			if err != nil || !strings.Contains(string(page), `"total":41`) || !strings.Contains(string(page), "9007199254740993.01") {
				t.Fatal("history lost total/precision", err)
			}
			if _, err = c.TopupStatus(ctx, cred, "trade&other=1"); err != nil {
				t.Fatal(err)
			}
			if _, err = c.AxoneChains(ctx, cred); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManageHTTPAndBusinessErrors(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   int
		code   string
	}{
		{400, "secret", 400, "KOVAR_INVALID_REQUEST"},
		{401, "secret", 401, "KOVAR_AUTH_FAILED"},
		{403, "secret", 403, "KOVAR_FORBIDDEN"},
		{404, "secret", 404, "KOVAR_NOT_FOUND"},
		{409, "secret", 409, "KOVAR_CONFLICT"},
		{429, "secret", 429, "UPSTREAM_RATE_LIMITED"},
		{500, "secret", 502, "UPSTREAM_ERROR"},
		{503, "secret", 502, "UPSTREAM_ERROR"},
		{200, `{"success":false,"message":"secret","data":{"access_token":"secret"}}`, 502, "KOVAR_REQUEST_REJECTED"},
		{200, `{"message":"error","data":"secret"}`, 502, "KOVAR_REQUEST_REJECTED"},
		{200, `{"success":true,"message":"error"}`, 502, "KOVAR_REQUEST_REJECTED"},
		{200, `{"success":"true"}`, 502, "KOVAR_CONTRACT_INCOMPLETE"},
		{200, `{"success":true,"message":{}}`, 502, "KOVAR_CONTRACT_INCOMPLETE"},
		{200, `{}`, 502, "KOVAR_CONTRACT_INCOMPLETE"},
		{200, `null`, 502, "KOVAR_CONTRACT_INCOMPLETE"},
		{200, `<html>secret</html>`, 502, "UPSTREAM_INVALID_RESPONSE"},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer s.Close()
			_, err := New(s.URL, time.Second, nil).Pricing(context.Background(), Credential{UserID: 7, Session: "fixture"})
			e := httpx.Normalize(err)
			if err == nil || e.Status != tt.want || e.Code != tt.code || strings.Contains(e.Message, "secret") {
				t.Fatalf("wrong sanitized error: %+v", e)
			}
		})
	}
}

func TestTopupStatusPreservesKovarState(t *testing.T) {
	for _, status := range []string{"pending", "success", "failed", "expired", "paid", "completed"} {
		t.Run(status, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"success":true,"data":{"trade_no":"order","status":%q}}`, status)
			}))
			defer s.Close()
			raw, err := New(s.URL, time.Second, nil).TopupStatus(context.Background(), Credential{UserID: 7, Session: "fixture"}, "order")
			if err != nil || !strings.Contains(string(raw), `"status":"`+status+`"`) {
				t.Fatal("state was inferred or changed", err)
			}
		})
	}
}

const axonePayload = `{"amount":9007199254740993,"currency":"USDC","chain_id":"fixture-chain","payment_wallet_address":"fixture-wallet"}`

func TestAxoneOrderValidationAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	var failure atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/user/axone/order" || r.Header.Get("New-Api-User") != "7" {
			t.Error("wrong Axone request")
		}
		var in axoneOrderRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Amount != 9007199254740993 || in.Currency != "USDC" || in.ChainID != "fixture-chain" || in.PaymentWalletAddress != "fixture-wallet" {
			t.Error("Axone payload changed or amount lost precision")
		}
		if failure.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"success":true,"data":{"trade_no":"order","status":"pending","address":"fixture-payment-address"}}`)
	}))
	defer s.Close()
	c := New(s.URL, time.Second, nil)
	cred := Credential{UserID: 7, AccessToken: "fixture"}
	for _, raw := range []string{`null`, `{}`, strings.Replace(axonePayload, "9007199254740993", "-1", 1), strings.Replace(axonePayload, "9007199254740993", "1.5", 1), strings.Replace(axonePayload, "9007199254740993", "9223372036854775808", 1), strings.Replace(axonePayload, `"USDC"`, `" "`, 1), strings.Replace(axonePayload, `"amount"`, `"user_id"`, 1), axonePayload + `{}`} {
		if _, err := c.Topup(context.Background(), cred, "axone", json.RawMessage(raw)); err == nil || httpx.Normalize(err).Status != 400 {
			t.Fatal("invalid Axone payload accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached payment provider")
	}
	raw, err := c.Topup(context.Background(), cred, "axone", json.RawMessage(axonePayload))
	if err != nil || !strings.Contains(string(raw), `"pending"`) {
		t.Fatal("order creation must remain pending", err)
	}
	failure.Store(true)
	if _, err = c.Topup(context.Background(), cred, "axone", json.RawMessage(axonePayload)); err == nil || calls.Load() != 2 {
		t.Fatal("payment POST was retried or failure ignored")
	}
}

func TestMalformedAccountResponses(t *testing.T) {
	cred := Credential{UserID: 7, Session: "fixture"}
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		body string
		call func(*KovarManageClient) error
	}{
		{"models-object", `{"success":true,"data":{}}`, func(c *KovarManageClient) error { _, e := c.Models(ctx, cred); return e }},
		{"models-missing", `{"success":true}`, func(c *KovarManageClient) error { _, e := c.Models(ctx, cred); return e }},
		{"models-empty-id", `{"success":true,"data":[""]}`, func(c *KovarManageClient) error { _, e := c.Models(ctx, cred); return e }},
		{"history-no-total", `{"success":true,"data":{"items":[]}}`, func(c *KovarManageClient) error { _, e := c.Topups(ctx, cred, 1, 20, ""); return e }},
		{"status-mismatch", `{"success":true,"data":{"trade_no":"other","status":"success"}}`, func(c *KovarManageClient) error { _, e := c.TopupStatus(ctx, cred, "order"); return e }},
		{"status-missing", `{"success":true,"data":{"trade_no":"order"}}`, func(c *KovarManageClient) error { _, e := c.TopupStatus(ctx, cred, "order"); return e }},
		{"order-failed", `{"success":true,"data":{"trade_no":"order","address":"address","status":"failed"}}`, func(c *KovarManageClient) error {
			_, e := c.Topup(ctx, cred, "axone", json.RawMessage(axonePayload))
			return e
		}},
		{"order-missing", `{"success":true,"data":{}}`, func(c *KovarManageClient) error {
			_, e := c.Topup(ctx, cred, "axone", json.RawMessage(axonePayload))
			return e
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tt.body) }))
			defer s.Close()
			if err := tt.call(New(s.URL, time.Second, nil)); err == nil || httpx.Normalize(err).Status != 502 {
				t.Fatal("malformed data accepted", err)
			}
		})
	}
}

func TestDedicatedKeyRetrievalAndReconciliation(t *testing.T) {
	for _, mode := range []string{"ok", "other-owner", "ambiguous", "truncated", "masked-key", "missing-key", "key-failed"} {
		t.Run(mode, func(t *testing.T) {
			var creates, keyCalls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("New-Api-User") != "7" || r.Header.Get("Authorization") != "Bearer management-fixture" {
					t.Error("model key retrieval lost management authentication")
				}
				switch r.URL.Path {
				case "/api/token/":
					creates.Add(1)
					fmt.Fprint(w, `{"success":true,"message":""}`)
				case "/api/token/search":
					if r.URL.Query().Get("keyword") != "agent-fixture" || r.URL.Query().Get("page_size") != "100" {
						t.Error("token discovery must use bounded exact-name search")
					}
					token := map[string]any{"id": 9, "user_id": 7, "name": "agent-fixture", "key": "mask****", "status": 1, "expired_time": 2000000000, "remain_quota": 100}
					if mode == "other-owner" {
						token["user_id"] = 8
					}
					items := []any{token}
					total := 1
					if mode == "ambiguous" {
						items = append(items, token)
						total = 2
					}
					if mode == "truncated" {
						total = 101
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"items": items, "total": total}})
				case "/api/token/9/key":
					keyCalls.Add(1)
					if r.Method != "POST" {
						t.Error("key endpoint method changed")
					}
					switch mode {
					case "masked-key":
						fmt.Fprint(w, `{"success":true,"data":{"key":"mask****"}}`)
					case "missing-key":
						fmt.Fprint(w, `{"success":true,"data":{}}`)
					case "key-failed":
						w.WriteHeader(503)
					default:
						fmt.Fprint(w, `{"success":true,"data":{"key":"fixture-full-key"}}`)
					}
				default:
					t.Error("unexpected token route", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer s.Close()
			token, err := New(s.URL, time.Second, nil).CreateToken(context.Background(), Credential{UserID: 7, AccessToken: "management-fixture"}, CreateTokenRequest{Name: "agent-fixture", RemainQuota: 100, ExpiredTime: 2000000000})
			if mode == "ok" {
				if err != nil || token.Key != "fixture-full-key" {
					t.Fatal("full key was not retrieved", err)
				}
			} else if err == nil {
				t.Fatal("unsafe token was accepted")
			}
			if creates.Load() != 1 || keyCalls.Load() > 1 {
				t.Fatal("token write/retrieval was retried")
			}
			if (mode == "other-owner" || mode == "ambiguous" || mode == "truncated") && keyCalls.Load() != 0 {
				t.Fatal("retrieved a key without proving unique ownership")
			}
		})
	}
}

func TestAccountInputBoundsBeforeUpstream(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid input reached upstream")
		w.WriteHeader(500)
	}))
	defer s.Close()
	c := New(s.URL, time.Second, nil)
	ctx := context.Background()
	cred := Credential{UserID: 7, Session: "fixture"}
	for _, trade := range []string{"", " ", strings.Repeat("x", 257), "x\ny"} {
		if _, err := c.TopupStatus(ctx, cred, trade); err == nil || httpx.Normalize(err).Status != 400 {
			t.Fatal("invalid trade_no accepted")
		}
	}
	for _, pair := range [][2]int{{0, 20}, {1000001, 20}, {1, 0}, {1, 101}} {
		if _, err := c.Topups(ctx, cred, pair[0], pair[1], ""); err == nil || httpx.Normalize(err).Status != 400 {
			t.Fatal("unbounded history query accepted")
		}
	}
}
