package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"squad/assistant"
	"squad/store"
)

// Rooms: a group conversation with at least two characters. The user's copy
// (`me`) is the transcript the UI shows; each member keeps its own copy in the
// store, which is what keeps memory and forget per character.

type createRoomRequest struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

func (s *server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var req createRoomRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 60 {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	var members []string
	seen := map[string]bool{}
	for _, id := range req.Members {
		if id == "me" {
			http.Error(w, "me cannot be a member", http.StatusBadRequest)
			return
		}
		if _, ok := s.chars[id]; !ok {
			http.Error(w, "unknown character", http.StatusBadRequest)
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		members = append(members, id)
	}
	if len(members) < 2 {
		http.Error(w, "a room needs at least two characters", http.StatusBadRequest)
		return
	}

	room, err := s.db.CreateRoom(r.Context(), name, members)
	if err != nil {
		serverError(w, "create room", err)
		return
	}
	writeJSON(w, http.StatusCreated, roomJSON(room))
}

func (s *server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.db.ListRooms(r.Context())
	if err != nil {
		serverError(w, "list rooms", err)
		return
	}
	out := make([]map[string]any, 0, len(rooms))
	for _, room := range rooms {
		out = append(out, roomJSON(room))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleRoomMessages(w http.ResponseWriter, r *http.Request) {
	room, ok := s.room(w, r)
	if !ok {
		return
	}
	after := int64(queryInt(r, "after", 0))
	limit := queryInt(r, "limit", 200)
	if limit < 1 || limit > 1000 {
		http.Error(w, "bad limit", http.StatusBadRequest)
		return
	}
	msgs, err := s.db.Messages(r.Context(), store.RoomConversationID(room.ID, "me"), after, limit)
	if err != nil {
		serverError(w, "get room messages", err)
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messageJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

func (s *server) handlePostRoomMessage(w http.ResponseWriter, r *http.Request) {
	room, ok := s.room(w, r)
	if !ok {
		return
	}
	var req postMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Content) == "" || len(req.Content) > 50_000 {
		http.Error(w, "bad content", http.StatusBadRequest)
		return
	}

	members := make([]assistant.Member, 0, len(room.Members))
	for _, id := range room.Members {
		c, ok := s.chars[id]
		if !ok {
			http.Error(w, "unknown character", http.StatusBadRequest)
			return
		}
		provider, model, err := resolveModel(c, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		members = append(members, assistant.Member{
			ID: c.ID, Name: c.Name,
			Character: assistant.Character{
				ID: c.ID, Name: c.Name, Provider: provider, Model: model, Persona: c.Persona,
				Tools: c.Tools, Context: c.Context, Daily: c.ID == assistantCharacterID,
				Discretion: discretionOf(c),
			},
		})
	}

	stream := newStream(w)
	err := s.runner.RoomTurn(r.Context(), assistant.RoomRequest{
		Room: room, Members: members, Input: req.Content,
	}, stream.event)
	if err != nil {
		if r.Context().Err() == nil {
			log.Printf("room %s: %v", room.ID, err)
			stream.send(assistant.Event{Type: "error", Message: err.Error()})
		}
		return
	}
	stream.send(map[string]any{"type": "done"})
}

// room resolves the {id} path value, writing the error response itself.
func (s *server) room(w http.ResponseWriter, r *http.Request) (store.Room, bool) {
	id := r.PathValue("id")
	if !conversationIDRe.MatchString(id) {
		http.Error(w, "bad room id", http.StatusBadRequest)
		return store.Room{}, false
	}
	room, err := s.db.GetRoom(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown room", http.StatusNotFound)
		return store.Room{}, false
	}
	if err != nil {
		serverError(w, "get room", err)
		return store.Room{}, false
	}
	return room, true
}

func roomJSON(room store.Room) map[string]any {
	return map[string]any{
		"id": room.ID, "name": room.Name, "members": room.Members, "createdAt": room.CreatedAt,
	}
}
