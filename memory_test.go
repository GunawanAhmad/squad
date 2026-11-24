package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"characters/store"
	"characters/store/storetest"
)

// Test fixtures live in a temp dir, never in memory/: that directory is the
// user's private profile and is never read by tests.
func TestImportMemoryFacts(t *testing.T) {
	db := storetest.New(t)
	dir := t.TempDir()
	writeLegacy := func(name string, facts ...legacyFact) {
		t.Helper()
		raw, err := json.Marshal(legacyMemoryFile{Facts: facts})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLegacy("socrates.json",
		legacyFact{Text: "prefers short answers", Source: "chat"},
		legacyFact{Text: "has a cat named Miso", Source: "chat"},
	)
	writeLegacy("ria.json", legacyFact{Text: "works in UTC+8"})
	if err := os.WriteFile(filepath.Join(dir, "profile.md"), []byte("ignored by import"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := importMemoryFacts(context.Background(), db, dir)
	if err != nil || n != 3 {
		t.Fatalf("import: %d, %v", n, err)
	}
	facts, err := db.Facts(context.Background(), store.FactFilter{Status: []string{"active"}})
	if err != nil || len(facts) != 3 {
		t.Fatalf("facts: %+v, %v", facts, err)
	}
	for _, f := range facts {
		if !f.Core || f.SourceKind != "user" {
			t.Fatalf("imported fact should be core and user-sourced: %+v", f)
		}
	}

	// A restart imports nothing twice.
	if n, err := importMemoryFacts(context.Background(), db, dir); err != nil || n != 0 {
		t.Fatalf("second import: %d, %v", n, err)
	}
	// A missing directory is not an error (fresh checkout without memory/).
	if n, err := importMemoryFacts(context.Background(), db, filepath.Join(dir, "nope")); err != nil || n != 0 {
		t.Fatalf("missing dir: %d, %v", n, err)
	}
}

func TestMemoryPromptRendersCoreFactsWithBudget(t *testing.T) {
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	facts := []store.Fact{
		{ID: 1, Text: "prefers short answers", Category: "preference", ValidFrom: from},
		{ID: 2, Text: "lives in Jakarta", Category: "circumstance", ValidFrom: from},
	}
	out := memoryPrompt("I am a developer.", facts)
	for _, want := range []string{"<memory>", "<about_user>", "I am a developer.", "<facts>", "[1] (preference) prefers short answers", "Notes may be outdated"} {
		if !strings.Contains(out, want) {
			t.Fatalf("memory prompt missing %q:\n%s", want, out)
		}
	}
	if memoryPrompt("", nil) != "" {
		t.Fatal("empty memory should render nothing")
	}

	// The budget holds: a long list is cut, and the block stays bounded.
	var many []store.Fact
	for i := 0; i < 40; i++ {
		many = append(many, store.Fact{ID: int64(i + 1), Text: fmt.Sprintf("%03d %s", i, strings.Repeat("x", 150)), Category: "general"})
	}
	long := memoryPrompt("", many)
	if len(long) > coreFactsBudget+300 {
		t.Fatalf("core facts block is %d chars, budget %d", len(long), coreFactsBudget)
	}
	if !strings.Contains(long, "[1]") {
		t.Fatalf("budget dropped the first fact:\n%s", long)
	}
	if strings.Contains(long, "[40]") {
		t.Fatalf("budget did not cut the list")
	}
}
