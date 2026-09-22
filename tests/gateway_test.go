package tests

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"kovar-gateway/internal/app"
	"kovar-gateway/internal/client/kovarmanage"
	"kovar-gateway/internal/platform/config"
	"kovar-gateway/internal/platform/secure"
	"kovar-gateway/internal/task"
	"kovar-gateway/internal/testutil"
)

type mockKovar struct {
	server           *httptest.Server
	mu               sync.Mutex
	tokens           map[int64]kovarmanage.Token
	next             int64
	models           atomic.Int32
	creates          atomic.Int32
	lastExpiry       atomic.Int64
	topups           atomic.Int32
	topupFailure     atomic.Int32
	paygo            atomic.Int32
	userModels       atomic.Int32
	keyModels        atomic.Int32
	deletes          atomic.Int32
	failure          atomic.Int32
	delay            atomic.Int64
	deleteFail       atomic.Bool
	streamDisconnect atomic.Bool
}

func newMock(t *testing.T) *mockKovar {
	t.Helper()
	m := &mockKovar{tokens: map[int64]kovarmanage.Token{}, next: 100}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}
func (m *mockKovar) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	send := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
	if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/user/login" && r.URL.Path != "/api/user/register" && r.URL.Path != "/api/usage/token/" {
		if r.Header.Get("New-Api-User") == "" {
			w.WriteHeader(401)
			return
		}
	}
	switch {
	case r.URL.Path == "/api/user/login":
		var in kovarmanage.LoginRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		id := int64(7)
		if in.Username == "other" {
			id = 8
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: fmt.Sprintf("fixture-user-%d", id)})
		send(map[string]any{"id": id})
	case r.URL.Path == "/api/user/register":
		send(nil)
	case r.URL.Path == "/api/user/self":
		var id int64
		_, _ = fmt.Sscan(r.Header.Get("New-Api-User"), &id)
		send(map[string]any{"id": id, "quota": 1000000, "used_quota": 10, "request_count": 1})
	case r.URL.Path == "/api/token/" && r.Method == "POST":
		m.creates.Add(1)
		var in kovarmanage.CreateTokenRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		m.lastExpiry.Store(in.ExpiredTime)
		m.mu.Lock()
		m.next++
		id := m.next
		status := 1
		token := kovarmanage.Token{ID: id, UserID: 7, Name: in.Name, Key: fmt.Sprintf("sk-fixture-%d", id), Status: &status, ExpiredTime: &in.ExpiredTime, RemainQuota: &in.RemainQuota}
		m.tokens[id] = token
		m.mu.Unlock()
		send(nil)
	case r.URL.Path == "/api/token/search":
		m.mu.Lock()
		items := []kovarmanage.Token{}
		for _, token := range m.tokens {
			if token.Name == r.URL.Query().Get("keyword") {
				token.Key = "sk-****masked"
				items = append(items, token)
			}
		}
		m.mu.Unlock()
		send(map[string]any{"items": items, "total": len(items)})
	case strings.HasPrefix(r.URL.Path, "/api/token/"):
		var id int64
		_, _ = fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/token/"), &id)
		if r.Method == "DELETE" {
			m.deletes.Add(1)
			if m.deleteFail.Load() {
				w.WriteHeader(500)
				return
			}
			m.mu.Lock()
			delete(m.tokens, id)
			m.mu.Unlock()
			send(nil)
			return
		}
		m.mu.Lock()
		token, ok := m.tokens[id]
		m.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/key") && r.Method == "POST" {
			send(map[string]string{"key": token.Key})
			return
		}
		token.Key = "sk-****masked"
		send(token)
	case r.URL.Path == "/api/usage/token/":
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-fixture-") {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": true, "message": "ok", "data": map[string]any{"fixture_usage": 0}})
	case r.URL.Path == "/api/pricing":
		send(map[string]any{"fixture-chat": "2", "fixture-image": "3", "fixture-video": "4", "fixture-speech": "1", "fixture-transcription": "1", "fixture-embedding": "1", "fixture-rerank": "1"})
	case r.URL.Path == "/api/log/self":
		send([]any{})
	case r.URL.Path == "/api/user/topup/self":
		send(map[string]any{"items": []any{map[string]string{"trade_no": "page-" + r.URL.Query().Get("p"), "keyword": r.URL.Query().Get("keyword")}}, "total": 41})
	case r.URL.Path == "/api/user/models":
		m.userModels.Add(1)
		send([]string{"fixture-chat", "user-only-model"})
	case r.URL.Path == "/api/user/topup/status":
		if r.URL.Query().Get("trade_no") != "AXONE-7-test" || r.Header.Get("New-Api-User") != "7" {
			fmt.Fprint(w, `{"success":false,"message":"topup order not found"}`)
			return
		}
		send(map[string]any{"trade_no": "AXONE-7-test", "status": "pending", "amount": 10, "money": json.Number("10.01"), "session": "fixture-user-7"})
	case r.URL.Path == "/api/user/axone/chains":
		send([]any{map[string]string{"chain_id": "fixture-chain"}})
	case r.URL.Path == "/api/user/axone/wallets":
		send(map[string]any{"total": 1, "current": 1, "list": []any{map[string]any{"id": "fixture-wallet", "currency": "USDC", "amount": 100, "total_balance": 100}}})
	case r.URL.Path == "/api/user/axone/order":
		m.topups.Add(1)
		if status := m.topupFailure.Load(); status != 0 {
			w.WriteHeader(int(status))
			return
		}
		send(map[string]any{"trade_no": "AXONE-7-test", "address": "fixture-payment-address", "status": "pending", "payment_money": "10.01", "access_token": "do-not-return"})
	case r.URL.Path == "/api/user/axone/paygo/sessions" && r.Method == "POST":
		m.paygo.Add(1)
		if r.Header.Get("Idempotency-Key") == "" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"success":false,"message":"Idempotency-Key is required"}`)
			return
		}
		var in struct {
			WalletID  string `json:"wallet_id"`
			MaxAmount string `json:"max_amount"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		send(map[string]any{"id": 3, "session_id": "fixture-session", "wallet_id": in.WalletID, "currency": "USDC", "status": "active", "reserved_q8": 100000000})
	case r.URL.Path == "/api/user/axone/paygo/sessions" && r.Method == "GET":
		send([]any{map[string]any{"id": 3, "session_id": "fixture-session", "status": "active"}})
	case strings.HasPrefix(r.URL.Path, "/api/user/axone/paygo/sessions/"):
		if !strings.HasSuffix(r.URL.Path, "/fixture-session") && r.Method != "POST" {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"success":false,"message":"AXOne PayGo session not found"}`)
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/close") {
			if r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"success":false,"message":"Idempotency-Key is required"}`)
				return
			}
			send(map[string]any{"id": 3, "session_id": "fixture-session", "status": "closed"})
			return
		}
		send(map[string]any{"id": 3, "session_id": "fixture-session", "status": "active"})
	case r.URL.Path == "/api/log/self/stat" || r.URL.Path == "/api/user/topup/info":
		send(map[string]any{})
	case r.URL.Path == "/v1/models":
		m.keyModels.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "fixture-chat"}, map[string]string{"id": "fixture-image"}, map[string]string{"id": "fixture-video"}, map[string]string{"id": "fixture-speech"}, map[string]string{"id": "fixture-transcription"}, map[string]string{"id": "fixture-embedding"}, map[string]string{"id": "fixture-rerank"}}})
	case r.URL.Path == "/v1/video/generations/provider1":
		fmt.Fprint(w, `{"task_id":"provider1","status":"completed","url":"https://example.com/video.mp4"}`)
	case strings.HasPrefix(r.URL.Path, "/v1/"):
		m.models.Add(1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-fixture-") {
			w.WriteHeader(401)
			return
		}
		if delay := m.delay.Load(); delay > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Duration(delay)):
			}
		}
		if status := m.failure.Load(); status != 0 {
			w.WriteHeader(int(status))
			fmt.Fprint(w, `{"error":{"message":"secret fixture should not be surfaced"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if stream, _ := body["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if m.streamDisconnect.Load() {
					<-r.Context().Done()
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
				return
			}
			fmt.Fprint(w, `{"id":"completion1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/images/generations/", "/v1/images/edits/":
			fmt.Fprint(w, `{"data":[{"url":"https://example.com/image.png"}]}`)
		case "/v1/video/generations":
			fmt.Fprint(w, `{"task_id":"provider1","status":"queued"}`)
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "fixture-audio")
		case "/v1/audio/transcriptions":
			fmt.Fprint(w, `{"text":"hello"}`)
		case "/v1/embeddings":
			fmt.Fprint(w, `{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		case "/v1/rerank":
			fmt.Fprint(w, `{"results":[{"index":0,"relevance_score":1}]}`)
		default:
			w.WriteHeader(404)
		}
	default:
		w.WriteHeader(404)
	}
}

type fixture struct {
	db             *sql.DB
	gateway        *app.Server
	http           *httptest.Server
	mock           *mockKovar
	key            *ecdsa.PrivateKey
	address, admin string
	cfg            config.Config
}

func setup(t *testing.T) *fixture {
	return setupWithRequestTimeout(t, 2*time.Second)
}

func setupWithRequestTimeout(t *testing.T, requestTimeout time.Duration) *fixture {
	t.Helper()
	db := testutil.Database(t)
	m := newMock(t)
	rules := []task.Rule{}
	for _, kind := range []string{"chat", "image", "video", "speech", "transcription", "embedding", "rerank"} {
		rules = append(rules, task.Rule{TaskType: kind, Model: "fixture-" + kind, PricingPointer: "/fixture-" + kind, QuotaMultiplier: "1", Unit: "request", MaxInputBytes: 8 << 20, MaxOutputTokens: 128})
	}
	b, _ := json.Marshal(task.ModelRouter{Rules: rules})
	path := filepath.Join(t.TempDir(), "router.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Env: "test", ManageURL: m.server.URL, ModelURL: m.server.URL, AdminUsername: "admin", AdminPassword: "fixture-admin-password", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)), RouterConfig: path, HTTPTimeout: 500 * time.Millisecond, RequestTimeout: requestTimeout, ClockSkew: 5 * time.Minute, NonceTTL: 5 * time.Minute, AdminTTL: time.Hour, PerRequest: 10, Daily: 100, Monthly: 1000, MaxBody: 8 << 20, IPRate: 100000, AgentRate: 100000, LoginRate: 100000}
	g, err := app.New(db, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Service.Identity.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	key, _ := crypto.GenerateKey()
	address, _ := secure.Address(crypto.PubkeyToAddress(key.PublicKey).Hex())
	f := &fixture{db: db, gateway: g, http: server, mock: m, key: key, address: address, cfg: cfg}
	res := f.request(t, "POST", "/api/v1/admin/login", `{"username":"admin","password":"fixture-admin-password"}`, "", false)
	var login struct {
		Token string `json:"access_token"`
	}
	decode(t, res, &login)
	f.admin = login.Token
	f.register(t)
	return f
}
func (f *fixture) request(t *testing.T, method, path, body, idem string, signed bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if signed {
		f.sign(req, []byte(body))
	} else if f.admin != "" {
		req.Header.Set("Authorization", "Bearer "+f.admin)
	}
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func (f *fixture) sign(r *http.Request, body []byte) {
	ts := fmt.Sprint(time.Now().Unix())
	nonce := secure.Random()
	sig, _ := crypto.Sign(secure.PersonalHash(secure.Canonical(r.Method, r.URL.RequestURI(), ts, nonce, r.Header.Get("Idempotency-Key"), body)), f.key)
	r.Header.Set("X-Agent-Id", f.address)
	r.Header.Set("X-Timestamp", ts)
	r.Header.Set("X-Nonce", nonce)
	r.Header.Set("X-Signature", "0x"+hex.EncodeToString(sig))
}
func decode(t *testing.T, res *http.Response, v any) {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode >= 400 {
		t.Fatalf("HTTP %d: %s", res.StatusCode, b)
	}
	if err = json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}
func expectCode(t *testing.T, res *http.Response, status int, code string) {
	t.Helper()
	defer res.Body.Close()
	var e struct {
		Code      string `json:"code"`
		RequestID string `json:"request_id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&e)
	if res.StatusCode != status || e.Code != code {
		t.Fatalf("wanted %d/%s, got %d/%s", status, code, res.StatusCode, e.Code)
	}
	if e.RequestID == "" {
		t.Error("error lacks request ID")
	}
}
func (f *fixture) register(t *testing.T) {
	t.Helper()
	var c struct {
		Nonce string `json:"nonce"`
	}
	decode(t, f.request(t, "POST", "/api/v1/agents/challenge", fmt.Sprintf(`{"address":%q}`, f.address), "", false), &c)
	ts := time.Now().Unix()
	sig, _ := crypto.Sign(secure.PersonalHash(secure.Registration(f.address, c.Nonce, ts)), f.key)
	body, _ := json.Marshal(map[string]any{"address": f.address, "nonce": c.Nonce, "timestamp": ts, "signature": "0x" + hex.EncodeToString(sig)})
	var result map[string]any
	decode(t, f.request(t, "POST", "/api/v1/agents/register", string(body), "registration", false), &result)
}
func (f *fixture) approveAndBind(t *testing.T) {
	t.Helper()
	var v map[string]any
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/approve", `{}`, "", false), &v)
	decode(t, f.request(t, "POST", "/api/v1/kovar/auth/login", `{"username":"fixture","password":"request-only-password"}`, "login", true), &v)
	decode(t, f.request(t, "POST", "/api/v1/agent/token", `{}`, "token", true), &v)
}

const chat = `{"task_type":"chat","payload":{"messages":[{"role":"user","content":"hello"}]}}`

func TestFullFlowAndRevocation(t *testing.T) {
	f := setup(t)
	expectCode(t, f.request(t, "GET", "/api/v1/agents/me", "", "", true), 403, "AGENT_NOT_WHITELISTED")
	f.approveAndBind(t)
	var result task.Task
	decode(t, f.request(t, "POST", "/api/v1/tasks", chat, "task-one", true), &result)
	if result.Status != "SUCCEEDED" || result.Actual != nil || result.TotalTokens != 3 || result.Estimated != 2 {
		t.Fatalf("incorrect completed task: %+v", result)
	}
	var replay task.Task
	decode(t, f.request(t, "POST", "/api/v1/tasks", chat, "task-one", true), &replay)
	if replay.TaskID != result.TaskID || f.mock.models.Load() != 1 {
		t.Fatal("idempotency executed second model task")
	}
	expectCode(t, f.request(t, "POST", "/api/v1/tasks", strings.Replace(chat, "hello", "changed", 1), "task-one", true), 409, "IDEMPOTENCY_CONFLICT")
	var usage map[string]any
	decode(t, f.request(t, "GET", "/api/v1/usage", "", "", true), &usage)
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM agent_usage_records`).Scan(&count); err != nil || count != 1 {
		t.Fatal("usage not saved", err)
	}
	var cipher []byte
	if err := f.db.QueryRow(`SELECT encrypted_kovar_key FROM agent_kovar_tokens WHERE agent_address=$1`, f.address).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte("sk-fixture")) {
		t.Fatal("unencrypted Kovar key")
	}
	var secrets int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE metadata::text LIKE '%password%' OR metadata::text LIKE '%sk-fixture%'`).Scan(&secrets); err != nil || secrets != 0 {
		t.Fatal("secret in audit", err)
	}
	f.mock.deleteFail.Store(true)
	var revoked map[string]any
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/revoke", `{}`, "", false), &revoked)
	if revoked["token_deletion_pending"] != true {
		t.Fatal("missing pending deletion status")
	}
	before := f.mock.models.Load()
	expectCode(t, f.request(t, "POST", "/api/v1/tasks", chat, "after-revoke", true), 403, "AGENT_REVOKED")
	if f.mock.models.Load() != before {
		t.Fatal("revoked request reached model API")
	}
	f.mock.deleteFail.Store(false)
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/revoke", `{}`, "", false), &revoked)
}
func TestConcurrentTaskAndKeyIdempotency(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	f.mock.delay.Store(int64(80 * time.Millisecond))
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.request(t, "POST", "/api/v1/tasks", chat, "concurrent-task", true)
			defer r.Body.Close()
			var result task.Task
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil || r.StatusCode != 200 {
				errs <- fmt.Sprint(r.StatusCode, err)
				return
			}
			ids <- result.TaskID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	id := ""
	for v := range ids {
		if id != "" && id != v {
			t.Error("multiple tasks")
		}
		id = v
	}
	if f.mock.models.Load() != 1 {
		t.Fatal("concurrent requests called model multiple times")
	}
	expectCode(t, f.request(t, "POST", "/api/v1/agent/token", `{}`, "another-key", true), 409, "KOVAR_TOKEN_ALREADY_BOUND")
	if f.mock.creates.Load() != 1 {
		t.Fatal("duplicate upstream token created")
	}
}

func TestDefaultTokenNeverExpires(t *testing.T) {
	// This test runs the complete admin/register/login/token flow. Keep its
	// request timeout tolerant of bcrypt under race-enabled CI builders.
	f := setupWithRequestTimeout(t, 20*time.Second)
	var v map[string]any
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/approve", `{}`, "", false), &v)
	decode(t, f.request(t, "POST", "/api/v1/kovar/auth/login", `{"username":"fixture","password":"request-only-password"}`, "login-permanent", true), &v)
	decode(t, f.request(t, "POST", "/api/v1/agent/token", `{}`, "token-permanent", true), &v)
	if got := f.mock.lastExpiry.Load(); got != -1 {
		t.Fatalf("default expired_time = %d, want -1", got)
	}
	var expiredAt *time.Time
	if err := f.db.QueryRow(`SELECT expired_at FROM agent_kovar_tokens WHERE agent_address=$1`, f.address).Scan(&expiredAt); err != nil {
		t.Fatal(err)
	}
	if expiredAt != nil {
		t.Fatalf("permanent token expired_at = %v, want NULL", expiredAt)
	}
}
func TestModelTasksAndFailures(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	cases := []struct{ key, body, status string }{{"image", `{"task_type":"image","payload":{"prompt":"draw"}}`, "SUCCEEDED"}, {"video", `{"task_type":"video","payload":{"prompt":"film"}}`, "RUNNING"}, {"speech", `{"task_type":"speech","payload":{"input":"hello","voice":"alloy"}}`, "SUCCEEDED"}, {"embedding", `{"task_type":"embedding","payload":{"input":"hello"}}`, "SUCCEEDED"}, {"rerank", `{"task_type":"rerank","payload":{"query":"hello","documents":["one"]}}`, "SUCCEEDED"}}
	for _, c := range cases {
		var out task.Task
		decode(t, f.request(t, "POST", "/api/v1/tasks", c.body, c.key, true), &out)
		if out.Status != c.status {
			t.Fatalf("%s got %s", c.key, out.Status)
		}
		if c.key == "video" {
			decode(t, f.request(t, "GET", "/api/v1/tasks/"+out.TaskID, "", "", true), &out)
			if out.Status != "SUCCEEDED" {
				t.Fatal("video did not finish")
			}
		}
	}
	f.mock.failure.Store(500)
	var out task.Task
	decode(t, f.request(t, "POST", "/api/v1/tasks", chat, "failure", true), &out)
	if out.Status != "FAILED" || out.ErrorCode == nil || *out.ErrorCode != "UPSTREAM_ERROR" || out.Actual != nil {
		t.Fatal("upstream failure not recorded", out)
	}
	f.mock.failure.Store(0)
	f.mock.delay.Store(int64(time.Second))
	decode(t, f.request(t, "POST", "/api/v1/tasks", chat, "timeout", true), &out)
	if out.Status != "FAILED" || out.ErrorCode == nil || *out.ErrorCode != "UPSTREAM_TIMEOUT" {
		t.Fatal("timeout not recorded", out)
	}
	calls := f.mock.models.Load()
	decode(t, f.request(t, "POST", "/api/v1/tasks", chat, "timeout", true), &out)
	if f.mock.models.Load() != calls {
		t.Fatal("timed out POST retried")
	}
}
func TestBindingSecurityAndAdminRead(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	var v map[string]any
	decode(t, f.request(t, "POST", "/api/v1/kovar/auth/login", `{"username":"fixture","password":"request-only-password"}`, "same-user", true), &v)
	expectCode(t, f.request(t, "POST", "/api/v1/kovar/auth/login", `{"username":"other","password":"request-only-password"}`, "different-user", true), 409, "KOVAR_USER_REBIND_FORBIDDEN")
	decode(t, f.request(t, "GET", "/api/v1/admin/agents/"+f.address, "", "", false), &v)
	for _, key := range []string{"agent_id", "kovar_user_id", "kovar_token_id", "per_request_limit", "today_usage", "task_count"} {
		if _, ok := v[key]; !ok {
			t.Error("missing admin field", key)
		}
	}
	b, _ := json.Marshal(v)
	if bytes.Contains(b, []byte("sk-fixture")) || bytes.Contains(b, []byte("password")) {
		t.Fatal("admin response exposes secret")
	}
	expectCode(t, f.request(t, "POST", "/api/v1/account/topup", `{"provider":"stripe","payload":{}}`, "topup", true), 501, "NOT_SUPPORTED")
	decode(t, f.request(t, "DELETE", "/api/v1/agent/token", "", "", true), &v)
	expectCode(t, f.request(t, "POST", "/api/v1/tasks", chat, "deleted-token", true), 409, "KOVAR_TOKEN_UNAVAILABLE")
}
func TestStreamingIsIncrementalAndUsageSaved(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	body := `{"task_type":"chat","payload":{"messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}}`
	res := f.request(t, "POST", "/api/v1/tasks", body, "stream", true)
	defer res.Body.Close()
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") || res.Header.Get("X-Task-Id") == "" {
		t.Fatal("missing SSE headers")
	}
	b, err := io.ReadAll(res.Body)
	if err != nil || !bytes.Contains(b, []byte("[DONE]")) {
		t.Fatal("stream incomplete", err)
	}
	var out task.Task
	decode(t, f.request(t, "GET", "/api/v1/tasks/"+res.Header.Get("X-Task-Id"), "", "", true), &out)
	if out.Status != "SUCCEEDED" || out.TotalTokens != 3 {
		t.Fatal("SSE usage missing", out)
	}
	f.mock.streamDisconnect.Store(true)
	res = f.request(t, "POST", "/api/v1/tasks", body, "disconnect", true)
	id := res.Header.Get("X-Task-Id")
	buf := make([]byte, 1)
	if _, err = res.Body.Read(buf); err != nil {
		t.Fatal("first streaming byte not delivered", err)
	}
	res.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		decode(t, f.request(t, "GET", "/api/v1/tasks/"+id, "", "", true), &out)
		if out.Status == "CANCELLED" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("disconnect did not cancel upstream task")
}

func TestAuthenticationBoundaries(t *testing.T) {
	f := setup(t)
	var v map[string]any
	decode(t, f.request(t, "POST", "/api/v1/admin/agents/"+f.address+"/approve", `{}`, "", false), &v)
	makeRequest := func() *http.Request {
		r, _ := http.NewRequest("GET", f.http.URL+"/api/v1/agents/me", nil)
		f.sign(r, nil)
		return r
	}
	cases := []struct {
		name, code string
		mutate     func(*http.Request)
	}{
		{"invalid signature", "INVALID_AGENT_SIGNATURE", func(r *http.Request) { r.Header.Set("X-Signature", "0x00") }},
		{"wrong address", "INVALID_AGENT_SIGNATURE", func(r *http.Request) {
			k, _ := crypto.GenerateKey()
			r.Header.Set("X-Agent-Id", crypto.PubkeyToAddress(k.PublicKey).Hex())
		}},
		{"invalid address", "INVALID_AGENT_ADDRESS", func(r *http.Request) { r.Header.Set("X-Agent-Id", "invalid") }},
		{"expired timestamp", "AGENT_TIMESTAMP_EXPIRED", func(r *http.Request) { r.Header.Set("X-Timestamp", fmt.Sprint(time.Now().Add(-time.Hour).Unix())) }},
		{"query tamper", "INVALID_AGENT_SIGNATURE", func(r *http.Request) { r.URL.RawQuery = "page=2" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := makeRequest()
			c.mutate(r)
			res, err := f.http.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			expectCode(t, res, 401, c.code)
		})
	}
	r := makeRequest()
	res, err := f.http.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	decode(t, res, &v)
	res, err = f.http.Client().Do(r.Clone(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	expectCode(t, res, 401, "AGENT_NONCE_REUSED")
	original, address := f.key, f.address
	f.key, _ = crypto.GenerateKey()
	f.address, _ = secure.Address(crypto.PubkeyToAddress(f.key.PublicKey).Hex())
	expectCode(t, f.request(t, "GET", "/api/v1/agents/me", "", "", true), 404, "AGENT_NOT_FOUND")
	f.key, f.address = original, address
	expectCode(t, f.request(t, "POST", "/api/v1/agents/challenge", strings.Repeat("x", int(f.cfg.MaxBody)+1), "", false), 413, "REQUEST_TOO_LARGE")
	ctx := context.Background()
	if err = f.gateway.Service.Identity.Rate(ctx, "test-scope", "subject", 1); err != nil {
		t.Fatal(err)
	}
	if err = f.gateway.Service.Identity.Rate(ctx, "test-scope", "subject", 1); err == nil {
		t.Fatal("rate limit not enforced")
	}
	if err = f.gateway.Service.Identity.Rate(ctx, "different-scope", "subject", 1); err != nil {
		t.Fatal("rate scope shared", err)
	}
}
func TestConcurrentBudgetAdmission(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	var out map[string]any
	decode(t, f.request(t, "PUT", "/api/v1/admin/agents/"+f.address+"/budget", `{"per_request_limit":10,"daily_limit":3,"monthly_limit":100}`, "", false), &out)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for j := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := f.request(t, "POST", "/api/v1/tasks", chat, fmt.Sprintf("budget-%d", j), true)
			defer res.Body.Close()
			if res.StatusCode == 200 {
				admitted.Add(1)
			} else if res.StatusCode != 402 {
				t.Errorf("unexpected status %d", res.StatusCode)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 || f.mock.models.Load() != 1 {
		t.Fatal("concurrent task admission exceeded daily budget")
	}
}
func TestUserOneToManyAndIndependentKeys(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	first := f.address
	var firstInfo map[string]any
	decode(t, f.request(t, "GET", "/api/v1/agent/token", "", "", true), &firstInfo)
	f.key, _ = crypto.GenerateKey()
	f.address, _ = secure.Address(crypto.PubkeyToAddress(f.key.PublicKey).Hex())
	f.register(t)
	f.approveAndBind(t)
	var info map[string]any
	decode(t, f.request(t, "GET", "/api/v1/agent/token", "", "", true), &info)
	if info["token_id"] == firstInfo["token_id"] {
		t.Fatal("agents share token")
	}
	var users, agents int
	if err := f.db.QueryRow(`SELECT COUNT(DISTINCT kovar_user_id),COUNT(*) FROM agent_user_bindings WHERE agent_address=ANY($1)`, []string{first, f.address}).Scan(&users, &agents); err != nil || users != 1 || agents != 2 {
		t.Fatal("user to agent relationship invalid", err)
	}
	_, err := f.db.Exec(`UPDATE agent_kovar_tokens SET key_fingerprint=(SELECT key_fingerprint FROM agent_kovar_tokens WHERE agent_address=$1) WHERE agent_address=$2`, first, f.address)
	if err == nil {
		t.Fatal("database allowed a shared key fingerprint")
	}
}
func TestTopupReplayKeepsCurrentRequestID(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	for range 2 {
		expectCode(t, f.request(t, "POST", "/api/v1/account/topup", `{"provider":"epay","payload":{}}`, "replayed-topup", true), 501, "NOT_SUPPORTED")
	}
}

func TestSignedMultipartTranscription(t *testing.T) {
	f := setup(t)
	f.approveAndBind(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("request", `{"task_type":"transcription","payload":{}}`); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "fixture.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("fixture-audio"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("POST", f.http.URL+"/api/v1/tasks", bytes.NewReader(body.Bytes()))
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Idempotency-Key", "multipart")
	f.sign(r, body.Bytes())
	res, err := f.http.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var out task.Task
	decode(t, res, &out)
	if out.Status != "SUCCEEDED" {
		t.Fatal("multipart transcription failed", out)
	}
}
