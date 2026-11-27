package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"squad/store"
)

// roomHiddenTools would bypass the share gate or expose the assistant's private
// day records. Writes (remember, forget, plans) stay available.
var roomHiddenTools = map[string]bool{
	"recall":           true, // searches private history directly
	"get_overview":     true,
	"get_summary":      true,
	"query_activities": true,
	"list_tasks":       true,
	"list_reminders":   true,
}

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

	userCopyID := store.RoomConversationID(req.Room.ID, store.UserSpeaker)
	names := map[string]string{store.UserSpeaker: "the user"}
	for _, m := range req.Members {
		names[m.ID] = m.Name
	}

	first := NextSpeaker(req.Input, req.Members, "")
	if first == "" {
		first = r.lastSpeaker(ctx, userCopyID, req.Members)
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
		if err := r.addRoomMessage(ctx, store.RoomConversationID(req.Room.ID, m.ID), "user", req.Input, store.UserSpeaker); err != nil {
			return err
		}
	}
	if err := r.addRoomMessage(ctx, userCopyID, "user", req.Input, store.UserSpeaker); err != nil {
		return err
	}

	speaker, heardFrom, text := first, store.UserSpeaker, req.Input
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
			Speaker:        heardFrom,
			Names:          names,
			Listeners:      listeners,
			Kind:           "room",
		}, emit)
		result := strings.TrimSpace(res.Text)
		if err != nil {
			// Everyone still hears the words that were said before the failure;
			// nobody speaks next.
			if result != "" {
				if fanOutErr := r.fanOutRoom(ctx, userCopyID, req.Room.ID, req.Members, speaker, "", result); fanOutErr != nil {
					return fanOutErr
				}
			}
			return err
		}
		if result == "" {
			return nil
		}

		next := NextSpeaker(result, req.Members, speaker)
		// Only the next speaker's Turn stores the text as its input. At the cap
		// nobody runs that Turn, so everyone must get the copy now.
		more := next != "" && turn < maxRoomTurns-1
		inputOwner := ""
		if more {
			inputOwner = next
		}
		if err := r.fanOutRoom(ctx, userCopyID, req.Room.ID, req.Members, speaker, inputOwner, result); err != nil {
			return err
		}
		if !more {
			return nil
		}
		speaker, heardFrom, text = next, speaker, result
	}
	return nil
}

// fanOutRoom writes one reply into the user's transcript (as the character)
// and into every member's copy except the speaker's (its Turn already stored
// it) and inputOwner (the member whose Turn will store it as its input).
func (r *Runner) fanOutRoom(ctx context.Context, userCopyID, roomID string, members []Member, speaker, inputOwner, text string) error {
	if err := r.addRoomMessage(ctx, userCopyID, "assistant", text, speaker); err != nil {
		return err
	}
	for _, m := range members {
		if m.ID == speaker || m.ID == inputOwner {
			continue
		}
		if err := r.addRoomMessage(ctx, store.RoomConversationID(roomID, m.ID), "user", text, speaker); err != nil {
			return err
		}
	}
	return nil
}

// lastSpeaker is the latest character that spoke in the user's copy, or "".
func (r *Runner) lastSpeaker(ctx context.Context, userCopyID string, members []Member) string {
	id, err := r.store.LastSpeaker(ctx, userCopyID)
	if err != nil {
		return ""
	}
	if _, ok := roomMember(members, id); ok {
		return id
	}
	return ""
}

func (r *Runner) addRoomMessage(ctx context.Context, conversationID, role, content, speaker string) error {
	_, err := r.store.AddMessage(ctx, store.Message{
		ConversationID: conversationID, Role: role, Content: content, Speaker: speaker,
	})
	return err
}
