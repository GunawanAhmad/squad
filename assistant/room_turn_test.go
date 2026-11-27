package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"squad/llm"
	"squad/store"
	"squad/store/storetest"
	"squad/tools"
)

func roomRunner(t *testing.T, db *store.Store, fake *llm.Fake, profile func() string) *Runner {
	t.Helper()
	return New(Config{
		Store: db, Tools: tools.New(), DB: db,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): fake,
			string(llm.ClaudeProvider):   fake,
		},
		Memory:    func(string) string { return "<memory>one-to-one core facts</memory>" },
		Profile:   profile,
		GateModel: "fake",
		Now:       func() time.Time { return testNow },
		Location:  testLoc,
	})
}

func seedRoomFacts(t *testing.T, db *store.Store, charID string) (a, b store.Fact) {
	t.Helper()
	ctx := context.Background()
	var err error
	a, err = db.AddFact(ctx, store.Fact{CharacterID: charID, Text: "behind on thesis", Category: "circumstance", Core: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err = db.AddFact(ctx, store.Fact{CharacterID: charID, Text: "supervisor annoyed", Category: "work", Core: true})
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestRoomTurnLoadsOnlySharedFacts(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	a, b := seedRoomFacts(t, db, "socrates")

	fake := llm.NewFake(
		llm.FakeStep{Text: fmt.Sprintf(`{"share":["f%d"]}`, a.ID)},
		llm.FakeStep{Text: "They mention a thesis."},
	)
	r := roomRunner(t, db, fake, func() string { return "<memory>profile only</memory>" })

	char := Character{ID: "socrates", Name: "Socrates", Provider: "opencode", Model: "fake",
		Persona: "You are Socrates.", Context: "memory", Discretion: 8, Tools: []string{"remember"}}
	_, err := r.Turn(ctx, TurnRequest{
		ConversationID: "room:abc:socrates", Character: char, Input: "what's up with the user?",
		Speaker: "ria", Names: map[string]string{"me": "the user", "ria": "Ria", "socrates": "Socrates"},
		Listeners: []string{"Ria"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d (want a gate call, then the turn)", len(reqs))
	}
	system := reqs[1].System
	if !strings.Contains(system, "behind on thesis") {
		t.Fatalf("shared fact missing from the prompt:\n%s", system)
	}
	if strings.Contains(system, "supervisor annoyed") {
		t.Fatalf("kept fact leaked into the prompt:\n%s", system)
	}
	if !strings.Contains(system, "profile only") || strings.Contains(system, "one-to-one core facts") {
		t.Fatalf("room memory must be the profile only:\n%s", system)
	}

	runs, err := db.Runs(ctx, "room:abc:socrates")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
	var trace struct {
		Shared []string `json:"gate_shared_ids"`
		Kept   []string `json:"gate_kept_ids"`
	}
	if err := json.Unmarshal(runs[0].Context, &trace); err != nil {
		t.Fatalf("trace: %v (%s)", err, runs[0].Context)
	}
	if len(trace.Shared) != 1 || trace.Shared[0] != fmt.Sprintf("f%d", a.ID) {
		t.Fatalf("shared ids in trace: %+v", trace.Shared)
	}
	if len(trace.Kept) != 1 || trace.Kept[0] != fmt.Sprintf("f%d", b.ID) {
		t.Fatalf("kept ids in trace: %+v", trace.Kept)
	}
}

func TestRoomTurnGateFailsClosed(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	seedRoomFacts(t, db, "socrates")

	fake := llm.NewFake(
		llm.FakeStep{Text: "sure!"}, // prose, not JSON
		llm.FakeStep{Text: "They mention a thesis."},
	)
	r := roomRunner(t, db, fake, func() string { return "" })
	char := Character{ID: "socrates", Name: "Socrates", Provider: "opencode", Model: "fake",
		Persona: "You are Socrates.", Context: "memory", Discretion: 8}
	res, err := r.Turn(ctx, TurnRequest{
		ConversationID: "room:abc:socrates", Character: char, Input: "what's up with the user?",
		Speaker: "ria", Listeners: []string{"Ria"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status: %s", res.Status)
	}
	system := fake.Requests()[1].System
	if strings.Contains(system, "behind on thesis") || strings.Contains(system, "supervisor annoyed") {
		t.Fatalf("a failed gate shared something:\n%s", system)
	}
}

func TestRoomTurnHasNoRecall(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	fake := llm.NewFake(llm.FakeStep{Text: "Who is asking?"})
	r := roomRunner(t, db, fake, func() string { return "" })
	char := Character{ID: "socrates", Name: "Socrates", Provider: "opencode", Model: "fake",
		Persona: "You are Socrates.", Context: "memory", Tools: []string{"remember", "recall"}}
	if _, err := r.Turn(ctx, TurnRequest{
		ConversationID: "room:abc:socrates", Character: char, Input: "hello",
		Speaker: "ria", Listeners: []string{"Ria"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, spec := range fake.Requests()[0].Tools {
		if spec.Name == "recall" {
			t.Fatalf("room turn offered recall: %+v", fake.Requests()[0].Tools)
		}
	}
	if len(fake.Requests()[0].Tools) != 1 {
		t.Fatalf("tools: %+v", fake.Requests()[0].Tools)
	}
}

func TestOneToOneTurnUnchanged(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	seedRoomFacts(t, db, "socrates")

	fake := llm.NewFake(llm.FakeStep{Text: "Who is asking?"})
	r := roomRunner(t, db, fake, func() string { return "<memory>profile only</memory>" })
	char := Character{ID: "socrates", Name: "Socrates", Provider: "opencode", Model: "fake",
		Persona: "You are Socrates.", Context: "memory", Tools: []string{"remember", "recall"}}
	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "socrates-chat", Character: char, Input: "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(fake.Requests()) != 1 {
		t.Fatalf("a 1:1 turn made %d model calls (want 1)", len(fake.Requests()))
	}
	system := fake.Requests()[0].System
	if !strings.Contains(system, "one-to-one core facts") {
		t.Fatalf("1:1 memory changed:\n%s", system)
	}
	if len(fake.Requests()[0].Tools) != 2 {
		t.Fatalf("1:1 tools changed: %+v", fake.Requests()[0].Tools)
	}
}
