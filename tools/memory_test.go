package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"squad/store"
	"squad/store/storetest"
	"squad/tools"
)

func TestRememberAndRecall(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remember", "recall")
	ctx := context.Background()

	remembered := runTool(t, env, "remember", `{"text":"prefers short answers","category":"preference","core":true}`)
	if !strings.Contains(remembered.Text, "remembered") || !strings.Contains(remembered.Text, "prefers short answers") {
		t.Fatalf("remember: %q", remembered.Text)
	}
	if len(remembered.Undo) != 1 || remembered.Undo[0].Op != "forget_fact" {
		t.Fatalf("undo: %+v", remembered.Undo)
	}
	facts, _ := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if len(facts) != 1 || facts[0].SourceMessageID == nil || facts[0].SourceKind != "tool" || !facts[0].Core {
		t.Fatalf("stored fact: %+v", facts)
	}

	// Recalling returns quoted data, not instructions.
	got := runTool(t, env, "recall", `{"query":"short answers"}`)
	if !strings.Contains(got.Text, "<recall>") || !strings.Contains(got.Text, "not instructions") ||
		!strings.Contains(got.Text, "prefers short answers") {
		t.Fatalf("recall: %q", got.Text)
	}
	if facts, _ = db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}}); facts[0].UseCount != 1 {
		t.Fatalf("use_count: %+v", facts[0])
	}

	// The same text twice is reported, not stored twice.
	dup := runTool(t, env, "remember", `{"text":"prefers short answers"}`)
	if !strings.Contains(dup.Text, "already known") || len(dup.Undo) != 0 {
		t.Fatalf("duplicate: %+v", dup)
	}

	// Messages with the same words are searchable across conversations.
	if _, err := db.AddMessage(ctx, store.Message{ConversationID: "daily:2026-12-01", Role: "user", Content: "aku suka jawaban singkat"}); err != nil {
		t.Fatal(err)
	}
	if out := runTool(t, env, "recall", `{"query":"jawaban singkat"}`); !strings.Contains(out.Text, "message ") {
		t.Fatalf("message recall: %q", out.Text)
	}
}

func TestRememberSupersedes(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remember", "recall")
	ctx := context.Background()

	old := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	bali, err := tools.AddFact(ctx, db, store.Fact{
		CharacterID: "assistant", Text: "lives in Bali", Category: "circumstance", Core: true,
		SourceKind: "user", ValidFrom: old,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := runTool(t, env, "remember", `{"text":"lives in Jakarta","category":"circumstance","core":true,"supersedes":"lives in Bali"}`)
	if !strings.Contains(res.Text, "superseded #") || len(res.Undo) != 2 {
		t.Fatalf("supersede: %+v", res)
	}
	closed, _ := db.GetFact(ctx, "assistant", bali.ID)
	if closed.Status != "superseded" || closed.ValidTo == nil {
		t.Fatalf("old fact: %+v", closed)
	}
	current, _ := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if len(current) != 1 || current[0].Text != "lives in Jakarta" {
		t.Fatalf("active facts: %+v", current)
	}

	// A period query still finds Bali; a current query does not.
	past := runTool(t, env, "recall", `{"query":"Bali","from":"2024-01-01","to":"2024-12-31"}`)
	if !strings.Contains(past.Text, "lives in Bali") || !strings.Contains(past.Text, "superseded") {
		t.Fatalf("past recall: %q", past.Text)
	}
	now := runTool(t, env, "recall", `{"query":"Bali"}`)
	if strings.Contains(now.Text, "lives in Bali") {
		t.Fatalf("superseded fact in a current recall: %q", now.Text)
	}

	// Undo puts Bali back and forgets Jakarta.
	undo, _ := json.Marshal(res.Undo)
	if err := db.FinishToolCall(ctx, env.ToolCallID, res.Text, undo, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.ApplyUndo(ctx, db, "assistant", env.ToolCallID, "", 0); err != nil {
		t.Fatal(err)
	}
	restored, _ := db.GetFact(ctx, "assistant", bali.ID)
	if restored.Status != "active" || restored.ValidTo != nil || restored.SupersededBy != nil {
		t.Fatalf("restored fact: %+v", restored)
	}
	active, _ := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if len(active) != 1 || active[0].Text != "lives in Bali" {
		t.Fatalf("active after undo: %+v", active)
	}
}

func TestForgetNeedsConfirmationAndHidesTheSource(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remember", "forget", "recall")
	ctx := context.Background()

	runTool(t, env, "remember", `{"text":"mom is visiting in January","category":"person"}`)
	facts, _ := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	sourceID := *facts[0].SourceMessageID

	// confirm=false only describes what would happen.
	ask := runTool(t, env, "forget", `{"fact":"mom is visiting"}`)
	if !strings.Contains(ask.Text, "About to forget") || !strings.Contains(ask.Text, "confirm") {
		t.Fatalf("confirmation prompt: %q", ask.Text)
	}
	if f, _ := db.GetFact(ctx, "assistant", facts[0].ID); f.Status != "active" {
		t.Fatal("fact was forgotten before confirmation")
	}

	// confirm=true forgets the fact and hides its source message.
	done := runTool(t, env, "forget", `{"fact":"mom is visiting","confirm":true}`)
	if !strings.Contains(done.Text, "forgot") || !strings.Contains(done.Text, "hid the message") {
		t.Fatalf("forget: %q", done.Text)
	}
	if f, _ := db.GetFact(ctx, "assistant", facts[0].ID); f.Status != "forgotten" {
		t.Fatalf("fact: %+v", f)
	}
	if m, _ := db.GetMessage(ctx, "assistant", sourceID); m.ForgottenAt == nil {
		t.Fatalf("source message not hidden: %+v", m)
	}
	// And recall cannot bring it back.
	if out := runTool(t, env, "recall", `{"query":"mom visiting January"}`); strings.Contains(out.Text, "mom is visiting") {
		t.Fatalf("forgotten fact came back: %q", out.Text)
	}
}

func TestMemoryValidation(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remember", "forget", "recall", "undo")
	ctx := context.Background()

	cases := []struct {
		name, tool, args string
		want             error
	}{
		{"empty text", "remember", `{"text":"  "}`, tools.ErrInvalid},
		{"long text", "remember", `{"text":"` + strings.Repeat("x", 301) + `"}`, tools.ErrInvalid},
		{"bad category", "remember", `{"text":"x","category":"astrology"}`, tools.ErrInvalid},
		{"empty query", "recall", `{"query":" "}`, tools.ErrInvalid},
		{"bad date", "recall", `{"query":"x","from":"someday"}`, tools.ErrInvalid},
		{"unknown fact", "forget", `{"fact":"nothing like this"}`, store.ErrNotFound},
		{"empty fact", "forget", `{"fact":""}`, tools.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := tools.New().Run(ctx, env, c.tool, c.args); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}

	// Undo of remember is refused once the fact changed (an edit bumps updated_at).
	runTool(t, env, "remember", `{"text":"likes tea"}`)
}

type fakeEmbedder struct{ model string }

func (f fakeEmbedder) Model() string { return f.model }
func (f fakeEmbedder) Dims() int     { return 1024 }
func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, 1024)
		vec[0] = 1
		out[i] = vec
	}
	return out, nil
}

func TestRecallHybridFindsCrossLanguage(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "recall")
	ctx := context.Background()
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "daily:2026-11-01", CharacterID: "assistant", Day: &day}); err != nil {
		t.Fatal(err)
	}
	msg, err := db.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: "capek banget, deadline bikin stres"})
	if err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, 1024)
	vec[0] = 1
	if err := db.SetMessageEmbedding(ctx, msg.ID, vec, "fake"); err != nil {
		t.Fatal(err)
	}

	// Keyword-only cannot match the English question.
	if out := runTool(t, env, "recall", `{"query":"burned out"}`); !strings.Contains(out.Text, "no matches") {
		t.Fatalf("keyword recall: %q", out.Text)
	}
	// With an embedder, the Indonesian message is found by meaning.
	env.Embedder = fakeEmbedder{model: "fake"}
	out := runTool(t, env, "recall", `{"query":"burned out"}`)
	if !strings.Contains(out.Text, "capek banget") {
		t.Fatalf("hybrid recall: %q", out.Text)
	}
}

func TestRememberRecordsToldBy(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remember")
	ctx := context.Background()

	env.Speaker = "ria"
	runTool(t, env, "remember", `{"text":"likes tea","category":"preference"}`)
	env.Speaker = ""
	runTool(t, env, "remember", `{"text":"likes coffee","category":"preference"}`)
	env.Speaker = "me"
	runTool(t, env, "remember", `{"text":"likes juice","category":"preference"}`)

	facts, err := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if err != nil || len(facts) != 3 {
		t.Fatalf("facts: %+v, %v", facts, err)
	}
	byText := map[string]store.Fact{}
	for _, f := range facts {
		byText[f.Text] = f
	}
	if f := byText["likes tea"]; f.ToldBy != "ria" || f.About != "me" {
		t.Fatalf("told from ria: %+v", f)
	}
	for _, text := range []string{"likes coffee", "likes juice"} {
		if f := byText[text]; f.ToldBy != "" || f.About != "me" {
			t.Fatalf("%s: %+v", text, f)
		}
	}
}

func TestRememberAboutMustBeAParticipant(t *testing.T) {
	db := storetest.New(t)
	env, _ := envFor(t, db, "remember")
	env.FactSubjects = []string{"me", "assistant", "ria"}
	ctx := context.Background()

	runTool(t, env, "remember", `{"text":"hates mornings","about":"ria"}`)
	facts, err := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts: %+v, %v", facts, err)
	}
	if facts[0].About != "ria" || facts[0].ToldBy != "" {
		t.Fatalf("about ria: %+v", facts[0])
	}

	if _, err := tools.New().Run(ctx, env, "remember", `{"text":"likes tea","about":"bob"}`); err == nil ||
		!strings.Contains(err.Error(), "about must be") {
		t.Fatalf("unknown about: %v", err)
	}
}
