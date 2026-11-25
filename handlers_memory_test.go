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
	bali, err := db.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "lives in Bali", Category: "circumstance", Core: true, SourceMessageID: &source.ID, ValidFrom: dayOf(2024, 3, 1)})
	if err != nil {
		t.Fatal(err)
	}
	jakarta, err := db.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "lives in Jakarta", Category: "circumstance", Core: true, ValidFrom: dayOf(2026, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SupersedeOn(ctx, "assistant", bali.ID, jakarta.ID, dayOf(2026, 12, 1)); err != nil {
		t.Fatal(err)
	}

	list := decode[struct {
		Facts []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/facts?character=assistant", nil))
	if len(list.Facts) != 2 {
		t.Fatalf("facts: %+v", list)
	}
	active := decode[struct {
		Facts []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/facts?status=active&character=assistant", nil))
	if len(active.Facts) != 1 || active.Facts[0]["text"] != "lives in Jakarta" || active.Facts[0]["validTo"] != nil {
		t.Fatalf("active: %+v", active)
	}

	hist := decode[struct {
		History []map[string]any `json:"history"`
	}](t, do(t, mux, "GET", "/api/memory/facts/"+itoa(bali.ID)+"/history?character=assistant", nil))
	if len(hist.History) != 2 || hist.History[0]["text"] != "lives in Bali" || hist.History[1]["text"] != "lives in Jakarta" {
		t.Fatalf("history: %+v", hist)
	}
	if hist.History[0]["validTo"] != "2026-11-30" || hist.History[0]["supersededBy"] != float64(jakarta.ID) {
		t.Fatalf("history validity: %+v", hist.History[0])
	}

	// Editing is validated like the tools are.
	rec := do(t, mux, "PATCH", "/api/memory/facts/"+itoa(jakarta.ID)+"?character=assistant", map[string]any{"core": false, "category": "work"})
	if rec.Code != http.StatusOK || decode[map[string]any](t, rec)["core"] != false {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, "PATCH", "/api/memory/facts/"+itoa(jakarta.ID)+"?character=assistant", map[string]any{"category": "astrology"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad category: %d", rec.Code)
	}
	if rec := do(t, mux, "PATCH", "/api/memory/facts/"+itoa(jakarta.ID)+"?character=assistant", map[string]any{"text": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad text: %d", rec.Code)
	}

	// Forget needs confirm=true and hides the source message.
	if rec := do(t, mux, "POST", "/api/memory/facts/"+itoa(bali.ID)+"/forget?character=assistant", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("forget without confirm: %d", rec.Code)
	}
	if rec := do(t, mux, "POST", "/api/memory/facts/"+itoa(bali.ID)+"/forget?character=assistant", map[string]any{"confirm": true}); rec.Code != http.StatusNoContent {
		t.Fatalf("forget: %d %s", rec.Code, rec.Body.String())
	}
	if f, _ := db.GetFact(ctx, "assistant", bali.ID); f.Status != "forgotten" {
		t.Fatalf("fact: %+v", f)
	}
	if m, _ := db.GetMessage(ctx, "assistant", source.ID); m.ForgottenAt == nil {
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
	fact, err := db.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "report deadline is next month", Category: "work", ValidFrom: dayOf(2026, 11, 1)})
	if err != nil {
		t.Fatal(err)
	}

	found := decode[struct {
		Messages []map[string]any `json:"messages"`
		Facts    []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/search?q=deadline&character=assistant", nil))
	if len(found.Messages) != 1 || len(found.Facts) != 1 {
		t.Fatalf("search: %+v", found)
	}
	if found.Messages[0]["day"] != "2026-11-01" || found.Messages[0]["characterId"] != "assistant" {
		t.Fatalf("message hit: %+v", found.Messages[0])
	}

	if rec := do(t, mux, "POST", "/api/memory/messages/"+itoa(msg.ID)+"/forget?character=assistant", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("forget message: %d", rec.Code)
	}
	after := decode[struct {
		Messages []map[string]any `json:"messages"`
	}](t, do(t, mux, "GET", "/api/memory/search?q=deadline&character=assistant", nil))
	if len(after.Messages) != 0 {
		t.Fatalf("forgotten message still searchable: %+v", after)
	}
	if found := decode[struct {
		Facts []map[string]any `json:"facts"`
	}](t, do(t, mux, "GET", "/api/memory/search?q=deadline&character=assistant", nil)); len(found.Facts) != 1 || found.Facts[0]["id"] != float64(fact.ID) {
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
	}](t, do(t, mux, "POST", "/api/memory/days/2026-11-01/forget?character=assistant", nil))
	if n.Forgotten != 1 {
		t.Fatalf("forget day: %+v", n)
	}
	if m, _ := db.GetMessage(ctx, "assistant", second.ID); m.ForgottenAt == nil {
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

// The Memory screen is the only cross-agent surface, and it always acts on the
// explicitly selected character.
func TestMemoryScreenIsPerCharacter(t *testing.T) {
	s, db, mux := newRecordServer(t)
	s.chars["ria"] = Character{ID: "ria", Name: "Ria"}
	ctx := context.Background()

	assistantFact, err := db.AddFact(ctx, store.Fact{CharacterID: "assistant", Text: "likes black coffee", Category: "preference", ValidFrom: dayOf(2026, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}
	riaFact, err := db.AddFact(ctx, store.Fact{CharacterID: "ria", Text: "is moving to Jakarta", Category: "circumstance", ValidFrom: dayOf(2026, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}

	list := func(character string) []map[string]any {
		t.Helper()
		return decode[struct {
			Facts []map[string]any `json:"facts"`
		}](t, do(t, mux, "GET", "/api/memory/facts?character="+character, nil)).Facts
	}
	if got := list("assistant"); len(got) != 1 || got[0]["text"] != assistantFact.Text {
		t.Fatalf("assistant facts: %+v", got)
	}
	if got := list("ria"); len(got) != 1 || got[0]["text"] != riaFact.Text || got[0]["characterId"] != "ria" {
		t.Fatalf("ria facts: %+v", got)
	}
	if rec := do(t, mux, "GET", "/api/memory/facts", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing character accepted: %d", rec.Code)
	}
	if rec := do(t, mux, "GET", "/api/memory/facts?character=nobody", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown character accepted: %d", rec.Code)
	}

	// Edits, history and forget cannot reach another character's fact.
	if rec := do(t, mux, "PATCH", "/api/memory/facts/"+itoa(riaFact.ID)+"?character=assistant", map[string]any{"core": false}); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-character patch: %d", rec.Code)
	}
	if rec := do(t, mux, "GET", "/api/memory/facts/"+itoa(riaFact.ID)+"/history?character=assistant", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-character history: %d", rec.Code)
	}
	if rec := do(t, mux, "POST", "/api/memory/facts/"+itoa(riaFact.ID)+"/forget?character=assistant", map[string]any{"confirm": true}); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-character forget: %d", rec.Code)
	}
	if got, _ := db.GetFact(ctx, "ria", riaFact.ID); got.Status != "active" {
		t.Fatalf("cross-character forget took effect: %+v", got)
	}

	// Search is scoped to the selected character too.
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "ria-chat", CharacterID: "ria"}); err != nil {
		t.Fatal(err)
	}
	riaMsg, err := db.AddMessageAt(ctx, store.Message{ConversationID: "ria-chat", Role: "user", Content: "aku pindah ke Jakarta"}, time.Date(2026, 11, 30, 21, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "daily:2026-12-01", CharacterID: "assistant", Day: ptrDay(2026, 12, 1)}); err != nil {
		t.Fatal(err)
	}
	assistantMsg, err := db.AddMessage(ctx, store.Message{ConversationID: "daily:2026-12-01", Role: "user", Content: "kopi hitam enak"})
	if err != nil {
		t.Fatal(err)
	}
	search := func(character string) []map[string]any {
		t.Helper()
		return decode[struct {
			Messages []map[string]any `json:"messages"`
		}](t, do(t, mux, "GET", "/api/memory/search?q=Jakarta&character="+character, nil)).Messages
	}
	if got := search("assistant"); len(got) != 0 {
		t.Fatalf("assistant search saw ria's message: %+v", got)
	}
	if got := search("ria"); len(got) != 1 || got[0]["id"] != float64(riaMsg.ID) {
		t.Fatalf("ria search: %+v", got)
	}

	// Forget-day applies to the selected character's conversations only.
	if res := decode[struct {
		Forgotten int `json:"forgotten"`
	}](t, do(t, mux, "POST", "/api/memory/days/2026-12-01/forget?character=ria", nil)); res.Forgotten != 1 {
		t.Fatalf("ria forget-day: %+v", res)
	}
	if m, _ := db.GetMessage(ctx, "ria", riaMsg.ID); m.ForgottenAt == nil {
		t.Fatal("ria's forget-day did not hide its own message")
	}
	if m, _ := db.GetMessage(ctx, "assistant", assistantMsg.ID); m.ForgottenAt != nil {
		t.Fatal("ria's forget-day hid the assistant's message")
	}
	if res := decode[struct {
		Forgotten int `json:"forgotten"`
	}](t, do(t, mux, "POST", "/api/memory/days/2026-12-01/forget?character=assistant", nil)); res.Forgotten != 1 {
		t.Fatalf("assistant forget-day: %+v", res)
	}
	if m, _ := db.GetMessage(ctx, "assistant", assistantMsg.ID); m.ForgottenAt == nil {
		t.Fatal("assistant's forget-day did not hide its own message")
	}
	if _, err := db.GetFact(ctx, "assistant", assistantFact.ID); err != nil {
		t.Fatalf("assistant fact gone: %v", err)
	}
}
