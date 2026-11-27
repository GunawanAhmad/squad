package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"squad/assistant"
	"squad/tools"
)

func loadCharacters(t *testing.T) []Character {
	t.Helper()
	raw, err := os.ReadFile("characters.json")
	if err != nil {
		t.Fatal(err)
	}
	var list []Character
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestCharactersConfigIsValid(t *testing.T) {
	list := loadCharacters(t)
	if err := validateCharacters(list, tools.New()); err != nil {
		t.Fatalf("characters.json: %v", err)
	}

	var assistant *Character
	for i := range list {
		if list[i].ID == assistantCharacterID {
			assistant = &list[i]
		}
	}
	if assistant == nil {
		t.Fatal("no assistant character")
	}
	for _, c := range list {
		if c.ID != assistantCharacterID && len(c.Tools) == 0 {
			t.Fatalf("%s has no tools", c.ID)
		}
	}
}

func TestCharactersConfigRejectsBadTools(t *testing.T) {
	base := func() []Character {
		return []Character{
			{ID: "assistant", Name: "A", Model: "m", Tools: []string{"plan_tasks"}},
			// Personas may read the web and keep their own memory.
			{ID: "ria", Name: "R", Model: "m", Context: "memory", Tools: []string{"web_search", "remember", "forget", "recall"}},
		}
	}
	if err := validateCharacters(base(), tools.New()); err != nil {
		t.Fatalf("baseline should pass: %v", err)
	}

	cases := map[string]func([]Character){
		"persona domain tool": func(l []Character) { l[1].Tools = append(l[1].Tools, "plan_tasks") },
		"unknown tool":        func(l []Character) { l[1].Tools = []string{"nope"} },
		"bad context":         func(l []Character) { l[1].Context = "everything" },
		"old profile context": func(l []Character) { l[1].Context = "profile" },
		"missing default":     func(l []Character) { l[1].Models = []string{"other"} },
		"duplicate id":        func(l []Character) { l[1].ID = "assistant" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			list := base()
			mutate(list)
			err := validateCharacters(list, tools.New())
			if err == nil {
				t.Fatal("expected an error")
			}
			if name == "unknown tool" && !strings.Contains(err.Error(), "unknown tool") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestAssistantPersonaFitsBudget(t *testing.T) {
	list := loadCharacters(t)
	var a Character
	for _, c := range list {
		if c.ID == "assistant" {
			a = c
		}
	}
	if a.ID == "" {
		t.Fatal("no assistant character")
	}
	loc, err := time.LoadLocation("Asia/Makassar")
	if err != nil {
		t.Fatal(err)
	}
	cctx := assistant.BuildContext(assistant.ContextInput{
		Character: assistant.Character{ID: a.ID, Name: a.Name, Persona: a.Persona},
		Window:    10,
		Now:       time.Date(2026, 12, 1, 9, 0, 0, 0, loc),
		Settings:  assistant.Settings{TZ: "Asia/Makassar", Loc: loc, DayStartsAt: 4 * time.Hour},
	})
	if strings.Contains(strings.Join(cctx.Truncated, ","), "persona") {
		t.Fatalf("assistant persona is truncated (%d chars); raise budgetPersona or shorten it", len(a.Persona))
	}
}

func TestMemoryEnabledFollowsContext(t *testing.T) {
	enabled := memoryEnabled(map[string]Character{
		"assistant": {ID: "assistant", Context: "memory"},
		"ria":       {ID: "ria"}, // empty means the default, memory
		"mute":      {ID: "mute", Context: "none"},
	})
	for id, want := range map[string]bool{"assistant": true, "ria": true, "mute": false, "removed": false} {
		if got := enabled(id); got != want {
			t.Fatalf("memoryEnabled(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestValidateDiscretion(t *testing.T) {
	n := func(v int) *int { return &v }
	cases := []struct {
		name string
		d    *int
		ok   bool
	}{
		{"unset defaults to 5", nil, true},
		{"zero", n(0), true},
		{"ten", n(10), true},
		{"eleven", n(11), false},
		{"negative", n(-1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := []Character{{ID: "assistant", Name: "A", Model: "m", Tools: []string{"plan_tasks"}, Discretion: tc.d}}
			err := validateCharacters(list, tools.New())
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "discretion")) {
				t.Fatalf("want a discretion error, got %v", err)
			}
		})
	}
	if got := discretionOf(Character{}); got != 5 {
		t.Fatalf("default discretion = %d, want 5", got)
	}
}
