package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"characters/assistant"
	"characters/tools"
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
