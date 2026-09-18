package audit

import (
	"context"

	"kovar-gateway/internal/platform/database"
	"kovar-gateway/internal/platform/httpx"
)

// Metadata is deliberately a fixed, secret-free result code. Request bodies,
// upstream error messages, and credentials never enter the audit interface.
func Record(ctx context.Context, db database.DBTX, actorType, actor, action, targetType, target, result string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO audit_logs(actor_type,actor_id,action,target_type,target_id,request_id,metadata) VALUES($1,$2,$3,$4,$5,$6,jsonb_build_object('result',$7::text))`, actorType, actor, action, targetType, target, httpx.RequestID(ctx), result)
	return err
}
