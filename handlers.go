package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"squad/assistant"
	"squad/store"
)

// apiStore is the store surface the HTTP handlers use. *store.Store satisfies
// it; handler tests use an in-memory implementation.
type apiStore interface {
	assistant.Store
	GetConversation(ctx context.Context, id string) (store.Conversation, error)
	ListConversations(ctx context.Context, characterID string, limit int) ([]store.Conversation, error)
	Messages(ctx context.Context, conversationID string, after int64, limit int) ([]store.Message, error)
	CreateRoom(ctx context.Context, name string, members []string) (store.Room, error)
	GetRoom(ctx context.Context, id string) (store.Room, error)
	ListRooms(ctx context.Context) ([]store.Room, error)
	NewRoomSession(ctx context.Context, id string) (store.Room, error)
}

var conversationIDRe = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,80}$`)

// maxMessageChars is the largest message the API accepts.
const maxMessageChars = 50_000

type server struct {
	roster    *roster
	db        apiStore
	records   *store.Store // nullable in conversation-only tests
	memoryDir string       // memory/ (tests use a temp dir)
	runner    *assistant.Runner
	engine    *assistant.Engine // check-in engine; nil without an assistant character
	now       func() time.Time  // injectable clock for tests
}

// settings is settings.tz and the day boundary; records is nil in tests.
func (s *server) settings(ctx context.Context) assistant.Settings {
	if s.records == nil {
		return assistant.DefaultSettings()
	}
	return assistant.LoadSettings(ctx, s.records)
}

// dailyConversation opens (or creates) the thread for the current logical day.
func (s *server) dailyConversation(ctx context.Context) (store.Conversation, error) {
	set := s.settings(ctx)
	day := assistant.LogicalDay(s.clock(), set.Loc, set.DayStartsAt)
	return s.db.CreateConversation(ctx, store.Conversation{
		ID: "daily:" + store.DayString(day), CharacterID: assistantCharacterID, Day: &day,
	})
}

// clock is the server's wall clock (assistant.Now in production).
func (s *server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return assistant.Now()
}

func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/characters", s.handleCharacters)
	mux.HandleFunc("POST /api/characters", s.handleCreateCharacter)
	mux.HandleFunc("POST /api/characters/draft", s.handleDraftCharacter)
	mux.HandleFunc("POST /api/conversations", s.handleCreateConversation)
	mux.HandleFunc("GET /api/conversations", s.handleListConversations)
	mux.HandleFunc("POST /api/conversations/import", s.handleImportConversation)
	mux.HandleFunc("GET /api/conversations/{id}/messages", s.handleMessages)
	mux.HandleFunc("POST /api/conversations/{id}/messages", s.handlePostMessage)

	mux.HandleFunc("POST /api/rooms", s.handleCreateRoom)
	mux.HandleFunc("GET /api/rooms", s.handleListRooms)
	mux.HandleFunc("GET /api/rooms/{id}/messages", s.handleRoomMessages)
	mux.HandleFunc("POST /api/rooms/{id}/messages", s.handlePostRoomMessage)
	mux.HandleFunc("POST /api/rooms/{id}/sessions", s.handleNewRoomSession)

	mux.HandleFunc("GET /api/assistant/tasks", s.handleListTasks)
	mux.HandleFunc("PATCH /api/assistant/tasks/{id}", s.handlePatchTask)
	mux.HandleFunc("DELETE /api/assistant/tasks/{id}", s.handleDeleteTask)
	mux.HandleFunc("GET /api/assistant/activities", s.handleListActivities)
	mux.HandleFunc("PATCH /api/assistant/activities/{id}", s.handlePatchActivity)
	mux.HandleFunc("DELETE /api/assistant/activities/{id}", s.handleDeleteActivity)
	mux.HandleFunc("GET /api/assistant/reminders", s.handleListReminders)
	mux.HandleFunc("PATCH /api/assistant/reminders/{id}", s.handlePatchReminder)
	mux.HandleFunc("DELETE /api/assistant/reminders/{id}", s.handleDeleteReminder)
	mux.HandleFunc("GET /api/assistant/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/assistant/settings", s.handlePutSettings)
	mux.HandleFunc("POST /api/assistant/undo", s.handleUndo)
	mux.HandleFunc("GET /api/assistant/due", s.handleDue)

	mux.HandleFunc("GET /api/memory/facts", s.handleListFacts)
	mux.HandleFunc("PATCH /api/memory/facts/{id}", s.handlePatchFact)
	mux.HandleFunc("GET /api/memory/facts/{id}/history", s.handleFactHistory)
	mux.HandleFunc("POST /api/memory/facts/{id}/forget", s.handleForgetFact)
	mux.HandleFunc("GET /api/memory/search", s.handleMemorySearch)
	mux.HandleFunc("POST /api/memory/messages/{id}/forget", s.handleForgetMessage)
	mux.HandleFunc("POST /api/memory/days/{day}/forget", s.handleForgetDay)
	mux.HandleFunc("GET /api/memory/profile", s.handleGetProfile)
	mux.HandleFunc("PUT /api/memory/profile", s.handlePutProfile)
}

func (s *server) handleCharacters(w http.ResponseWriter, _ *http.Request) {
	list := s.roster.all()
	public := make([]map[string]any, 0, len(list))
	for _, c := range list {
		public = append(public, publicCharacter(c))
	}
	writeJSON(w, http.StatusOK, public)
}

// publicCharacter is what the browser sees of a character: no persona or tools.
func publicCharacter(c Character) map[string]any {
	return map[string]any{"id": c.ID, "name": c.Name, "ui": c.UI, "model": choice(c.Provider, c.Model), "models": c.Models, "discretion": discretionOf(c)}
}

type createConversationRequest struct {
	CharacterID string `json:"characterId"`
}

func (s *server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	var req createConversationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if _, ok := s.roster.get(req.CharacterID); !ok {
		http.Error(w, "unknown character", http.StatusBadRequest)
		return
	}
	// The assistant has one thread per day (D5); "new chat" means today's.
	if req.CharacterID == assistantCharacterID {
		conv, err := s.dailyConversation(r.Context())
		if err != nil {
			serverError(w, "open today's thread", err)
			return
		}
		writeJSON(w, http.StatusOK, conversationJSON(conv))
		return
	}

	conv, err := s.db.CreateConversation(r.Context(), store.Conversation{
		ID: newConversationID(), CharacterID: req.CharacterID,
	})
	if err != nil {
		serverError(w, "create conversation", err)
		return
	}
	writeJSON(w, http.StatusCreated, conversationJSON(conv))
}

func (s *server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	characterID := r.URL.Query().Get("characterId")
	if _, ok := s.roster.get(characterID); !ok {
		http.Error(w, "unknown character", http.StatusBadRequest)
		return
	}
	limit := queryInt(r, "limit", 50)
	if limit < 1 || limit > 200 {
		http.Error(w, "bad limit", http.StatusBadRequest)
		return
	}

	convs, err := s.db.ListConversations(r.Context(), characterID, limit)
	if err != nil {
		serverError(w, "list conversations", err)
		return
	}
	out := make([]map[string]any, 0, len(convs))
	for _, c := range convs {
		out = append(out, conversationJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !conversationIDRe.MatchString(id) {
		http.Error(w, "bad conversation id", http.StatusBadRequest)
		return
	}
	after := int64(queryInt(r, "after", 0))
	limit := queryInt(r, "limit", 200)
	if limit < 1 || limit > 1000 {
		http.Error(w, "bad limit", http.StatusBadRequest)
		return
	}
	// A stale browser id (e.g. after a database reset) must be distinguishable
	// from an empty chat, so the UI can create a new conversation.
	if _, err := s.db.GetConversation(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown conversation", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, "get conversation", err)
		return
	}

	msgs, err := s.db.Messages(r.Context(), id, after, limit)
	if err != nil {
		serverError(w, "get messages", err)
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messageJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

type postMessageRequest struct {
	Content string `json:"content"`
	Model   string `json:"model"`
}

func (s *server) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !conversationIDRe.MatchString(id) {
		http.Error(w, "bad conversation id", http.StatusBadRequest)
		return
	}
	var req postMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Content) == "" || len(req.Content) > maxMessageChars {
		http.Error(w, "bad content", http.StatusBadRequest)
		return
	}

	conv, err := s.db.GetConversation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown conversation", http.StatusNotFound)
		return
	}
	if err != nil {
		serverError(w, "get conversation", err)
		return
	}
	c, ok := s.roster.get(conv.CharacterID)
	if !ok {
		http.Error(w, "unknown character", http.StatusBadRequest)
		return
	}
	provider, model, err := resolveModel(c, req.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stream := newStream(w)
	res, err := s.runner.Turn(r.Context(), assistant.TurnRequest{
		ConversationID: conv.ID,
		Character:      toAssistantCharacter(c, provider, model),
		Input:          req.Content,
		Model:          model,
	}, stream.event)
	if err != nil {
		if r.Context().Err() == nil {
			log.Printf("chat %s: %v", conv.ID, err)
			stream.send(assistant.Event{Type: "error", Message: err.Error()})
		}
		return
	}
	stream.send(map[string]any{
		"type": "done", "runId": res.RunID,
		"userMessageId": res.UserMessageID, "assistantMessageId": res.AssistantMessageID,
	})
}

type importMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type importRequest struct {
	CharacterID string          `json:"characterId"`
	SessionID   string          `json:"sessionId"`
	Messages    []importMessage `json:"messages"`
}

// handleImportConversation stores a browser localStorage chat once. The old
// session id becomes the conversation id, so a retry is a no-op.
func (s *server) handleImportConversation(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if _, ok := s.roster.get(req.CharacterID); !ok {
		http.Error(w, "unknown character", http.StatusBadRequest)
		return
	}
	if !sessionIDRe.MatchString(req.SessionID) {
		http.Error(w, "bad sessionId", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 || len(req.Messages) > 5000 {
		http.Error(w, "messages must have 1-5000 items", http.StatusBadRequest)
		return
	}
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "assistant") || len(m.Content) > maxMessageChars {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
	}

	existing, err := s.db.GetConversation(r.Context(), req.SessionID)
	switch {
	case err == nil:
		if existing.CharacterID != req.CharacterID {
			http.Error(w, "conversation belongs to another character", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": existing.ID, "imported": false})
		return
	case !errors.Is(err, store.ErrNotFound):
		serverError(w, "get conversation", err)
		return
	}

	if _, err := s.db.CreateConversation(r.Context(), store.Conversation{
		ID: req.SessionID, CharacterID: req.CharacterID,
	}); err != nil {
		serverError(w, "create conversation", err)
		return
	}
	for _, m := range req.Messages {
		if _, err := s.db.AddMessage(r.Context(), store.Message{
			ConversationID: req.SessionID, Role: m.Role, Content: m.Content,
		}); err != nil {
			serverError(w, "import message", err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": req.SessionID, "imported": true, "messages": len(req.Messages)})
}

// resolveModel validates a picker choice against the character's allowlist and
// returns the provider/model to use.
func resolveModel(c Character, requested string) (provider, model string, err error) {
	provider, model = c.Provider, c.Model
	if requested == "" || requested == choice(c.Provider, c.Model) {
		return provider, model, nil
	}
	if !slices.Contains(c.Models, requested) {
		return "", "", errors.New("unknown model")
	}
	provider, model = "", requested
	if m, ok := strings.CutPrefix(requested, "claude/"); ok {
		provider, model = "claude", m
	}
	return provider, model, nil
}

type ndjsonStream struct {
	enc    *json.Encoder
	rc     *http.ResponseController
	broken bool
}

func newStream(w http.ResponseWriter) *ndjsonStream {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	return &ndjsonStream{enc: json.NewEncoder(w), rc: http.NewResponseController(w)}
}

func (s *ndjsonStream) event(e assistant.Event) { s.send(e) }

func (s *ndjsonStream) send(e any) {
	if s.broken {
		return
	}
	if err := s.enc.Encode(e); err != nil {
		s.broken = true
		return
	}
	if err := s.rc.Flush(); err != nil {
		s.broken = true
	}
}

func conversationJSON(c store.Conversation) map[string]any {
	out := map[string]any{
		"id": c.ID, "characterId": c.CharacterID,
		"createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt,
	}
	if c.Day != nil {
		out["day"] = c.Day.Format("2006-01-02")
	}
	return out
}

func messageJSON(m store.Message) map[string]any {
	out := map[string]any{
		"id": m.ID, "role": m.Role, "content": m.Content, "createdAt": m.CreatedAt,
	}
	if len(m.ToolCalls) > 0 && string(m.ToolCalls) != "null" {
		out["toolCalls"] = json.RawMessage(m.ToolCalls)
	}
	if m.ToolCallID != "" {
		out["toolCallId"] = m.ToolCallID
	}
	if m.RunID != nil {
		out["runId"] = *m.RunID
	}
	if m.Speaker != "" {
		out["speaker"] = m.Speaker
	}
	return out
}

func newConversationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func queryInt(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
