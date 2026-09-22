package kovarmanage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"kovar-gateway/internal/platform/httpx"
)

// ApiResponse.data is unspecified in OpenAPI. These minimal checks follow
// GetUserModels/GetUserTopUps/GetTopUpStatus/RequestAxoneAddress in the upstream
// checkout recorded in docs/manage-api-migration.md. Other fields remain raw.
func (c *KovarManageClient) Models(ctx context.Context, cred Credential) ([]string, error) {
	e, _, err := c.call(ctx, "GET", "/api/user/models", cred, nil)
	if err != nil {
		return nil, err
	}
	var ids []string
	if len(e.Data) == 0 || json.Unmarshal(e.Data, &ids) != nil {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "user models must be a string array")
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "user model ID is empty")
		}
	}
	return ids, nil
}

func (c *KovarManageClient) Topups(ctx context.Context, cred Credential, page, size int, keyword string) (json.RawMessage, error) {
	if page < 1 || page > 1000000 || size < 1 || size > 100 || len(keyword) > 256 || strings.ContainsFunc(keyword, unicode.IsControl) {
		return nil, httpx.Invalid("invalid topup history query")
	}
	q := url.Values{"p": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(size)}}
	if keyword != "" {
		q.Set("keyword", keyword)
	}
	e, _, err := c.call(ctx, "GET", "/api/user/topup/self?"+q.Encode(), cred, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []json.RawMessage `json:"items"`
		Total *int64            `json:"total"`
	}
	if json.Unmarshal(e.Data, &result) != nil || result.Items == nil || result.Total == nil || *result.Total < int64(len(result.Items)) || len(result.Items) > size {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "topup history lacks bounded PageInfo.items/total")
	}
	// The upstream already applied pagination. Keep the Gateway's page field
	// names and the upstream total; slicing again would erase later pages.
	return json.Marshal(map[string]any{"items": result.Items, "total": *result.Total, "page": page, "page_size": size})
}

func (c *KovarManageClient) TopupStatus(ctx context.Context, cred Credential, tradeNo string) (json.RawMessage, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if !boundedText(tradeNo, 256) {
		return nil, httpx.Invalid("trade_no is required and must not exceed 256 bytes")
	}
	// GetTopUpStatus checks topUp.UserId against the authenticated user. There is
	// deliberately no user_id parameter or fallback to an administrator route.
	e, _, err := c.call(ctx, "GET", "/api/user/topup/status?"+url.Values{"trade_no": {tradeNo}}.Encode(), cred, nil)
	if err != nil {
		return nil, err
	}
	var status struct {
		TradeNo string `json:"trade_no"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(e.Data, &status) != nil || status.TradeNo != tradeNo || strings.TrimSpace(status.Status) == "" {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "topup status lacks matching trade_no/status")
	}
	// Preserve Kovar's status (including pending/success/failed/expired), without
	// inventing paid/completed aliases or crediting quota locally.
	return e.Data, nil
}

func (c *KovarManageClient) AxoneChains(ctx context.Context, cred Credential) (json.RawMessage, error) {
	e, _, err := c.call(ctx, "GET", "/api/user/axone/chains", cred, nil)
	if err != nil {
		return nil, err
	}
	if len(e.Data) == 0 || string(e.Data) == "null" {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Axone chain data is missing")
	}
	return e.Data, nil
}

func (c *KovarManageClient) AxoneWallets(ctx context.Context, cred Credential) (json.RawMessage, error) {
	e, _, err := c.call(ctx, "GET", "/api/user/axone/wallets", cred, nil)
	if err != nil {
		return nil, err
	}
	var wallets struct {
		List []json.RawMessage `json:"list"`
	}
	if json.Unmarshal(e.Data, &wallets) != nil || wallets.List == nil {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Axone wallet data lacks a list array")
	}
	return e.Data, nil
}

type axoneOrderRequest struct {
	Amount               int64  `json:"amount"`
	Currency             string `json:"currency"`
	ChainID              string `json:"chain_id"`
	PaymentWalletAddress string `json:"payment_wallet_address"`
}

func (c *KovarManageClient) axoneOrder(ctx context.Context, cred Credential, payload json.RawMessage) (json.RawMessage, error) {
	var in axoneOrderRequest
	if err := httpx.Decode(payload, &in); err != nil {
		return nil, err
	}
	if in.Amount <= 0 || !boundedText(in.Currency, 32) || !boundedText(in.ChainID, 128) || !boundedText(in.PaymentWalletAddress, 256) {
		return nil, httpx.Invalid("Axone requires positive integer amount, currency, chain_id and payment_wallet_address")
	}
	// Currency/chain support and minimum amounts are configured by Kovar. Do not
	// select them automatically or interpret chain-specific wallet formats here.
	e, _, err := c.call(ctx, "POST", "/api/user/axone/order", cred, in)
	if err != nil {
		return nil, err
	}
	var order struct {
		TradeNo string `json:"trade_no"`
		Address string `json:"address"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(e.Data, &order) != nil || !boundedText(order.TradeNo, 256) || !boundedText(order.Address, 256) || order.Status == "" {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Axone order lacks trade_no/address/status")
	}
	if order.Status == "failed" || order.Status == "expired" {
		return nil, httpx.E(502, "KOVAR_REQUEST_REJECTED", "Axone order was rejected")
	}
	if order.Status != "pending" {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "unexpected Axone order creation status; query order status")
	}
	return e.Data, nil
}

type paygoSessionRequest struct {
	WalletID  string `json:"wallet_id"`
	MaxAmount string `json:"max_amount"`
}

var paygoAmount = regexp.MustCompile(`^[0-9]{1,32}(\.[0-9]{1,18})?$`)

func validPaygoAmount(value string) bool {
	if !paygoAmount.MatchString(value) {
		return false
	}
	for _, r := range value {
		if r >= '1' && r <= '9' {
			return true
		}
	}
	return false
}

func validPaygoSession(data json.RawMessage) error {
	var session struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if json.Unmarshal(data, &session) != nil || !boundedText(session.SessionID, 128) || strings.TrimSpace(session.Status) == "" {
		return httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Axone paygo session lacks session_id/status")
	}
	return nil
}

func validPaygoSessionList(data json.RawMessage) error {
	var items []json.RawMessage
	if json.Unmarshal(data, &items) != nil {
		return httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "Axone paygo session list must be an array")
	}
	return nil
}

func (c *KovarManageClient) CreatePaygoSession(ctx context.Context, cred Credential, key, walletID, maxAmount string) (json.RawMessage, error) {
	if !boundedText(walletID, 256) || !validPaygoAmount(maxAmount) {
		return nil, httpx.Invalid("paygo session requires wallet_id and a positive decimal max_amount")
	}
	headers := http.Header{}
	headers.Set("Idempotency-Key", key)
	e, _, err := c.callHeaders(ctx, "POST", "/api/user/axone/paygo/sessions", cred, headers, paygoSessionRequest{WalletID: walletID, MaxAmount: maxAmount})
	if err != nil {
		return nil, err
	}
	if err := validPaygoSession(e.Data); err != nil {
		return nil, err
	}
	return e.Data, nil
}

func (c *KovarManageClient) ListPaygoSessions(ctx context.Context, cred Credential) (json.RawMessage, error) {
	e, _, err := c.call(ctx, "GET", "/api/user/axone/paygo/sessions", cred, nil)
	if err != nil {
		return nil, err
	}
	if err := validPaygoSessionList(e.Data); err != nil {
		return nil, err
	}
	return e.Data, nil
}

func (c *KovarManageClient) GetPaygoSession(ctx context.Context, cred Credential, id string) (json.RawMessage, error) {
	if !httpx.Identifier(id) {
		return nil, httpx.Invalid("paygo session id is required and must be a safe identifier")
	}
	e, _, err := c.call(ctx, "GET", "/api/user/axone/paygo/sessions/"+id, cred, nil)
	if err != nil {
		return nil, err
	}
	if err := validPaygoSession(e.Data); err != nil {
		return nil, err
	}
	return e.Data, nil
}

func (c *KovarManageClient) ClosePaygoSession(ctx context.Context, cred Credential, id, key string) (json.RawMessage, error) {
	if !httpx.Identifier(id) {
		return nil, httpx.Invalid("paygo session id is required and must be a safe identifier")
	}
	headers := http.Header{}
	headers.Set("Idempotency-Key", key)
	e, _, err := c.callHeaders(ctx, "POST", "/api/user/axone/paygo/sessions/"+id+"/close", cred, headers, nil)
	if err != nil {
		return nil, err
	}
	if err := validPaygoSession(e.Data); err != nil {
		return nil, err
	}
	return e.Data, nil
}

func boundedText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= max && !strings.ContainsFunc(value, unicode.IsControl)
}
