package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
)

//go:embed *.sql
var files embed.FS

func Apply(ctx context.Context, db *sql.DB, down bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734938234)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version bigint PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)`).Scan(&exists); err != nil {
		return err
	}
	if exists == down {
		name := "001_initial.up.sql"
		if down {
			name = "001_initial.down.sql"
		}
		b, e := files.ReadFile(name)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("apply migration: %w", err)
		}
		q := `INSERT INTO schema_migrations(version) VALUES(1)`
		if down {
			q = `DELETE FROM schema_migrations WHERE version=1`
		}
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
