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

const paygoSession = `{"id":3,"session_id":"737c3d75-f12b-4803-9636-a4b5abd6bc1e","status":"active"}`

func TestPaygoSessionCreateForwardsIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/user/axone/paygo/sessions" || r.Header.Get("New-Api-User") != "7" || r.Header.Get("Idempotency-Key") != "fixture-key" {
			t.Errorf("wrong paygo create request: %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		var in paygoSessionRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.WalletID != "fixture-wallet" || in.MaxAmount != "1.00" {
			t.Error("paygo create payload changed")
		}
		fmt.Fprint(w, `{"success":true,"data":`+paygoSession+`}`)
	}))
	defer s.Close()
	raw, err := New(s.URL, time.Second, nil).CreatePaygoSession(context.Background(), Credential{UserID: 7, Session: "fixture"}, "fixture-key", "fixture-wallet", "1.00")
	if err != nil || !strings.Contains(string(raw), `"session_id":"737c3d75-f12b-4803-9636-a4b5abd6bc1e"`) {
		t.Fatal("paygo create failed", err)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected upstream calls")
	}
}

func TestPaygoSessionListDetailClose(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("New-Api-User") != "7" {
			t.Error("wrong user credential")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/user/axone/paygo/sessions":
			fmt.Fprint(w, `{"success":true,"data":[`+paygoSession+`]}`)
		case r.Method == "GET" && r.URL.Path == "/api/user/axone/paygo/sessions/fixture-session":
			fmt.Fprint(w, `{"success":true,"data":`+paygoSession+`}`)
		case r.Method == "POST" && r.URL.Path == "/api/user/axone/paygo/sessions/fixture-session/close":
			if r.Header.Get("Idempotency-Key") != "fixture-close-key" {
				t.Error("close did not forward idempotency key")
			}
			fmt.Fprint(w, `{"success":true,"data":{"id":3,"session_id":"fixture-session","status":"closed"}}`)
		default:
			t.Error("unexpected route", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := New(s.URL, time.Second, nil)
	cred := Credential{UserID: 7, Session: "fixture"}
	ctx := context.Background()
	list, err := c.ListPaygoSessions(ctx, cred)
	if err != nil || !strings.Contains(string(list), `"session_id"`) {
		t.Fatal("list failed", err)
	}
	detail, err := c.GetPaygoSession(ctx, cred, "fixture-session")
	if err != nil || !strings.Contains(string(detail), `"status":"active"`) {
		t.Fatal("detail failed", err)
	}
	closed, err := c.ClosePaygoSession(ctx, cred, "fixture-session", "fixture-close-key")
	if err != nil || !strings.Contains(string(closed), `"status":"closed"`) {
		t.Fatal("close failed", err)
	}
}

func TestPaygoSessionValidation(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"success":true,"data":`+paygoSession+`}`)
	}))
	defer s.Close()
	c := New(s.URL, time.Second, nil)
	cred := Credential{UserID: 7, Session: "fixture"}
	ctx := context.Background()

	for _, raw := range []string{"", " ", "0", "0.00", "-1", "abc", "1.234.5"} {
		if _, err := c.CreatePaygoSession(ctx, cred, "key", "wallet", raw); err == nil || httpx.Normalize(err).Status != 400 {
			t.Fatalf("invalid max_amount accepted: %q", raw)
		}
	}
	if _, err := c.CreatePaygoSession(ctx, cred, "key", " ", "1.00"); err == nil || httpx.Normalize(err).Status != 400 {
		t.Fatal("blank wallet_id accepted")
	}
	for _, id := range []string{"", "a/b", "a b", "../etc"} {
		if _, err := c.GetPaygoSession(ctx, cred, id); err == nil || httpx.Normalize(err).Status != 400 {
			t.Fatalf("invalid session id accepted: %q", id)
		}
		if _, err := c.ClosePaygoSession(ctx, cred, id, "key"); err == nil || httpx.Normalize(err).Status != 400 {
			t.Fatalf("invalid session id accepted on close: %q", id)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached upstream")
	}
}

func TestPaygoSessionContractValidation(t *testing.T) {
	cred := Credential{UserID: 7, Session: "fixture"}
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		body string
		call func(*KovarManageClient) error
	}{
		{"create-missing", `{"success":true,"data":{}}`, func(c *KovarManageClient) error { _, e := c.CreatePaygoSession(ctx, cred, "k", "w", "1"); return e }},
		{"detail-missing", `{"success":true,"data":{"status":"active"}}`, func(c *KovarManageClient) error { _, e := c.GetPaygoSession(ctx, cred, "s"); return e }},
		{"list-object", `{"success":true,"data":{}}`, func(c *KovarManageClient) error { _, e := c.ListPaygoSessions(ctx, cred); return e }},
		{"close-missing", `{"success":true,"data":null}`, func(c *KovarManageClient) error { _, e := c.ClosePaygoSession(ctx, cred, "s", "k"); return e }},
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

func TestAxoneWallets(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/user/axone/wallets" || r.Header.Get("New-Api-User") != "7" {
			t.Error("wrong wallets request")
		}
		fmt.Fprint(w, `{"success":true,"data":{"total":1,"current":1,"list":[{"id":"fixture-wallet","currency":"USDC"}]}}`)
	}))
	defer s.Close()
	raw, err := New(s.URL, time.Second, nil).AxoneWallets(context.Background(), Credential{UserID: 7, Session: "fixture"})
	if err != nil || !strings.Contains(string(raw), `"id":"fixture-wallet"`) {
		t.Fatal("wallets failed", err)
	}
}

func TestAxoneWalletsContractValidation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"data":{}}`)
	}))
	defer s.Close()
	if _, err := New(s.URL, time.Second, nil).AxoneWallets(context.Background(), Credential{UserID: 7, Session: "fixture"}); err == nil || httpx.Normalize(err).Status != 502 {
		t.Fatal("malformed wallets accepted", err)
	}
}
