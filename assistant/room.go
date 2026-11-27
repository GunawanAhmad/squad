package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"squad/store"
)

// maxRoomTurns caps one user message: a room is a conversation, not a runaway
// chain of agents talking to each other.
const maxRoomTurns = 4

// Member is one character in a room: its id, display name and turn config.
type Member struct {
	ID        string
	Name      string
	Character Character
}

// RoomRequest is one user message in a room.
type RoomRequest struct {
	Room    store.Room
	Members []Member
	Input   string
}

// NextSpeaker finds the first @mentioned member that isn't the current speaker.
// A mention is "@" followed by the member's whole id or name, case-insensitive;
// an email's local part is not a mention. A self-mention is skipped, so "thanks
// @Ria" from Ria ends the chain rather than looping.
func NextSpeaker(text string, members []Member, current string) string {
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 && isWordByte(text[i-1]) {
			continue // someone@example.com is not a mention
		}
		j := i + 1
		for j < len(text) && isWordByte(text[j]) {
			j++
		}
		token := strings.ToLower(text[i+1 : j])
		if token == "" {
			continue
		}
		for _, m := range members {
			if strings.ToLower(m.ID) != token && strings.ToLower(m.Name) != token {
				continue
			}
			if m.ID != current {
				return m.ID
			}
		}
	}
	return ""
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

func roomMember(members []Member, id string) (Member, bool) {
	for _, m := range members {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}

// RoomTurn runs one user message through a room: it picks the first speaker,
// fans the input out to every member's copy, then lets the chain pass by
// @mention. Every character turn is a normal Turn on that character's own copy,
// so its memory, tools and isolation behave exactly as in a 1:1 chat (minus
// recall and plus the share gate).
func (r *Runner) RoomTurn(ctx context.Context, req RoomRequest, emit func(Event)) error {
	if emit == nil {
		emit = func(Event) {}
	}
	if req.Room.ID == "" || len(req.Members) < 2 {
		return errors.New("room: a room id and at least two members are required")
	}
	if strings.TrimSpace(req.Input) == "" {
		return errors.New("room: empty input")
	}

	lock := r.lockFor("room:" + req.Room.ID)
	lock.Lock()
	defer lock.Unlock()

	meID := store.RoomConversationID(req.Room.ID, "me")
	names := map[string]string{"me": "the user"}
	for _, m := range req.Members {
		names[m.ID] = m.Name
	}

	first := NextSpeaker(req.Input, req.Members, "")
	if first == "" {
		first = lastSpeaker(ctx, r, meID, req.Members)
	}
	if first == "" {
		first = req.Members[0].ID
	}

	// Everyone but the first speaker hears the user's message now; the first
	// speaker's copy gets it as the input of its Turn.
	for _, m := range req.Members {
		if m.ID == first {
			continue
		}
		if err := r.addRoomMessage(ctx, store.RoomConversationID(req.Room.ID, m.ID), "user", req.Input, "me"); err != nil {
			return err
		}
	}
	if err := r.addRoomMessage(ctx, meID, "user", req.Input, "me"); err != nil {
		return err
	}

	speaker, from, text := first, "me", req.Input
	for turn := 0; turn < maxRoomTurns; turn++ {
		member, ok := roomMember(req.Members, speaker)
		if !ok {
			return fmt.Errorf("room: speaker %q is not a member", speaker)
		}
		var listeners []string
		for _, m := range req.Members {
			if m.ID != speaker {
				listeners = append(listeners, m.Name)
			}
		}

		emit(Event{Type: "speaker", Name: speaker})
		res, err := r.Turn(ctx, TurnRequest{
			ConversationID: store.RoomConversationID(req.Room.ID, speaker),
			Character:      member.Character,
			Input:          text,
			Speaker:        from,
			Names:          names,
			Listeners:      listeners,
			Kind:           "room",
		}, emit)
		if err != nil {
			return err
		}
		result := strings.TrimSpace(res.Text)
		if result == "" {
			return nil
		}

		next := NextSpeaker(result, req.Members, speaker)
		if err := r.addRoomMessage(ctx, meID, "assistant", result, speaker); err != nil {
			return err
		}
		for _, m := range req.Members {
			if m.ID == speaker || m.ID == next {
				continue // its own copy has the assistant row; the next speaker gets it as Turn input
			}
			if err := r.addRoomMessage(ctx, store.RoomConversationID(req.Room.ID, m.ID), "user", result, speaker); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		speaker, from, text = next, speaker, result
	}
	return nil
}

// lastSpeaker is the latest character that spoke in the user's copy, or "".
func lastSpeaker(ctx context.Context, r *Runner, meID string, members []Member) string {
	msgs, err := r.store.RecentMessages(ctx, meID, 50)
	if err != nil {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if _, ok := roomMember(members, msgs[i].Speaker); ok {
			return msgs[i].Speaker
		}
	}
	return ""
}

func (r *Runner) addRoomMessage(ctx context.Context, conversationID, role, content, speaker string) error {
	_, err := r.store.AddMessage(ctx, store.Message{
		ConversationID: conversationID, Role: role, Content: content, Speaker: speaker,
	})
	return err
}
