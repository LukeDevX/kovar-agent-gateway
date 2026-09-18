CREATE TABLE gateway_admins (
 id bigserial PRIMARY KEY, username text NOT NULL UNIQUE, password_hash text NOT NULL,
 status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','DISABLED')),
 last_login_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE admin_sessions (
 token_hash bytea PRIMARY KEY, admin_id bigint NOT NULL REFERENCES gateway_admins(id), expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_sessions_expiry ON admin_sessions(expires_at);
CREATE TABLE agents (
 address text PRIMARY KEY CHECK(address ~ '^0x[0-9a-f]{40}$'),
 status text NOT NULL CHECK(status IN ('REGISTERED','ACTIVE','SUSPENDED','REVOKED')),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz, revoked_at timestamptz
);
CREATE INDEX agents_status_created ON agents(status,created_at);
CREATE TABLE agent_challenges (
 id bigserial PRIMARY KEY, agent_address text NOT NULL CHECK(agent_address ~ '^0x[0-9a-f]{40}$'), nonce text NOT NULL UNIQUE,
 purpose text NOT NULL CHECK(purpose IN ('REGISTER','REQUEST')), expires_at timestamptz NOT NULL,
 used_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX challenges_address ON agent_challenges(agent_address);
CREATE INDEX challenges_expiry ON agent_challenges(expires_at);
CREATE TABLE agent_whitelist (
 id bigserial PRIMARY KEY, agent_address text NOT NULL UNIQUE REFERENCES agents(address),
 status text NOT NULL CHECK(status IN ('PENDING','APPROVED','REJECTED','REVOKED')),
 reviewed_by bigint REFERENCES gateway_admins(id), reviewed_at timestamptz, reason text NOT NULL DEFAULT '', remark text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX whitelist_status_created ON agent_whitelist(status,created_at);
CREATE TABLE agent_user_bindings (
 id bigserial PRIMARY KEY, agent_address text NOT NULL UNIQUE REFERENCES agents(address), kovar_user_id bigint NOT NULL CHECK(kovar_user_id>0),
 status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','DISABLED')),
 encrypted_session bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(agent_address,kovar_user_id)
);
CREATE INDEX bindings_user ON agent_user_bindings(kovar_user_id);
CREATE TABLE pending_kovar_logins (
 agent_address text PRIMARY KEY REFERENCES agents(address), encrypted_session bytea NOT NULL, expires_at timestamptz NOT NULL
);
CREATE TABLE agent_kovar_tokens (
 id bigserial PRIMARY KEY, agent_address text NOT NULL UNIQUE REFERENCES agents(address), kovar_user_id bigint NOT NULL,
 kovar_token_id bigint UNIQUE, encrypted_kovar_key bytea, key_fingerprint bytea UNIQUE,
 status text NOT NULL CHECK(status IN ('CREATING','ACTIVE','DELETE_PENDING','DELETED','UNKNOWN')),
 provider_status integer, remain_quota bigint, expired_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(agent_address,kovar_user_id) REFERENCES agent_user_bindings(agent_address,kovar_user_id),
 CHECK(status <> 'ACTIVE' OR (kovar_token_id IS NOT NULL AND encrypted_kovar_key IS NOT NULL AND key_fingerprint IS NOT NULL))
);
CREATE INDEX tokens_user ON agent_kovar_tokens(kovar_user_id);
CREATE TABLE agent_budget_policies (
 agent_address text PRIMARY KEY REFERENCES agents(address), per_request_limit bigint NOT NULL CHECK(per_request_limit>=0),
 daily_limit bigint NOT NULL CHECK(daily_limit>=0), monthly_limit bigint NOT NULL CHECK(monthly_limit>=0),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE gateway_tasks (
 id bigserial PRIMARY KEY, task_id text NOT NULL UNIQUE, agent_id text NOT NULL REFERENCES agents(address),
 kovar_user_id bigint NOT NULL, kovar_token_id bigint NOT NULL, task_type text NOT NULL CHECK(task_type IN ('chat','image','video','speech','transcription','embedding','rerank')),
 model text NOT NULL, provider_task_id text, request_payload jsonb NOT NULL, result_payload jsonb,
 estimated_cost bigint NOT NULL CHECK(estimated_cost>=0), actual_cost bigint CHECK(actual_cost>=0),
 prompt_tokens bigint NOT NULL DEFAULT 0 CHECK(prompt_tokens>=0), completion_tokens bigint NOT NULL DEFAULT 0 CHECK(completion_tokens>=0), total_tokens bigint NOT NULL DEFAULT 0 CHECK(total_tokens>=0),
 status text NOT NULL CHECK(status IN ('CREATED','RUNNING','SUCCEEDED','FAILED','CANCELLED')),
 error_code text, error_message text, idempotency_key text NOT NULL, request_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, completed_at timestamptz, updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(agent_id,idempotency_key)
);
CREATE INDEX tasks_agent_created ON gateway_tasks(agent_id,created_at DESC);
CREATE INDEX tasks_status_updated ON gateway_tasks(status,updated_at);
CREATE INDEX tasks_token ON gateway_tasks(kovar_token_id);
CREATE INDEX tasks_user ON gateway_tasks(kovar_user_id);
CREATE TABLE agent_usage_records (
 id bigserial PRIMARY KEY, agent_address text NOT NULL REFERENCES agents(address), task_id text NOT NULL UNIQUE REFERENCES gateway_tasks(task_id),
 kovar_token_id bigint NOT NULL, task_type text NOT NULL, model text NOT NULL,
 prompt_tokens bigint NOT NULL CHECK(prompt_tokens>=0), completion_tokens bigint NOT NULL CHECK(completion_tokens>=0), total_tokens bigint NOT NULL CHECK(total_tokens>=0),
 estimated_cost bigint NOT NULL CHECK(estimated_cost>=0), actual_cost bigint CHECK(actual_cost>=0), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_agent_created ON agent_usage_records(agent_address,created_at);
CREATE INDEX usage_token ON agent_usage_records(kovar_token_id);
CREATE TABLE idempotency_records (
 agent_id text NOT NULL, operation text NOT NULL, idempotency_key text NOT NULL,
 request_hash text NOT NULL, response jsonb, status text NOT NULL CHECK(status IN ('PROCESSING','COMPLETED','UNKNOWN')),
 http_status integer, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(agent_id,operation,idempotency_key)
);
CREATE INDEX idempotency_created ON idempotency_records(created_at);
CREATE TABLE audit_logs (
 id bigserial PRIMARY KEY, actor_type text NOT NULL, actor_id text NOT NULL, action text NOT NULL,
 target_type text NOT NULL, target_id text NOT NULL, request_id text NOT NULL, metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_target_created ON audit_logs(target_id,created_at);
CREATE TABLE rate_limit_windows (
 scope text NOT NULL, subject text NOT NULL, window_start bigint NOT NULL, requests integer NOT NULL CHECK(requests>0),
 PRIMARY KEY(scope,subject,window_start)
);
