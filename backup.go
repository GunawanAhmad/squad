package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"squad/assistant"
)

const (
	backupDir   = "backups"
	backupKeep  = 14
	backupLimit = 60 * time.Second
)

var backupName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.dump$`)

// dailyBackup writes one pg_dump per local day. It is lazy: the code path that
// notices "the app is being used today" calls Maybe. In Phase 0 that is the
// first turn; Phase 1's Due() will call the same method from its poll.
type dailyBackup struct {
	dir  string
	keep int
	dump func(ctx context.Context, w io.Writer) error

	mu sync.Mutex
}

func newDailyBackup(dir string) *dailyBackup {
	return &dailyBackup{dir: dir, keep: backupKeep, dump: composeDump}
}

// Maybe dumps today's database unless backups/<day>.dump already exists. A
// failed dump leaves no file, so the next turn retries. Errors never fail the
// turn: a missing backup is logged, not fatal.
func (b *dailyBackup) Maybe(ctx context.Context, now time.Time) {
	if b == nil {
		return
	}
	day := now.In(assistant.TimeZone()).Format("2006-01-02")
	path := filepath.Join(b.dir, day+".dump")

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		log.Printf("backup: %v", err)
		return
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("backup: %v", err)
		return
	}

	dctx, cancel := context.WithTimeout(ctx, backupLimit)
	defer cancel()
	dumpErr := b.dump(dctx, f)
	closeErr := f.Close()
	if dumpErr != nil {
		_ = os.Remove(tmp)
		log.Printf("backup: %v", dumpErr)
		return
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		log.Printf("backup: %v", closeErr)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("backup: %v", err)
		return
	}
	log.Printf("backup: wrote %s", path)
	b.prune()
}

func (b *dailyBackup) prune() {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		log.Printf("backup: %v", err)
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && backupName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // YYYY-MM-DD sorts chronologically
	for len(names) > b.keep {
		name := names[0]
		names = names[1:]
		if err := os.Remove(filepath.Join(b.dir, name)); err != nil {
			log.Printf("backup: remove %s: %v", name, err)
		}
	}
}

// composeDump runs pg_dump in the Compose Postgres. It uses the local socket
// inside the container, so no password is involved. The app must run from the
// repo root (make dev does).
func composeDump(ctx context.Context, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", "compose", "exec", "-T", "postgres",
		"pg_dump", "-Fc", "-U", "characters", "-d", "characters")
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("pg_dump: %w: %s", err, msg)
		}
		return fmt.Errorf("pg_dump: %w", err)
	}
	return nil
}
