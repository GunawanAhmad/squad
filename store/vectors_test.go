package store_test

import (
	"context"
	"testing"

	"squad/store"
	"squad/store/storetest"
)

// unit returns a 1024-dim vector with 1 at i. Cosine distance then behaves like
// "which axis": close vectors score high.
func unit(i int) []float32 {
	v := make([]float32, 1024)
	v[i] = 1
	return v
}

func TestEmbeddingColumnsAndHybridSearch(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	idMessage, err := s.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "capek banget, deadline bikin stres"})
	if err != nil {
		t.Fatal(err)
	}
	english, err := s.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "a completely different topic about cooking"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageEmbedding(ctx, idMessage.ID, unit(0), "fake"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageEmbedding(ctx, english.ID, unit(1), "fake"); err != nil {
		t.Fatal(err)
	}

	// Keyword-only cannot match "burned out", so it returns nothing.
	kw, err := s.SearchMessages(ctx, store.MessageQuery{CharacterID: testOwner, Text: "burned out"})
	if err != nil || len(kw) != 0 {
		t.Fatalf("keyword: %+v, %v", kw, err)
	}
	// Hybrid puts the vector match first.
	hybrid, err := s.SearchMessages(ctx, store.MessageQuery{CharacterID: testOwner,
		Text: "burned out", Vector: unit(0), EmbeddingModel: "fake", Limit: 5,
	})
	if err != nil || len(hybrid) == 0 || hybrid[0].ID != idMessage.ID {
		t.Fatalf("hybrid should rank the vector match first: %+v, %v", hybrid, err)
	}
	// A vector from another model is never compared.
	other, err := s.SearchMessages(ctx, store.MessageQuery{CharacterID: testOwner,
		Text: "burned out", Vector: unit(0), EmbeddingModel: "other", Limit: 5,
	})
	if err != nil || len(other) != 0 {
		t.Fatalf("model mismatch should have no vector hits: %+v, %v", other, err)
	}

	// Pending rows are only the long, non-forgotten ones (short rows and tool
	// rows are never embedded).
	if _, err := s.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "short"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, store.Message{ConversationID: "c", Role: "tool", Content: "a tool result that is definitely long enough"}); err != nil {
		t.Fatal(err)
	}
	forgotten, err := s.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "a forgotten message that is long enough"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetMessage(ctx, testOwner, forgotten.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := s.UnembeddedMessages(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after embedding: %+v, %v", pending, err)
	}

	fact, err := s.AddFact(ctx, store.Fact{CharacterID: testOwner, Text: "allergic to peanuts", Category: "health", ValidFrom: day(2026, 1, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetFactEmbedding(ctx, fact.ID, fact.Text, unit(2), "fake"); err != nil {
		t.Fatal(err)
	}
	if m, f, err := s.EmbeddedCounts(ctx, "fake"); err != nil || m != 2 || f != 1 {
		t.Fatalf("counts: %d, %d, %v", m, f, err)
	}
	// Facts are hybrid too: a wordless query finds peanuts by meaning.
	facts, err := s.SearchFacts(ctx, store.FactQuery{CharacterID: testOwner, Text: "nut allergy", Vector: unit(2), EmbeddingModel: "fake", Limit: 3})
	if err != nil || len(facts) != 1 || facts[0].ID != fact.ID {
		t.Fatalf("fact hybrid: %+v, %v", facts, err)
	}

	// Clearing an embedding makes the fact pending again (text edits).
	if err := s.ClearFactEmbedding(ctx, fact.ID); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.UnembeddedFacts(ctx, 10); len(pending) != 1 || pending[0].ID != fact.ID {
		t.Fatalf("cleared fact not pending: %+v", pending)
	}
}
