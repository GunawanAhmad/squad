package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"squad/assistant"
	"squad/store"
	"squad/tools"
)

// The Memory screen: facts (list, edit, history, forget), search, forgetting one
// message or a whole day, and editing profile.md. Facts are never hard-deleted,
// and forgetting a fact also hides its source.
//
// These endpoints are the only cross-agent access in the app, and every one of
// them takes an explicit ?character= so switching the selector changes exactly
// whose memory is read or written. The agents themselves never pass through
// here: their store calls are scoped by the calling character.

// memoryCharacter reads the explicit character the screen is acting for.
func (s *server) memoryCharacter(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.URL.Query().Get("character"))
	if id == "" {
		http.Error(w, "character is required", http.StatusBadRequest)
		return "", false
	}
	if _, ok := s.chars[id]; !ok {
		http.Error(w, "unknown character", http.StatusBadRequest)
		return "", false
	}
	return id, true
}

func (s *server) memoryPath() string {
	if s.memoryDir != "" {
		return s.memoryDir
	}
	return memDir
}

func (s *server) handleListFacts(w http.ResponseWriter, r *http.Request) {
	characterID, ok := s.memoryCharacter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := queryInt(r, "limit", 100)
	if limit < 1 || limit > 500 {
		http.Error(w, "bad limit", http.StatusBadRequest)
		return
	}
	f := store.FactFilter{Category: strings.TrimSpace(q.Get("category")), Limit: limit, Order: q.Get("order")}
	switch q.Get("status") {
	case "", "active,superseded":
		f.Status = []string{"active", "superseded"}
	case "all":
	case "active", "superseded", "forgotten":
		f.Status = []string{q.Get("status")}
	default:
		http.Error(w, "bad status", http.StatusBadRequest)
		return
	}
	if v := q.Get("core"); v == "true" || v == "false" {
		core := v == "true"
		f.Core = &core
	}

	var facts []store.Fact
	var err error
	if text := strings.TrimSpace(q.Get("q")); text != "" {
		facts, err = s.records.SearchFacts(r.Context(), store.FactQuery{CharacterID: characterID, Text: text, Limit: limit})
	} else {
		facts, err = s.records.Facts(r.Context(), characterID, f)
	}
	if err != nil {
		serverError(w, "list facts", err)
		return
	}
	out := make([]map[string]any, 0, len(facts))
	for _, fact := range facts {
		out = append(out, factJSON(fact))
	}
	writeJSON(w, http.StatusOK, map[string]any{"facts": out})
}

func (s *server) handlePatchFact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	characterID, ok := s.memoryCharacter(w, r)
	if !ok {
		return
	}
	var patch struct {
		Text     *string `json:"text,omitempty"`
		Category *string `json:"category,omitempty"`
		Core     *bool   `json:"core,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	fact, err := s.records.GetFact(r.Context(), characterID, id)
	if err != nil {
		recordError(w, err)
		return
	}
	if patch.Text != nil {
		text := strings.TrimSpace(*patch.Text)
		if text == "" || len(text) > 300 {
			http.Error(w, "text must be 1-300 characters", http.StatusBadRequest)
			return
		}
		fact.Text = text
	}
	if patch.Category != nil {
		category := strings.ToLower(strings.TrimSpace(*patch.Category))
		if !store.ValidFactCategory(category) {
			http.Error(w, "unknown category", http.StatusBadRequest)
			return
		}
		fact.Category = category
	}
	if patch.Core != nil {
		fact.Core = *patch.Core
	}
	updated, err := s.records.UpdateFact(r.Context(), fact)
	if err != nil {
		serverError(w, "update fact", err)
		return
	}
	writeJSON(w, http.StatusOK, factJSON(updated))
}

func (s *server) handleFactHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	characterID, ok := s.memoryCharacter(w, r)
	if !ok {
		return
	}
	chain, err := s.records.FactHistory(r.Context(), characterID, id)
	if err != nil {
		recordError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(chain))
	for _, fact := range chain {
		out = append(out, factJSON(fact))
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

// handleForgetFact is the explicit Memory-screen delete: the UI asks first,
// then sends confirm=true. It also hides the fact's source message.
func (s *server) handleForgetFact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	characterID, ok := s.memoryCharacter(w, r)
	if !ok {
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "confirm=true is required", http.StatusBadRequest)
		return
	}
	fact, err := s.records.GetFact(r.Context(), characterID, id)
	if err != nil {
		recordError(w, err)
		return
	}
	if err := s.records.ForgetFact(r.Context(), characterID, id, s.clock()); err != nil {
		recordError(w, err)
		return
	}
	if fact.SourceMessageID != nil {
		if err := s.records.ForgetMessage(r.Context(), characterID, *fact.SourceMessageID); err != nil && !errors.Is(err, store.ErrNotFound) {
			serverError(w, "hide source message", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	characterID, ok := s.memoryCharacter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	text := strings.TrimSpace(q.Get("q"))
	if text == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	settings := s.settings(r.Context())
	day := assistant.LogicalDay(s.clock(), settings.Loc, settings.DayStartsAt)
	var from, to *time.Time
	if v := q.Get("from"); v != "" {
		d, err := tools.ParseDay(v, day)
		if err != nil {
			recordError(w, err)
			return
		}
		from = &d
	}
	if v := q.Get("to"); v != "" {
		d, err := tools.ParseDay(v, day)
		if err != nil {
			recordError(w, err)
			return
		}
		to = &d
	}
	limit := queryInt(r, "limit", 20)
	if limit < 1 || limit > 100 {
		http.Error(w, "bad limit", http.StatusBadRequest)
		return
	}

	hits, err := s.records.SearchMessages(r.Context(), store.MessageQuery{
		CharacterID: characterID, Text: text, From: from, To: to, TZ: settings.TZ, Limit: limit,
	})
	if err != nil {
		serverError(w, "search messages", err)
		return
	}
	facts, err := s.records.SearchFacts(r.Context(), store.FactQuery{CharacterID: characterID, Text: text, From: from, To: to, Limit: limit})
	if err != nil {
		serverError(w, "search facts", err)
		return
	}
	msgs := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		day := h.CreatedAt.In(settings.Loc).Format("2006-01-02")
		if h.Day != nil {
			day = store.DayString(*h.Day)
		}
		msgs = append(msgs, map[string]any{
			"id": h.ID, "conversationId": h.ConversationID, "characterId": h.CharacterID,
			"role": h.Role, "content": h.Content, "day": day, "createdAt": h.CreatedAt,
		})
	}
	factList := make([]map[string]any, 0, len(facts))
	for _, f := range facts {
		factList = append(factList, factJSON(f))
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "facts": factList})
}

func (s *server) handleForgetMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	characterID, ok := s.memoryCharacter(w, r)
	if !ok {
		return
	}
	if err := s.records.ForgetMessage(r.Context(), characterID, id); err != nil {
		recordError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleForgetDay(w http.ResponseWriter, r *http.Request) {
	characterID, ok := s.memoryCharacter(w, r)
	if !ok {
		return
	}
	settings := s.settings(r.Context())
	day, err := tools.ParseDay(r.PathValue("day"), assistant.LogicalDay(s.clock(), settings.Loc, settings.DayStartsAt))
	if err != nil {
		recordError(w, err)
		return
	}
	from := assistant.At(day, settings.DayStartsAt, settings.Loc)
	to := from.AddDate(0, 0, 1)
	n, err := s.records.ForgetDay(r.Context(), characterID, day, from, to)
	if err != nil {
		serverError(w, "forget day", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"forgotten": n})
}

func (s *server) handleGetProfile(w http.ResponseWriter, _ *http.Request) {
	raw, err := os.ReadFile(filepath.Join(s.memoryPath(), "profile.md"))
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, map[string]any{"profile": ""})
		return
	}
	if err != nil {
		serverError(w, "read profile", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": string(raw)})
}

func (s *server) handlePutProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if len(req.Profile) > 32<<10 {
		http.Error(w, "profile is too long", http.StatusBadRequest)
		return
	}
	dir := s.memoryPath()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		serverError(w, "create memory dir", err)
		return
	}
	// Write to a temp file, then rename, so a crash cannot truncate the profile.
	tmp, err := os.CreateTemp(dir, ".profile-*.tmp")
	if err != nil {
		serverError(w, "write profile", err)
		return
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.WriteString(req.Profile); err != nil {
		_ = tmp.Close()
		serverError(w, "write profile", err)
		return
	}
	if err := tmp.Close(); err != nil {
		serverError(w, "write profile", err)
		return
	}
	if err := os.Chmod(name, 0o600); err != nil {
		serverError(w, "chmod profile", err)
		return
	}
	if err := os.Rename(name, filepath.Join(dir, "profile.md")); err != nil {
		serverError(w, "replace profile", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": req.Profile})
}

func factJSON(f store.Fact) map[string]any {
	out := map[string]any{
		"id": f.ID, "characterId": f.CharacterID, "text": f.Text, "category": f.Category, "core": f.Core, "status": f.Status,
		"sourceKind": f.SourceKind, "validFrom": store.DayString(f.ValidFrom),
		"useCount": f.UseCount, "createdAt": f.CreatedAt,
		"about": f.About, "toldBy": f.ToldBy,
	}
	if f.ValidTo != nil {
		out["validTo"] = store.DayString(*f.ValidTo)
	}
	if f.SupersededBy != nil {
		out["supersededBy"] = *f.SupersededBy
	}
	if f.SourceMessageID != nil {
		out["sourceMessageId"] = *f.SourceMessageID
	}
	if f.LastUsedAt != nil {
		out["lastUsedAt"] = *f.LastUsedAt
	}
	return out
}

func itoa64(id int64) string { return strconv.FormatInt(id, 10) }
