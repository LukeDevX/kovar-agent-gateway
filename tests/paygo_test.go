package tests

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestPaygoSessionLifecycle(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)

	var v map[string]any

	// Fetch Axone wallets to obtain a wallet_id.
	var wallets map[string]any
	decode(t, f.request(t, "GET", "/api/v1/account/axone/wallets", "", "", true), &wallets)
	rawWallets, _ := json.Marshal(wallets)
	if !strings.Contains(string(rawWallets), "fixture-wallet") {
		t.Fatal("wallets did not return fixture-wallet")
	}

	// Create
	res := f.request(t, "POST", "/api/v1/account/paygo/sessions", `{"wallet_id":"fixture-wallet","max_amount":"1.00"}`, "paygo-create", true)
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || !strings.Contains(string(raw), `"session_id":"fixture-session"`) || !strings.Contains(string(raw), `"status":"active"`) {
		t.Fatalf("paygo create failed: %d %s", res.StatusCode, raw)
	}
	if f.mock.paygo.Load() != 1 {
		t.Fatal("create did not reach upstream")
	}

	// Replay must not create a second session.
	decode(t, f.request(t, "POST", "/api/v1/account/paygo/sessions", `{"wallet_id":"fixture-wallet","max_amount":"1.00"}`, "paygo-create", true), &v)
	if f.mock.paygo.Load() != 1 || v["session_id"] != "fixture-session" {
		t.Fatal("replay created a second paygo session")
	}

	// Same key, different body conflicts.
	expectCode(t, f.request(t, "POST", "/api/v1/account/paygo/sessions", `{"wallet_id":"fixture-wallet","max_amount":"2.00"}`, "paygo-create", true), 409, "IDEMPOTENCY_CONFLICT")

	// List
	var list []map[string]any
	decode(t, f.request(t, "GET", "/api/v1/account/paygo/sessions", "", "", true), &list)
	if len(list) != 1 || list[0]["session_id"] != "fixture-session" {
		t.Fatal("list did not return the session")
	}

	// Detail (path id is the session id)
	decode(t, f.request(t, "GET", "/api/v1/account/paygo/sessions/fixture-session", "", "", true), &v)
	if v["session_id"] != "fixture-session" || v["status"] != "active" {
		t.Fatal("detail did not return the session")
	}

	// Close
	decode(t, f.request(t, "POST", "/api/v1/account/paygo/sessions/fixture-session/close", "", "paygo-close", true), &v)
	if v["status"] != "closed" {
		t.Fatal("close did not close the session")
	}
}

func TestPaygoSessionValidationAndAuth(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	expectCode(t, f.request(t, "POST", "/api/v1/account/paygo/sessions", `{"wallet_id":"","max_amount":"1.00"}`, "invalid-wallet", true), 400, "INVALID_REQUEST")
	expectCode(t, f.request(t, "POST", "/api/v1/account/paygo/sessions", `{"wallet_id":"fixture-wallet","max_amount":"-1"}`, "invalid-amount", true), 400, "INVALID_REQUEST")
	expectCode(t, f.request(t, "POST", "/api/v1/account/paygo/sessions/fixture-session/close", "", "", true), 400, "INVALID_REQUEST")
	if f.mock.paygo.Load() != 0 {
		t.Fatal("invalid input reached upstream")
	}

	// Missing Idempotency-Key is rejected at the gateway before any upstream call.
	expectCode(t, f.request(t, "POST", "/api/v1/account/paygo/sessions/fixture-session/close", "", "", true), 400, "INVALID_REQUEST")
}
