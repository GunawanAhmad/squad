package main

import "fmt"

// The Grounding set (R-3): the assistant may only suggest things that exist in
// the seeded plans/facts, and every number it states must come from the tool
// result. Cases are seeded from scratch, not with the default week.

type groundingCase struct {
	Name     string
	Question string
	Tool     string // get_overview or get_summary
	Seed     func(*seedData)
	WantAll  []string
	WantAny  [][]string
	WantNot  []string
	Allowed  []string // seeded plans/facts/projects the judge may accept as citations
}

var groundingCases = []groundingCase{
	{
		Name:     "focus on open plans",
		Question: "What should I focus on this week?",
		Tool:     "get_overview",
		Seed: func(s *seedData) {
			s.projects = []string{"work"}
			s.tasks = []seedTask{
				{Title: "finish the report", Project: "work", PlannedOn: evalDay.AddDate(0, 0, -3), Status: "open", TimesMoved: 2},
				{Title: "write tests", Project: "work", PlannedOn: evalDay.AddDate(0, 0, -1), Status: "open"},
				{Title: "old newsletter", PlannedOn: evalDay.AddDate(0, 0, -2), Status: "dropped"},
			}
			s.activities = []seedActivity{
				{Description: "api refactor", Project: "work", Minutes: intPtr(120), DayOffset: -1},
				{Description: "review", Project: "work", Minutes: intPtr(30), DayOffset: -2},
				{Description: "inbox", Minutes: intPtr(45), DayOffset: -1},
			}
			s.facts = []seedFact{
				{Text: "finish the book", Category: "goal", ValidDaysAgo: 10},
				{Text: "learn Go", Category: "goal", ValidDaysAgo: 5},
			}
		},
		Allowed: []string{"finish the report", "write tests", "finish the book", "learn Go", "work", "api refactor", "inbox", "review"},
	},
	{
		Name:     "focus with a goal and no plans",
		Question: "What should I focus on?",
		Tool:     "get_overview",
		Seed: func(s *seedData) {
			s.facts = []seedFact{{Text: "learn Go", Category: "goal", ValidDaysAgo: 14}}
		},
		Allowed: []string{"learn Go"},
	},
	{
		Name:     "focus in Indonesian",
		Question: "Aku harus fokus ke apa minggu ini?",
		Tool:     "get_overview",
		Seed: func(s *seedData) {
			s.projects = []string{"kantor"}
			s.tasks = []seedTask{
				{Title: "bikin laporan", Project: "kantor", PlannedOn: evalDay.AddDate(0, 0, -4), Status: "open", TimesMoved: 1},
			}
			s.activities = []seedActivity{
				{Description: "refactor API", Project: "kantor", Minutes: intPtr(90), DayOffset: -1},
			}
			s.facts = []seedFact{{Text: "nabung buat rumah", Category: "goal", ValidDaysAgo: 20}}
		},
		Allowed: []string{"bikin laporan", "nabung buat rumah", "kantor", "refactor API"},
	},
	{
		Name:     "focus with many plans stays at three",
		Question: "Which three plans should I work on this week?",
		Tool:     "get_overview",
		Seed: func(s *seedData) {
			for i := 1; i <= 18; i++ {
				s.tasks = append(s.tasks, seedTask{
					Title: fmt.Sprintf("plan %02d", i), PlannedOn: evalDay.AddDate(0, 0, -i), Status: "open",
				})
			}
			s.activities = []seedActivity{{Description: "inbox", Minutes: intPtr(45), DayOffset: -1}}
		},
		Allowed: []string{"plan 01", "#1", "plan 02", "#2", "plan 03", "#3", "plan 04", "#4", "plan 05", "#5", "plan 06", "#6", "plan 07", "#7", "plan 08", "#8", "plan 09", "#9", "plan 10", "#10", "plan 11", "#11", "plan 12", "#12", "plan 13", "#13", "plan 14", "#14", "plan 15", "#15", "plan 16", "#16", "plan 17", "#17", "plan 18", "#18"},
	},
	{
		Name:     "focus with an unrelated question stays grounded",
		Question: "What should I focus on today? Also, anything new in AI?",
		Tool:     "get_overview",
		Seed: func(s *seedData) {
			s.tasks = []seedTask{{Title: "finish the report", PlannedOn: evalDay.AddDate(0, 0, -2), Status: "open"}}
			s.facts = []seedFact{{Text: "run a marathon", Category: "goal", ValidDaysAgo: 8}}
		},
		Allowed: []string{"finish the report", "run a marathon"},
	},
	{
		Name:     "focus with no records",
		Question: "What should I focus on?",
		Tool:     "get_overview",
		WantNot:  []string{"finish the report", "marathon"},
	},
	{
		Name:     "summary of the week",
		Question: "Summarize the last 7 days.",
		Tool:     "get_summary",
		Seed: func(s *seedData) {
			s.projects = []string{"work", "home"}
			s.activities = []seedActivity{
				{Description: "api refactor", Project: "work", Minutes: intPtr(120), DayOffset: -1},
				{Description: "review", Project: "work", Minutes: intPtr(30), DayOffset: -2},
				{Description: "cleaning", Project: "home", Minutes: intPtr(45), DayOffset: -3},
				{Description: "inbox", Minutes: intPtr(30), DayOffset: -4},
			}
			s.tasks = []seedTask{
				{Title: "ship it", PlannedOn: evalDay.AddDate(0, 0, -1), Status: "done"},
				{Title: "old idea", PlannedOn: evalDay.AddDate(0, 0, -3), Status: "dropped"},
				{Title: "finish the report", PlannedOn: evalDay.AddDate(0, 0, -2), Status: "open", TimesMoved: 2},
				{Title: "write tests", PlannedOn: evalDay.AddDate(0, 0, -1), Status: "open"},
			}
		},
		WantAll: []string{"225", "work", "home", "150", "45", "1 done", "1 dropped", "1 moved", "2 still open"},
		Allowed: []string{"work", "home", "ship it", "finish the report", "write tests"},
	},
	{
		Name:     "summary in Indonesian",
		Question: "Rangkum minggu ini dong.",
		Tool:     "get_summary",
		Seed: func(s *seedData) {
			s.projects = []string{"kantor"}
			s.activities = []seedActivity{
				{Description: "rapat", Project: "kantor", Minutes: intPtr(60), DayOffset: -1},
				{Description: "coding", Project: "kantor", Minutes: intPtr(90), DayOffset: 0},
			}
		},
		WantAll: []string{"150", "kantor"},
		Allowed: []string{"kantor"},
	},
	{
		Name:     "summary of an empty week",
		Question: "How did my week go?",
		Tool:     "get_summary",
		WantNot:  []string{"120", "225", "finished the report"},
	},
	{
		Name:     "summary with moved plans",
		Question: "Summarize my week, including plans that slipped.",
		Tool:     "get_summary",
		Seed: func(s *seedData) {
			s.activities = []seedActivity{{Description: "deep work", Minutes: intPtr(300), DayOffset: -2}}
			s.tasks = []seedTask{
				{Title: "slipped plan", PlannedOn: evalDay.AddDate(0, 0, -4), Status: "open", TimesMoved: 3},
				{Title: "done plan", PlannedOn: evalDay.AddDate(0, 0, -2), Status: "done"},
			}
		},
		WantAll: []string{"300"},
		Allowed: []string{"slipped plan", "done plan"},
	},
}
