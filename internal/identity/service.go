package identity

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"kovar-gateway/internal/audit"
	"kovar-gateway/internal/idempotency"
	"kovar-gateway/internal/platform/config"
	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/platform/secure"
)

type Service struct {
	db    *sql.DB
	cfg   config.Config
	idem  *idempotency.Service
	dummy []byte
}

func New(db *sql.DB, cfg config.Config, idem *idempotency.Service) *Service {
	dummy, _ := bcrypt.GenerateFromPassword([]byte(secure.Random()), bcrypt.DefaultCost)
	return &Service{db, cfg, idem, dummy}
}
func (s *Service) Bootstrap(ctx context.Context) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_admins WHERE username=$1)`, s.cfg.AdminUsername).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gateway_admins(username,password_hash) VALUES($1,$2) ON CONFLICT(username) DO NOTHING`, s.cfg.AdminUsername, string(hash))
	return err
}
func (s *Service) Challenge(ctx context.Context, address string) (Challenge, error) {
	a, err := secure.Address(address)
	if err != nil {
		return Challenge{}, httpx.Invalid("invalid EVM address")
	}
	c := Challenge{a, secure.Random(), time.Now().UTC().Add(s.cfg.NonceTTL)}
	return c, (repository{s.db}).challenge(ctx, c)
}
func (s *Service) Register(ctx context.Context, in Registration, key string) (Agent, error) {
	a, err := secure.Address(in.Address)
	if err != nil {
		return Agent{}, httpx.E(400, "INVALID_AGENT_ADDRESS", "invalid EVM address")
	}
	in.Address = a
	if !secure.Timestamp(in.Timestamp, time.Now(), s.cfg.ClockSkew) {
		return Agent{}, httpx.E(401, "AGENT_TIMESTAMP_EXPIRED", "timestamp outside allowed window")
	}
	if !secure.Verify(a, secure.Registration(a, in.Nonce, in.Timestamp), in.Signature) {
		return Agent{}, httpx.E(401, "INVALID_AGENT_SIGNATURE", "invalid EVM signature")
	}
	// Signature bytes and nonce are not persisted in the idempotency response.
	b, _ := json.Marshal(in)
	claim, err := s.idem.Claim(ctx, a, "register", key, secure.Hash(b))
	if err != nil {
		return Agent{}, err
	}
	if claim.Replay {
		var out Agent
		err = json.Unmarshal(claim.Body, &out)
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	r := repository{tx}
	if err = r.consumeChallenge(ctx, a, in.Nonce); err != nil {
		return Agent{}, err
	}
	if err = r.create(ctx, a, Policy{s.cfg.PerRequest, s.cfg.Daily, s.cfg.Monthly}); err != nil {
		return Agent{}, err
	}
	out, err := r.agent(ctx, a, false)
	if err != nil {
		return Agent{}, err
	}
	b, _ = json.Marshal(out)
	if err = audit.Record(ctx, tx, "agent", a, "Agent Register", "agent", a, "REGISTERED"); err != nil {
		return Agent{}, err
	}
	if err = idempotency.Complete(ctx, tx, a, "register", key, 201, b); err != nil {
		return Agent{}, err
	}
	return out, tx.Commit()
}
func StatusError(a Agent) error {
	switch a.Status {
	case "REVOKED":
		return httpx.E(403, "AGENT_REVOKED", "agent has been revoked")
	case "SUSPENDED":
		return httpx.E(403, "AGENT_SUSPENDED", "agent is suspended")
	}
	if a.Status != "ACTIVE" || a.Whitelist != "APPROVED" {
		return httpx.E(403, "AGENT_NOT_WHITELISTED", "agent is not approved")
	}
	return nil
}
func (s *Service) Active(ctx context.Context, address string) error {
	a, err := s.Get(ctx, address)
	if err != nil {
		return err
	}
	return StatusError(a)
}

// LockActive shares the identity transaction boundary with dependent writes.
// Revocation and task admission serialize on the same agent row.
func (s *Service) LockActive(ctx context.Context, tx *sql.Tx, address string) (Policy, error) {
	a, err := (repository{tx}).agent(ctx, address, true)
	if err != nil {
		return Policy{}, err
	}
	if err = StatusError(a); err != nil {
		return Policy{}, err
	}
	return (repository{tx}).policy(ctx, address)
}
func (s *Service) Authenticate(ctx context.Context, address, method, target, ts, nonce, sig, key string, body []byte) (string, error) {
	a, err := secure.Address(address)
	if err != nil {
		return "", httpx.E(401, "INVALID_AGENT_ADDRESS", "invalid EVM address")
	}
	var timestamp int64
	if json.Unmarshal([]byte(ts), &timestamp) != nil || !secure.Timestamp(timestamp, time.Now(), s.cfg.ClockSkew) {
		return "", httpx.E(401, "AGENT_TIMESTAMP_EXPIRED", "timestamp outside allowed window")
	}
	if len(nonce) != 64 {
		return "", httpx.Invalid("X-Nonce must be 32 random bytes encoded as hex")
	}
	if _, err = hex.DecodeString(nonce); err != nil {
		return "", httpx.Invalid("invalid nonce")
	}
	if !secure.Verify(a, secure.Canonical(method, target, ts, nonce, key, body), sig) {
		return "", httpx.E(401, "INVALID_AGENT_SIGNATURE", "invalid EVM signature")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = s.LockActive(ctx, tx, a); err != nil {
		return "", err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_challenges(agent_address,nonce,purpose,expires_at,used_at) VALUES($1,$2,'REQUEST',$3,now()) ON CONFLICT(nonce) DO NOTHING`, a, strings.ToLower(nonce), time.Unix(timestamp, 0).Add(s.cfg.ClockSkew))
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", httpx.E(401, "AGENT_NONCE_REUSED", "nonce was already used")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agents SET last_seen_at=now() WHERE address=$1`, a); err != nil {
		return "", err
	}
	return a, tx.Commit()
}
func (s *Service) Get(ctx context.Context, a string) (Agent, error) {
	return (repository{s.db}).agent(ctx, a, false)
}
func (s *Service) List(ctx context.Context, p, n int, w string) ([]Agent, error) {
	switch w {
	case "", "PENDING", "APPROVED", "REJECTED", "REVOKED":
	default:
		return nil, httpx.Invalid("invalid whitelist status")
	}
	return (repository{s.db}).list(ctx, p, n, w)
}
func (s *Service) Policy(ctx context.Context, a string) (Policy, error) {
	return (repository{s.db}).policy(ctx, a)
}
func (s *Service) UpdatePolicy(ctx context.Context, admin Admin, a string, p Policy) error {
	if p.PerRequest < 0 || p.Daily < 0 || p.Monthly < 0 {
		return httpx.Invalid("budget limits cannot be negative")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = (repository{tx}).agent(ctx, a, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_budget_policies SET per_request_limit=$2,daily_limit=$3,monthly_limit=$4,updated_at=now() WHERE agent_address=$1`, a, p.PerRequest, p.Daily, p.Monthly); err != nil {
		return err
	}
	if err = audit.Record(ctx, tx, "admin", admin.Username, "Budget Update", "agent", a, "UPDATED"); err != nil {
		return err
	}
	return tx.Commit()
}
func Transition(a Agent, action string) (string, string, error) {
	switch action {
	case "approve":
		if a.Status == "REGISTERED" && (a.Whitelist == "PENDING" || a.Whitelist == "REJECTED") {
			return "ACTIVE", "APPROVED", nil
		}
	case "reject":
		if a.Status == "REGISTERED" && a.Whitelist == "PENDING" {
			return "REGISTERED", "REJECTED", nil
		}
	case "suspend":
		if a.Status == "ACTIVE" && a.Whitelist == "APPROVED" {
			return "SUSPENDED", "APPROVED", nil
		}
	case "resume":
		if a.Status == "SUSPENDED" && a.Whitelist == "APPROVED" {
			return "ACTIVE", "APPROVED", nil
		}
	case "revoke":
		return "REVOKED", "REVOKED", nil
	default:
		return "", "", httpx.Invalid("invalid whitelist action")
	}
	return "", "", httpx.Conflict("INVALID_AGENT_STATE", "agent cannot perform this state transition")
}
func (s *Service) Review(ctx context.Context, admin Admin, a, action, remark string) (Agent, error) {
	if len(remark) > 1000 {
		return Agent{}, httpx.Invalid("remark exceeds 1000 bytes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	r := repository{tx}
	old, err := r.agent(ctx, a, true)
	if err != nil {
		return Agent{}, err
	}
	status, white, err := Transition(old, action)
	if err != nil {
		return Agent{}, err
	}
	if err = r.state(ctx, a, status, white, admin.ID, remark); err != nil {
		return Agent{}, err
	}
	if err = audit.Record(ctx, tx, "admin", admin.Username, action, "agent", a, status); err != nil {
		return Agent{}, err
	}
	out, err := r.agent(ctx, a, false)
	if err != nil {
		return Agent{}, err
	}
	return out, tx.Commit()
}
func (s *Service) Login(ctx context.Context, username, password string) (string, Admin, error) {
	if len(username) > 128 || len(password) > 72 {
		return "", Admin{}, httpx.E(401, "INVALID_ADMIN_CREDENTIALS", "invalid administrator credentials")
	}
	var a Admin
	var hash, status string
	err := s.db.QueryRowContext(ctx, `SELECT id,username,password_hash,status FROM gateway_admins WHERE username=$1`, username).Scan(&a.ID, &a.Username, &hash, &status)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(s.dummy, []byte(password))
		return "", Admin{}, httpx.E(401, "INVALID_ADMIN_CREDENTIALS", "invalid administrator credentials")
	}
	if err != nil {
		return "", Admin{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || status != "ACTIVE" {
		return "", Admin{}, httpx.E(401, "INVALID_ADMIN_CREDENTIALS", "invalid administrator credentials")
	}
	token := secure.Random()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Admin{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,admin_id,expires_at) VALUES(decode($1,'hex'),$2,$3)`, secure.Hash([]byte(token)), a.ID, time.Now().Add(s.cfg.AdminTTL)); err != nil {
		return "", Admin{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE gateway_admins SET last_login_at=now(),updated_at=now() WHERE id=$1`, a.ID); err != nil {
		return "", Admin{}, err
	}
	if err = audit.Record(ctx, tx, "admin", a.Username, "Admin Login", "admin", a.Username, "SUCCESS"); err != nil {
		return "", Admin{}, err
	}
	return token, a, tx.Commit()
}
func (s *Service) Admin(ctx context.Context, token string) (Admin, error) {
	var a Admin
	if len(token) != 64 {
		return a, httpx.E(401, "INVALID_ADMIN_TOKEN", "invalid or expired admin token")
	}
	err := s.db.QueryRowContext(ctx, `SELECT a.id,a.username FROM admin_sessions s JOIN gateway_admins a ON a.id=s.admin_id WHERE s.token_hash=decode($1,'hex') AND s.expires_at>now() AND a.status='ACTIVE'`, secure.Hash([]byte(token))).Scan(&a.ID, &a.Username)
	if errors.Is(err, sql.ErrNoRows) {
		err = httpx.E(401, "INVALID_ADMIN_TOKEN", "invalid or expired admin token")
	}
	return a, err
}
func (s *Service) Rate(ctx context.Context, scope, subject string, limit int) error {
	var count int
	err := s.db.QueryRowContext(ctx, `INSERT INTO rate_limit_windows(scope,subject,window_start,requests) VALUES($1,$2,$3,1) ON CONFLICT(scope,subject,window_start) DO UPDATE SET requests=LEAST(rate_limit_windows.requests+1,$4+1) RETURNING requests`, scope, subject, time.Now().Unix()/60, limit).Scan(&count)
	if err != nil {
		return err
	}
	if count > limit {
		return httpx.E(429, "RATE_LIMITED", "request rate limit exceeded")
	}
	return nil
}
func (s *Service) Cleanup(ctx context.Context) error {
	for _, q := range []string{`DELETE FROM agent_challenges WHERE expires_at<now()-interval '1 day'`, `DELETE FROM admin_sessions WHERE expires_at<now()`, `DELETE FROM rate_limit_windows WHERE window_start<extract(epoch from now())::bigint/60-2`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) LockAgent(ctx context.Context, tx *sql.Tx, address string) (Agent, error) {
	return (repository{tx}).agent(ctx, address, true)
}
