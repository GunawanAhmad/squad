package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func backupDay(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDailyBackupOncePerDay(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	b := &dailyBackup{dir: dir, keep: 14, dump: func(_ context.Context, w io.Writer) error {
		calls.Add(1)
		_, err := io.WriteString(w, "dump")
		return err
	}}
	now := backupDay("2026-12-01")

	var wg sync.WaitGroup
	for range 4 { // concurrent turns must not write twice
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Maybe(context.Background(), now)
		}()
	}
	wg.Wait()
	b.Maybe(context.Background(), now.Add(3*time.Hour))

	if got := calls.Load(); got != 1 {
		t.Fatalf("dump calls = %d, want 1", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "2026-12-01.dump"))
	if err != nil || string(data) != "dump" {
		t.Fatalf("backup file: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-12-01.dump.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// A second day writes a second file.
	b.Maybe(context.Background(), backupDay("2026-12-02"))
	if got := calls.Load(); got != 2 {
		t.Fatalf("dump calls = %d, want 2", got)
	}
}

func TestDailyBackupFailureRetries(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	b := &dailyBackup{dir: dir, keep: 14, dump: func(_ context.Context, _ io.Writer) error {
		if calls.Add(1) == 1 {
			return errors.New("pg_dump exploded")
		}
		return nil
	}}
	now := backupDay("2026-12-01")

	b.Maybe(context.Background(), now)
	if _, err := os.Stat(filepath.Join(dir, "2026-12-01.dump")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed dump left a file: %v", err)
	}
	b.Maybe(context.Background(), now)
	if _, err := os.Stat(filepath.Join(dir, "2026-12-01.dump")); err != nil {
		t.Fatalf("retry did not write: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("dump calls = %d, want 2", got)
	}
}

func TestDailyBackupPrune(t *testing.T) {
	dir := t.TempDir()
	b := &dailyBackup{dir: dir, keep: 3, dump: func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "x")
		return err
	}}
	for _, day := range []string{"2026-11-01.dump", "2026-11-02.dump", "2026-11-03.dump", "not-a-backup.txt"} {
		if err := os.WriteFile(filepath.Join(dir, day), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b.Maybe(context.Background(), backupDay("2026-12-01"))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	want := []string{"2026-11-02.dump", "2026-11-03.dump", "2026-12-01.dump", "not-a-backup.txt"}
	if len(got) != len(want) {
		t.Fatalf("files = %v", got)
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("missing %s in %v", name, got)
		}
	}
	if got["2026-11-01.dump"] {
		t.Fatalf("oldest backup not pruned: %v", got)
	}
}
