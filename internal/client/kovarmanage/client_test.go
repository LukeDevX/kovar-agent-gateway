package kovarmanage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManageDocumentedOperations(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			posts.Add(1)
		}
		if r.URL.Path != "/api/user/register" && r.URL.Path != "/api/user/login" && r.URL.Path != "/api/usage/token/" && r.URL.Path != "/api/ratio_config" {
			if r.Header.Get("New-Api-User") != "7" || r.Header.Get("Authorization") != "Bearer management-fixture" {
				t.Error("management auth headers missing")
			}
		}
		switch r.URL.Path {
		case "/api/user/register":
			var b RegisterRequest
			if json.NewDecoder(r.Body).Decode(&b) != nil || b.Username != "fixture" {
				t.Error("invalid register body")
			}
			fmt.Fprint(w, `{"success":true}`)
		case "/api/user/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture-session"})
			fmt.Fprint(w, `{"success":true,"data":{"id":7}}`)
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"id":7,"quota":1000,"used_quota":10,"request_count":2}}`)
		case "/api/token/":
			fmt.Fprint(w, `{"success":true,"data":{"id":9,"user_id":7,"name":"agent-fixture","key":"sk-fixture","status":1,"expired_time":2000000000,"remain_quota":100}}`)
		case "/api/token/9":
			fmt.Fprint(w, `{"success":true}`)
		case "/api/token/9/key":
			if r.Method != "POST" {
				t.Error("full key retrieval must use POST")
			}
			fmt.Fprint(w, `{"success":true,"data":{"key":"sk-fixture"}}`)
		case "/api/usage/token/":
			if r.Header.Get("Authorization") != "Bearer sk-fixture" || r.URL.RawQuery != "" {
				t.Error("token secret must be only in Authorization")
			}
			fmt.Fprint(w, `{"code":true,"message":"ok","data":{}}`)
		case "/api/pricing", "/api/ratio_config", "/api/user/topup/info", "/api/user/topup/self", "/api/log/self", "/api/log/self/stat", "/api/data/self", "/api/task/self", "/api/user/models":
			fmt.Fprint(w, `{"success":true,"data":[]}`)
		default:
			t.Error("unexpected undocumented path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := New(server.URL, time.Second, nil)
	ctx := context.Background()
	cred := Credential{UserID: 7, AccessToken: "management-fixture"}
	if err := c.Register(ctx, RegisterRequest{Username: "fixture", Password: "test-only"}); err != nil {
		t.Fatal(err)
	}
	u, session, err := c.Login(ctx, LoginRequest{Username: "fixture", Password: "test-only"})
	if err != nil || u.ID != 7 || session.Session == "" {
		t.Fatal("login failed")
	}
	if _, err = c.Self(ctx, cred); err != nil {
		t.Fatal(err)
	}
	token, err := c.CreateToken(ctx, cred, CreateTokenRequest{Name: "agent-fixture", RemainQuota: 100, ExpiredTime: 2000000000})
	if err != nil || token.ID != 9 {
		t.Fatal(err)
	}
	if err = c.DeleteToken(ctx, cred, 9); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Usage(ctx, "sk-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Pricing(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Ratios(ctx); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"topup_info", "topups", "logs", "logs_stat", "data", "tasks", "models"} {
		if _, err = c.Read(ctx, cred, op); err != nil {
			t.Fatal(err)
		}
	}
	before := posts.Load()
	for _, provider := range []string{"epay", "stripe", "creem"} {
		if _, err = c.Topup(ctx, cred, provider, json.RawMessage(`{}`)); err == nil {
			t.Fatal("undocumented payment schema accepted")
		}
	}
	if posts.Load() != before {
		t.Fatal("payment was sent without contract")
	}
}
func TestManageNoPOSTRetryOrSecretError(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		fmt.Fprint(w, "fixture-password Authorization Bearer sk-fixture")
	}))
	defer s.Close()
	c := New(s.URL, time.Second, nil)
	err := c.Register(context.Background(), RegisterRequest{Username: "x", Password: "fixture-password"})
	if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "fixture-password") {
		t.Fatal("unsafe POST retry or error disclosure")
	}
}

func TestTwoFASessionAndMissingContracts(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login/2fa":
			c, err := r.Cookie("session")
			if err != nil || c.Value != "pending-fixture" {
				t.Error("pending session missing")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["code"] != "123456" {
				t.Error("2FA code missing")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "verified-fixture"})
			fmt.Fprint(w, `{"success":true,"data":{"id":7}}`)
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{}}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer s.Close()
	c := New(s.URL, time.Second, nil)
	u, cred, err := c.TwoFA(context.Background(), Credential{Session: "pending-fixture"}, "123456")
	if err != nil || u.ID != 7 || cred.Session != "verified-fixture" {
		t.Fatal("2FA did not establish session")
	}
	if _, err = c.Self(context.Background(), cred); err == nil {
		t.Fatal("self without identity accepted")
	}
	if err = c.Register(context.Background(), RegisterRequest{}); err == nil {
		t.Fatal("missing success envelope accepted")
	}
}
