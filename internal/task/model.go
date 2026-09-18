package task

import (
	"encoding/json"
	"time"

	"kovar-gateway/internal/client/kovarmodel"
)

type Input struct {
	TaskType  string                     `json:"task_type"`
	Model     string                     `json:"model,omitempty"`
	Operation string                     `json:"operation,omitempty"`
	Protocol  string                     `json:"protocol,omitempty"`
	Payload   map[string]any             `json:"payload"`
	Files     map[string]kovarmodel.File `json:"-"`
}
type Task struct {
	TaskID           string          `json:"task_id"`
	AgentID          string          `json:"agent_id"`
	UserID           int64           `json:"kovar_user_id"`
	TokenID          int64           `json:"kovar_token_id"`
	Type             string          `json:"task_type"`
	Model            string          `json:"model"`
	ProviderID       *string         `json:"provider_task_id"`
	Request          json.RawMessage `json:"request_payload"`
	Result           json.RawMessage `json:"result_payload"`
	Estimated        int64           `json:"estimated_cost"`
	Actual           *int64          `json:"actual_cost"`
	PromptTokens     int64           `json:"prompt_tokens"`
	CompletionTokens int64           `json:"completion_tokens"`
	TotalTokens      int64           `json:"total_tokens"`
	Status           string          `json:"status"`
	ErrorCode        *string         `json:"error_code"`
	ErrorMessage     *string         `json:"error_message"`
	IdempotencyKey   string          `json:"idempotency_key"`
	CreatedAt        time.Time       `json:"created_at"`
	StartedAt        *time.Time      `json:"started_at"`
	CompletedAt      *time.Time      `json:"completed_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}
type Usage struct {
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	Total      int64 `json:"total_tokens"`
}
type Summary struct {
	Today       int64 `json:"today_usage"`
	Month       int64 `json:"month_usage"`
	Count       int64 `json:"task_count"`
	UnknownCost int64 `json:"tasks_without_actual_cost"`
}
