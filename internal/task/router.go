package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"kovar-gateway/internal/identity"
	"kovar-gateway/internal/platform/httpx"
)

type Rule struct {
	TaskType        string `json:"task_type"`
	Model           string `json:"model"`
	PricingPointer  string `json:"pricing_pointer"`
	QuotaMultiplier string `json:"quota_multiplier"`
	Unit            string `json:"unit"`
	MaxInputBytes   int64  `json:"max_input_bytes"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
}
type ModelRouter struct {
	Rules []Rule `json:"rules"`
}

func LoadRouter(path string) (*ModelRouter, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r ModelRouter
	if err = httpx.Decode(b, &r); err != nil {
		return nil, err
	}
	if err = r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
func (r *ModelRouter) Validate() error {
	seen := map[string]bool{}
	for _, v := range r.Rules {
		key := v.TaskType + ":" + v.Model
		if seen[key] {
			return errors.New("duplicate routing rule")
		}
		seen[key] = true
		switch v.TaskType {
		case "chat", "image", "video", "speech", "transcription", "embedding", "rerank":
		default:
			return errors.New("invalid routing task type")
		}
		if v.Model == "" || v.PricingPointer == "" || !strings.HasPrefix(v.PricingPointer, "/") || v.MaxInputBytes < 1 || v.MaxInputBytes > 32<<20 || v.MaxOutputTokens < 0 || v.MaxOutputTokens > 1000000 {
			return errors.New("invalid routing rule")
		}
		switch v.Unit {
		case "request", "token", "image", "second", "character":
		default:
			return errors.New("invalid pricing unit")
		}
		if !decimal.MatchString(v.QuotaMultiplier) {
			return errors.New("invalid quota multiplier")
		}
		n, ok := new(big.Rat).SetString(v.QuotaMultiplier)
		if !ok || n.Sign() <= 0 {
			return errors.New("invalid quota multiplier")
		}
	}
	return nil
}

type PricingService struct{}

// Quote uses deployment-verified JSON pointers, not invented upstream fields.
// Integers are Kovar quota units; rational arithmetic rounds estimates upward.
var decimal = regexp.MustCompile(`^-?[0-9]{1,32}(\.[0-9]{1,24})?([eE][+-]?[0-9]{1,2})?$`)

// normalizePricing rewrites the real Kovar /api/pricing data (an array of
// model entries like {"model_name":"deepseek-v4-pro","model_ratio":...}) into
// a model-name-keyed map, so routing rules can use a stable pointer such as
// /deepseek-v4-pro/model_ratio instead of a volatile array index. Map-shaped
// pricing (test fixtures) is returned unchanged.
func normalizePricing(pricing json.RawMessage) json.RawMessage {
	var entries []map[string]json.RawMessage
	if json.Unmarshal(pricing, &entries) != nil {
		return pricing
	}
	byName := make(map[string]json.RawMessage, len(entries))
	for _, e := range entries {
		raw, ok := e["model_name"]
		if !ok {
			continue
		}
		var name string
		if json.Unmarshal(raw, &name) != nil || name == "" {
			continue
		}
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		byName[name] = b
	}
	if len(byName) == 0 {
		return pricing
	}
	out, err := json.Marshal(byName)
	if err != nil {
		return pricing
	}
	return out
}

func (PricingService) Quote(pricing json.RawMessage, r Rule, payload map[string]any, size int64) (int64, error) {
	if size > r.MaxInputBytes {
		return 0, httpx.Invalid("request exceeds configured model input limit")
	}
	pricing = normalizePricing(pricing)
	var doc any
	d := json.NewDecoder(bytes.NewReader(pricing))
	d.UseNumber()
	if d.Decode(&doc) != nil {
		return 0, httpx.E(502, "PRICING_UNAVAILABLE", "invalid pricing response")
	}
	v := doc
	for _, part := range strings.Split(strings.TrimPrefix(r.PricingPointer, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch x := v.(type) {
		case map[string]any:
			v = x[part]
		case []any:
			i, e := strconv.Atoi(part)
			if e != nil || i < 0 || i >= len(x) {
				v = nil
			} else {
				v = x[i]
			}
		default:
			v = nil
		}
	}
	var price string
	switch x := v.(type) {
	case json.Number:
		price = string(x)
	case string:
		price = x
	default:
		return 0, httpx.E(503, "PRICING_NOT_CONFIGURED", "pricing pointer does not resolve to a documented deployment price")
	}
	if !decimal.MatchString(price) {
		return 0, httpx.E(502, "PRICING_UNAVAILABLE", "price must be a bounded decimal")
	}
	rate, ok := new(big.Rat).SetString(price)
	if !ok || rate.Sign() < 0 {
		return 0, httpx.E(502, "PRICING_UNAVAILABLE", "price must be a non-negative decimal")
	}
	factor, ok := new(big.Rat).SetString(r.QuotaMultiplier)
	if !ok || factor.Sign() <= 0 {
		return 0, httpx.E(503, "PRICING_NOT_CONFIGURED", "quota multiplier is not configured")
	}
	count := int64(1)
	if v, exists := payload["n"]; exists {
		var err error
		count, err = positiveInteger(v)
		if err != nil || count > 10 {
			return 0, httpx.Invalid("n must be 1..10")
		}
	}

	if params, ok := payload["parameters"].(map[string]any); ok {
		if n, exists := params["n"]; exists {
			c, e := positiveInteger(n)
			if e != nil || c > 10 {
				return 0, httpx.Invalid("n must be 1..10")
			}
			count = c
		}
	}
	output := int64(0)
	if r.TaskType == "chat" {
		if r.MaxOutputTokens <= 0 {
			return 0, httpx.E(503, "PRICING_NOT_CONFIGURED", "chat output cap required")
		}
		output = r.MaxOutputTokens
		if _, a := payload["max_tokens"]; a {
			if _, b := payload["max_completion_tokens"]; b {
				return 0, httpx.Invalid("specify only one output token limit")
			}
		}
		for _, k := range []string{"max_tokens", "max_completion_tokens"} {
			if v, exists := payload[k]; exists {
				n, e := positiveInteger(v)
				if e != nil || n > r.MaxOutputTokens {
					return 0, httpx.Invalid("output tokens exceed configured model cap")
				}
				output = n
			}
		}
	}
	units := new(big.Rat).SetInt64(count)
	switch r.Unit {
	case "token":
		if r.TaskType != "chat" && r.TaskType != "embedding" && r.TaskType != "rerank" {
			return 0, httpx.E(503, "PRICING_NOT_CONFIGURED", "token estimation only supports text tasks")
		}
		if r.TaskType == "chat" {
			if modalities, ok := payload["modalities"]; ok {
				b, _ := json.Marshal(modalities)
				if string(b) != `["text"]` {
					return 0, httpx.E(503, "PRICING_NOT_CONFIGURED", "multimodal chat requires a verified request estimate")
				}
			}
			if messages, ok := payload["messages"].([]any); ok {
				for _, m := range messages {
					message, ok := m.(map[string]any)
					if !ok {
						continue
					}
					if _, ok = message["content"].(string); !ok {
						return 0, httpx.E(503, "PRICING_NOT_CONFIGURED", "multimodal chat requires a verified request estimate")
					}
				}
			}
		}
		units.Mul(units, new(big.Rat).SetInt64(size+output))
	case "character":
		input, ok := payload["input"].(string)
		if !ok {
			return 0, httpx.Invalid("character pricing requires input text")
		}
		units.Mul(units, new(big.Rat).SetInt64(int64(utf8.RuneCountInString(input))))
	case "second":
		v, ok := payload["duration"]
		if !ok {
			return 0, httpx.Invalid("duration required for video estimate")
		}
		duration, ok := new(big.Rat).SetString(numberString(v))
		if !ok || duration.Sign() <= 0 || duration.Cmp(big.NewRat(3600, 1)) > 0 {
			return 0, httpx.Invalid("invalid video duration")
		}
		units.Mul(units, duration)
	case "image":
	case "request":
	default:
		return 0, httpx.E(503, "PRICING_NOT_CONFIGURED", "unsupported estimate unit")
	}
	cost := new(big.Rat).Mul(new(big.Rat).Mul(rate, factor), units)
	n := new(big.Int).Quo(cost.Num(), cost.Denom())
	if new(big.Int).Mod(cost.Num(), cost.Denom()).Sign() != 0 {
		n.Add(n, big.NewInt(1))
	}
	if !n.IsInt64() {
		return 0, httpx.Invalid("estimated cost overflows quota units")
	}
	return n.Int64(), nil
}
func numberString(v any) string {
	switch x := v.(type) {
	case json.Number:
		return string(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return x
	default:
		return ""
	}
}
func positiveInteger(v any) (int64, error) {
	n, err := strconv.ParseInt(numberString(v), 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid positive integer")
	}
	return n, nil
}
func BudgetGuard(p identity.Policy, estimate, account, token, today, month int64) error {
	if estimate < 0 || account < 0 || token < 0 || today < 0 || month < 0 {
		return httpx.E(502, "QUOTA_UNAVAILABLE", "invalid quota data")
	}
	switch {
	case estimate > account:
		return httpx.E(402, "INSUFFICIENT_ACCOUNT_QUOTA", "insufficient Kovar account quota")
	case estimate > token:
		return httpx.E(402, "INSUFFICIENT_AGENT_TOKEN_QUOTA", "insufficient agent token quota")
	case estimate > p.PerRequest:
		return httpx.E(402, "PER_REQUEST_LIMIT_EXCEEDED", "per request budget exceeded")
	case today > p.Daily || estimate > p.Daily-today:
		return httpx.E(402, "DAILY_LIMIT_EXCEEDED", "daily budget exceeded")
	case month > p.Monthly || estimate > p.Monthly-month:
		return httpx.E(402, "MONTHLY_LIMIT_EXCEEDED", "monthly budget exceeded")
	}
	return nil
}
