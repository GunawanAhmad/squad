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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"characters/store"
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

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("store tests need Postgres: DATABASE_URL is unusable (%v); run `make search`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("store tests need Postgres: cannot connect (%v); run `make search`", err)
	}

	schema := schemaName()
	// The name is generated here (not user input), so it is safe to interpolate.
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create test schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(c, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Logf("drop test schema %s: %v", schema, err)
		}
		admin.Close()
	})

	s, err := store.Open(ctx, withSearchPath(dsn, schema))
	if err != nil {
		t.Fatalf("open store on schema %s: %v", schema, err)
	}
	t.Cleanup(s.Close)
	return s
}

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
	return dsn + sep + "search_path=" + schema
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
