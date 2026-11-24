package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"characters/llm"
	"characters/store"
	"characters/store/storetest"
)

func localDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func llmStep(text string) llm.FakeStep { return llm.FakeStep{Text: text} }

func TestRetrieve(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "old", CharacterID: "socrates"}); err != nil {
		t.Fatal(err)
	}
	old, err := db.AddMessage(ctx, store.Message{ConversationID: "old", Role: "user", Content: "capek banget, deadline bikin stres"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMessage(ctx, store.Message{ConversationID: "old", Role: "assistant", Content: "unrelated chatter"}); err != nil {
		t.Fatal(err)
	}
	day := localDay(2026, 12, 1)
	fact, err := db.AddFact(ctx, store.Fact{Text: "deadline stres bikin capek", Category: "circumstance", ValidFrom: day})
	if err != nil {
		t.Fatal(err)
	}

	items, err := Retrieve(ctx, db, "deadline stres", testLoc, "Asia/Makassar", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("retrieved: %+v", items)
	}
	if items[0].Kind != "message" || items[0].ID != old.ID || !strings.Contains(items[0].Text, "deadline") {
		t.Fatalf("message hit: %+v", items[0])
	}
	if items[1].Kind != "fact" || items[1].ID != fact.ID {
		t.Fatalf("fact hit: %+v", items[1])
	}
	if got, _ := db.GetFact(ctx, fact.ID); got.UseCount != 1 || got.LastUsedAt == nil {
		t.Fatalf("fact usage not counted: %+v", got)
	}

	// A message already in the window is not repeated.
	items, err = Retrieve(ctx, db, "deadline stres", testLoc, "Asia/Makassar", []int64{old.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "fact" {
		t.Fatalf("dedupe: %+v", items)
	}

	// Short queries and misses cost nothing.
	if items, _ := Retrieve(ctx, db, "a", testLoc, "Asia/Makassar", nil, nil); len(items) != 0 {
		t.Fatalf("short query: %+v", items)
	}
	if items, _ := Retrieve(ctx, db, "zeppelin", testLoc, "Asia/Makassar", nil, nil); len(items) != 0 {
		t.Fatalf("miss: %+v", items)
	}
}

func TestRetrieveSkipsForgotten(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	m, err := db.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "my secret deadline plan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ForgetMessage(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := Retrieve(ctx, db, "secret deadline", testLoc, "UTC", nil, nil); len(items) != 0 {
		t.Fatalf("forgotten message retrieved: %+v", items)
	}
}

func TestAssistantTurnRetrievesOldMessages(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "old", CharacterID: "socrates"}); err != nil {
		t.Fatal(err)
	}
	old, err := db.AddMessage(ctx, store.Message{ConversationID: "old", Role: "user", Content: "my passport expires in March"})
	if err != nil {
		t.Fatal(err)
	}

	r, fake := testRunner(db, llmStep("noted"), llmStep("noted"))
	char := testChar
	char.ID, char.Daily = "assistant", false
	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "persona-chat", Character: char, Input: "passport expires"}, nil); err != nil {
		t.Fatal(err)
	}
	assistantChar := char
	assistantChar.Daily = true
	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "daily:2026-12-01", Character: assistantChar, Input: "passport expires"}, nil); err != nil {
		t.Fatal(err)
	}

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	if strings.Contains(reqs[0].System, "<recall>") {
		t.Fatalf("persona got pre-retrieval:\n%s", reqs[0].System)
	}
	if !strings.Contains(reqs[1].System, "<recall>") || !strings.Contains(reqs[1].System, "passport expires in March") {
		t.Fatalf("assistant did not retrieve the old message:\n%s", reqs[1].System)
	}

	runs, err := db.Runs(ctx, "daily:2026-12-01")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
	var trace struct {
		RetrievedMessageIDs []int64  `json:"retrieved_message_ids"`
		Sections            []string `json:"sections"`
	}
	if err := json.Unmarshal(runs[0].Context, &trace); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range trace.RetrievedMessageIDs {
		if id == old.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("trace ids do not include the old message: %+v", trace)
	}
	if !containsSection(trace.Sections, "recall") {
		t.Fatalf("trace sections: %v", trace.Sections)
	}
}

type fixedEmbedder struct{ model string }

func (f fixedEmbedder) Model() string { return f.model }
func (f fixedEmbedder) Dims() int     { return 1024 }
func (f fixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, 1024)
		vec[3] = 1
		out[i] = vec
	}
	return out, nil
}

func TestRetrieveHybrid(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	msg, err := db.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "capek banget, deadline bikin stres"})
	if err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, 1024)
	vec[3] = 1
	if err := db.SetMessageEmbedding(ctx, msg.ID, vec, "fake"); err != nil {
		t.Fatal(err)
	}
	emb := fixedEmbedder{model: "fake"}
	items, err := Retrieve(ctx, db, "burned out", testLoc, "Asia/Makassar", nil, emb)
	if err != nil || len(items) == 0 || !strings.Contains(items[0].Text, "capek") {
		t.Fatalf("hybrid retrieval: %+v, %v", items, err)
	}
	// Without an embedder the same query finds nothing.
	if items, _ := Retrieve(ctx, db, "burned out", testLoc, "Asia/Makassar", nil, nil); len(items) != 0 {
		t.Fatalf("keyword retrieval found something: %+v", items)
	}
}
