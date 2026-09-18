package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"kovar-gateway/internal/platform/secure"
)

func TestManageMigrationDiscoveryHistoryAndOwnership(t *testing.T) {
	f := setup(t)
	var v map[string]any
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/approve", `{}`, "", false), &v)
	expectCode(t, f.request(t, "GET", "/api/v1/models", "", "", true), 409, "KOVAR_USER_NOT_BOUND")
	decode(t, f.request(t, "POST", "/api/v1/kovar/auth/login", `{"username":"fixture","password":"request-only-password"}`, "login", true), &v)
	// Discovery must work with a user binding before creating a model Key.
	decode(t, f.request(t, "GET", "/api/v1/models", "", "", true), &v)
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `{"id":"user-only-model"}`) || f.mock.userModels.Load() != 1 || f.mock.keyModels.Load() != 0 {
		t.Fatal("public discovery used the wrong API or changed its contract")
	}
	decode(t, f.request(t, "GET", "/api/v1/account/topups?page=2&page_size=1&keyword=a%26b&user_id=8", "", "", true), &v)
	raw, _ = json.Marshal(v)
	if v["total"] != float64(41) || v["page"] != float64(2) || !strings.Contains(string(raw), "page-2") || !strings.Contains(string(raw), `a\u0026b`) {
		t.Fatal("history was sliced twice or lost query/total")
	}
	expectCode(t, f.request(t, "GET", "/api/v1/account/topups?page_size=101", "", "", true), 400, "INVALID_REQUEST")
	expectCode(t, f.request(t, "GET", "/api/v1/account/topup/status?trade_no=%20", "", "", true), 400, "INVALID_REQUEST")
	expectCode(t, f.request(t, "GET", "/api/v1/account/topup/status?trade_no=foreign-order", "", "", true), 404, "KOVAR_NOT_FOUND")
	// These unsigned identity hints must never override the signed Agent binding.
	r, _ := http.NewRequest("GET", f.http.URL+"/api/v1/account/topup/status?trade_no=AXONE-7-test&user_id=8", nil)
	f.sign(r, nil)
	r.Header.Set("New-Api-User", "8")
	r.Header.Set("Authorization", "Bearer forged-management")
	r.Header.Set("Cookie", "session=forged-session")
	res, err := f.http.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	decode(t, res, &v)
	if v["status"] != "pending" || v["session"] != nil {
		t.Fatal("status mapping or credential redaction failed")
	}
	decode(t, f.request(t, "GET", "/api/v1/account/topup/axone/chains", "", "", true), &[]any{})
	decode(t, f.request(t, "GET", "/api/v1/account/topup/info", "", "", true), &v)
	decode(t, f.request(t, "GET", "/api/v1/pricing", "", "", true), &v)
	// A second authenticated user cannot read the first user's order.
	f.key, _ = crypto.GenerateKey()
	f.address, _ = secure.Address(crypto.PubkeyToAddress(f.key.PublicKey).Hex())
	f.register(t)
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/approve", `{}`, "", false), &v)
	decode(t, f.request(t, "POST", "/api/v1/kovar/auth/login", `{"username":"other","password":"request-only-password"}`, "login", true), &v)
	expectCode(t, f.request(t, "GET", "/api/v1/account/topup/status?trade_no=AXONE-7-test&user_id=7", "", "", true), 404, "KOVAR_NOT_FOUND")
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/suspend", `{}`, "", false), &v)
	expectCode(t, f.request(t, "GET", "/api/v1/account/topup/status?trade_no=AXONE-7-test", "", "", true), 403, "AGENT_SUSPENDED")
}

func TestAxoneGatewayIdempotencyAndQuota(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	before, err := f.gateway.Service.Binding.Account(context.Background(), f.address)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"provider":"axone","payload":{"amount":10,"currency":"USDC","chain_id":"fixture-chain","payment_wallet_address":"fixture-wallet"}}`
	for range 2 {
		res := f.request(t, "POST", "/api/v1/account/topup", body, "axone-order", true)
		raw, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || !strings.Contains(string(raw), `"status":"pending"`) || strings.Contains(string(raw), "access_token") || strings.Contains(string(raw), "do-not-return") {
			t.Fatal("Axone response failed validation/redaction")
		}
	}
	if f.mock.topups.Load() != 1 {
		t.Fatal("replay created a second payment order")
	}
	expectCode(t, f.request(t, "POST", "/api/v1/account/topup", strings.Replace(body, `"amount":10`, `"amount":11`, 1), "axone-order", true), 409, "IDEMPOTENCY_CONFLICT")
	after, err := f.gateway.Service.Binding.Account(context.Background(), f.address)
	if err != nil || before != after {
		t.Fatal("order creation must not credit quota", err)
	}
	f.mock.topupFailure.Store(503)
	for range 2 {
		expectCode(t, f.request(t, "POST", "/api/v1/account/topup", body, "uncertain-order", true), 502, "UPSTREAM_ERROR")
	}
	if f.mock.topups.Load() != 2 {
		t.Fatal("failed payment order was retried")
	}
	key, _, err := f.gateway.Service.Binding.Key(context.Background(), f.address)
	if err != nil || !strings.HasPrefix(key, "sk-fixture-") || strings.Contains(key, "*") {
		t.Fatal("dedicated key retrieval stored a masked key")
	}
}
