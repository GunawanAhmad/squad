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
		c, err := db.GetConversation(ctx, store.RoomConversationID(r.ID, p))
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
