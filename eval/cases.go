package main

// The eval sets from docs/assistant-roadmap.md §7. Sizes are deliberately the
// documented ones: ~40 tool calls, ~15 time queries, ~10 check-in scenarios.

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"squad/tools"
)

// evalNow is the fixed clock every case is judged against:
// Tuesday 1 December 2026, 09:00 in Asia/Makassar.
var (
	evalNow = time.Date(2026, 12, 1, 1, 0, 0, 0, time.UTC)
	evalDay = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	evalLoc = mustLoc("Asia/Makassar")
)

func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// toolCase is one utterance. Tool "" means the model must not record anything.
// Want keys match the tool's argument names; see matchArg.
type toolCase struct {
	Text string
	Tool string
	Want map[string]string
}

var toolCases = []toolCase{
	// English
	{Text: "I need to finish the API refactor today", Tool: "plan_tasks", Want: map[string]string{"tasks": "api refactor"}},
	{Text: "add gym and call the dentist to my plans for tomorrow", Tool: "plan_tasks", Want: map[string]string{"tasks": "gym,call the dentist"}},
	{Text: "move the report to Monday", Tool: "update_task", Want: map[string]string{"task": "report", "plannedOn": "2026-12-07"}},
	{Text: "mark 'write tests' as done", Tool: "complete_task", Want: map[string]string{"task": "write tests", "status": "done"}},
	{Text: "I dropped the newsletter idea, stop tracking it", Tool: "complete_task", Want: map[string]string{"task": "newsletter", "status": "dropped"}},
	{Text: "what's left for today?", Tool: "list_tasks", Want: map[string]string{}},
	{Text: "log 2 hours on the API refactor", Tool: "log_activity", Want: map[string]string{"description": "api refactor", "minutes": "120"}},
	{Text: "I spent 45 minutes reviewing PRs", Tool: "log_activity", Want: map[string]string{"description": "PRs", "minutes": "45"}},
	{Text: "I fixed a prod bug, took about half an hour", Tool: "log_activity", Want: map[string]string{"description": "prod bug", "minutes": "30"}},
	{Text: "what did I do yesterday?", Tool: "query_activities", Want: map[string]string{"from": "2026-11-30", "to": "2026-11-30"}},
	{Text: "what did I do on 28 November?", Tool: "query_activities", Want: map[string]string{"from": "2026-11-28", "to": "2026-11-28"}},
	{Text: "remind me at 3pm to call mom", Tool: "remind_me", Want: map[string]string{"text": "call mom", "at": "15:00"}},
	{Text: "remind me every day at 9 to stretch", Tool: "remind_me", Want: map[string]string{"text": "stretch", "at": "09:00", "repeat": "daily"}},
	{Text: "set a reminder for tomorrow at 8:30 to submit the invoice", Tool: "remind_me", Want: map[string]string{"text": "invoice", "at": "08:30"}},
	{Text: "remind me in 20 minutes to check the oven", Tool: "remind_me", Want: map[string]string{"text": "oven", "at": "09:20"}},
	{Text: "what reminders do I have?", Tool: "list_reminders", Want: map[string]string{}},
	{Text: "cancel the call mom reminder", Tool: "cancel_reminder", Want: map[string]string{"reminder": "call mom"}},
	{Text: "no check-in tomorrow morning", Tool: "skip_checkin", Want: map[string]string{"day": "2026-12-02", "kind": "morning"}},
	{Text: "skip today's evening check-in", Tool: "skip_checkin", Want: map[string]string{"day": "2026-12-01", "kind": "evening"}},
	{Text: "I might go to the gym later", Tool: "", Want: nil},
	{Text: "the gym was nice yesterday", Tool: "", Want: nil},
	{Text: "what day is it?", Tool: "", Want: nil},
	{Text: "thanks!", Tool: "", Want: nil},
	{Text: "hmm, not sure yet", Tool: "", Want: nil},
	{Text: "I have to write the quarterly report by Friday, plan it", Tool: "plan_tasks", Want: map[string]string{"tasks": "quarterly report"}},

	// Indonesian (the user mixes languages)
	{Text: "tolong ingatkan aku jam 3 sore buat telepon ibu", Tool: "remind_me", Want: map[string]string{"text": "telepon ibu", "at": "15:00"}},
	{Text: "tadi pagi aku olahraga 30 menit", Tool: "log_activity", Want: map[string]string{"description": "olahraga", "minutes": "30"}},
	{Text: "rencananya besok mau ke kantor dan beli susu", Tool: "plan_tasks", Want: map[string]string{"tasks": "kantor,susu"}},
	{Text: "tugas bikin laporan udah selesai", Tool: "complete_task", Want: map[string]string{"task": "laporan"}},
	{Text: "apa aja yang aku kerjain hari ini?", Tool: "query_activities", Want: map[string]string{"from": "2026-12-01", "to": "2026-12-01"}},
	{Text: "ingatkan setiap hari jam 9 buat minum obat", Tool: "remind_me", Want: map[string]string{"text": "minum obat", "at": "09:00", "repeat": "daily"}},
	{Text: "batalkan pengingat minum obat", Tool: "cancel_reminder", Want: map[string]string{"reminder": "minum obat"}},
	{Text: "geser bikin laporan ke besok", Tool: "update_task", Want: map[string]string{"task": "laporan", "plannedOn": "2026-12-02"}},
	{Text: "aku udah ngerjain refactor API dua jam", Tool: "log_activity", Want: map[string]string{"description": "refactor API", "minutes": "120"}},
	{Text: "besok pagi jangan check-in dulu", Tool: "skip_checkin", Want: map[string]string{"day": "2026-12-02", "kind": "morning"}},
	{Text: "sisa tugas apa aja?", Tool: "list_tasks", Want: map[string]string{}},
	{Text: "pengingat apa yang masih ada?", Tool: "list_reminders", Want: map[string]string{}},
	{Text: "mungkin nanti aku ke gym", Tool: "", Want: nil},
	{Text: "makasih ya", Tool: "", Want: nil},
}

// timeCase is one "what did I do" question over the seeded week. MustAll must
// all appear; MustNot must not; every MustAny group needs one member. This is
// how "no invented items, totals match SQL" is checked without a human.
type timeCase struct {
	Question string
	MustAll  []string
	MustNot  []string
	MustAny  [][]string
}

var timeCases = []timeCase{
	{Question: "What did I do yesterday?", MustAll: []string{"api refactor", "gym"}, MustNot: []string{"prod bug", "wrote tests"}},
	{Question: "How long did I work on the API refactor yesterday?", MustAny: [][]string{{"150", "2.5", "2,5", "two and a half", "2 hours 30", "2h30", "2 h 30"}}},
	{Question: "What did I do today?", MustAll: []string{"wrote tests"}, MustNot: []string{"api refactor", "prod bug"}},
	{Question: "What did I do the day before yesterday?", MustAll: []string{"prod bug"}, MustNot: []string{"wrote tests", "gym"}},
	{Question: "What's still open from the last few days?", MustAll: []string{"finish the report"}, MustNot: []string{"ship it"}},
	{Question: "What did I plan for yesterday?", MustAll: []string{"ship it"}},
	{Question: "List everything I logged on 29 November 2026", MustAll: []string{"prod bug"}, MustNot: []string{"gym", "wrote tests"}},
	{Question: "How many minutes did I log today?", MustAny: [][]string{{"45"}}},
	{Question: "How much did I work yesterday in total?", MustAny: [][]string{{"150", "2.5", "2,5", "two and a half", "2 hours 30", "2h30", "2 h 30"}}},
	{Question: "What did I do last Monday?", MustAll: []string{"api refactor"}, MustNot: []string{"prod bug"}},
	{Question: "Did I do anything at all yesterday?", MustAll: []string{"api refactor"}},
	{Question: "What did I do on 30 November?", MustAll: []string{"api refactor"}, MustNot: []string{"prod bug"}},
}

// advice case: a seeded state the morning or evening check-in runs against.
type adviceCase struct {
	Name     string
	Kind     string // morning | evening
	Setup    func(seed *seedData)
	NowLocal time.Time // local hour matters: morning vs evening
}

func evalHour(h, m int) time.Time { return time.Date(2026, 12, 1, h, m, 0, 0, evalLoc) }

var adviceCases = []adviceCase{
	{Name: "morning, nothing planned", Kind: "morning", NowLocal: evalHour(9, 0)},
	{Name: "morning, yesterday's plans open", Kind: "morning", NowLocal: evalHour(8, 30), Setup: func(s *seedData) {
		s.tasks = append(s.tasks, seedTask{Title: "finish the report", PlannedOn: evalDay.AddDate(0, 0, -1), Status: "open", TimesMoved: 2})
	}},
	{Name: "morning, already has plans", Kind: "morning", NowLocal: evalHour(9, 30), Setup: func(s *seedData) {
		s.tasks = append(s.tasks, seedTask{Title: "write the tests", PlannedOn: evalDay, Status: "open"})
	}},
	{Name: "morning, a slipped plan", Kind: "morning", NowLocal: evalHour(10, 0), Setup: func(s *seedData) {
		s.tasks = append(s.tasks, seedTask{Title: "call the dentist", PlannedOn: evalDay.AddDate(0, 0, -3), Status: "open"})
	}},
	{Name: "evening, everything done", Kind: "evening", NowLocal: evalHour(21, 0), Setup: func(s *seedData) {
		s.tasks = append(s.tasks, seedTask{Title: "api refactor", PlannedOn: evalDay, Status: "done"})
		s.activities = append(s.activities, seedActivity{Description: "api refactor", Minutes: intPtr(120)})
	}},
	{Name: "evening, nothing logged", Kind: "evening", NowLocal: evalHour(21, 0), Setup: func(s *seedData) {
		s.tasks = append(s.tasks, seedTask{Title: "gym", PlannedOn: evalDay, Status: "open"})
	}},
	{Name: "evening, a hard day", Kind: "evening", NowLocal: evalHour(22, 0), Setup: func(s *seedData) {
		s.tasks = append(s.tasks, seedTask{Title: "finish the report", PlannedOn: evalDay, Status: "open"})
		s.activities = append(s.activities, seedActivity{Description: "prod bug", Minutes: intPtr(180)})
	}},
	{Name: "morning, reminder pending", Kind: "morning", NowLocal: evalHour(8, 5), Setup: func(s *seedData) {
		s.reminderText = "call mom"
	}},
	{Name: "evening, plans undone", Kind: "evening", NowLocal: evalHour(21, 30), Setup: func(s *seedData) {
		s.tasks = append(s.tasks,
			seedTask{Title: "newsletter", PlannedOn: evalDay, Status: "dropped"},
			seedTask{Title: "write tests", PlannedOn: evalDay, Status: "done"},
		)
	}},
	{Name: "morning, a fresh week", Kind: "morning", NowLocal: evalHour(8, 15), Setup: func(s *seedData) {
		for i := 1; i <= 3; i++ {
			s.activities = append(s.activities, seedActivity{Description: "gym", Minutes: intPtr(45), DayOffset: -i})
		}
	}},
}

type seedData struct {
	projects     []string
	tasks        []seedTask
	activities   []seedActivity
	facts        []seedFact
	reminderText string
}

type seedTask struct {
	Title      string
	Project    string
	PlannedOn  time.Time
	Status     string
	TimesMoved int
}

type seedActivity struct {
	Description string
	Project     string
	Minutes     *int
	DayOffset   int
}

func intPtr(v int) *int { return &v }

// matchArg checks one expected argument against what the model produced.
// lookupID, when set, resolves a task id ("#2", "2") to its title so that
// naming the task by id counts as naming it.
func matchArg(key string, got json.RawMessage, want string, lookupID func(int64) (string, bool)) bool {
	if len(got) == 0 {
		return false
	}
	var raw any
	if err := json.Unmarshal(got, &raw); err != nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(got)))
	text = strings.Trim(text, "\"")

	switch key {
	case "minutes":
		n, err := strconv.Atoi(text)
		if err != nil {
			var f float64
			if json.Unmarshal(got, &f) != nil {
				return false
			}
			n = int(f)
		}
		wantN, _ := strconv.Atoi(want)
		return n == wantN
	case "at":
		t, err := tools.ParseReminderAt(text, evalNow, evalLoc)
		if err != nil {
			return false
		}
		return t.In(evalLoc).Format("15:04") == want
	case "plannedOn", "dueOn", "day", "from", "to":
		d, err := tools.ParseDay(text, evalDay)
		if err != nil {
			return false
		}
		return d.Equal(mustParseDay(want))
	case "tasks":
		// judgeTool passes the value of the "tasks" key, i.e. the array itself.
		var tasks []struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(got, &tasks); err != nil || len(tasks) == 0 {
			return false
		}
		var titles []string
		for _, t := range tasks {
			titles = append(titles, strings.ToLower(t.Title))
		}
		joined := strings.Join(titles, "\n")
		for _, need := range strings.Split(want, ",") {
			if !strings.Contains(joined, strings.ToLower(strings.TrimSpace(need))) {
				return false
			}
		}
		return true
	case "task", "reminder":
		if strings.Contains(text, strings.ToLower(want)) {
			return true
		}
		if lookupID != nil {
			idText := strings.TrimPrefix(text, "#")
			if id, err := strconv.ParseInt(idText, 10, 64); err == nil {
				if title, ok := lookupID(id); ok && strings.Contains(strings.ToLower(title), strings.ToLower(want)) {
					return true
				}
			}
		}
		return false
	default:
		return strings.Contains(text, strings.ToLower(want))
	}
}

func mustParseDay(s string) time.Time {
	d, err := tools.ParseDay(s, evalDay)
	if err != nil {
		panic(err)
	}
	return d
}
