// Package storetest gives tests an isolated Postgres schema.
//
// It uses DATABASE_URL (from the environment, or the repo .env) against the
// Compose Postgres. If Postgres is not reachable the test is skipped with a
// message, never failed: `go test` should work without Docker.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"squad/store"
)

// New creates a fresh schema, migrates it, and drops it when the test ends.
func New(t testing.TB) *store.Store {
	t.Helper()

	dsn, ok := databaseURL()
	if !ok {
		t.Skip("store tests need Postgres: set DATABASE_URL (copy .env.example to .env) and run `make search`")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, cleanup, err := NewSchema(ctx, dsn)
	if err != nil {
		if errors.Is(err, ErrUnreachable) {
			t.Skipf("store tests need Postgres: %v; run `make search`", err)
		}
		t.Fatalf("store test schema: %v", err)
	}
	t.Cleanup(cleanup)
	t.Cleanup(s.Close)
	return s
}

// ErrUnreachable means Postgres is not running; callers may skip instead of fail.
var ErrUnreachable = errors.New("postgres unreachable")

// NewSchema is New without a testing.TB, for the eval harness. The caller must
// run the returned cleanup (it drops the schema).
func NewSchema(ctx context.Context, dsn string) (*store.Store, func(), error) {
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		return nil, nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}

	schema := schemaName()
	// The name is generated here (not user input), so it is safe to interpolate.
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		return nil, nil, fmt.Errorf("create schema %s: %w", schema, err)
	}
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(c, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}

	s, err := store.Open(ctx, withSearchPath(dsn, schema))
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("open store on schema %s: %w", schema, err)
	}
	return s, cleanup, nil
}

// DSN returns DATABASE_URL from the environment or the nearest .env.
func DSN() (string, bool) { return databaseURL() }

func schemaName() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("test_%d_%s", os.Getpid(), hex.EncodeToString(b[:]))
}

func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	// public stays on the path for the pgvector type (the extension lives in
	// public); the throwaway schema still shadows every app table.
	return dsn + sep + "search_path=" + schema + ",public"
}

// databaseURL reads DATABASE_URL from the environment, then from the nearest
// .env going up from the working directory (tests run inside a package dir).
func databaseURL() (string, bool) {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v, true
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				k, v, ok := strings.Cut(line, "=")
				if ok && strings.TrimSpace(k) == "DATABASE_URL" {
					v = strings.Trim(strings.TrimSpace(v), `"'`)
					if v != "" {
						return v, true
					}
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
