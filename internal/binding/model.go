package binding

import "time"

type Binding struct {
	Agent  string `json:"agent_id"`
	UserID int64  `json:"kovar_user_id"`
	Status string `json:"status"`
}
type TokenInfo struct {
	TokenID        *int64     `json:"token_id"`
	Status         string     `json:"status"`
	ProviderStatus *int       `json:"kovar_token_status"`
	ExpiresAt      *time.Time `json:"expired_at"`
	RemainQuota    *int64     `json:"remain_quota"`
	Bound          bool       `json:"key_bound"`
}
type Account struct {
	UserID         int64 `json:"kovar_user_id"`
	Quota          int64 `json:"quota"`
	UsedQuota      int64 `json:"used_quota"`
	AvailableQuota int64 `json:"available_quota"`
	RequestCount   int64 `json:"request_count"`
}
type KeyOptions struct {
	RemainQuota int64 `json:"remain_quota"`
	ExpiredTime int64 `json:"expired_time"`
}
