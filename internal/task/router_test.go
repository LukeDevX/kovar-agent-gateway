package task

import (
	"encoding/json"
	"math"
	"testing"

	"kovar-gateway/internal/identity"
	"kovar-gateway/internal/platform/httpx"
)

func TestBudgetGuard(t *testing.T) {
	p := identity.Policy{PerRequest: 10, Daily: 20, Monthly: 100}
	cases := []struct {
		name          string
		e, a, k, d, m int64
		code          string
	}{{"valid", 1, 100, 100, 0, 0, ""}, {"account", 2, 1, 100, 0, 0, "INSUFFICIENT_ACCOUNT_QUOTA"}, {"token", 2, 100, 1, 0, 0, "INSUFFICIENT_AGENT_TOKEN_QUOTA"}, {"per request", 11, 100, 100, 0, 0, "PER_REQUEST_LIMIT_EXCEEDED"}, {"daily", 2, 100, 100, 19, 0, "DAILY_LIMIT_EXCEEDED"}, {"monthly", 2, 100, 100, 0, 99, "MONTHLY_LIMIT_EXCEEDED"}, {"overflow", 1, 100, 100, math.MaxInt64, 0, "DAILY_LIMIT_EXCEEDED"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := BudgetGuard(p, c.e, c.a, c.k, c.d, c.m)
			if c.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || httpx.Normalize(err).Code != c.code {
				t.Fatalf("wanted %s got %v", c.code, err)
			}
		})
	}
}
func TestPricingExactAndFailClosed(t *testing.T) {
	r := Rule{TaskType: "chat", Model: "fixture", PricingPointer: "/price", QuotaMultiplier: "1000", Unit: "request", MaxInputBytes: 1000, MaxOutputTokens: 128}
	n, err := (PricingService{}).Quote(json.RawMessage(`{"price":"0.0001"}`), r, map[string]any{}, 1)
	if err != nil || n != 1 {
		t.Fatalf("round up got %d %v", n, err)
	}
	for _, raw := range []string{`{}`, `{"price":"NaN"}`, `{"price":"-1"}`, `{"price":"10000000000000000000000"}`} {
		if _, err := (PricingService{}).Quote(json.RawMessage(raw), r, map[string]any{}, 1); err == nil {
			t.Fatal("invalid pricing accepted")
		}
	}
}
func TestUsageFromResponse(t *testing.T) {
	u, err := extractUsage([]byte(`{"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	if err != nil || u.Total != 8 || u.Prompt != 5 {
		t.Fatal(u, err)
	}
	if _, err = extractUsage([]byte(`{"usage":{"total_tokens":-1}}`)); err == nil {
		t.Fatal("negative usage accepted")
	}
}

func TestOutputLimitAppliesToFixedRequestPricing(t *testing.T) {
	r := Rule{TaskType: "chat", Model: "fixture", PricingPointer: "/price", QuotaMultiplier: "1", Unit: "request", MaxInputBytes: 1000, MaxOutputTokens: 128}
	for _, p := range []map[string]any{{"max_tokens": int64(1000000)}, {"max_tokens": int64(10), "max_completion_tokens": int64(20)}} {
		if _, err := (PricingService{}).Quote(json.RawMessage(`{"price":1}`), r, p, 100); err == nil {
			t.Fatal("uncapped generation admitted")
		}
	}
}
