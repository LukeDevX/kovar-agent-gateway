package task

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"kovar-gateway/internal/audit"
	"kovar-gateway/internal/binding"
	"kovar-gateway/internal/client/kovarmodel"
	"kovar-gateway/internal/identity"
	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/platform/secure"
	"kovar-gateway/internal/platform/upstream"
)

type Service struct {
	db      *sql.DB
	auth    *identity.Service
	binding *binding.Service
	model   *kovarmodel.KovarModelClient
	router  *ModelRouter
}

func New(db *sql.DB, auth *identity.Service, b *binding.Service, m *kovarmodel.KovarModelClient, r *ModelRouter) *Service {
	return &Service{db, auth, b, m, r}
}
func (s *Service) Models(ctx context.Context, a string) ([]kovarmodel.Model, error) {
	key, _, err := s.binding.Key(ctx, a)
	if err != nil {
		return nil, err
	}
	return s.model.Models(ctx, key)
}
func (s *Service) Summary(ctx context.Context, a string) (Summary, error) {
	return (repository{s.db}).summary(ctx, a)
}
func (s *Service) List(ctx context.Context, a string, p, n int) ([]Task, error) {
	return (repository{s.db}).list(ctx, a, p, n)
}
func (s *Service) Usage(ctx context.Context, a string, p, n int) (any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT jsonb_build_object('task_id',task_id,'kovar_token_id',kovar_token_id,'task_type',task_type,'model',model,'prompt_tokens',prompt_tokens,'completion_tokens',completion_tokens,'total_tokens',total_tokens,'estimated_cost',estimated_cost,'actual_cost',actual_cost,'created_at',created_at) FROM agent_usage_records WHERE agent_address=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, a, n, (p-1)*n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b json.RawMessage
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sum, err := s.Summary(ctx, a)
	if err != nil {
		return nil, err
	}
	return map[string]any{"records": out, "summary": sum, "page": p, "page_size": n, "cost_unit": "Kovar quota units", "budget_usage_basis": "actual when known; otherwise conservative estimate"}, nil
}
func operation(in Input) (string, error) {
	if in.Payload == nil {
		return "", httpx.Invalid("payload object is required")
	}
	if in.TaskType == "image" {
		if in.Operation != "" && in.Operation != "generate" && in.Operation != "edit" {
			return "", httpx.Invalid("invalid image operation")
		}
		op := "image"
		if in.Operation == "edit" {
			op = "image_edit"
		}
		switch in.Protocol {
		case "", "openai":
		case "qwen":
			op = "qwen_" + op
		default:
			return "", httpx.NotSupported("unsupported image protocol")
		}
		return op, nil
	}
	if in.Operation != "" || in.Protocol != "" {
		return "", httpx.Invalid("operation and protocol are only supported for images")
	}
	switch in.TaskType {
	case "chat", "video", "speech", "transcription", "embedding", "rerank":
		return in.TaskType, nil
	default:
		return "", httpx.NotSupported("unsupported task type")
	}
}

// StreamSink receives individual SSE lines as soon as they arrive. A nil line
// starts the response. Its caller owns flushing and downstream cancellation.
type StreamSink func(taskID string, line []byte) error

func (s *Service) Create(ctx context.Context, a, key, hash string, in Input, sink StreamSink) (Task, bool, error) {
	if !httpx.Identifier(key) {
		return Task{}, false, httpx.Invalid("Idempotency-Key required")
	}
	if err := s.auth.Active(ctx, a); err != nil {
		return Task{}, false, err
	}
	if t, found, err := (repository{s.db}).existing(ctx, a, key, hash); err != nil || found {
		return t, false, err
	}
	op, err := operation(in)
	if err != nil {
		return Task{}, false, err
	}
	if len(in.Files) > 0 && op != "image_edit" && op != "transcription" {
		return Task{}, false, httpx.Invalid("files require image edit or transcription")
	}
	if pm, ok := in.Payload["model"]; ok {
		v, ok := pm.(string)
		if !ok || v == "" || (in.Model != "" && in.Model != v) {
			return Task{}, false, httpx.Invalid("conflicting model fields")
		}
		in.Model = v
	}
	raw, err := json.Marshal(in.Payload)
	if err != nil {
		return Task{}, false, httpx.Invalid("invalid payload")
	}
	size := int64(len(raw))
	for _, f := range in.Files {
		size += int64(len(f.Data))
	}
	secret, tokenID, err := s.binding.Key(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	bound, err := s.binding.Get(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	available, err := s.model.Models(ctx, secret)
	if err != nil {
		return Task{}, false, err
	}
	models := map[string]bool{}
	for _, m := range available {
		models[m.ID] = true
	}
	if in.Model != "" && !models[in.Model] {
		return Task{}, false, httpx.E(400, "MODEL_NOT_AVAILABLE", "requested model is not available for agent key")
	}
	account, err := s.binding.Account(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	if _, err = s.binding.Usage(ctx, a); err != nil {
		return Task{}, false, err
	}
	token, err := s.binding.Token(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	if token.RemainQuota == nil || token.ExpiresAt != nil && !time.Now().Before(*token.ExpiresAt) {
		return Task{}, false, httpx.E(402, "INSUFFICIENT_AGENT_TOKEN_QUOTA", "agent token is expired or quota is unknown")
	}
	pricing, err := s.binding.Pricing(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	p, err := s.auth.Policy(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	sum, err := s.Summary(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	var chosen *Rule
	var estimate int64
	var routeErr error
	for _, r := range s.router.Rules {
		if r.TaskType != in.TaskType || !models[r.Model] || (in.Model != "" && r.Model != in.Model) {
			continue
		}
		cost, e := (PricingService{}).Quote(pricing, r, in.Payload, size)
		if e != nil {
			routeErr = e
			continue
		}
		if e = BudgetGuard(p, cost, account.AvailableQuota, *token.RemainQuota, sum.Today, sum.Month); e != nil {
			routeErr = e
			continue
		}
		v := r
		chosen = &v
		estimate = cost
		break
	}
	if chosen == nil {
		if routeErr != nil {
			return Task{}, false, routeErr
		}
		return Task{}, false, httpx.E(503, "PRICING_NOT_CONFIGURED", "no available model has a verified routing and pricing rule")
	}
	in.Payload["model"] = chosen.Model
	if in.TaskType == "chat" && chosen.MaxOutputTokens > 0 {
		if _, ok := in.Payload["max_tokens"]; !ok {
			if _, ok = in.Payload["max_completion_tokens"]; !ok {
				in.Payload["max_completion_tokens"] = chosen.MaxOutputTokens
			}
		}
	}
	req := kovarmodel.Request{Operation: op, Payload: in.Payload, Files: in.Files}
	if err = s.model.Validate(req); err != nil {
		return Task{}, false, err
	}
	stream, _ := in.Payload["stream"].(bool)
	if stream && sink == nil {
		return Task{}, false, httpx.Invalid("stream transport unavailable")
	}
	metadata, _ := json.Marshal(map[string]any{"sha256": hash, "bytes": size, "operation": op, "payload_storage": "omitted"})
	t := Task{TaskID: "task_" + secure.Random()[:32], AgentID: a, UserID: bound.UserID, TokenID: tokenID, Type: in.TaskType, Model: chosen.Model, Estimated: estimate, Request: metadata, IdempotencyKey: key}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, err
	}
	defer tx.Rollback()
	p, err = s.auth.LockActive(ctx, tx, a)
	if err != nil {
		return Task{}, false, err
	}
	if existing, found, e := (repository{tx}).existing(ctx, a, key, hash); e != nil || found {
		return existing, false, e
	}
	sum, err = (repository{tx}).summary(ctx, a)
	if err != nil {
		return Task{}, false, err
	}
	if err = BudgetGuard(p, estimate, account.AvailableQuota, *token.RemainQuota, sum.Today, sum.Month); err != nil {
		return Task{}, false, err
	}
	if err = (repository{tx}).create(ctx, t, hash); err != nil {
		return Task{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO idempotency_records(agent_id,operation,idempotency_key,request_hash,status,response,http_status) VALUES($1,'task',$2,$3,'COMPLETED',jsonb_build_object('task_id',$4::text),202)`, a, key, hash, t.TaskID); err != nil {
		return Task{}, false, err
	}
	if err = audit.Record(ctx, tx, "agent", a, "Task Create", "task", t.TaskID, "CREATED"); err != nil {
		return Task{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, false, err
	}
	if err = s.start(ctx, a, t.TaskID); err != nil {
		finished, e := s.finish(ctx, t, "FAILED", nil, nil, Usage{}, err)
		if e != nil {
			return Task{}, false, e
		}
		return finished, false, nil
	}
	resp, err := s.model.Call(ctx, secret, req)
	if err != nil {
		finished, e := s.finish(ctx, t, terminal(ctx), nil, nil, Usage{}, err)
		return finished, false, e
	}
	if stream {
		return s.stream(ctx, t, resp, secret, sink)
	}
	raw, err = upstream.Read(resp)
	if err != nil {
		finished, e := s.finish(ctx, t, terminal(ctx), nil, nil, Usage{}, err)
		return finished, false, e
	}
	raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[REDACTED]"))
	var result json.RawMessage
	usage := Usage{}
	if in.TaskType == "speech" {
		result, _ = json.Marshal(map[string]any{"content_type": resp.Header.Get("Content-Type"), "audio_base64": base64.StdEncoding.EncodeToString(raw)})
	} else if in.TaskType == "transcription" && isTextTranscription(in.Payload) {
		result, _ = json.Marshal(map[string]string{"text": string(raw)})
	} else {
		if !json.Valid(raw) {
			finished, e := s.finish(ctx, t, "FAILED", nil, nil, usage, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "Kovar returned invalid JSON"))
			return finished, false, e
		}
		result = raw
		usage, err = extractUsage(raw)
		if err != nil {
			finished, e := s.finish(ctx, t, "FAILED", nil, nil, Usage{}, err)
			return finished, false, e
		}
		var upstreamError struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(raw, &upstreamError)
		if len(upstreamError.Error) > 0 && string(upstreamError.Error) != "null" {
			finished, e := s.finish(ctx, t, "FAILED", nil, nil, usage, httpx.E(502, "UPSTREAM_ERROR", "Kovar returned a model error"))
			return finished, false, e
		}
	}
	if in.TaskType == "video" {
		var video struct {
			ID     string `json:"task_id"`
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &video) != nil || !httpx.Identifier(video.ID) {
			finished, e := s.finish(ctx, t, "FAILED", nil, nil, usage, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "video response lacks task_id"))
			return finished, false, e
		}
		switch video.Status {
		case "queued", "in_progress":
			finished, e := s.finish(ctx, t, "RUNNING", &video.ID, result, usage, nil)
			return finished, false, e
		case "completed":
			finished, e := s.finish(ctx, t, "SUCCEEDED", &video.ID, result, usage, nil)
			return finished, false, e
		case "failed":
			finished, e := s.finish(ctx, t, "FAILED", &video.ID, result, usage, httpx.E(502, "UPSTREAM_ERROR", "video generation failed"))
			return finished, false, e
		default:
			finished, e := s.finish(ctx, t, "FAILED", &video.ID, result, usage, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "unknown video status"))
			return finished, false, e
		}
	}
	finished, e := s.finish(ctx, t, "SUCCEEDED", nil, result, usage, nil)
	return finished, false, e
}
func (s *Service) start(ctx context.Context, a, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.auth.LockActive(ctx, tx, a); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE gateway_tasks SET status='RUNNING',started_at=now(),updated_at=now() WHERE task_id=$1 AND status='CREATED'`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return httpx.Conflict("TASK_ALREADY_STARTED", "task already started")
	}
	return tx.Commit()
}
func terminal(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "CANCELLED"
	}
	return "FAILED"
}
func (s *Service) finish(ctx context.Context, t Task, status string, provider *string, result json.RawMessage, u Usage, cause error) (Task, error) {
	// Only this bounded database finalization survives a client disconnect. Model
	// HTTP requests always use the original cancellable request context.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(finish, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	var code, message *string
	if cause != nil {
		e := httpx.Normalize(cause)
		code = &e.Code
		message = &e.Message
	}
	r, err := tx.ExecContext(finish, `UPDATE gateway_tasks SET status=$2,provider_task_id=COALESCE($3,provider_task_id),result_payload=$4,prompt_tokens=$5,completion_tokens=$6,total_tokens=$7,error_code=$8,error_message=$9,completed_at=CASE WHEN $2='RUNNING' THEN NULL ELSE now() END,updated_at=now() WHERE task_id=$1 AND status IN ('CREATED','RUNNING')`, t.TaskID, status, provider, nullableJSON(result), u.Prompt, u.Completion, u.Total, code, message)
	if err != nil {
		return Task{}, err
	}
	count, err := r.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if count == 1 && status != "RUNNING" {
		_, err = tx.ExecContext(finish, `INSERT INTO agent_usage_records(agent_address,task_id,kovar_token_id,task_type,model,prompt_tokens,completion_tokens,total_tokens,estimated_cost,actual_cost) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL) ON CONFLICT(task_id) DO NOTHING`, t.AgentID, t.TaskID, t.TokenID, t.Type, t.Model, u.Prompt, u.Completion, u.Total, t.Estimated)
		if err != nil {
			return Task{}, err
		}
		if cause != nil {
			if err = audit.Record(finish, tx, "gateway", t.AgentID, "Task Failed", "task", t.TaskID, *code); err != nil {
				return Task{}, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return (repository{s.db}).get(finish, t.AgentID, t.TaskID)
}
func nullableJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}
func extractUsage(raw []byte) (Usage, error) {
	var r struct {
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
			Total      int64 `json:"total_tokens"`
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return Usage{}, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "invalid model usage")
	}
	u := Usage{r.Usage.Prompt, r.Usage.Completion, r.Usage.Total}
	if u.Prompt == 0 {
		u.Prompt = r.Usage.Input
	}
	if u.Completion == 0 {
		u.Completion = r.Usage.Output
	}
	if u.Prompt < 0 || u.Completion < 0 || u.Total < 0 {
		return Usage{}, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "negative model usage")
	}
	return u, nil
}
func isTextTranscription(p map[string]any) bool {
	v, _ := p["response_format"].(string)
	return v == "text" || v == "srt" || v == "vtt"
}
func (s *Service) stream(ctx context.Context, t Task, resp *http.Response, secret string, sink StreamSink) (Task, bool, error) {
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		finished, e := s.finish(ctx, t, "FAILED", nil, nil, Usage{}, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "expected SSE response"))
		return finished, false, e
	}
	if err := sink(t.TaskID, nil); err != nil {
		finished, e := s.finish(ctx, t, "CANCELLED", nil, nil, Usage{}, err)
		return finished, true, e
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	u := Usage{}
	done := false
	size := 0
	var failure error
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		size += len(line)
		if size > upstream.MaxResponse {
			failure = httpx.E(502, "UPSTREAM_RESPONSE_TOO_LARGE", "stream exceeded response limit")
			break
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if bytes.Equal(data, []byte("[DONE]")) {
				done = true
			} else if len(data) > 0 {
				if !json.Valid(data) {
					failure = httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "invalid SSE JSON")
					break
				}
				var event map[string]json.RawMessage
				_ = json.Unmarshal(data, &event)
				if _, ok := event["error"]; ok {
					failure = httpx.E(502, "UPSTREAM_ERROR", "Kovar stream failed")
					break
				}
				if _, ok := event["usage"]; ok {
					var e error
					u, e = extractUsage(data)
					if e != nil {
						failure = e
						break
					}
				}
			}
		}
		line = bytes.ReplaceAll(line, []byte(secret), []byte("[REDACTED]"))
		line = append(line, '\n')
		if err := sink(t.TaskID, line); err != nil {
			failure = err
			break
		}
	}
	if failure == nil {
		failure = scanner.Err()
	}
	if failure == nil && !done {
		failure = httpx.E(502, "UPSTREAM_STREAM_INCOMPLETE", "Kovar stream ended without DONE")
	}
	status := "SUCCEEDED"
	if failure != nil {
		status = terminal(ctx)
		e := httpx.Normalize(failure)
		frame, _ := json.Marshal(map[string]string{"code": e.Code, "message": e.Message})
		_ = sink(t.TaskID, []byte("event: gateway_error\ndata: "+string(frame)+"\n\n"))
	}
	result, _ := json.Marshal(map[string]any{"streamed": true, "usage": u})
	finished, err := s.finish(ctx, t, status, nil, result, u, failure)
	return finished, true, err
}
func (s *Service) Get(ctx context.Context, a, id string) (Task, error) {
	if !httpx.Identifier(id) {
		return Task{}, httpx.Invalid("invalid task id")
	}
	t, err := (repository{s.db}).get(ctx, a, id)
	if err != nil {
		return t, err
	}
	if t.Type != "video" || t.Status != "RUNNING" || t.ProviderID == nil {
		return t, nil
	}
	key, tokenID, err := s.binding.Key(ctx, a)
	if err != nil {
		return t, err
	}
	if tokenID != t.TokenID {
		return t, httpx.Conflict("TASK_TOKEN_CHANGED", "task belongs to a previous Kovar key")
	}
	raw, err := s.model.Video(ctx, key, *t.ProviderID)
	if err != nil {
		return t, err
	}
	raw = bytes.ReplaceAll(raw, []byte(key), []byte("[REDACTED]"))
	var v struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &v) != nil || v.TaskID != *t.ProviderID {
		return t, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "invalid video task response")
	}
	switch v.Status {
	case "queued", "in_progress":
		return t, nil
	case "completed":
		return s.finish(ctx, t, "SUCCEEDED", t.ProviderID, raw, Usage{}, nil)
	case "failed":
		return s.finish(ctx, t, "FAILED", t.ProviderID, nil, Usage{}, httpx.E(502, "UPSTREAM_ERROR", "video generation failed"))
	default:
		return t, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "unknown video status")
	}
}

// No background retry of billable requests. Operators can inspect an interrupted
// RUNNING task; a request with the same idempotency key always returns that task.

func (s *Service) Summaries(ctx context.Context, addresses []string) (map[string]Summary, error) {
	out := map[string]Summary{}
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,COALESCE(SUM(COALESCE(actual_cost,estimated_cost)) FILTER(WHERE created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),0),COALESCE(SUM(COALESCE(actual_cost,estimated_cost)) FILTER(WHERE created_at>=date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),0),COUNT(*),COUNT(*) FILTER(WHERE actual_cost IS NULL) FROM gateway_tasks WHERE agent_id=ANY($1) GROUP BY agent_id`, addresses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var v Summary
		if err = rows.Scan(&a, &v.Today, &v.Month, &v.Count, &v.UnknownCost); err != nil {
			return nil, err
		}
		out[a] = v
	}
	return out, rows.Err()
}
