package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"characters/store"
	"characters/store/storetest"
)

func TestFactsLifecycle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	bali := day(2024, 3, 1)
	f, err := s.AddFact(ctx, store.Fact{Text: "lives in Bali", Category: "circumstance", Core: true, ValidFrom: bali})
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != "active" || f.SourceKind != "user" || !store.Day(f.ValidFrom).Equal(bali) || f.ValidTo != nil {
		t.Fatalf("added fact: %+v", f)
	}
	if _, err := s.AddFact(ctx, store.Fact{Text: strings.Repeat("x", 301)}); err == nil {
		t.Fatal("text over 300 characters must be rejected by the database")
	}

	// Superseding closes the old fact the day before the new one starts.
	jakarta := day(2026, 12, 1)
	moved, err := s.AddFact(ctx, store.Fact{Text: "lives in Jakarta", Category: "circumstance", Core: true, ValidFrom: jakarta})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeOn(ctx, f.ID, moved.ID, jakarta); err != nil {
		t.Fatal(err)
	}
	old, err := s.GetFact(ctx, f.ID)
	if err != nil || old.Status != "superseded" || old.SupersededBy == nil || *old.SupersededBy != moved.ID {
		t.Fatalf("superseded fact: %+v, %v", old, err)
	}
	if old.ValidTo == nil || !store.Day(*old.ValidTo).Equal(day(2026, 11, 30)) {
		t.Fatalf("valid_to: %+v", old.ValidTo)
	}
	active, err := s.Facts(ctx, store.FactFilter{Status: []string{"active"}})
	if err != nil || len(active) != 1 || active[0].ID != moved.ID {
		t.Fatalf("active facts: %+v, %v", active, err)
	}
	core := true
	coreFacts, err := s.Facts(ctx, store.FactFilter{Status: []string{"active"}, Core: &core})
	if err != nil || len(coreFacts) != 1 {
		t.Fatalf("core facts: %+v, %v", coreFacts, err)
	}

	// Forgetting keeps the row but removes it from search and context.
	if err := s.ForgetFact(ctx, moved.ID, day(2026, 12, 2)); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFact(ctx, moved.ID); f.Status != "forgotten" {
		t.Fatalf("forgotten fact: %+v", f)
	}
	hits, err := s.SearchFacts(ctx, store.FactQuery{Text: "Jakarta"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("forgotten fact in search: %+v, %v", hits, err)
	}
	// A period query still finds the superseded Bali fact.
	past, err := s.SearchFacts(ctx, store.FactQuery{
		Text: "Bali", From: ptr(day(2024, 1, 1)), To: ptr(day(2024, 12, 31)),
	})
	if err != nil || len(past) != 1 || past[0].ID != f.ID || past[0].Status != "superseded" {
		t.Fatalf("period fact search: %+v, %v", past, err)
	}

	if err := s.TouchFacts(ctx, []int64{moved.ID}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFact(ctx, moved.ID); f.UseCount != 1 || f.LastUsedAt == nil {
		t.Fatalf("touched fact: %+v", f)
	}

	edited := old
	edited.Text, edited.Category, edited.Core = "lives in Bali (edited)", "person", false
	if _, err := s.UpdateFact(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetFact(ctx, edited.ID); got.Text != "lives in Bali (edited)" || got.Core {
		t.Fatalf("edited fact: %+v", got)
	}
}

func TestImportFactsIdempotent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	facts := []store.Fact{
		{Text: "prefers short answers", Category: "preference", Core: true},
		{Text: "has a cat named Miso", Category: "person", Core: true},
	}
	n, err := s.ImportFacts(ctx, facts)
	if err != nil || n != 2 {
		t.Fatalf("import: %d, %v", n, err)
	}
	n, err = s.ImportFacts(ctx, facts)
	if err != nil || n != 0 {
		t.Fatalf("second import duplicated: %d, %v", n, err)
	}
	all, err := s.Facts(ctx, store.FactFilter{Status: []string{"active"}})
	if err != nil || len(all) != 2 || !all[0].Core || all[0].SourceKind != "user" {
		t.Fatalf("imported facts: %+v, %v", all, err)
	}
}

func TestSearchMessagesAndForget(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "persona", CharacterID: "socrates"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "daily:2026-11-01", CharacterID: "assistant", Day: ptr(day(2026, 11, 1))}); err != nil {
		t.Fatal(err)
	}
	m1, err := s.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: "tolong ingat deadline laporan bulan depan"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(ctx, store.Message{ConversationID: "persona", Role: "assistant", Content: "the unexamined life is not worth living"}); err != nil {
		t.Fatal(err)
	}

	// Indonesian words match Indonesian text, no stemming needed.
	hits, err := s.SearchMessages(ctx, store.MessageQuery{Text: "laporan"})
	if err != nil || len(hits) != 1 || hits[0].ID != m1.ID {
		t.Fatalf("search: %+v, %v", hits, err)
	}
	if hits[0].CharacterID != "assistant" || hits[0].Day == nil || !store.Day(*hits[0].Day).Equal(day(2026, 11, 1)) {
		t.Fatalf("hit context: %+v", hits[0])
	}
	hits, err = s.SearchMessages(ctx, store.MessageQuery{Text: "laporan", Limit: 5, Exclude: []int64{m1.ID}})
	if err != nil || len(hits) != 0 {
		t.Fatalf("excluded hit: %+v, %v", hits, err)
	}
	hits, err = s.SearchMessages(ctx, store.MessageQuery{Text: "laporan", From: ptr(day(2026, 11, 2))})
	if err != nil || len(hits) != 0 {
		t.Fatalf("date filter: %+v, %v", hits, err)
	}

	// Forget hides the message from search and from the prompt window.
	if err := s.ForgetMessage(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.SearchMessages(ctx, store.MessageQuery{Text: "laporan"}); len(hits) != 0 {
		t.Fatalf("forgotten message found: %+v", hits)
	}
	if recent, _ := s.RecentMessages(ctx, "daily:2026-11-01", 10); len(recent) != 0 {
		t.Fatalf("forgotten message in context: %+v", recent)
	}
	// The UI history still has it; only the model is blind to it.
	if all, _ := s.Messages(ctx, "daily:2026-11-01", 0, 0); len(all) != 1 || all[0].ForgottenAt == nil {
		t.Fatalf("forgotten message disappeared from history: %+v", all)
	}
}

func TestForgetDay(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	d := day(2026, 11, 1)
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "daily:2026-11-01", CharacterID: "assistant", Day: &d}); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"one", "two"} {
		if _, err := s.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.ForgetDay(ctx, d, time.Time{}, time.Time{})
	if err != nil || n != 2 {
		t.Fatalf("forget day: %d, %v", n, err)
	}
	msgs, _ := s.Messages(ctx, "daily:2026-11-01", 0, 0)
	for _, m := range msgs {
		if m.ForgottenAt == nil {
			t.Fatalf("message not forgotten: %+v", m)
		}
	}
}

func TestImportDoesNotResurrectForgottenFact(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	facts := []store.Fact{{Text: "a note I will delete", Category: "general"}}
	if n, err := s.ImportFacts(ctx, facts); err != nil || n != 1 {
		t.Fatalf("import: %d, %v", n, err)
	}
	active, _ := s.Facts(ctx, store.FactFilter{Status: []string{"active"}})
	if len(active) != 1 {
		t.Fatalf("active: %+v", active)
	}
	if err := s.ForgetFact(ctx, active[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A restart imports the same file again; the forgotten note must stay gone.
	if n, err := s.ImportFacts(ctx, facts); err != nil || n != 0 {
		t.Fatalf("re-import: %d, %v", n, err)
	}
	if active, _ := s.Facts(ctx, store.FactFilter{Status: []string{"active"}}); len(active) != 0 {
		t.Fatalf("forgotten imported fact came back: %+v", active)
	}
}

func TestForgetClearsSummary(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	d := day(2026, 11, 1)
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "daily:2026-11-01", CharacterID: "assistant", Day: &d}); err != nil {
		t.Fatal(err)
	}
	m, err := s.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: "a private detail"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummary(ctx, "daily:2026-11-01", "summary quoting a private detail", m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetMessage(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetConversation(ctx, "daily:2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	if c.Summary != "" || c.SummaryUpto != nil {
		t.Fatalf("summary survived a forget: %q, %v", c.Summary, c.SummaryUpto)
	}
}

func TestUpdateFactClearsItsEmbedding(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	f, err := s.AddFact(ctx, store.Fact{Text: "lives in Bali", Category: "circumstance", ValidFrom: day(2026, 1, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetFactEmbedding(ctx, f.ID, f.Text, unit(0), "fake"); err != nil {
		t.Fatal(err)
	}
	f, _ = s.GetFact(ctx, f.ID)
	f.Text = "lives in Jakarta"
	if _, err := s.UpdateFact(ctx, f); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.UnembeddedFacts(ctx, 10)
	if len(pending) != 1 || pending[0].ID != f.ID {
		t.Fatalf("edited fact not pending: %+v", pending)
	}
	// A worker that read the old text before the edit must not write a stale
	// vector back.
	if err := s.SetFactEmbedding(ctx, f.ID, "lives in Bali", unit(1), "fake"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.UnembeddedFacts(ctx, 10); len(pending) != 1 {
		t.Fatalf("stale vector written: %+v", pending)
	}
}

func TestSetMessageEmbeddingSkipsForgotten(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "c", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	m, err := s.AddMessage(ctx, store.Message{ConversationID: "c", Role: "user", Content: "a message long enough to be embedded"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetMessage(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageEmbedding(ctx, m.ID, unit(0), "fake"); err != nil {
		t.Fatal(err)
	}
	if n, _, err := s.EmbeddedCounts(ctx, "fake"); err != nil || n != 0 {
		t.Fatalf("forgotten message got an embedding: %d, %v", n, err)
	}
}

func TestApplyFactOpsKeepsSourceMessage(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.CreateConversation(ctx, store.Conversation{ID: "daily:2026-11-01", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	m, err := s.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: "my sister is Ana"})
	if err != nil {
		t.Fatal(err)
	}
	day := day(2026, 11, 1)
	if _, err := s.ApplyFactOps(ctx, day, []store.FactOp{{
		Op: "ADD", Text: "my sister is Ana", Category: "person", Core: false, SourceMessageID: &m.ID,
	}}); err != nil {
		t.Fatal(err)
	}
	facts, _ := s.Facts(ctx, store.FactFilter{Status: []string{"active"}})
	if len(facts) != 1 || facts[0].SourceMessageID == nil || *facts[0].SourceMessageID != m.ID {
		t.Fatalf("source lost: %+v", facts)
	}
}
