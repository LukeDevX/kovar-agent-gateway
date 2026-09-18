package identity

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"kovar-gateway/internal/idempotency"
	"kovar-gateway/internal/platform/config"
	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/platform/secure"
	"kovar-gateway/internal/testutil"
)

func TestStateTransitions(t *testing.T) {
	cases := []struct{ s, w, action, want string }{{"REGISTERED", "PENDING", "approve", "ACTIVE"}, {"REGISTERED", "PENDING", "reject", "REGISTERED"}, {"ACTIVE", "APPROVED", "suspend", "SUSPENDED"}, {"SUSPENDED", "APPROVED", "resume", "ACTIVE"}, {"ACTIVE", "APPROVED", "revoke", "REVOKED"}, {"REVOKED", "REVOKED", "resume", ""}, {"ACTIVE", "APPROVED", "approve", ""}}
	for _, c := range cases {
		status, _, err := Transition(Agent{Status: c.s, Whitelist: c.w}, c.action)
		if c.want == "" {
			if err == nil {
				t.Fatal("invalid transition accepted")
			}
		} else if err != nil || status != c.want {
			t.Fatal(status, err)
		}
	}
}
func TestAgentAndAdminDatabase(t *testing.T) {
	db := testutil.Database(t)
	cfg := config.Config{ClockSkew: 5 * time.Minute, NonceTTL: 5 * time.Minute, AdminTTL: time.Hour, AdminUsername: "admin", AdminPassword: "test-password-123", PerRequest: 10, Daily: 100, Monthly: 1000}
	s := New(db, cfg, idempotency.New(db))
	ctx := context.Background()
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "admin", "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	token, admin, err := s.Login(ctx, "admin", cfg.AdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Admin(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE admin_sessions SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Admin(ctx, token); err == nil {
		t.Fatal("expired admin token accepted")
	}
	key, _ := crypto.GenerateKey()
	address, _ := secure.Address(crypto.PubkeyToAddress(key.PublicKey).Hex())
	challenge, err := s.Challenge(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	sig, _ := crypto.Sign(secure.PersonalHash(secure.Registration(address, challenge.Nonce, ts)), key)
	reg := Registration{address, challenge.Nonce, ts, "0x" + hex.EncodeToString(sig)}
	agent, err := s.Register(ctx, reg, "register")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != "REGISTERED" || agent.Whitelist != "PENDING" {
		t.Fatal(agent)
	}
	if _, err = s.Register(ctx, reg, "register"); err != nil {
		t.Fatal("registration replay failed", err)
	}
	if _, err = s.Register(ctx, reg, "reuse"); httpx.Normalize(err).Code != "AGENT_NONCE_REUSED" {
		t.Fatal("nonce reuse accepted", err)
	}
	authenticate := func(nonce string) (string, error) {
		ts := time.Now().Unix()
		b, _ := json.Marshal(ts)
		message := secure.Canonical("GET", "/api/v1/agents/me", string(b), nonce, "", nil)
		sig, _ := crypto.Sign(secure.PersonalHash(message), key)
		return s.Authenticate(ctx, address, "GET", "/api/v1/agents/me", string(b), nonce, "0x"+hex.EncodeToString(sig), "", nil)
	}
	if _, err = authenticate(secure.Random()); httpx.Normalize(err).Code != "AGENT_NOT_WHITELISTED" {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, admin, address, "approve", ""); err != nil {
		t.Fatal(err)
	}
	nonce := secure.Random()
	if _, err = authenticate(nonce); err != nil {
		t.Fatal(err)
	}
	if _, err = authenticate(nonce); httpx.Normalize(err).Code != "AGENT_NONCE_REUSED" {
		t.Fatal(err)
	}
	for _, step := range []struct{ action, code string }{{"suspend", "AGENT_SUSPENDED"}, {"resume", ""}, {"revoke", "AGENT_REVOKED"}} {
		if _, err = s.Review(ctx, admin, address, step.action, ""); err != nil {
			t.Fatal(err)
		}
		_, err = authenticate(secure.Random())
		if step.code == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || httpx.Normalize(err).Code != step.code {
			t.Fatal(err)
		}
	}
	second, _ := crypto.GenerateKey()
	other, _ := secure.Address(crypto.PubkeyToAddress(second.PublicKey).Hex())
	challenge, err = s.Challenge(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE agent_challenges SET expires_at=now()-interval '1 second' WHERE nonce=$1`, challenge.Nonce); err != nil {
		t.Fatal(err)
	}
	sig, _ = crypto.Sign(secure.PersonalHash(secure.Registration(other, challenge.Nonce, ts)), second)
	_, err = s.Register(ctx, Registration{other, challenge.Nonce, ts, "0x" + hex.EncodeToString(sig)}, "expired")
	if err == nil || httpx.Normalize(err).Code != "AGENT_NONCE_EXPIRED" {
		t.Fatal(err)
	}
}
