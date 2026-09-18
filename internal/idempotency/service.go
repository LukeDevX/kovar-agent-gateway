package idempotency

import (
	"context"
	"database/sql"
	"encoding/json"

	"kovar-gateway/internal/platform/database"
	"kovar-gateway/internal/platform/httpx"
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db} }

type Result struct {
	Body   json.RawMessage
	Status int
	Replay bool
}

// Claim commits before any external write. An ambiguous upstream outcome is
// never automatically retried, even after a process crash.
func (s *Service) Claim(ctx context.Context, agent, operation, key, hash string) (Result, error) {
	if !httpx.Identifier(key) {
		return Result{}, httpx.Invalid("Idempotency-Key is required (1..128 safe characters)")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO idempotency_records(agent_id,operation,idempotency_key,request_hash,status) VALUES($1,$2,$3,$4,'PROCESSING') ON CONFLICT DO NOTHING`, agent, operation, key, hash)
	if err != nil {
		return Result{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Result{}, err
	}
	if n == 1 {
		return Result{}, nil
	}
	var h, status string
	var b []byte
	var code sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT request_hash,status,response,http_status FROM idempotency_records WHERE agent_id=$1 AND operation=$2 AND idempotency_key=$3`, agent, operation, key).Scan(&h, &status, &b, &code)
	if err != nil {
		return Result{}, err
	}
	if h != hash {
		return Result{}, httpx.Conflict("IDEMPOTENCY_CONFLICT", "idempotency key was used with a different request")
	}
	if status != "COMPLETED" {
		return Result{}, httpx.Conflict("IDEMPOTENCY_IN_PROGRESS", "request is in progress or needs upstream reconciliation; it will not be executed again")
	}
	return Result{Body: b, Status: int(code.Int64), Replay: true}, nil
}
func (s *Service) Complete(ctx context.Context, agent, operation, key string, status int, body []byte) error {
	return Complete(ctx, s.db, agent, operation, key, status, body)
}
func Complete(ctx context.Context, db database.DBTX, agent, operation, key string, status int, body []byte) error {
	r, err := db.ExecContext(ctx, `UPDATE idempotency_records SET response=$4,http_status=$5,status='COMPLETED',updated_at=now() WHERE agent_id=$1 AND operation=$2 AND idempotency_key=$3 AND status='PROCESSING'`, agent, operation, key, body, status)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return httpx.Conflict("IDEMPOTENCY_CONFLICT", "idempotency record is not pending")
	}
	return nil
}
