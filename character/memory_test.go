package character

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"squad/store"
	"squad/store/storetest"
	"squad/tools"
)

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

// Every character's always-in-prompt block is its own; only profile.md is shared.
func TestCharacterMemoryIsPerAgent(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "likes black coffee", Category: "preference", Core: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddFact(ctx, store.Fact{CharacterID: "ria", Text: "is moving to Jakarta", Category: "circumstance", Core: true}); err != nil {
		t.Fatal(err)
	}
	chars := NewRoster([]Character{
		{ID: "assistant", Context: "memory"},
		{ID: "ria", Context: "memory"},
		{ID: "mute", Context: "none"},
	}, tools.New())
	memoryFor := Memory(chars, db)

	assistantBlock := memoryFor("assistant")
	if !strings.Contains(assistantBlock, "likes black coffee") || strings.Contains(assistantBlock, "Jakarta") {
		t.Fatalf("assistant memory crossed owners:\n%s", assistantBlock)
	}
	riaBlock := memoryFor("ria")
	if !strings.Contains(riaBlock, "Jakarta") || strings.Contains(riaBlock, "black coffee") {
		t.Fatalf("ria memory crossed owners:\n%s", riaBlock)
	}
	if got := memoryFor("mute"); got != "" {
		t.Fatalf("context none still has memory: %q", got)
	}
	if got := memoryFor("nobody"); got != "" {
		t.Fatalf("unknown character has memory: %q", got)
	}
}
