package migrations

import (
	"context"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"sort"
)

//go:embed *.sql
var files embed.FS

func Apply(ctx context.Context, db *pgxpool.Pool) error {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(82943001)"); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(82943001)")
	if _, err = conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	names, _ := files.ReadDir(".")
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	for _, file := range names {
		if file.IsDir() {
			continue
		}
		var exists bool
		if err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", file.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		sql, _ := files.ReadFile(file.Name())
		if _, e = tx.Exec(ctx, string(sql)); e == nil {
			_, e = tx.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", file.Name())
		}
		if e != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", file.Name(), e)
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	return nil
}
