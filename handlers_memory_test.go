package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"characters/store"
)

func dayOf(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func ptrDay(y int, m time.Month, d int) *time.Time {
	v := dayOf(y, m, d)
	return &v
}

func TestMemoryFactsAPI(t *testing.T) {
	_, db, mux := newRecordServer(t)
	ctx := context.Background()

	// A fact with a source message, then a superseding fact.
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "daily:2026-11-01", CharacterID: "assistant"}); err != nil {
		t.Fatal(err)
	}
	source, err := db.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: "I live in Bali"})
	if err != nil {
		t.Fatal(err)
	}
	bali, err := db.AddFact(ctx, store.Fact{Text: "lives in Bali", Category: "circumstance", Core: true, SourceMessageID: &source.ID, ValidFrom: dayOf(2024, 3, 1)})
	if err != nil {
		t.Fatal(err)
	}
	jakarta, err := db.AddFact(ctx, store.Fact{Text: "lives in Jakarta", Category: "circumstance", Core: true, ValidFrom: dayOf(2026, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeOn(ctx, bali.ID, jakarta.ID, dayOf(2026, 12, 1)); err != nil {
		t.Fatal(err)
	}

	list := decode[struct {
		Facts []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/facts", nil))
	if len(list.Facts) != 2 {
		t.Fatalf("facts: %+v", list)
	}
	active := decode[struct {
		Facts []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/facts?status=active", nil))
	if len(active.Facts) != 1 || active.Facts[0]["text"] != "lives in Jakarta" || active.Facts[0]["validTo"] != nil {
		t.Fatalf("active: %+v", active)
	}

	hist := decode[struct {
		History []map[string]any `json:"history"`
	}](t, do(t, mux, "GET", "/api/memory/facts/"+itoa(bali.ID)+"/history", nil))
	if len(hist.History) != 2 || hist.History[0]["text"] != "lives in Bali" || hist.History[1]["text"] != "lives in Jakarta" {
		t.Fatalf("history: %+v", hist)
	}
	if hist.History[0]["validTo"] != "2026-11-30" || hist.History[0]["supersededBy"] != float64(jakarta.ID) {
		t.Fatalf("history validity: %+v", hist.History[0])
	}

	// Editing is validated like the tools are.
	rec := do(t, mux, "PATCH", "/api/memory/facts/"+itoa(jakarta.ID), map[string]any{"core": false, "category": "work"})
	if rec.Code != http.StatusOK || decode[map[string]any](t, rec)["core"] != false {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "PATCH", "/api/memory/facts/"+itoa(jakarta.ID), map[string]any{"category": "astrology"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad category: %d", rec.Code)
	}
	if rec := do(t, mux, "PATCH", "/api/memory/facts/"+itoa(jakarta.ID), map[string]any{"text": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad text: %d", rec.Code)
	}

	// Forget needs confirm=true and hides the source message.
	if rec := do(t, mux, "POST", "/api/memory/facts/"+itoa(bali.ID)+"/forget", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("forget without confirm: %d", rec.Code)
	}
	if rec := do(t, mux, "POST", "/api/memory/facts/"+itoa(bali.ID)+"/forget", map[string]any{"confirm": true}); rec.Code != http.StatusNoContent {
		t.Fatalf("forget: %d %s", rec.Code, rec.Body.String())
	}
	if f, _ := db.GetFact(ctx, bali.ID); f.Status != "forgotten" {
		t.Fatalf("fact: %+v", f)
	}
	if m, _ := db.GetMessage(ctx, source.ID); m.ForgottenAt == nil {
		t.Fatalf("source message not hidden: %+v", m)
	}
}

func TestMemorySearchAndForgetMessages(t *testing.T) {
	_, db, mux := newRecordServer(t)
	ctx := context.Background()
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "daily:2026-11-01", CharacterID: "assistant", Day: ptrDay(2026, 11, 1)}); err != nil {
		t.Fatal(err)
	}
	msg, err := db.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: "the deadline for the report is next month"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := db.AddFact(ctx, store.Fact{Text: "report deadline is next month", Category: "work", ValidFrom: dayOf(2026, 11, 1)})
	if err != nil {
		t.Fatal(err)
	}

	found := decode[struct {
		Messages []map[string]any `json:"messages"`
		Facts    []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/search?q=deadline", nil))
	if len(found.Messages) != 1 || len(found.Facts) != 1 {
		t.Fatalf("search: %+v", found)
	}
	if found.Messages[0]["day"] != "2026-11-01" || found.Messages[0]["characterId"] != "assistant" {
		t.Fatalf("message hit: %+v", found.Messages[0])
	}

	if rec := do(t, mux, "POST", "/api/memory/messages/"+itoa(msg.ID)+"/forget", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("forget message: %d", rec.Code)
	}
	after := decode[struct {
		Messages []map[string]any `json:"messages"`
	}](t, do(t, mux, "GET", "/api/memory/search?q=deadline", nil))
	if len(after.Messages) != 0 {
		t.Fatalf("forgotten message still searchable: %+v", after)
	}
	if found := decode[struct {
		Facts []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/search?q=deadline", nil)); len(found.Facts) != 1 || found.Facts[0]["id"] != float64(fact.ID) {
		t.Fatalf("fact search affected by message forget: %+v", found)
	}

	// A whole day: one more message, then forget the day (the already
	// forgotten one is not counted again).
	second, err := db.AddMessage(ctx, store.Message{ConversationID: "daily:2026-11-01", Role: "user", Content: "another note"})
	if err != nil {
		t.Fatal(err)
	}
	n := decode[struct {
		Forgotten int `json:"forgotten"`
	}](t, do(t, mux, "POST", "/api/memory/days/2026-11-01/forget", nil))
	if n.Forgotten != 1 {
		t.Fatalf("forget day: %+v", n)
	}
	if m, _ := db.GetMessage(ctx, second.ID); m.ForgottenAt == nil {
		t.Fatalf("day message not forgotten: %+v", m)
	}
}

func TestMemoryProfileAPI(t *testing.T) {
	s, _, mux := newRecordServer(t)
	if got := decode[map[string]any](t, do(t, mux, "GET", "/api/memory/profile", nil)); got["profile"] != "" {
		t.Fatalf("empty profile: %+v", got)
	}
	rec := do(t, mux, "PUT", "/api/memory/profile", map[string]any{"profile": "# Me\n\n- writes Go\n"})
	if rec.Code != http.StatusOK {
		t.Fatalf("put profile: %d %s", rec.Code, rec.Body.String())
	}
	got := decode[map[string]any](t, do(t, mux, "GET", "/api/memory/profile", nil))
	if !strings.Contains(got["profile"].(string), "writes Go") {
		t.Fatalf("profile: %+v", got)
	}
	raw, err := os.ReadFile(filepath.Join(s.memoryDir, "profile.md"))
	if err != nil || !strings.Contains(string(raw), "writes Go") {
		t.Fatalf("file: %q, %v", raw, err)
	}
	info, err := os.Stat(filepath.Join(s.memoryDir, "profile.md"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile permissions: %v, %v", info.Mode(), err)
	}
}
