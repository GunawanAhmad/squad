package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"squad/llm"
	"squad/store"
	"squad/store/storetest"
	"squad/tools"
)

func TestNextSpeaker(t *testing.T) {
	m := []Member{{ID: "socrates", Name: "Socrates"}, {ID: "ria", Name: "Ria"}}
	cases := []struct{ name, text, current, want string }{
		{"by name", "@Ria find this", "", "ria"},
		{"by id, case-insensitive", "@SOCRATES?", "", "socrates"},
		{"non-member", "@bob hi", "", ""},
		{"none", "hello", "", ""},
		{"self mention ends", "@Ria I'll do it", "ria", ""},
		{"first other mention wins", "@Ria and @Socrates", "ria", "socrates"},
		{"email is not a mention", "mail ria@x.com", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextSpeaker(tc.text, m, tc.current); got != tc.want {
				t.Fatalf("NextSpeaker(%q, current %q) = %q, want %q", tc.text, tc.current, got, tc.want)
			}
		})
	}
}

func roomFixture(t *testing.T, db *store.Store, fake *llm.Fake, members ...string) (*Runner, store.Room, []Member) {
	t.Helper()
	r, err := db.CreateRoom(context.Background(), "office", members)
	if err != nil {
		t.Fatal(err)
	}
	runner := New(Config{
		Store: db, Tools: tools.New(), DB: db,
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): fake,
			string(llm.ClaudeProvider):   fake,
		},
		Profile:   func() string { return "" },
		GateModel: "fake",
		Now:       func() time.Time { return testNow },
		Location:  testLoc,
	})
	out := make([]Member, 0, len(members))
	for _, id := range members {
		out = append(out, Member{
			ID: id, Name: strings.ToUpper(id[:1]) + id[1:],
			Character: Character{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Provider: "opencode", Model: "fake",
				Persona: "You are " + id + ".", Context: "memory", Discretion: 5},
		})
	}
	return runner, r, out
}

func TestRoomTurnChainAndFanOut(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	fake := llm.NewFake(
		llm.FakeStep{Text: "@Socrates what do you think?"},
		llm.FakeStep{Text: "Ask them."},
	)
	runner, room, members := roomFixture(t, db, fake, "socrates", "ria")

	var spoke []string
	err := runner.RoomTurn(ctx, RoomRequest{Room: room, Members: members, Input: "@Ria hi"}, func(e Event) {
		if e.Type == "speaker" {
			spoke = append(spoke, e.Name)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(spoke) != 2 || spoke[0] != "ria" || spoke[1] != "socrates" {
		t.Fatalf("speaker events: %v", spoke)
	}

	me, err := db.Messages(ctx, store.RoomConversationID(room.ID, "me"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ role, speaker, text string }{
		{"user", "me", "@Ria hi"},
		{"assistant", "ria", "@Socrates what do you think?"},
		{"assistant", "socrates", "Ask them."},
	}
	if len(me) != len(want) {
		t.Fatalf("me transcript: %+v", me)
	}
	for i, w := range want {
		if me[i].Role != w.role || me[i].Speaker != w.speaker || me[i].Content != w.text {
			t.Fatalf("me[%d] = %+v, want %+v", i, me[i], w)
		}
	}

	soc, err := db.Messages(ctx, store.RoomConversationID(room.ID, "socrates"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawUser, sawOwn bool
	for _, m := range soc {
		if m.Role == "user" && m.Speaker == "me" && m.Content == "@Ria hi" {
			sawUser = true
		}
		if m.Role == "assistant" && m.Content == "Ask them." {
			sawOwn = true
		}
		if m.Speaker == "ria" && m.Role == "assistant" {
			t.Fatalf("socrates's copy has ria's assistant row: %+v", m)
		}
	}
	if !sawUser || !sawOwn {
		t.Fatalf("socrates's copy: %+v", soc)
	}

	ria, err := db.Messages(ctx, store.RoomConversationID(room.ID, "ria"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ria {
		if m.Speaker == "socrates" && m.Role == "assistant" {
			t.Fatalf("ria's copy has socrates's assistant row: %+v", m)
		}
	}
}

func TestRoomTurnCapsAtFour(t *testing.T) {
	db := storetest.New(t)
	fake := llm.NewFake(
		llm.FakeStep{Text: "@Socrates keep going"},
		llm.FakeStep{Text: "@Ria keep going"},
		llm.FakeStep{Text: "@Socrates keep going"},
		llm.FakeStep{Text: "@Ria keep going"},
		llm.FakeStep{Text: "this fifth turn must never run"},
	)
	runner, room, members := roomFixture(t, db, fake, "socrates", "ria")
	if err := runner.RoomTurn(context.Background(), RoomRequest{Room: room, Members: members, Input: "@Ria go"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.Requests()); got != maxRoomTurns {
		t.Fatalf("turn runs: %d, want %d", got, maxRoomTurns)
	}
	// The fourth reply mentions Ria, but the cap stops anyone from running her
	// turn. Her copy must still receive the words said in the room.
	ria, err := db.Messages(context.Background(), store.RoomConversationID(room.ID, "ria"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range ria {
		if m.Role == "user" && m.Speaker == "socrates" && m.Content == "@Ria keep going" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the capped reply never reached ria's copy: %+v", ria)
	}
}

func TestRoomTurnDefaultsToLastSpeaker(t *testing.T) {
	db := storetest.New(t)
	fake := llm.NewFake(
		llm.FakeStep{Text: "Hello."}, // ria, first
		llm.FakeStep{Text: "Again."}, // ria, because she spoke last
	)
	runner, room, members := roomFixture(t, db, fake, "socrates", "ria")
	ctx := context.Background()
	if err := runner.RoomTurn(ctx, RoomRequest{Room: room, Members: members, Input: "@Ria hi"}, nil); err != nil {
		t.Fatal(err)
	}

	var spoke []string
	if err := runner.RoomTurn(ctx, RoomRequest{Room: room, Members: members, Input: "anyone there?"}, func(e Event) {
		if e.Type == "speaker" {
			spoke = append(spoke, e.Name)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(spoke) != 1 || spoke[0] != "ria" {
		t.Fatalf("second message answered by %v, want ria", spoke)
	}
}

func TestRoomTurnLastSpeakerBeyondTheWindow(t *testing.T) {
	db := storetest.New(t)
	fake := llm.NewFake(llm.FakeStep{Text: "Hello again."})
	runner, room, members := roomFixture(t, db, fake, "socrates", "ria")
	ctx := context.Background()
	meID := store.RoomConversationID(room.ID, "me")

	add := func(role, speaker, content string) {
		t.Helper()
		if _, err := db.AddMessage(ctx, store.Message{ConversationID: meID, Role: role, Content: content, Speaker: speaker}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 60; i++ {
		add("user", "me", "filler")
	}
	add("assistant", "socrates", "I spoke last.")
	for i := 0; i < 60; i++ {
		add("user", "me", "filler")
	}

	var spoke []string
	if err := runner.RoomTurn(ctx, RoomRequest{Room: room, Members: members, Input: "anyone there?"}, func(e Event) {
		if e.Type == "speaker" {
			spoke = append(spoke, e.Name)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(spoke) != 1 || spoke[0] != "socrates" {
		t.Fatalf("answered by %v, want socrates (the last speaker, 61 messages back)", spoke)
	}
}
