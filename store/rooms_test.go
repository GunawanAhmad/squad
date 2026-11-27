package store_test

import (
	"context"
	"errors"
	"testing"

	"squad/store"
	"squad/store/storetest"
)

func TestCreateRoomMakesOneCopyPerMember(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	r, err := db.CreateRoom(ctx, "office", []string{"socrates", "ria"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ID) != 8 {
		t.Fatalf("room id: %q (want 8 hex characters)", r.ID)
	}
	for _, p := range []string{"socrates", "ria", "me"} {
		c, err := db.GetConversation(ctx, r.ConversationID(p))
		if err != nil {
			t.Fatalf("conversation for %s: %v", p, err)
		}
		if c.CharacterID != p {
			t.Fatalf("conversation for %s belongs to %s", p, c.CharacterID)
		}
	}
	got, err := db.GetRoom(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 || got.Members[0] != "socrates" || got.Members[1] != "ria" {
		t.Fatalf("members: %v", got.Members)
	}
	if _, err := db.GetRoom(ctx, "deadbeef"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown room: %v (want ErrNotFound)", err)
	}
}

func TestMessageSpeakerRoundTrips(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	if _, err := db.CreateConversation(ctx, store.Conversation{ID: "room:abc:me", CharacterID: "me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMessage(ctx, store.Message{
		ConversationID: "room:abc:me", Role: "user", Content: "hi all", Speaker: "ria",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMessage(ctx, store.Message{
		ConversationID: "room:abc:me", Role: "user", Content: "plain",
	}); err != nil {
		t.Fatal(err)
	}
	msgs, err := db.RecentMessages(ctx, "room:abc:me", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages: %d", len(msgs))
	}
	if msgs[0].Speaker != "ria" {
		t.Fatalf("speaker: %q", msgs[0].Speaker)
	}
	if msgs[1].Speaker != "" {
		t.Fatalf("empty speaker round-trips as %q", msgs[1].Speaker)
	}
}

func TestFactAboutAndToldBy(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	f, err := db.AddFact(ctx, store.Fact{CharacterID: testOwner, Text: "likes tea"})
	if err != nil {
		t.Fatal(err)
	}
	if f.About != "me" || f.ToldBy != "" {
		t.Fatalf("default about/told_by: %+v", f)
	}

	if _, err := db.ApplyFactOps(ctx, testOwner, day(2026, 11, 27), []store.FactOp{{
		Op: "ADD", Text: "is behind on the thesis", About: "ria", ToldBy: "socrates",
	}}); err != nil {
		t.Fatal(err)
	}
	facts, err := db.Facts(ctx, testOwner, store.FactFilter{Status: []string{"active"}})
	if err != nil {
		t.Fatal(err)
	}
	var found *store.Fact
	for i := range facts {
		if facts[i].Text == "is behind on the thesis" {
			found = &facts[i]
		}
	}
	if found == nil {
		t.Fatalf("added fact not found in %+v", facts)
	}
	if found.About != "ria" || found.ToldBy != "socrates" {
		t.Fatalf("about/told_by: %+v", found)
	}
}

func TestFactProvenance(t *testing.T) {
	cases := []struct {
		name string
		fact store.Fact
		want string
	}{
		{"plain user fact", store.Fact{About: "me"}, ""},
		{"about someone", store.Fact{About: "ria"}, "about ria"},
		{"heard from someone", store.Fact{About: "me", ToldBy: "socrates"}, "heard from socrates"},
		{"about the speaker", store.Fact{About: "ria", ToldBy: "ria"}, "about ria"},
		{"both", store.Fact{About: "ria", ToldBy: "socrates"}, "about ria, heard from socrates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fact.Provenance(); got != tc.want {
				t.Fatalf("Provenance() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A new session gives every participant an empty copy; the old session's
// messages stay in each character's memory.
func TestNewRoomSession(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	r, err := db.CreateRoom(ctx, "office", []string{"socrates", "ria"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ConversationID("me") != "room:"+r.ID+":me" {
		t.Fatalf("session 0 id changed: %q", r.ConversationID("me"))
	}
	if _, err := db.AddMessage(ctx, store.Message{ConversationID: r.ConversationID("ria"), Role: "user", Content: "old", Speaker: "me"}); err != nil {
		t.Fatal(err)
	}

	next, err := db.NewRoomSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Session != 1 || next.ConversationID("ria") == r.ConversationID("ria") {
		t.Fatalf("new session: %+v", next)
	}
	for _, p := range []string{"socrates", "ria", "me"} {
		c, err := db.GetConversation(ctx, next.ConversationID(p))
		if err != nil || c.CharacterID != p {
			t.Fatalf("session 1 conversation for %s: %+v, %v", p, c, err)
		}
		msgs, _ := db.Messages(ctx, next.ConversationID(p), 0, 0)
		if len(msgs) != 0 {
			t.Fatalf("session 1 copy for %s is not empty: %+v", p, msgs)
		}
	}
	if got, _ := db.GetRoom(ctx, r.ID); got.Session != 1 {
		t.Fatalf("GetRoom session = %d", got.Session)
	}
	if old, _ := db.Messages(ctx, r.ConversationID("ria"), 0, 0); len(old) != 1 {
		t.Fatalf("old session lost: %+v", old)
	}
	if _, err := db.NewRoomSession(ctx, "deadbeef"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown room: %v", err)
	}
}
