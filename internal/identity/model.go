package identity

import "time"

type Agent struct {
	Policy
	Address    string     `json:"agent_id"`
	Status     string     `json:"agent_status"`
	Whitelist  string     `json:"whitelist_status"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeen   *time.Time `json:"last_seen_at"`
	ReviewedBy *int64     `json:"reviewed_by"`
	ReviewedAt *time.Time `json:"reviewed_at"`
	Remark     string     `json:"remark"`
}
type Policy struct {
	PerRequest int64 `json:"per_request_limit"`
	Daily      int64 `json:"daily_limit"`
	Monthly    int64 `json:"monthly_limit"`
}
type Admin struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
type Challenge struct {
	Address   string    `json:"address"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Registration struct {
	Address   string `json:"address"`
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}
