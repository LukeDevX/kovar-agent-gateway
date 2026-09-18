package identity

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"kovar-gateway/internal/platform/database"
	"kovar-gateway/internal/platform/httpx"
)

type repository struct{ db database.DBTX }

const agentSelect = `SELECT a.address,a.status,w.status,a.created_at,a.last_seen_at,w.reviewed_by,w.reviewed_at,w.remark,p.per_request_limit,p.daily_limit,p.monthly_limit FROM agents a JOIN agent_whitelist w ON w.agent_address=a.address JOIN agent_budget_policies p ON p.agent_address=a.address`

func scanAgent(row interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	err := row.Scan(&a.Address, &a.Status, &a.Whitelist, &a.CreatedAt, &a.LastSeen, &a.ReviewedBy, &a.ReviewedAt, &a.Remark, &a.PerRequest, &a.Daily, &a.Monthly)
	if errors.Is(err, sql.ErrNoRows) {
		err = httpx.E(404, "AGENT_NOT_FOUND", "agent does not exist")
	}
	return a, err
}
func (r repository) agent(ctx context.Context, address string, lock bool) (Agent, error) {
	q := agentSelect + ` WHERE a.address=$1`
	if lock {
		q += ` FOR UPDATE OF a,w`
	}
	return scanAgent(r.db.QueryRowContext(ctx, q, address))
}
func (r repository) list(ctx context.Context, page, size int, whitelist string) ([]Agent, error) {
	rows, err := r.db.QueryContext(ctx, agentSelect+` WHERE ($1='' OR w.status=$1) ORDER BY a.created_at DESC,a.address LIMIT $2 OFFSET $3`, whitelist, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, e := scanAgent(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (r repository) challenge(ctx context.Context, c Challenge) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_challenges(agent_address,nonce,purpose,expires_at) VALUES($1,$2,'REGISTER',$3)`, c.Address, c.Nonce, c.ExpiresAt)
	return err
}
func (r repository) consumeChallenge(ctx context.Context, address, nonce string) error {
	var owner, purpose string
	var expires time.Time
	var used *time.Time
	err := r.db.QueryRowContext(ctx, `SELECT agent_address,purpose,expires_at,used_at FROM agent_challenges WHERE nonce=$1 FOR UPDATE`, nonce).Scan(&owner, &purpose, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.E(401, "INVALID_AGENT_SIGNATURE", "challenge not found")
	}
	if err != nil {
		return err
	}
	if owner != address || purpose != "REGISTER" {
		return httpx.E(401, "INVALID_AGENT_SIGNATURE", "challenge does not match agent")
	}
	if used != nil {
		return httpx.E(401, "AGENT_NONCE_REUSED", "nonce was already used")
	}
	if !time.Now().Before(expires) {
		return httpx.E(401, "AGENT_NONCE_EXPIRED", "nonce expired")
	}
	_, err = r.db.ExecContext(ctx, `UPDATE agent_challenges SET used_at=now() WHERE nonce=$1`, nonce)
	return err
}
func (r repository) create(ctx context.Context, address string, p Policy) error {
	if _, err := r.db.ExecContext(ctx, `INSERT INTO agents(address,status) VALUES($1,'REGISTERED')`, address); err != nil {
		if database.Unique(err) {
			return httpx.Conflict("AGENT_ALREADY_REGISTERED", "agent already registered")
		}
		return err
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO agent_whitelist(agent_address,status) VALUES($1,'PENDING')`, address); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_budget_policies(agent_address,per_request_limit,daily_limit,monthly_limit) VALUES($1,$2,$3,$4)`, address, p.PerRequest, p.Daily, p.Monthly)
	return err
}
func (r repository) policy(ctx context.Context, address string) (Policy, error) {
	var p Policy
	err := r.db.QueryRowContext(ctx, `SELECT per_request_limit,daily_limit,monthly_limit FROM agent_budget_policies WHERE agent_address=$1`, address).Scan(&p.PerRequest, &p.Daily, &p.Monthly)
	return p, err
}
func (r repository) state(ctx context.Context, address, status, whitelist string, admin int64, remark string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE agents SET status=$2,updated_at=now(),revoked_at=CASE WHEN $2='REVOKED' THEN now() ELSE revoked_at END WHERE address=$1`, address, status); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE agent_whitelist SET status=$2,reviewed_by=$3,reviewed_at=now(),remark=$4,updated_at=now() WHERE agent_address=$1`, address, whitelist, admin, remark)
	return err
}
