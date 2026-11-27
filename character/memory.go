package character

import (
	"context"
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

// MemoryDir holds profile.md, a file the user edits for every character.
const MemoryDir = "memory"

// memoryGuard keeps the always-in-prompt notes from outranking what was just said.
const memoryGuard = "Notes may be outdated. The current messages win. Don't mention these notes unless asked."

// coreFactsBudget is how many characters of core facts go into the prompt.
// Context v2 keeps the same number (M-7 asserts the total budget).
const coreFactsBudget = 2000

// ReadProfile returns memory/profile.md, or "" when the user has not written one.
func ReadProfile() string {
	raw, err := os.ReadFile(filepath.Join(MemoryDir, "profile.md"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Print(err)
		}
		return ""
	}
	return string(raw)
}

// ProfilePrompt is the room-mode memory block: profile.md only. The user wrote
// it for everyone, so it is not gated by the share gate.
func ProfilePrompt() string {
	return memoryPrompt(ReadProfile(), nil)
}

// Memory is the always-in-prompt block for one agent: the shared
// profile.md the user writes, plus that agent's own core facts. Context "none"
// turns it off; another agent's facts are never part of it.
func Memory(chars *Roster, db *store.Store) func(string) string {
	return func(charID string) string {
		c, ok := chars.Get(charID)
		if !ok || c.Context == "none" {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		facts, err := db.CoreFacts(ctx, charID, 100)
		if err != nil {
			log.Printf("memory: reading core facts for %s: %v", charID, err)
		}
		return memoryPrompt(ReadProfile(), facts)
	}
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
			line := fmt.Sprintf("- [%d] (%s) %s%s\n", f.ID, f.Category, f.Text, f.ProvenanceNote())
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
			line := fmt.Sprintf("- [%d] (%s) %s%s\n", facts[0].ID, facts[0].Category, facts[0].Text, facts[0].ProvenanceNote())
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
