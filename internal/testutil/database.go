package testutil

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"

	"kovar-gateway/internal/platform/database"
	"kovar-gateway/internal/platform/secure"
	"kovar-gateway/migrations"
)

func Database(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL or run make test-integration for real PostgreSQL tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect to test PostgreSQL")
	}
	schema := "test_" + secure.Random()[:16]
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := database.Open(ctx, u.String())
	if err != nil {
		t.Fatal("cannot connect to isolated test schema")
	}
	t.Cleanup(func() { db.Close() })
	if err = migrations.Apply(ctx, db, false); err != nil {
		t.Fatal(err)
	}
	return db
}
