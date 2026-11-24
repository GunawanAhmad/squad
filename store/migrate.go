package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// One advisory lock keeps two app instances from racing the same migrations.
const migrationLock int64 = 0x6368617261637473 // "characts"

// migrate applies numbered migrations in order and records the highest one in
// the single-row schema_version table.
func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLock) }()

	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (version int NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());
		INSERT INTO schema_version (version)
		SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM schema_version)`)
	if err != nil {
		return fmt.Errorf("schema_version: %w", err)
	}

	var current int
	if err := conn.QueryRow(ctx, `SELECT version FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("schema_version: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		version, err := migrationNumber(name)
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}

		sql, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		// Simple protocol: a migration file holds several statements.
		if _, err := tx.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE schema_version SET version = $1, applied_at = now()`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		current = version
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func migrationNumber(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q: want NNNN_name.sql", name)
	}
	n, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q: %w", name, err)
	}
	return n, nil
}
