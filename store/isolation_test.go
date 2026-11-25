package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"characters/store"
	"characters/store/storetest"
)

// Isolation is enforced here, not in a prompt: every fact and message read
// names one character, and there is no all-characters variant.

func TestFactsArePerCharacter(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	ria, err := s.AddFact(ctx, store.Fact{CharacterID: "ria", Text: "is moving to Jakarta", Category: "circumstance", Core: true, ValidFrom: day(2026, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := s.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "likes black coffee", Category: "preference", Core: true, ValidFrom: day(2026, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}

	// Collections and core facts never cross owners.
	for _, tc := range []struct{ id, want string }{{"ria", ria.Text}, {"assistant", assistant.Text}} {
		all, err := s.Facts(ctx, tc.id, store.FactFilter{Status: []string{"active"}})
		if err != nil || len(all) != 1 || all[0].Text != tc.want || all[0].CharacterID != tc.id {
			t.Fatalf("%s facts: %+v, %v", tc.id, all, err)
		}
		core, err := s.CoreFacts(ctx, tc.id, 10)
		if err != nil || len(core) != 1 || core[0].Text != tc.want {
			t.Fatalf("%s core facts: %+v, %v", tc.id, core, err)
		}
	}

	// A read with another owner is not found, not "empty".
	if got, err := s.GetFact(ctx, "socrates", ria.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-owner GetFact: %+v, %v", got, err)
	}
	// A write with another owner cannot touch it either.
	if err := s.ForgetFact(ctx, "assistant", ria.ID, day(2026, 12, 2)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-owner ForgetFact: %v", err)
	}
	if got, _ := s.GetFact(ctx, "ria", ria.ID); got.Status != "active" {
		t.Fatalf("ria's fact changed: %+v", got)
	}
	// And a fact can only supersede another fact of the same owner.
	if err := s.SupersedeOn(ctx, "assistant", ria.ID, assistant.ID, day(2026, 12, 2)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-owner supersede: %v", err)
	}
	if got, _ := s.GetFact(ctx, "ria", ria.ID); got.Status != "active" {
		t.Fatalf("cross-owner supersede took effect: %+v", got)
	}
	// The supersede chain is scoped too.
	if _, err := s.FactHistory(ctx, "assistant", ria.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-owner history: %v", err)
	}
	// Owner is mandatory: there is no accidental "all characters".
	if _, err := s.Facts(ctx, "", store.FactFilter{}); !errors.Is(err, store.ErrOwnerRequired) {
		t.Fatalf("missing owner: %v", err)
	}
	if _, err := s.SearchFacts(ctx, store.FactQuery{Text: "Jakarta"}); !errors.Is(err, store.ErrOwnerRequired) {
		t.Fatalf("search without owner: %v", err)
	}
}

func TestFactSearchAndNotedArePerCharacter(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	riaFact, err := s.AddFact(ctx, store.Fact{CharacterID: "ria", Text: "is moving to Jakarta", Category: "circumstance", ValidFrom: day(2026, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "likes black coffee", Category: "preference", ValidFrom: day(2026, 12, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFactEmbedding(ctx, riaFact.ID, riaFact.Text, unit(7), "fake"); err != nil {
		t.Fatal(err)
	}

	// Keyword and vector search both stay inside the owner.
	if hits, err := s.SearchFacts(ctx, store.FactQuery{CharacterID: "assistant", Text: "Jakarta"}); err != nil || len(hits) != 0 {
		t.Fatalf("assistant keyword search saw ria's fact: %+v, %v", hits, err)
	}
	if hits, err := s.SearchFacts(ctx, store.FactQuery{CharacterID: "assistant", Text: "nothing matches", Vector: unit(7), EmbeddingModel: "fake"}); err != nil || len(hits) != 0 {
		t.Fatalf("assistant vector search saw ria's fact: %+v, %v", hits, err)
	}
	if hits, err := s.SearchFacts(ctx, store.FactQuery{CharacterID: "ria", Text: "nothing matches", Vector: unit(7), EmbeddingModel: "fake"}); err != nil || len(hits) != 1 || hits[0].ID != riaFact.ID {
		t.Fatalf("ria's own vector search: %+v, %v", hits, err)
	}

	// Extracted facts (the evening "I noted" source) are per character.
	if _, err := s.ApplyFactOps(ctx, "ria", day(2026, 12, 1), []store.FactOp{
		{Op: "ADD", Text: "has a sister named Ana", Category: "person"},
	}); err != nil {
		t.Fatal(err)
	}
	if noted, err := s.NotedFacts(ctx, "assistant", time.Now().Add(-time.Hour), 8); err != nil || len(noted) != 0 {
		t.Fatalf("assistant noted ria's extraction: %+v, %v", noted, err)
	}
	if noted, err := s.NotedFacts(ctx, "ria", time.Now().Add(-time.Hour), 8); err != nil || len(noted) != 1 || noted[0].CharacterID != "ria" {
		t.Fatalf("ria's noted facts: %+v, %v", noted, err)
	}
}

func TestMessagesArePerCharacter(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "ria-chat", CharacterID: "ria"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "daily:2026-12-01", CharacterID: "assistant", Day: ptr(day(2026, 12, 1))}); err != nil {
		t.Fatal(err)
	}
	riaMsg, err := s.AddMessage(ctx, store.Message{ConversationID: "ria-chat", Role: "user", Content: "aku pindah ke Jakarta"})
	if err != nil {
		t.Fatal(err)
	}
	assistantMsg, err := s.AddMessage(ctx, store.Message{ConversationID: "daily:2026-12-01", Role: "user", Content: "aku suka kopi hitam"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageEmbedding(ctx, riaMsg.ID, unit(5), "fake"); err != nil {
		t.Fatal(err)
	}

	// Keyword and vector paths are both scoped.
	if hits, err := s.SearchMessages(ctx, store.MessageQuery{CharacterID: "assistant", Text: "Jakarta"}); err != nil || len(hits) != 0 {
		t.Fatalf("assistant keyword search saw ria's message: %+v, %v", hits, err)
	}
	if hits, err := s.SearchMessages(ctx, store.MessageQuery{CharacterID: "assistant", Text: "nothing", Vector: unit(5), EmbeddingModel: "fake"}); err != nil || len(hits) != 0 {
		t.Fatalf("assistant vector search saw ria's message: %+v, %v", hits, err)
	}
	if hits, err := s.SearchMessages(ctx, store.MessageQuery{CharacterID: "ria", Text: "Jakarta"}); err != nil || len(hits) != 1 || hits[0].ID != riaMsg.ID {
		t.Fatalf("ria's own message search: %+v, %v", hits, err)
	}
	if _, err := s.SearchMessages(ctx, store.MessageQuery{Text: "Jakarta"}); !errors.Is(err, store.ErrOwnerRequired) {
		t.Fatalf("search without owner: %v", err)
	}

	// The day's messages and the day-close grouping are per character.
	from := time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	// A persona thread has no conversations.day, so its message is selected by
	// created_at inside the logical day.
	riaDated, err := s.AddMessageAt(ctx, store.Message{ConversationID: "ria-chat", Role: "user", Content: "aku pindah ke Jakarta"}, from.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.DayMessages(ctx, "assistant", day(2026, 12, 1), from, to)
	if err != nil || len(msgs) != 1 || msgs[0].ID != assistantMsg.ID {
		t.Fatalf("assistant day messages: %+v, %v", msgs, err)
	}
	msgs, err = s.DayMessages(ctx, "ria", day(2026, 12, 1), from, to)
	if err != nil || len(msgs) != 1 || msgs[0].ID != riaDated.ID {
		t.Fatalf("ria day messages: %+v, %v", msgs, err)
	}
	for _, m := range msgs {
		if m.CharacterID != "ria" {
			t.Fatalf("cross-owner day message: %+v", m)
		}
	}
	chars, err := s.DayCharacters(ctx, day(2026, 12, 1), from, to)
	if err != nil || len(chars) != 2 || chars[0] != "assistant" || chars[1] != "ria" {
		t.Fatalf("day characters: %+v, %v", chars, err)
	}

	// Forget-message, forget-day and GetMessage are scoped too.
	if err := s.ForgetMessage(ctx, "assistant", riaMsg.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("assistant forgot ria's message: %v", err)
	}
	if n, err := s.ForgetDay(ctx, "assistant", day(2026, 12, 1), from, to); err != nil || n != 1 {
		t.Fatalf("assistant forget day: %d, %v", n, err)
	}
	if m, _ := s.GetMessage(ctx, "ria", riaDated.ID); m.ForgottenAt != nil {
		t.Fatal("assistant forget-day hid ria's message")
	}
	if _, err := s.GetMessage(ctx, "assistant", riaMsg.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-owner GetMessage: %v", err)
	}
}
