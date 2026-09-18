package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env, Addr, DatabaseURL, ManageURL, ModelURL, AdminUsername, AdminPassword, EncryptionKey, LogLevel, RouterConfig string
	HTTPTimeout, RequestTimeout, NonceTTL, ClockSkew, AdminTTL                                                       time.Duration
	PerRequest, Daily, Monthly, MaxBody                                                                              int64
	AgentRate, IPRate, LoginRate                                                                                     int
}

func Load() (Config, error) {
	c := Config{Env: value("APP_ENV", "development"), Addr: value("HTTP_ADDR", "127.0.0.1:8080"), DatabaseURL: os.Getenv("DATABASE_URL"), AdminUsername: os.Getenv("GATEWAY_ADMIN_USERNAME"), AdminPassword: os.Getenv("GATEWAY_ADMIN_PASSWORD"), EncryptionKey: os.Getenv("ENCRYPTION_KEY"), LogLevel: value("LOG_LEVEL", "INFO"), RouterConfig: value("ROUTER_CONFIG", "configs/router.json")}
	base := value("KOVAR_BASE_URL", "https://kovar.ai")
	c.ManageURL = value("KOVAR_MANAGE_BASE_URL", base)
	c.ModelURL = value("KOVAR_MODEL_BASE_URL", base)
	for name, p := range map[string]*time.Duration{"KOVAR_HTTP_TIMEOUT": &c.HTTPTimeout, "REQUEST_TIMEOUT": &c.RequestTimeout, "AGENT_NONCE_TTL": &c.NonceTTL, "AGENT_SIGNATURE_MAX_CLOCK_SKEW": &c.ClockSkew, "ADMIN_TOKEN_TTL": &c.AdminTTL} {
		defaults := map[string]string{"KOVAR_HTTP_TIMEOUT": "120s", "REQUEST_TIMEOUT": "130s", "AGENT_NONCE_TTL": "5m", "AGENT_SIGNATURE_MAX_CLOCK_SKEW": "5m", "ADMIN_TOKEN_TTL": "8h"}
		d, e := time.ParseDuration(value(name, defaults[name]))
		if e != nil || d <= 0 || d > 24*time.Hour {
			return c, fmt.Errorf("invalid %s", name)
		}
		*p = d
	}
	for name, p := range map[string]*int64{"DEFAULT_PER_REQUEST_LIMIT": &c.PerRequest, "DEFAULT_DAILY_LIMIT": &c.Daily, "DEFAULT_MONTHLY_LIMIT": &c.Monthly, "MAX_BODY_BYTES": &c.MaxBody} {
		defaults := map[string]string{"DEFAULT_PER_REQUEST_LIMIT": "10000", "DEFAULT_DAILY_LIMIT": "100000", "DEFAULT_MONTHLY_LIMIT": "1000000", "MAX_BODY_BYTES": "8388608"}
		n, e := strconv.ParseInt(value(name, defaults[name]), 10, 64)
		if e != nil || n < 0 || n > 1000000000000 {
			return c, fmt.Errorf("invalid %s", name)
		}
		*p = n
	}
	for name, p := range map[string]*int{"RATE_LIMIT": &c.AgentRate, "RATE_LIMIT_IP": &c.IPRate, "RATE_LIMIT_ADMIN_LOGIN": &c.LoginRate} {
		n, e := strconv.Atoi(value(name, map[string]string{"RATE_LIMIT": "120", "RATE_LIMIT_IP": "300", "RATE_LIMIT_ADMIN_LOGIN": "10"}[name]))
		if e != nil || n < 1 || n > 100000 {
			return c, fmt.Errorf("invalid %s", name)
		}
		*p = n
	}
	if c.DatabaseURL == "" || c.AdminUsername == "" || len(c.AdminUsername) > 128 || len(c.AdminPassword) < 8 || len(c.AdminPassword) > 72 {
		return c, errors.New("DATABASE_URL and valid admin bootstrap credentials are required")
	}
	key, e := base64.StdEncoding.DecodeString(c.EncryptionKey)
	if e != nil || len(key) != 32 {
		return c, errors.New("ENCRYPTION_KEY must be base64-encoded 32 bytes")
	}
	if c.MaxBody < 1024 || c.MaxBody > 32<<20 {
		return c, errors.New("MAX_BODY_BYTES must be between 1024 and 33554432")
	}
	if c.RequestTimeout < c.HTTPTimeout {
		return c, errors.New("REQUEST_TIMEOUT must cover KOVAR_HTTP_TIMEOUT")
	}
	if c.Env != "development" && c.Env != "test" && c.Env != "production" {
		return c, errors.New("invalid APP_ENV")
	}
	for _, raw := range []string{c.ManageURL, c.ModelURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.Env != "production" && u.Scheme == "http")) {
			return c, errors.New("invalid Kovar URL; production requires HTTPS")
		}
	}
	if c.Env == "production" && (c.AdminPassword == "Aa123456" || strings.Contains(c.AdminPassword, "CHANGE_ME")) {
		return c, errors.New("production requires non-default admin password")
	}
	return c, nil
}
func value(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
