package task

import (
	"context"
	"database/sql"
	"errors"

	"kovar-gateway/internal/platform/database"
	"kovar-gateway/internal/platform/httpx"
)

type repository struct{ db database.DBTX }

const taskSelect = `SELECT task_id,agent_id,kovar_user_id,kovar_token_id,task_type,model,provider_task_id,request_payload,result_payload,estimated_cost,actual_cost,prompt_tokens,completion_tokens,total_tokens,status,error_code,error_message,idempotency_key,created_at,started_at,completed_at,updated_at FROM gateway_tasks`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var requestPayload, resultPayload []byte
	err := row.Scan(&t.TaskID, &t.AgentID, &t.UserID, &t.TokenID, &t.Type, &t.Model, &t.ProviderID, &requestPayload, &resultPayload, &t.Estimated, &t.Actual, &t.PromptTokens, &t.CompletionTokens, &t.TotalTokens, &t.Status, &t.ErrorCode, &t.ErrorMessage, &t.IdempotencyKey, &t.CreatedAt, &t.StartedAt, &t.CompletedAt, &t.UpdatedAt)
	t.Request = requestPayload
	t.Result = resultPayload
	if errors.Is(err, sql.ErrNoRows) {
		err = httpx.E(404, "TASK_NOT_FOUND", "task does not exist")
	}
	return t, err
}
func (r repository) get(ctx context.Context, a, id string) (Task, error) {
	return scanTask(r.db.QueryRowContext(ctx, taskSelect+` WHERE agent_id=$1 AND task_id=$2`, a, id))
}
func (r repository) existing(ctx context.Context, a, key, hash string) (Task, bool, error) {
	var savedHash string
	err := r.db.QueryRowContext(ctx, `SELECT request_hash FROM gateway_tasks WHERE agent_id=$1 AND idempotency_key=$2`, a, key).Scan(&savedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	if savedHash != hash {
		return Task{}, false, httpx.Conflict("IDEMPOTENCY_CONFLICT", "task idempotency key was used with another request")
	}
	t, err := scanTask(r.db.QueryRowContext(ctx, taskSelect+` WHERE agent_id=$1 AND idempotency_key=$2`, a, key))
	return t, true, err
}
func (r repository) summary(ctx context.Context, a string) (Summary, error) {
	var s Summary
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(actual_cost,estimated_cost)) FILTER(WHERE created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),0),COALESCE(SUM(COALESCE(actual_cost,estimated_cost)) FILTER(WHERE created_at>=date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),0),COUNT(*),COUNT(*) FILTER(WHERE actual_cost IS NULL) FROM gateway_tasks WHERE agent_id=$1`, a).Scan(&s.Today, &s.Month, &s.Count, &s.UnknownCost)
	return s, err
}
func (r repository) create(ctx context.Context, t Task, hash string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO gateway_tasks(task_id,agent_id,kovar_user_id,kovar_token_id,task_type,model,request_payload,estimated_cost,status,idempotency_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'CREATED',$9,$10)`, t.TaskID, t.AgentID, t.UserID, t.TokenID, t.Type, t.Model, []byte(t.Request), t.Estimated, t.IdempotencyKey, hash)
	return err
}
func (r repository) list(ctx context.Context, a string, page, size int) ([]Task, error) {
	rows, err := r.db.QueryContext(ctx, taskSelect+` WHERE agent_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, a, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
