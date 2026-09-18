package binding

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"kovar-gateway/internal/audit"
	"kovar-gateway/internal/client/kovarmanage"
	"kovar-gateway/internal/identity"
	"kovar-gateway/internal/platform/database"
	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/platform/secure"
)

type Service struct {
	db         *sql.DB
	auth       *identity.Service
	manage     *kovarmanage.KovarManageClient
	encryption *secure.EncryptionService
}

func New(db *sql.DB, auth *identity.Service, manage *kovarmanage.KovarManageClient, e *secure.EncryptionService) *Service {
	return &Service{db, auth, manage, e}
}
func (s *Service) Get(ctx context.Context, a string) (Binding, error) {
	b, _, err := (repository{s.db}).binding(ctx, a)
	return b, err
}
func (s *Service) Credential(ctx context.Context, a string) (kovarmanage.Credential, error) {
	b, cipher, err := (repository{s.db}).binding(ctx, a)
	if err != nil {
		return kovarmanage.Credential{}, err
	}
	plain, err := s.encryption.Decrypt(cipher, a+":manage-session")
	if err != nil {
		return kovarmanage.Credential{}, err
	}
	defer clear(plain)
	var c kovarmanage.Credential
	if err = json.Unmarshal(plain, &c); err != nil {
		return c, err
	}
	if c.UserID != b.UserID || b.Status != "ACTIVE" {
		return c, httpx.E(409, "KOVAR_USER_NOT_BOUND", "Kovar binding unavailable")
	}
	return c, nil
}
func (s *Service) Register(ctx context.Context, a string, in kovarmanage.RegisterRequest) error {
	if err := s.auth.Active(ctx, a); err != nil {
		return err
	}
	if len(in.Username) < 1 || len(in.Username) > 128 || len(in.Password) < 1 || len(in.Password) > 256 || len(in.Email) > 320 || len(in.VerificationCode) > 128 || len(in.AffCode) > 128 {
		return httpx.Invalid("invalid Kovar registration fields")
	}
	return s.manage.Register(ctx, in)
}
func (s *Service) Login(ctx context.Context, a string, in kovarmanage.LoginRequest) (any, error) {
	if err := s.auth.Active(ctx, a); err != nil {
		return nil, err
	}
	if in.Username == "" || len(in.Username) > 128 || in.Password == "" || len(in.Password) > 256 {
		return nil, httpx.Invalid("invalid Kovar login fields")
	}
	u, c, err := s.manage.Login(ctx, in)
	in.Password = ""
	if err != nil {
		return nil, err
	}
	if c.Session == "" {
		return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "login did not return documented session cookie")
	}
	if u.ID <= 0 {
		b, _ := json.Marshal(c)
		cipher, err := s.encryption.Encrypt(b, a+":pending-login")
		clear(b)
		if err != nil {
			return nil, err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO pending_kovar_logins(agent_address,encrypted_session,expires_at) VALUES($1,$2,now()+interval '5 minutes') ON CONFLICT(agent_address) DO UPDATE SET encrypted_session=$2,expires_at=now()+interval '5 minutes'`, a, cipher)
		return map[string]any{"authentication_complete": false, "continuation_required": true}, err
	}
	return s.Bind(ctx, a, c)
}
func (s *Service) TwoFA(ctx context.Context, a, code string) (Binding, error) {
	if len(code) < 1 || len(code) > 128 {
		return Binding{}, httpx.Invalid("invalid 2FA code")
	}
	if err := s.auth.Active(ctx, a); err != nil {
		return Binding{}, err
	}
	var cipher []byte
	err := s.db.QueryRowContext(ctx, `SELECT encrypted_session FROM pending_kovar_logins WHERE agent_address=$1 AND expires_at>now()`, a).Scan(&cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, httpx.E(409, "KOVAR_LOGIN_REQUIRED", "no pending Kovar login")
	}
	if err != nil {
		return Binding{}, err
	}
	plain, err := s.encryption.Decrypt(cipher, a+":pending-login")
	if err != nil {
		return Binding{}, err
	}
	defer clear(plain)
	var c kovarmanage.Credential
	if err = json.Unmarshal(plain, &c); err != nil {
		return Binding{}, err
	}
	_, c, err = s.manage.TwoFA(ctx, c, code)
	if err != nil {
		return Binding{}, err
	}
	return s.Bind(ctx, a, c)
}
func (s *Service) Bind(ctx context.Context, a string, c kovarmanage.Credential) (Binding, error) {
	if err := s.auth.Active(ctx, a); err != nil {
		return Binding{}, err
	}
	if c.UserID <= 0 || (c.Session == "" && c.AccessToken == "") || len(c.AccessToken) > 8192 || strings.ContainsAny(c.AccessToken, "\r\n") {
		return Binding{}, httpx.Invalid("valid Kovar user id and management credential required")
	}
	u, err := s.manage.Self(ctx, c)
	if err != nil {
		return Binding{}, err
	}
	plain, _ := json.Marshal(c)
	cipher, err := s.encryption.Encrypt(plain, a+":manage-session")
	clear(plain)
	if err != nil {
		return Binding{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback()
	if _, err = s.auth.LockActive(ctx, tx, a); err != nil {
		return Binding{}, err
	}
	var existing int64
	err = tx.QueryRowContext(ctx, `SELECT kovar_user_id FROM agent_user_bindings WHERE agent_address=$1`, a).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, err
	}
	if err == nil && existing != u.ID {
		return Binding{}, httpx.Conflict("KOVAR_USER_REBIND_FORBIDDEN", "agent is already bound to another Kovar user")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_user_bindings(agent_address,kovar_user_id,encrypted_session) VALUES($1,$2,$3) ON CONFLICT(agent_address) DO UPDATE SET encrypted_session=$3,updated_at=now()`, a, u.ID, cipher); err != nil {
		return Binding{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM pending_kovar_logins WHERE agent_address=$1`, a); err != nil {
		return Binding{}, err
	}
	if err = audit.Record(ctx, tx, "agent", a, "User Binding", "agent", a, "BOUND"); err != nil {
		return Binding{}, err
	}
	return Binding{a, u.ID, "ACTIVE"}, tx.Commit()
}
func (s *Service) LocalToken(ctx context.Context, a string) (TokenInfo, error) {
	t, _, err := (repository{s.db}).token(ctx, a)
	return t, err
}
func (s *Service) Key(ctx context.Context, a string) (string, int64, error) {
	t, cipher, err := (repository{s.db}).token(ctx, a)
	if err != nil {
		return "", 0, err
	}
	if !t.Bound || t.TokenID == nil {
		return "", 0, httpx.E(409, "KOVAR_TOKEN_UNAVAILABLE", "agent token is unavailable")
	}
	key, err := s.encryption.Decrypt(cipher, a+":model-key")
	if err != nil {
		return "", 0, err
	}
	defer clear(key)
	return string(key), *t.TokenID, nil
}
func (s *Service) CreateKey(ctx context.Context, a string, opt KeyOptions) (TokenInfo, error) {
	c, err := s.Credential(ctx, a)
	if err != nil {
		return TokenInfo{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenInfo{}, err
	}
	defer tx.Rollback()
	p, err := s.auth.LockActive(ctx, tx, a)
	if err != nil {
		return TokenInfo{}, err
	}
	if opt.RemainQuota == 0 {
		opt.RemainQuota = p.Monthly
	}
	if opt.RemainQuota < 1 || opt.RemainQuota > p.Monthly {
		return TokenInfo{}, httpx.Invalid("token quota must be positive and within monthly policy")
	}
	if opt.ExpiredTime == 0 {
		opt.ExpiredTime = time.Now().Add(30 * 24 * time.Hour).Unix()
	}
	if opt.ExpiredTime <= time.Now().Unix() {
		return TokenInfo{}, httpx.Invalid("token expiry must be in the future")
	}
	admission, err := tx.ExecContext(ctx, `INSERT INTO agent_kovar_tokens(agent_address,kovar_user_id,status) VALUES($1,$2,'CREATING') ON CONFLICT(agent_address) DO UPDATE SET status='CREATING',kovar_token_id=NULL,encrypted_kovar_key=NULL,key_fingerprint=NULL,updated_at=now() WHERE agent_kovar_tokens.status='DELETED'`, a, c.UserID)
	if err != nil {
		return TokenInfo{}, err
	}
	n, err := admission.RowsAffected()
	if err != nil {
		return TokenInfo{}, err
	}
	if n != 1 {
		return TokenInfo{}, httpx.Conflict("KOVAR_TOKEN_ALREADY_BOUND", "agent already has a token or unresolved creation")
	}
	if err = tx.Commit(); err != nil {
		return TokenInfo{}, err
	}
	t, err := s.manage.CreateToken(ctx, c, kovarmanage.CreateTokenRequest{Name: "agent-" + a, RemainQuota: opt.RemainQuota, ExpiredTime: opt.ExpiredTime})
	if err != nil {
		s.recordKeyFailure(ctx, a)
		return TokenInfo{}, err
	}
	key := t.Key
	if !strings.HasPrefix(key, "sk-") {
		key = "sk-" + key
	}
	cipher, err := s.encryption.Encrypt([]byte(key), a+":model-key")
	if err != nil {
		s.recordKeyFailure(ctx, a)
		return TokenInfo{}, err
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	tx2, err := s.db.BeginTx(finish, nil)
	if err != nil {
		return TokenInfo{}, err
	}
	defer tx2.Rollback()
	agent, err := s.auth.LockAgent(finish, tx2, a)
	if err != nil {
		return TokenInfo{}, err
	}
	state := "ACTIVE"
	if identity.StatusError(agent) != nil {
		state = "DELETE_PENDING"
	}
	finalized, err := tx2.ExecContext(finish, `UPDATE agent_kovar_tokens SET kovar_token_id=$2,encrypted_kovar_key=$3,key_fingerprint=decode($4,'hex'),status=$5,provider_status=$6,remain_quota=$7,expired_at=CASE WHEN $8::bigint<0 THEN NULL ELSE to_timestamp($8) END,updated_at=now() WHERE agent_address=$1 AND status='CREATING'`, a, t.ID, cipher, secure.Hash([]byte(key)), state, t.Status, t.RemainQuota, t.ExpiredTime)
	if err != nil {
		if database.Unique(err) {
			return TokenInfo{}, httpx.Conflict("KOVAR_KEY_ALREADY_BOUND", "Kovar key is already assigned to an agent")
		}
		return TokenInfo{}, err
	}
	count, err := finalized.RowsAffected()
	if err != nil {
		return TokenInfo{}, err
	}
	if count != 1 {
		return TokenInfo{}, httpx.Conflict("KOVAR_TOKEN_RECONCILIATION_REQUIRED", "token creation state changed")
	}
	if err = audit.Record(finish, tx2, "agent", a, "Kovar Key Create", "agent", a, state); err != nil {
		return TokenInfo{}, err
	}
	if err = tx2.Commit(); err != nil {
		return TokenInfo{}, err
	}
	if state != "ACTIVE" {
		_ = s.DeleteKey(finish, a)
		return TokenInfo{}, httpx.E(403, "AGENT_NOT_WHITELISTED", "agent authorization changed while creating token")
	}
	return s.LocalToken(finish, a)
}
func (s *Service) recordKeyFailure(ctx context.Context, a string) {
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	_, _ = s.db.ExecContext(finish, `UPDATE agent_kovar_tokens SET status='UNKNOWN',updated_at=now() WHERE agent_address=$1 AND status='CREATING'`, a)
	_ = audit.Record(finish, s.db, "agent", a, "Kovar Key Create", "agent", a, "RECONCILIATION_REQUIRED")
}
func (s *Service) DeleteKey(ctx context.Context, a string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.auth.LockAgent(ctx, tx, a); err != nil {
		return err
	}
	t, _, err := (repository{tx}).token(ctx, a)
	if err != nil {
		var e *httpx.Error
		if errors.As(err, &e) && e.Code == "KOVAR_TOKEN_NOT_BOUND" {
			return nil
		}
		return err
	}
	if t.Status == "DELETED" {
		return nil
	}
	if t.TokenID == nil {
		return httpx.Conflict("KOVAR_TOKEN_RECONCILIATION_REQUIRED", "token creation outcome is unknown")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_kovar_tokens SET status='DELETE_PENDING',updated_at=now() WHERE agent_address=$1`, a); err != nil {
		return err
	}
	if err = audit.Record(ctx, tx, "gateway", a, "Kovar Key Delete", "agent", a, "DELETE_PENDING"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	c, err := s.Credential(ctx, a)
	if err == nil {
		err = s.manage.DeleteToken(ctx, c, *t.TokenID)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err != nil {
		_ = audit.Record(finish, s.db, "gateway", a, "Kovar Key Delete", "agent", a, httpx.Normalize(err).Code)
		return err
	}
	tx2, err := s.db.BeginTx(finish, nil)
	if err != nil {
		return err
	}
	defer tx2.Rollback()
	if _, err = tx2.ExecContext(finish, `UPDATE agent_kovar_tokens SET status='DELETED',encrypted_kovar_key=NULL,key_fingerprint=NULL,updated_at=now() WHERE agent_address=$1 AND kovar_token_id=$2`, a, *t.TokenID); err != nil {
		return err
	}
	if err = audit.Record(finish, tx2, "gateway", a, "Kovar Key Delete", "agent", a, "DELETED"); err != nil {
		return err
	}
	return tx2.Commit()
}
func (s *Service) Account(ctx context.Context, a string) (Account, error) {
	c, err := s.Credential(ctx, a)
	if err != nil {
		return Account{}, err
	}
	u, err := s.manage.Self(ctx, c)
	if err != nil {
		return Account{}, err
	}
	if u.Quota == nil || u.UsedQuota == nil || u.RequestCount == nil || *u.Quota < 0 || *u.UsedQuota < 0 {
		return Account{}, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "account response lacks documented quota fields")
	}
	available := *u.Quota - *u.UsedQuota
	if available < 0 {
		available = 0
	}
	return Account{u.ID, *u.Quota, *u.UsedQuota, available, *u.RequestCount}, nil
}
func (s *Service) Token(ctx context.Context, a string) (TokenInfo, error) {
	t, err := s.LocalToken(ctx, a)
	if err != nil || t.TokenID == nil || !t.Bound {
		return t, err
	}
	c, err := s.Credential(ctx, a)
	if err != nil {
		return t, err
	}
	up, err := s.manage.Token(ctx, c, *t.TokenID)
	if err != nil {
		return t, err
	}
	if up.Status == nil || up.RemainQuota == nil || up.ExpiredTime == nil {
		return t, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "token response lacks quota/status/expiry")
	}
	t.ProviderStatus = up.Status
	t.RemainQuota = up.RemainQuota
	t.ExpiresAt = nil
	if *up.ExpiredTime >= 0 {
		x := time.Unix(*up.ExpiredTime, 0)
		t.ExpiresAt = &x
	}
	_, err = s.db.ExecContext(ctx, `UPDATE agent_kovar_tokens SET provider_status=$2,remain_quota=$3,expired_at=$4,updated_at=now() WHERE agent_address=$1`, a, t.ProviderStatus, t.RemainQuota, t.ExpiresAt)
	return t, err
}
func (s *Service) Usage(ctx context.Context, a string) (json.RawMessage, error) {
	key, _, err := s.Key(ctx, a)
	if err != nil {
		return nil, err
	}
	data, err := s.manage.Usage(ctx, key)
	if err != nil {
		return nil, err
	}
	return sanitize(data, key), nil
}
func (s *Service) Pricing(ctx context.Context, a string) (json.RawMessage, error) {
	c, err := s.Credential(ctx, a)
	if err != nil {
		return nil, err
	}
	return s.manage.Pricing(ctx, c)
}
func (s *Service) Read(ctx context.Context, a, operation string, page, size int) (json.RawMessage, error) {
	c, err := s.Credential(ctx, a)
	if err != nil {
		return nil, err
	}
	data, err := s.manage.Read(ctx, c, operation)
	if err != nil {
		return nil, err
	}
	secrets := []string{c.Session, c.AccessToken}
	if key, _, e := s.Key(ctx, a); e == nil {
		secrets = append(secrets, key, strings.TrimPrefix(key, "sk-"))
	}
	data = sanitize(data, secrets...)
	if operation == "logs" || operation == "topups" || operation == "tasks" {
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			var p struct {
				Items []json.RawMessage `json:"items"`
			}
			if json.Unmarshal(data, &p) != nil || p.Items == nil {
				return nil, httpx.E(502, "KOVAR_CONTRACT_INCOMPLETE", "list response lacks an array or PageInfo.items")
			}
			items = p.Items
		}
		start := (page - 1) * size
		if start > len(items) {
			start = len(items)
		}
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		return json.Marshal(map[string]any{"items": items[start:end], "page": page, "page_size": size, "total": len(items)})
	}
	return data, nil
}
func (s *Service) Topup(ctx context.Context, a, provider string, payload json.RawMessage) (json.RawMessage, error) {
	c, err := s.Credential(ctx, a)
	if err != nil {
		return nil, err
	}
	return s.manage.Topup(ctx, c, provider, payload)
}

type Summary struct {
	UserID         *int64     `json:"kovar_user_id"`
	TokenID        *int64     `json:"kovar_token_id"`
	KeyBound       bool       `json:"key_bound"`
	Status         *string    `json:"token_binding_status"`
	ProviderStatus *int       `json:"kovar_token_status"`
	ExpiresAt      *time.Time `json:"expired_at"`
	RemainQuota    *int64     `json:"remain_quota"`
}

func (s *Service) Summaries(ctx context.Context, addresses []string) (map[string]Summary, error) {
	out := map[string]Summary{}
	rows, err := s.db.QueryContext(ctx, `SELECT b.agent_address,b.kovar_user_id,t.kovar_token_id,COALESCE(t.status='ACTIVE',false),t.status,t.provider_status,t.expired_at,t.remain_quota FROM agent_user_bindings b LEFT JOIN agent_kovar_tokens t ON t.agent_address=b.agent_address WHERE b.agent_address=ANY($1)`, addresses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var v Summary
		if err = rows.Scan(&a, &v.UserID, &v.TokenID, &v.KeyBound, &v.Status, &v.ProviderStatus, &v.ExpiresAt, &v.RemainQuota); err != nil {
			return nil, err
		}
		out[a] = v
	}
	return out, rows.Err()
}

func (s *Service) Cleanup(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_kovar_logins WHERE expires_at<now()`)
	return err
}
