package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestFailClosedConfig(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://fixture:CHANGE_ME@localhost/fixture")
	t.Setenv("GATEWAY_ADMIN_USERNAME", "admin")
	t.Setenv("GATEWAY_ADMIN_PASSWORD", "fixture-password")
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
	c, err := Load()
	if err != nil || c.PerRequest <= 0 {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTION_KEY", "invalid")
	if _, err = Load(); err == nil {
		t.Fatal("invalid key accepted")
	}
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))))
	t.Setenv("APP_ENV", "production")
	t.Setenv("KOVAR_MODEL_BASE_URL", "http://example.invalid")
	if _, err = Load(); err == nil {
		t.Fatal("production plaintext upstream accepted")
	}
}
