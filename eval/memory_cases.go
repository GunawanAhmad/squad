package main

import (
	"context"
	"fmt"
	"strings"

	"squad/store"
)

// The memory eval (§7): knowledge update, temporal question, forget (and it
// stays forgotten), abstention, and recall of a detail from ≥30 days ago. The
// cross-language set is the one that decides whether M-10 ships: the fact is
// stored in one language and asked about in the other, which keyword search
// cannot match.

type seedFact struct {
	Text         string
	Category     string
	Core         bool
	ValidDaysAgo int
	FromMessage  string // seed a source message with this text
}

type seedMessage struct {
	CharacterID string // assistant or a persona
	Role        string
	Content     string
	DaysAgo     int
}

type memoryCase struct {
	Name     string
	Facts    []seedFact
	Messages []seedMessage
	Turns    []string
	MustAll  []string
	MustNot  []string
	MustCall []string
	// MustAny needs one member of each group to appear.
	MustAny [][]string
	// Check asserts the database state after the conversation (facts
	// superseded, forgotten, messages hidden).
	Check func(ctx context.Context, db *store.Store) error
}

var memoryCases = []memoryCase{
	{
		Name:     "knowledge update: moved to Jakarta",
		Facts:    []seedFact{{Text: "lives in Bali", Category: "circumstance", Core: true, ValidDaysAgo: 700}},
		Turns:    []string{"I moved to Jakarta last week.", "Where do I live now?"},
		MustAll:  []string{"Jakarta"},
		MustNot:  []string{"you live in Bali", "still in Bali"},
		MustCall: []string{"remember"},
		Check: func(ctx context.Context, db *store.Store) error {
			active, err := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"active"}})
			if err != nil {
				return err
			}
			hasJakarta := false
			for _, f := range active {
				if strings.Contains(strings.ToLower(f.Text), "jakarta") {
					hasJakarta = true
				}
				if strings.Contains(strings.ToLower(f.Text), "bali") {
					return fmt.Errorf("Bali is still active after the update")
				}
			}
			if !hasJakarta {
				return fmt.Errorf("no active Jakarta fact")
			}
			return nil
		},
	},
	{
		Name:     "temporal question: where last year",
		Facts:    []seedFact{{Text: "lives in Bali", Category: "circumstance", Core: true, ValidDaysAgo: 700}},
		Turns:    []string{"I moved to Jakarta last week.", "Where did I live last year?"},
		MustAll:  []string{"Bali"},
		MustNot:  []string{"you lived in Jakarta", "you live in Jakarta"},
		MustCall: []string{"recall"},
	},
	{
		Name:  "forget a fact and it stays forgotten",
		Facts: []seedFact{{Text: "mom is visiting in January", Category: "person", ValidDaysAgo: 10, FromMessage: "mom is visiting in January"}},
		Turns: []string{
			"Forget that mom is visiting in January.",
			"Yes, forget it.",
			"What do you know about mom visiting?",
		},
		MustNot:  []string{"January", "visiting"},
		MustCall: []string{"forget"},
		Check: func(ctx context.Context, db *store.Store) error {
			all, err := db.Facts(ctx, "assistant", store.FactFilter{Status: []string{"forgotten"}})
			if err != nil || len(all) == 0 {
				return fmt.Errorf("fact not forgotten: %v", err)
			}
			if all[0].SourceMessageID == nil {
				return fmt.Errorf("forgotten fact has no source message")
			}
			m, err := db.GetMessage(ctx, "assistant", *all[0].SourceMessageID)
			if err != nil || m.ForgottenAt == nil {
				return fmt.Errorf("source message not hidden: %v", err)
			}
			return nil
		},
	},
	{
		Name:    "abstention: nothing stored about a wedding",
		Turns:   []string{"When is my sister's wedding?"},
		MustAny: [][]string{{"don't know", "do not know", "no record", "not stored", "can't find", "cannot find", "couldn't find", "could not find", "don't have", "do not have", "nothing in", "no mention", "tidak tahu", "tidak ada"}},
		MustNot: []string{"June", "July", "August", "September", "October", "November", "December", "2026", "2027"},
	},
	{
		Name:    "core fact is always in context",
		Facts:   []seedFact{{Text: "prefers short answers", Category: "preference", Core: true, ValidDaysAgo: 30}},
		Turns:   []string{"How should you answer me?"},
		MustAll: []string{"short"},
	},
	{
		Name:    "person fact",
		Facts:   []seedFact{{Text: "my partner is Sam", Category: "person", Core: true, ValidDaysAgo: 60}},
		Turns:   []string{"Who is my partner?"},
		MustAll: []string{"Sam"},
	},
	{
		Name:     "health fact by recall",
		Facts:    []seedFact{{Text: "allergic to peanuts", Category: "health", ValidDaysAgo: 90}},
		Turns:    []string{"Am I allergic to anything?"},
		MustAll:  []string{"peanut"},
		MustCall: []string{"recall"},
	},
	{
		Name:     "recall a detail from 40 days ago",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "my passport expires in March 2027", DaysAgo: 40}},
		Turns:    []string{"When does my passport expire?"},
		MustAll:  []string{"March"},
		MustCall: []string{"recall"},
	},
	{
		Name:     "recall an Indonesian detail from 45 days ago",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "deadline laporan pajak tanggal 15", DaysAgo: 45}},
		Turns:    []string{"Kapan deadline laporan pajak?"},
		MustAll:  []string{"15"},
		MustCall: []string{"recall"},
	},
	{
		Name:     "correction: new job",
		Facts:    []seedFact{{Text: "works at Acme", Category: "work", ValidDaysAgo: 100}},
		Turns:    []string{"I work at Globex now.", "Where do I work?"},
		MustAll:  []string{"Globex"},
		MustNot:  []string{"at Acme"},
		MustCall: []string{"remember"},
	},
	{
		Name:     "recall a name from a message",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "my sister's name is Ana", DaysAgo: 20}},
		Turns:    []string{"What's my sister's name?"},
		MustAll:  []string{"Ana"},
		MustCall: []string{"recall"},
	},
	{
		Name:    "abstention: blood type",
		Turns:   []string{"What's my blood type?"},
		MustAny: [][]string{{"don't know", "do not know", "no record", "not stored", "can't find", "cannot find", "couldn't find", "could not find", "don't have", "do not have", "nothing in", "no mention", "tidak tahu", "tidak ada"}},
		MustNot: []string{"O+", "A+", "B+", "AB+"},
	},
}

var crossLanguageCases = []memoryCase{
	{
		Name:     "Indonesian message, English question (burned out)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "capek banget, deadline bikin stres", DaysAgo: 35}},
		Turns:    []string{"When was I burned out?"},
		MustAny:  [][]string{{"capek", "stres", "deadline"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "English message, Indonesian question (burned out)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "I felt completely burned out after the launch", DaysAgo: 35}},
		Turns:    []string{"Kapan aku capek banget?"},
		MustAny:  [][]string{{"burned out", "launch", "exhausted"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "Indonesian message, English question (sad about moving)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "aku sedih banget waktu pindah dari Bali", DaysAgo: 50}},
		Turns:    []string{"When was I sad about moving?"},
		MustAny:  [][]string{{"Bali", "sedih"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "English message, Indonesian question (mother ill)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "my mother was diagnosed with diabetes", DaysAgo: 60}},
		Turns:    []string{"Kapan ibu saya sakit?"},
		MustAny:  [][]string{{"diabetes", "mother", "diagnosed"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "Indonesian message, English question (late salary)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "gaji bulan ini telat lagi", DaysAgo: 25}},
		Turns:    []string{"Why was I annoyed about money?"},
		MustAny:  [][]string{{"gaji", "salary", "telat", "late"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "English message, Indonesian question (rent)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "the landlord raised the rent by 20 percent", DaysAgo: 70}},
		Turns:    []string{"Kenapa sewa naik?"},
		MustAny:  [][]string{{"rent", "landlord", "20"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "Indonesian message, English question (afraid of losing job)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "aku takut kehilangan pekerjaan", DaysAgo: 80}},
		Turns:    []string{"What was I afraid of?"},
		MustAny:  [][]string{{"job", "pekerjaan", "takut"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "English message, Indonesian question (learning Portuguese)",
		Messages: []seedMessage{{CharacterID: "ria", Role: "user", Content: "I'm learning Portuguese for the trip", DaysAgo: 55}},
		Turns:    []string{"Bahasa apa yang sedang saya pelajari?"},
		MustAny:  [][]string{{"Portuguese", "Portugis", "trip"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "Indonesian message, English question (nervous presentation)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "minggu lalu aku presentasi di kantor dan deg-degan", DaysAgo: 30}},
		Turns:    []string{"When was I nervous about a presentation?"},
		MustAny:  [][]string{{"presentasi", "deg-degan"}},
		MustCall: []string{"recall"},
	},
	{
		Name:     "English message, Indonesian question (knee)",
		Messages: []seedMessage{{CharacterID: "assistant", Role: "user", Content: "my knee hurts when I run", DaysAgo: 90}},
		Turns:    []string{"Kenapa lutut saya sakit?"},
		MustAny:  [][]string{{"knee", "run", "hurts"}},
		MustCall: []string{"recall"},
	},
}
