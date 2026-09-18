package binding

import (
	"context"
	"database/sql"
	"errors"

	"kovar-gateway/internal/platform/database"
	"kovar-gateway/internal/platform/httpx"
)

type repository struct{ db database.DBTX }

func (r repository) binding(ctx context.Context, a string) (Binding, []byte, error) {
	var b Binding
	var cipher []byte
	err := r.db.QueryRowContext(ctx, `SELECT agent_address,kovar_user_id,status,encrypted_session FROM agent_user_bindings WHERE agent_address=$1`, a).Scan(&b.Agent, &b.UserID, &b.Status, &cipher)
	if errors.Is(err, sql.ErrNoRows) {
		err = httpx.E(409, "KOVAR_USER_NOT_BOUND", "agent has no Kovar user binding")
	}
	return b, cipher, err
}
func (r repository) token(ctx context.Context, a string) (TokenInfo, []byte, error) {
	var t TokenInfo
	var cipher []byte
	err := r.db.QueryRowContext(ctx, `SELECT kovar_token_id,status,provider_status,expired_at,remain_quota,encrypted_kovar_key FROM agent_kovar_tokens WHERE agent_address=$1`, a).Scan(&t.TokenID, &t.Status, &t.ProviderStatus, &t.ExpiresAt, &t.RemainQuota, &cipher)
	if errors.Is(err, sql.ErrNoRows) {
		err = httpx.E(409, "KOVAR_TOKEN_NOT_BOUND", "agent has no Kovar token")
	}
	t.Bound = t.Status == "ACTIVE" && len(cipher) > 0
	return t, cipher, err
}
