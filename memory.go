package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"squad/store"
)

// memory/ holds profile.md (a file the user edits) and, from Phase 0, one
// <char>.json of notes per character. Phase 2 imports those notes into the
// facts table once; after that the database is the only fact store. profile.md
// stays a file.

type legacyFact struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Source  string `json:"source"`
	Updated string `json:"updated"`
}

type legacyMemoryFile struct {
	Facts []legacyFact `json:"facts"`
}

const memDir = "memory"

// memoryGuard keeps the always-in-prompt notes from outranking what was just said.
const memoryGuard = "Notes may be outdated. The current messages win. Don't mention these notes unless asked."

// coreFactsBudget is how many characters of core facts go into the prompt.
// Context v2 keeps the same number (M-7 asserts the total budget).
const coreFactsBudget = 2000

// importMemoryFacts copies every memory/<char>.json fact into the facts table
// (active, source kind 'user', core: they used to be always in the prompt).
// It is idempotent: an active fact with the same text is skipped, so restarts
// do not duplicate anything. Missing directory is fine.
const legacyImportSetting = "legacy_facts_imported"

func importMemoryFacts(ctx context.Context, db *store.Store, dir string) (int, error) {
	if set, err := db.Settings(ctx); err == nil && set[legacyImportSetting] == "1" {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var facts []store.Fact
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return 0, err
		}
		var m legacyMemoryFile
		if err := json.Unmarshal(raw, &m); err != nil {
			log.Printf("memory: %s is not valid JSON (%v); skipped", e.Name(), err)
			continue
		}
		for _, f := range m.Facts {
			if strings.TrimSpace(f.Text) == "" {
				continue
			}
			facts = append(facts, store.Fact{
				Text: f.Text, Category: "general", Core: true, SourceKind: "user",
				ValidFrom: time.Now(),
			})
		}
	}
	// The old memory/*.json notes are the assistant's: personas never had
	// their own files and must not inherit the assistant's facts.
	n, err := db.ImportFacts(ctx, assistantCharacterID, facts)
	if err != nil {
		return n, err
	}
	// One shot: a fact the user edits or forgets must not be re-imported on
	// the next restart.
	if err := db.SetSettings(ctx, map[string]string{legacyImportSetting: "1"}); err != nil {
		return n, err
	}
	return n, nil
}

// readProfile returns memory/profile.md, or "" when the user has not written one.
func readProfile() string {
	raw, err := os.ReadFile(filepath.Join(memDir, "profile.md"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Print(err)
		}
		return ""
	}
	return string(raw)
}

// profilePrompt is the room-mode memory block: profile.md only. The user wrote
// it for everyone, so it is not gated by the share gate.
func profilePrompt() string {
	return memoryPrompt(readProfile(), nil)
}

// memoryPrompt renders the always-in-prompt block: the profile file plus core
// facts from the database, capped so a long list cannot crowd out the day.
func memoryPrompt(profile string, facts []store.Fact) string {
	profile = strings.TrimSpace(profile)
	if profile == "" && len(facts) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n<memory>\n")
	b.WriteString(memoryGuard)
	b.WriteString("\n")

	if len(facts) > 0 {
		b.WriteString("\n<facts>\n")
		used := 0
		shown := 0
		for _, f := range facts {
			line := fmt.Sprintf("- [%d] (%s) %s\n", f.ID, f.Category, f.Text)
			if used+len(line) > coreFactsBudget {
				break
			}
			b.WriteString(line)
			used += len(line)
			shown++
		}
		if shown == 0 {
			// One fact is longer than the budget; show it truncated rather
			// than nothing at all.
			line := fmt.Sprintf("- [%d] (%s) %s\n", facts[0].ID, facts[0].Category, facts[0].Text)
			b.WriteString(line[:min(len(line), coreFactsBudget)])
		}
		b.WriteString("</facts>\n")
	}

	if profile != "" {
		b.WriteString("\n<about_user>\n")
		b.WriteString(profile)
		b.WriteString("\n</about_user>\n")
	}

	b.WriteString("\n</memory>\n")
	return b.String()
}
