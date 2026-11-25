package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"characters/llm"
	"characters/store"
	"characters/store/storetest"
	"characters/tools"
)

// One agent's memory must never reach another agent. These tests use the real
// store and the fake model; each check would fail if a read lost its owner
// filter or pre-retrieval went back to "all conversations".

// memoryBlock mirrors the app's core-fact block closely enough to assert what
// the prompt contains.
func memoryBlock(ctx context.Context, db *store.Store, characterID string) (string, error) {
	facts, err := db.CoreFacts(ctx, characterID, 100)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "<fact id=%d>%s</fact>\n", f.ID, f.Text)
	}
	return b.String(), nil
}

func isolationRunner(t *testing.T, db *store.Store, fake *llm.Fake) *Runner {
	t.Helper()
	ctx := context.Background()
	r := New(Config{
		Store: db, Tools: tools.New(), DB: db,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): fake,
			string(llm.ClaudeProvider):   fake,
		},
		Memory: func(characterID string) string {
			block, err := memoryBlock(ctx, db, characterID)
			if err != nil {
				t.Fatalf("core facts for %s: %v", characterID, err)
			}
			return block
		},
		Now:      func() time.Time { return testNow },
		Location: testLoc,
		Embedder: fixedEmbedder{model: "fake"},
	})
	return r
}

func TestMemoryIsPerAgent(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	now := testNow
	day := LogicalDay(now, testLoc, 4*time.Hour)

	fake := llm.NewFake(
		// Ria: the user tells her something durable.
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "r1", Name: "remember", Arguments: `{"text":"is moving to Jakarta","category":"circumstance","core":true}`}}},
		llm.FakeStep{Text: "Noted that you're moving to Jakarta."},
		// Ria's next turn sees her own fact and message.
		llm.FakeStep{Text: "You told me Jakarta."},
		// Assistant: recall by keyword and by vector, both must miss.
		llm.FakeStep{ToolCalls: []llm.ToolCall{
			{ID: "a1", Name: "recall", Arguments: `{"query":"Jakarta"}`},
			{ID: "a2", Name: "recall", Arguments: `{"query":"zeppelin"}`},
		}},
		llm.FakeStep{Text: "I don't know anything about that."},
		// Socrates: tries to forget a fact that belongs to Ria.
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "s1", Name: "forget", Arguments: `{"fact":"@@RIA_FACT@@","confirm":true}`}}},
		llm.FakeStep{Text: "I know that I know nothing."},
	)
	r := isolationRunner(t, db, fake)

	ria := Character{ID: "ria", Name: "Ria", Provider: "opencode", Model: "fake",
		Persona: "You are Ria.", Context: "memory", Tools: []string{"remember", "recall", "forget"}}
	assistantChar := Character{ID: "assistant", Name: "Assistant", Provider: "opencode", Model: "fake",
		Persona: "You are the assistant.", Context: "memory", Daily: true,
		Tools: []string{"remember", "recall", "forget"}}
	socrates := Character{ID: "socrates", Name: "Socrates", Provider: "opencode", Model: "fake",
		Persona: "You are Socrates.", Context: "memory", Tools: []string{"remember", "recall", "forget"}}

	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "ria-chat", Character: ria, Input: "I'm moving to Jakarta."}, nil); err != nil {
		t.Fatal(err)
	}
	facts, err := db.Facts(ctx, "ria", store.FactFilter{Status: []string{"active"}})
	if err != nil || len(facts) != 1 {
		t.Fatalf("ria's facts: %+v, %v", facts, err)
	}
	riaFact := facts[0]
	if riaFact.SourceMessageID == nil {
		t.Fatal("ria's fact has no source message")
	}
	// Make the leaked rows maximally findable: the same vector as every query.
	if err := db.SetFactEmbedding(ctx, riaFact.ID, riaFact.Text, unitVector(3), "fake"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMessageEmbedding(ctx, *riaFact.SourceMessageID, unitVector(3), "fake"); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "ria-chat", Character: ria, Input: "Where am I moving?"}, nil); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	riaSystem := reqs[len(reqs)-1].System
	if !strings.Contains(riaSystem, "Jakarta") {
		t.Fatalf("ria's own fact is missing from her prompt:\n%s", riaSystem)
	}

	// Ria owns the fact; the other agents do not, in core facts or in the prompt.
	for _, id := range []string{"assistant", "socrates"} {
		core, err := db.CoreFacts(ctx, id, 10)
		if err != nil || len(core) != 0 {
			t.Fatalf("%s core facts: %+v, %v", id, core, err)
		}
	}

	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "daily:2026-12-01", Character: assistantChar, Input: "burned out"}, nil); err != nil {
		t.Fatal(err)
	}
	reqs = fake.Requests()
	assistantSystem := reqs[len(reqs)-1].System
	if strings.Contains(assistantSystem, "Jakarta") || strings.Contains(assistantSystem, "I'm moving") {
		t.Fatalf("ria's memory reached the assistant prompt:\n%s", assistantSystem)
	}
	// Both recall calls (keyword and vector) said "no matches".
	runs, err := db.Runs(ctx, "daily:2026-12-01")
	if err != nil || len(runs) != 1 {
		t.Fatalf("assistant runs: %+v, %v", runs, err)
	}
	calls, err := db.ToolCalls(ctx, runs[0].ID)
	if err != nil || len(calls) != 2 {
		t.Fatalf("assistant tool calls: %+v, %v", calls, err)
	}
	for _, c := range calls {
		if c.Name != "recall" || !strings.Contains(c.Result, "no matches") || strings.Contains(c.Result, "Jakarta") {
			t.Fatalf("assistant recall leaked another agent's memory: %+v", c)
		}
	}

	// forget resolves only the caller's facts: Ria's fact is not found for
	// Socrates, neither by its text nor by its id.
	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "socrates-chat", Character: socrates,
		Input: "Forget that I'm moving to Jakarta."}, nil); err != nil {
		t.Fatal(err)
	}
	socRuns, err := db.Runs(ctx, "socrates-chat")
	if err != nil || len(socRuns) != 1 {
		t.Fatalf("socrates runs: %+v, %v", socRuns, err)
	}
	socCalls, err := db.ToolCalls(ctx, socRuns[0].ID)
	if err != nil || len(socCalls) != 1 {
		t.Fatalf("socrates tool calls: %+v, %v", socCalls, err)
	}
	if socCalls[0].Status != "error" || !strings.Contains(socCalls[0].Result, "not found") {
		t.Fatalf("cross-owner forget was not refused: %+v", socCalls[0])
	}
	if got, _ := db.GetFact(ctx, "ria", riaFact.ID); got.Status != "active" {
		t.Fatalf("ria's fact was forgotten by another agent: %+v", got)
	}
	reqs = fake.Requests()
	if strings.Contains(reqs[len(reqs)-1].System, "Jakarta") {
		t.Fatalf("ria's memory reached the socrates prompt:\n%s", reqs[len(reqs)-1].System)
	}

	// The evening "I noted: …" list is built from the assistant's own facts.
	if _, err := db.ApplyFactOps(ctx, "ria", day, []store.FactOp{{Op: "ADD", Text: "has a sister named Ana", Category: "person"}}); err != nil {
		t.Fatal(err)
	}
	set := Settings{TZ: "Asia/Makassar", Loc: testLoc, DayStartsAt: 4 * time.Hour, MorningAt: 8 * time.Hour, EveningAt: 21 * time.Hour}
	if rec := LoadRecords(ctx, db, "assistant", day, set); len(rec.NotedFacts) != 0 {
		t.Fatalf("assistant's evening noted ria's extraction: %+v", rec.NotedFacts)
	}
	// The extraction exists, but only for its owner (created_at is real time in
	// the test, so query the last hour rather than the fixed logical day).
	if noted, err := db.NotedFacts(ctx, "assistant", time.Now().Add(-time.Hour), 8); err != nil || len(noted) != 0 {
		t.Fatalf("assistant's noted facts: %+v, %v", noted, err)
	}
	if noted, err := db.NotedFacts(ctx, "ria", time.Now().Add(-time.Hour), 8); err != nil || len(noted) != 1 || noted[0].CharacterID != "ria" {
		t.Fatalf("ria's noted facts: %+v, %v", noted, err)
	}
}

// Context "none" turns the memory stack off completely: no facts and no
// pre-retrieval, even though the character owns the rows.
func TestContextNoneTurnsMemoryOff(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "ria-old", CharacterID: "ria"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMessage(ctx, store.Message{ConversationID: "ria-old", Role: "user", Content: "my passport expires in March"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddFact(ctx, store.Fact{CharacterID: "ria", Text: "passport expires in March", Category: "circumstance", Core: true}); err != nil {
		t.Fatal(err)
	}

	fake := llm.NewFake(llm.FakeStep{Text: "ok"})
	// The app's memory callback returns "" for context:none; this mirrors it.
	r := New(Config{
		Store: db, Tools: tools.New(), DB: db,
		Models:   map[string]llm.Model{string(llm.OpenCodeProvider): fake},
		Memory:   func(string) string { return "" },
		Now:      func() time.Time { return testNow },
		Location: testLoc,
		Embedder: fixedEmbedder{model: "fake"},
	})
	char := Character{ID: "ria", Name: "Ria", Provider: "opencode", Model: "fake", Context: "none"}
	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "ria-off", Character: char, Input: "passport expires"}, nil); err != nil {
		t.Fatal(err)
	}
	system := fake.Requests()[0].System
	if strings.Contains(system, "passport") || strings.Contains(system, "<recall>") {
		t.Fatalf("context:none still got memory:\n%s", system)
	}
}

func unitVector(i int) []float32 {
	v := make([]float32, 1024)
	v[i] = 1
	return v
}
