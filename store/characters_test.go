package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"squad/store"
	"squad/store/storetest"
)

func TestAddCharacterRoundTripAndDuplicate(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	if err := db.AddCharacter(ctx, "pip", json.RawMessage(`{"id":"pip","name":"Pip"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddCharacter(ctx, "pip", json.RawMessage(`{"id":"pip","name":"Other"}`)); !errors.Is(err, store.ErrCharacterExists) {
		t.Fatalf("duplicate id: err = %v, want ErrCharacterExists", err)
	}
	specs, err := db.Characters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(specs))
	}
	var c struct{ Name string }
	if err := json.Unmarshal(specs[0], &c); err != nil || c.Name != "Pip" {
		t.Fatalf("spec = %s (%v)", specs[0], err)
	}
}
