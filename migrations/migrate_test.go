package migrations_test

import (
	"context"
	"testing"

	"kovar-gateway/internal/testutil"
	"kovar-gateway/migrations"
)

func TestMigrationRoundTrip(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	if err := migrations.Apply(ctx, db, false); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db, true); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db, true); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db, false); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&tables); err != nil || tables != 15 {
		t.Fatalf("expected 15 tables including version tracking, got %d, %v", tables, err)
	}
}
