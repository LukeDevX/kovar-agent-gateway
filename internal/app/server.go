package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"kovar-gateway/internal/binding"
	"kovar-gateway/internal/client/kovarmanage"
	"kovar-gateway/internal/client/kovarmodel"
	"kovar-gateway/internal/idempotency"
	"kovar-gateway/internal/identity"
	"kovar-gateway/internal/platform/config"
	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/platform/secure"
	"kovar-gateway/internal/task"
)

type Server struct {
	Service *Service
	cfg     config.Config
	db      *sql.DB
	idem    *idempotency.Service
	log     *slog.Logger
}

func New(db *sql.DB, cfg config.Config, log *slog.Logger) (*Server, error) {
	enc, err := secure.NewEncryption(cfg.EncryptionKey)
	if err != nil {
		return nil, err
	}
	idem := idempotency.New(db)
	auth := identity.New(db, cfg, idem)
	manage := kovarmanage.New(cfg.ManageURL, cfg.HTTPTimeout, log)
	model, err := kovarmodel.New(cfg.ModelURL, cfg.HTTPTimeout, log)
	if err != nil {
		return nil, err
	}
	router, err := task.LoadRouter(cfg.RouterConfig)
	if err != nil {
		return nil, err
	}
	b := binding.New(db, auth, manage, enc)
	tasks := task.New(db, auth, b, model, router)
	return &Server{&Service{auth, b, tasks}, cfg, db, idem, log}, nil
}

type input struct {
	Agent string
	Admin identity.Admin
	Body  []byte
}
type endpoint func(http.ResponseWriter, *http.Request, input) (any, error)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern, auth string, fn endpoint) { mux.HandleFunc(pattern, s.endpoint(auth, fn)) }
	route("GET /health", "public", func(_ http.ResponseWriter, _ *http.Request, _ input) (any, error) {
		return map[string]string{"status": "ok"}, nil
	})
	route("GET /ready", "public", func(_ http.ResponseWriter, r *http.Request, _ input) (any, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var ready bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)`).Scan(&ready)
		if err != nil || !ready {
			return nil, httpx.E(503, "NOT_READY", "database schema is unavailable")
		}
		return map[string]string{"status": "ready"}, nil
	})
	route("POST /api/v1/agents/challenge", "public", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in struct {
			Address string `json:"address"`
		}
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Identity.Challenge(r.Context(), in.Address)
	})
	route("POST /api/v1/agents/register", "public", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in identity.Registration
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Identity.Register(r.Context(), in, r.Header.Get("Idempotency-Key"))
	})
	route("GET /api/v1/agents/me", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Identity.Get(r.Context(), i.Agent)
	})
	route("POST /api/v1/admin/login", "login", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		token, a, err := s.Service.Identity.Login(r.Context(), in.Username, in.Password)
		if err != nil {
			return nil, err
		}
		return map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": int64(s.cfg.AdminTTL.Seconds()), "admin": a}, nil
	})
	route("GET /api/v1/admin/me", "admin", func(_ http.ResponseWriter, _ *http.Request, i input) (any, error) { return i.Admin, nil })
	route("GET /api/v1/admin/agents", "admin", func(_ http.ResponseWriter, r *http.Request, _ input) (any, error) {
		p, n, err := httpx.Page(r)
		if err != nil {
			return nil, err
		}
		return s.Service.AdminAgents(r.Context(), p, n, "")
	})
	route("GET /api/v1/admin/whitelist", "admin", func(_ http.ResponseWriter, r *http.Request, _ input) (any, error) {
		p, n, err := httpx.Page(r)
		if err != nil {
			return nil, err
		}
		return s.Service.AdminAgents(r.Context(), p, n, r.URL.Query().Get("status"))
	})
	route("GET /api/v1/admin/agents/{agent_id}", "admin", func(_ http.ResponseWriter, r *http.Request, _ input) (any, error) {
		a, err := pathAgent(r)
		if err != nil {
			return nil, err
		}
		return s.Service.AdminAgent(r.Context(), a)
	})
	for _, action := range []string{"approve", "reject", "suspend", "resume", "revoke"} {
		route("POST /api/v1/admin/agents/{agent_id}/"+action, "admin", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
			a, err := pathAgent(r)
			if err != nil {
				return nil, err
			}
			var in struct {
				Remark string `json:"remark"`
			}
			if len(i.Body) > 0 {
				if err = httpx.Decode(i.Body, &in); err != nil {
					return nil, err
				}
			}
			return s.Service.Review(r.Context(), i.Admin, a, action, in.Remark)
		})
	}
	route("PUT /api/v1/admin/agents/{agent_id}/budget", "admin", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		a, err := pathAgent(r)
		if err != nil {
			return nil, err
		}
		var p identity.Policy
		if err = httpx.Decode(i.Body, &p); err != nil {
			return nil, err
		}
		err = s.Service.Identity.UpdatePolicy(r.Context(), i.Admin, a, p)
		return p, err
	})
	route("POST /api/v1/kovar/auth/register", "agent", s.idempotent("kovar-register", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in kovarmanage.RegisterRequest
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		err := s.Service.Binding.Register(r.Context(), i.Agent, in)
		return map[string]bool{"registered": err == nil}, err
	}))
	route("POST /api/v1/kovar/auth/login", "agent", s.idempotent("kovar-login", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in kovarmanage.LoginRequest
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Binding.Login(r.Context(), i.Agent, in)
	}))
	route("POST /api/v1/kovar/auth/login/2fa", "agent", s.idempotent("kovar-2fa", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in struct {
			Code string `json:"code"`
		}
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Binding.TwoFA(r.Context(), i.Agent, in.Code)
	}))
	route("POST /api/v1/bindings", "agent", s.idempotent("binding", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in struct {
			UserID      int64  `json:"kovar_user_id"`
			AccessToken string `json:"access_token"`
		}
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Binding.Bind(r.Context(), i.Agent, kovarmanage.Credential{UserID: in.UserID, AccessToken: in.AccessToken})
	}))
	route("GET /api/v1/bindings/me", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.Get(r.Context(), i.Agent)
	})
	route("POST /api/v1/agent/token", "agent", s.idempotent("create-token", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in binding.KeyOptions
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Binding.CreateKey(r.Context(), i.Agent, in)
	}))
	route("GET /api/v1/agent/token", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.Token(r.Context(), i.Agent)
	})
	route("DELETE /api/v1/agent/token", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		err := s.Service.Binding.DeleteKey(r.Context(), i.Agent)
		return map[string]bool{"deleted": err == nil}, err
	})
	route("GET /api/v1/account", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.Account(r.Context(), i.Agent)
	})
	for path, op := range map[string]string{"/api/v1/account/topup/info": "topup_info", "/api/v1/account/topups": "topups", "/api/v1/logs": "logs", "/api/v1/logs/stat": "logs_stat"} {
		route("GET "+path, "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
			p, n, err := httpx.Page(r)
			if err != nil {
				return nil, err
			}
			if op == "topups" {
				return s.Service.Binding.Topups(r.Context(), i.Agent, p, n, r.URL.Query().Get("keyword"))
			}
			return s.Service.Binding.Read(r.Context(), i.Agent, op, p, n)
		})
	}
	route("GET /api/v1/account/topup/status", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.TopupStatus(r.Context(), i.Agent, r.URL.Query().Get("trade_no"))
	})
	route("GET /api/v1/account/topup/axone/chains", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.AxoneChains(r.Context(), i.Agent)
	})
	route("GET /api/v1/account/axone/wallets", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.AxoneWallets(r.Context(), i.Agent)
	})
	route("POST /api/v1/account/paygo/sessions", "agent", s.idempotent("paygo-create", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in struct {
			WalletID  string `json:"wallet_id"`
			MaxAmount string `json:"max_amount"`
		}
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Binding.CreatePaygoSession(r.Context(), i.Agent, r.Header.Get("Idempotency-Key"), in.WalletID, in.MaxAmount)
	}))
	route("GET /api/v1/account/paygo/sessions", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.ListPaygoSessions(r.Context(), i.Agent)
	})
	route("GET /api/v1/account/paygo/sessions/{id}", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.GetPaygoSession(r.Context(), i.Agent, r.PathValue("id"))
	})
	route("POST /api/v1/account/paygo/sessions/{id}/close", "agent", s.idempotent("paygo-close", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.ClosePaygoSession(r.Context(), i.Agent, r.PathValue("id"), r.Header.Get("Idempotency-Key"))
	}))
	route("POST /api/v1/account/topup", "agent", s.idempotent("topup", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		var in struct {
			Provider string          `json:"provider"`
			Payload  json.RawMessage `json:"payload"`
		}
		if err := httpx.Decode(i.Body, &in); err != nil {
			return nil, err
		}
		return s.Service.Binding.Topup(r.Context(), i.Agent, in.Provider, in.Payload)
	}))
	route("GET /api/v1/models", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		m, err := s.Service.Tasks.Models(r.Context(), i.Agent)
		return map[string]any{"data": m}, err
	})
	route("GET /api/v1/pricing", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Binding.Pricing(r.Context(), i.Agent)
	})
	route("POST /api/v1/tasks", "agent", func(w http.ResponseWriter, r *http.Request, i input) (any, error) {
		in, err := taskInput(r, i.Body)
		if err != nil {
			return nil, err
		}
		sink := func(id string, line []byte) error {
			w.Header().Set("X-Task-Id", id)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if line != nil {
				if _, err := w.Write(line); err != nil {
					return err
				}
			}
			return rc.Flush()
		}
		t, streamed, err := s.Service.Tasks.Create(r.Context(), i.Agent, r.Header.Get("Idempotency-Key"), secure.Hash(i.Body), in, sink)
		if streamed {
			return nil, err
		}
		return t, err
	})
	route("GET /api/v1/tasks", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		p, n, err := httpx.Page(r)
		if err != nil {
			return nil, err
		}
		items, err := s.Service.Tasks.List(r.Context(), i.Agent, p, n)
		return map[string]any{"items": items, "page": p, "page_size": n}, err
	})
	route("GET /api/v1/tasks/{task_id}", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		return s.Service.Tasks.Get(r.Context(), i.Agent, r.PathValue("task_id"))
	})
	route("GET /api/v1/usage", "agent", func(_ http.ResponseWriter, r *http.Request, i input) (any, error) {
		p, n, err := httpx.Page(r)
		if err != nil {
			return nil, err
		}
		return s.Service.Tasks.Usage(r.Context(), i.Agent, p, n)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, httpx.E(404, "NOT_FOUND", "endpoint does not exist"))
	})
	return s.middleware(mux)
}
func pathAgent(r *http.Request) (string, error) {
	a, err := secure.Address(r.PathValue("agent_id"))
	if err != nil {
		return "", httpx.E(400, "INVALID_AGENT_ADDRESS", "invalid EVM address")
	}
	return a, nil
}
func (s *Server) endpoint(auth string, fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		i := input{}
		if r.Body != nil {
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBody))
			if err != nil {
				httpx.Fail(w, r, httpx.E(413, "REQUEST_TOO_LARGE", "request body exceeds limit"))
				return
			}
			i.Body = b
			defer clear(b)
		}
		var err error
		switch auth {
		case "agent":
			i.Agent, err = s.Service.Identity.Authenticate(r.Context(), r.Header.Get("X-Agent-Id"), r.Method, r.URL.RequestURI(), r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), r.Header.Get("X-Signature"), r.Header.Get("Idempotency-Key"), i.Body)
			if err == nil {
				err = s.Service.Identity.Rate(r.Context(), "agent", i.Agent, s.cfg.AgentRate)
			}
		case "admin":
			token := ""
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			i.Admin, err = s.Service.Identity.Admin(r.Context(), token)
		case "login":
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			err = s.Service.Identity.Rate(r.Context(), "admin-login", ip, s.cfg.LoginRate)
		}
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		v, err := fn(w, r, i)
		if err != nil {
			if out, ok := w.(*writer); ok && out.wrote {
				s.log.ErrorContext(r.Context(), "response finalization failed", "request_id", httpx.RequestID(r.Context()), "code", httpx.Normalize(err).Code)
				return
			}
			httpx.Fail(w, r, err)
			return
		}
		if v != nil {
			httpx.JSON(w, 200, v)
		}
	}
}
func (s *Server) idempotent(operation string, fn endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request, i input) (any, error) {
		key := r.Header.Get("Idempotency-Key")
		h := hmac.New(sha256.New, []byte(s.cfg.EncryptionKey))
		_, _ = h.Write(i.Body)
		hash := hex.EncodeToString(h.Sum(nil))
		claim, err := s.idem.Claim(r.Context(), i.Agent, operation, key, hash)
		if err != nil {
			return nil, err
		}
		if claim.Replay {
			if claim.Status >= 400 {
				var saved httpx.Error
				if json.Unmarshal(claim.Body, &saved) != nil {
					return nil, httpx.E(500, "INTERNAL_ERROR", "invalid stored response")
				}
				saved.Status = claim.Status
				return nil, &saved
			}
			httpx.JSON(w, claim.Status, claim.Body)
			return nil, nil
		}
		value, callErr := fn(w, r, i)
		status := 200
		if callErr != nil {
			e := httpx.Normalize(callErr)
			value = map[string]string{"code": e.Code, "message": e.Message}
			status = e.Status
		}
		body, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if err = s.idem.Complete(ctx, i.Agent, operation, key, status, body); err != nil {
			return nil, err
		}
		if callErr != nil {
			return nil, callErr
		}
		return value, nil
	}
}
func taskInput(r *http.Request, body []byte) (task.Input, error) {
	var in task.Input
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return in, httpx.Invalid("valid Content-Type is required")
	}
	if media == "application/json" {
		return in, httpx.Decode(body, &in)
	}
	if media != "multipart/form-data" {
		return in, httpx.Invalid("tasks accept application/json or multipart/form-data")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	in.Files = map[string]kovarmodel.File{}
	seen := map[string]bool{}
	for count := 0; ; count++ {
		if count > 8 {
			return in, httpx.Invalid("too many multipart fields")
		}
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return in, httpx.Invalid("invalid multipart body")
		}
		name := part.FormName()
		if seen[name] {
			return in, httpx.Invalid("duplicate multipart field")
		}
		seen[name] = true
		b, err := io.ReadAll(io.LimitReader(part, 8<<20+1))
		part.Close()
		if err != nil || len(b) > 8<<20 {
			return in, httpx.Invalid("upload exceeds 8 MiB")
		}
		if name == "request" {
			files := in.Files
			if err = httpx.Decode(b, &in); err != nil {
				return in, err
			}
			in.Files = files
		} else if name == "image" || name == "mask" || name == "file" {
			filename := filepath.Base(part.FileName())
			if filename == "." || filename == "" {
				return in, httpx.Invalid("file name required")
			}
			in.Files[name] = kovarmodel.File{Name: filename, ContentType: part.Header.Get("Content-Type"), Data: b}
		} else {
			return in, httpx.Invalid("unsupported multipart field")
		}
	}
	if !seen["request"] {
		return in, httpx.Invalid("multipart request JSON part is required")
	}
	return in, nil
}

type writer struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *writer) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}
func (w *writer) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(p)
}
func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *writer) Flush() {
	if !w.wrote {
		w.WriteHeader(200)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(base http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !httpx.Identifier(id) {
			id = secure.Random()[:32]
		}
		ctx, cancel := context.WithTimeout(httpx.WithRequestID(r.Context(), id), s.cfg.RequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		w := &writer{ResponseWriter: base}
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		start := time.Now()
		defer func() {
			if recover() != nil {
				if !w.wrote {
					httpx.Fail(w, r, httpx.E(500, "INTERNAL_ERROR", "internal server error"))
				}
				s.log.ErrorContext(ctx, "request panic", "request_id", id)
			}
			s.log.InfoContext(ctx, "request completed", "request_id", id, "method", r.Method, "route", r.Pattern, "status", w.status, "duration_ms", time.Since(start).Milliseconds())
		}()
		if r.URL.Path != "/health" && r.URL.Path != "/ready" {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			if err = s.Service.Identity.Rate(ctx, "ip", ip, s.cfg.IPRate); err != nil {
				w.Header().Set("Retry-After", "60")
				httpx.Fail(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
