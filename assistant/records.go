package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"squad/store"
	"squad/tools"
)

// Records is the "hard facts about the day" section of the prompt: today's
// plans, the open plans that slipped, what was logged, today's reminders and a
// seven-day digest. It is built from rows, never from an LLM call.
type Records struct {
	Day             time.Time
	Projects        []string
	TodayPlans      []store.Task
	EarlierOpen     []store.Task
	EarlierOpenMore int // open plans from earlier days beyond the cap
	TodayActivities []store.Activity
	ActivityMore    int
	TodayReminders  []store.Reminder
	NotedFacts      []store.Fact // extracted today (about a past day)
	Digest          []DaySummary // oldest first, at most 7
}

// DaySummary is one past day as the records remember it.
type DaySummary struct {
	Day        time.Time
	Activities []store.Activity
	Completed  []store.Task
	Slipped    []store.Task // planned that day and still open
	Dropped    []store.Task
}

const (
	maxEarlierOpen    = 15 // then list_tasks is the way to see the rest
	maxDayActivities  = 12
	maxDayTasks       = 8
	maxDigestDays     = 7
	maxTodayActivity  = 20
	maxTodayReminders = 10
)

// RecordsStore is the slice of the store LoadRecords reads. *store.Store
// satisfies it; the loop tests' in-memory store does not, so they keep the v0
// prompt.
type RecordsStore interface {
	Tasks(ctx context.Context, f store.TaskFilter) ([]store.Task, error)
	Activities(ctx context.Context, f store.ActivityFilter) ([]store.Activity, error)
	Reminders(ctx context.Context, f store.ReminderFilter) ([]store.Reminder, error)
	Projects(ctx context.Context) ([]store.Project, error)
	NotedFacts(ctx context.Context, characterID string, since time.Time, limit int) ([]store.Fact, error)
}

// LoadRecords reads one turn's (or one check-in's) records for one character.
// A failure returns an empty section rather than failing the turn: a chat
// without context beats no chat. In practice only the assistant has a daily
// thread, so today's records are its own; "I noted" is its own facts too.
func LoadRecords(ctx context.Context, rs RecordsStore, characterID string, day time.Time, set Settings) Records {
	rec := Records{Day: store.Day(day)}
	dayStart := At(rec.Day, set.DayStartsAt, set.Loc)
	nextStart := At(rec.Day.AddDate(0, 0, 1), set.DayStartsAt, set.Loc)

	plans, err := rs.Tasks(ctx, store.TaskFilter{PlannedOn: &rec.Day, Order: "created"})
	if err != nil {
		return Records{Day: rec.Day}
	}
	rec.TodayPlans = plans

	earlier, err := rs.Tasks(ctx, store.TaskFilter{
		Status: []string{"open"}, PlannedBefore: &rec.Day, Order: "moved", Limit: maxEarlierOpen + 1,
	})
	if err == nil {
		if len(earlier) > maxEarlierOpen {
			rec.EarlierOpen, rec.EarlierOpenMore = earlier[:maxEarlierOpen], len(earlier)-maxEarlierOpen
		} else {
			rec.EarlierOpen = earlier
		}
	}

	acts, err := rs.Activities(ctx, store.ActivityFilter{From: &rec.Day, To: &rec.Day})
	if err == nil {
		if len(acts) > maxTodayActivity {
			rec.TodayActivities, rec.ActivityMore = acts[:maxTodayActivity], len(acts)-maxTodayActivity
		} else {
			rec.TodayActivities = acts
		}
	}

	reminders, err := rs.Reminders(ctx, store.ReminderFilter{
		Status: []string{"pending"}, From: &dayStart, To: &nextStart, Limit: maxTodayReminders,
	})
	if err == nil {
		rec.TodayReminders = reminders
	}

	projects, err := rs.Projects(ctx)
	if err == nil {
		for _, p := range projects {
			rec.Projects = append(rec.Projects, p.Name)
		}
	}

	if noted, err := rs.NotedFacts(ctx, characterID, dayStart, 8); err == nil {
		rec.NotedFacts = noted
	}

	rec.Digest = loadDigest(ctx, rs, rec.Day, set)
	return rec
}

// loadDigest covers the seven days before day, oldest first. Days with nothing
// recorded are left out, so the section stays small.
func loadDigest(ctx context.Context, rs RecordsStore, day time.Time, set Settings) []DaySummary {
	from := day.AddDate(0, 0, -maxDigestDays)
	to := day.AddDate(0, 0, -1)
	summaries := map[string]*DaySummary{}
	get := func(d time.Time) *DaySummary {
		key := store.DayString(d)
		if summaries[key] == nil {
			summaries[key] = &DaySummary{Day: store.Day(d)}
		}
		return summaries[key]
	}

	if acts, err := rs.Activities(ctx, store.ActivityFilter{From: &from, To: &to}); err == nil {
		for _, a := range acts {
			s := get(a.Day)
			s.Activities = append(s.Activities, a)
		}
	}

	completedFrom := At(from, set.DayStartsAt, set.Loc)
	completedTo := At(day, set.DayStartsAt, set.Loc)
	if tasks, err := rs.Tasks(ctx, store.TaskFilter{CompletedFrom: &completedFrom, CompletedTo: &completedTo}); err == nil {
		for _, t := range tasks {
			if t.CompletedAt == nil {
				continue
			}
			s := get(store.Day(t.CompletedAt.In(set.Loc)))
			s.Completed = append(s.Completed, t)
		}
	}

	if tasks, err := rs.Tasks(ctx, store.TaskFilter{
		Status: []string{"open", "dropped"}, PlannedFrom: &from, PlannedTo: &to,
	}); err == nil {
		for _, t := range tasks {
			if t.PlannedOn == nil {
				continue
			}
			s := get(*t.PlannedOn)
			if t.Status == "open" {
				s.Slipped = append(s.Slipped, t)
			} else {
				s.Dropped = append(s.Dropped, t)
			}
		}
	}

	var out []DaySummary
	for i := 0; i < maxDigestDays; i++ {
		d := from.AddDate(0, 0, i)
		if s, ok := summaries[store.DayString(d)]; ok {
			out = append(out, *s)
		}
	}
	return out
}

// Prompt renders the records as prompt text, with hard caps so a busy week
// cannot crowd out the conversation. The model can call list_tasks or
// query_activities for the rest.
func (r Records) Prompt(loc *time.Location, dayStartsAt time.Duration, now time.Time) string {
	today, digest := r.PromptParts(loc, dayStartsAt, now)
	return today + digest
}

// PromptParts splits the section so the context builder can budget today and
// the digest separately (and log exactly which part was cut).
func (r Records) PromptParts(loc *time.Location, dayStartsAt time.Duration, now time.Time) (string, string) {
	var b strings.Builder
	b.WriteString("\n<today>\n")
	fmt.Fprintf(&b, "The current day runs %s to %s (%s): anything before %s belongs to the previous day.\n",
		clock(dayStartsAt), clock(dayStartsAt), r.Day.Format("Monday 2 January 2006"), clock(dayStartsAt))
	fmt.Fprintf(&b, "Now: %s\n", now.In(loc).Format("15:04"))

	if len(r.Projects) > 0 {
		fmt.Fprintf(&b, "Projects: %s\n", strings.Join(r.Projects, ", "))
	}

	if len(r.TodayPlans) == 0 {
		b.WriteString("Plans for today: none yet.\n")
	} else {
		fmt.Fprintf(&b, "Plans for today (%d):\n", len(r.TodayPlans))
		for _, t := range r.TodayPlans {
			fmt.Fprintf(&b, "- %s\n", tools.TaskLine(t, loc))
		}
	}

	if len(r.EarlierOpen) > 0 {
		fmt.Fprintf(&b, "Open plans from earlier days (oldest first, up to %d):\n", maxEarlierOpen)
		for _, t := range r.EarlierOpen {
			fmt.Fprintf(&b, "- %s\n", tools.TaskLine(t, loc))
		}
		if r.EarlierOpenMore > 0 {
			fmt.Fprintf(&b, "- and %d more; call list_tasks to see them.\n", r.EarlierOpenMore)
		}
	}

	if len(r.TodayActivities) > 0 {
		fmt.Fprintf(&b, "Logged today (%d):\n", len(r.TodayActivities))
		for _, a := range r.TodayActivities {
			fmt.Fprintf(&b, "- %s\n", tools.ActivityLine(a))
		}
		if r.ActivityMore > 0 {
			fmt.Fprintf(&b, "- and %d more; call query_activities.\n", r.ActivityMore)
		}
	}

	if len(r.NotedFacts) > 0 {
		b.WriteString("Facts noted today (extracted from past conversations; the user can correct them):\n")
		for _, f := range r.NotedFacts {
			fmt.Fprintf(&b, "- #%d (%s, since %s) %s\n", f.ID, f.Category, store.DayString(f.ValidFrom), f.Text)
		}
	}

	if len(r.TodayReminders) > 0 {
		fmt.Fprintf(&b, "Reminders still pending today:\n")
		for _, rem := range r.TodayReminders {
			fmt.Fprintf(&b, "- %s at %s\n", rem.Text, tools.FormatClock(rem.RemindAt, loc))
		}
	}
	b.WriteString("</today>\n")
	today := b.String()

	if len(r.Digest) == 0 {
		return today, ""
	}
	b.Reset()
	b.WriteString("\n<recent_days>\n")
	b.WriteString("The last seven days, from records. Use this to reflect back; it is not advice.\n")
	for _, d := range r.Digest {
		fmt.Fprintf(&b, "%s (%s):", store.DayString(d.Day), d.Day.Format("Monday"))
		if len(d.Activities) > 0 {
			var items []string
			for i, a := range d.Activities {
				if i == maxDayActivities {
					items = append(items, fmt.Sprintf("and %d more", len(d.Activities)-maxDayActivities))
					break
				}
				items = append(items, tools.ActivityLine(a))
			}
			fmt.Fprintf(&b, " did %s;", strings.Join(items, "; "))
		}
		if len(d.Completed) > 0 {
			fmt.Fprintf(&b, " completed %s;", taskTitles(d.Completed, maxDayTasks))
		}
		if len(d.Dropped) > 0 {
			fmt.Fprintf(&b, " dropped %s;", taskTitles(d.Dropped, maxDayTasks))
		}
		if len(d.Slipped) > 0 {
			fmt.Fprintf(&b, " still open from that day: %s;", taskTitles(d.Slipped, maxDayTasks))
		}
		b.WriteString("\n")
	}
	b.WriteString("</recent_days>\n")
	return today, b.String()
}

func taskTitles(tasks []store.Task, max int) string {
	var out []string
	for i, t := range tasks {
		if i == max {
			out = append(out, fmt.Sprintf("and %d more", len(tasks)-max))
			break
		}
		title := t.Title
		if t.TimesMoved > 0 {
			title += fmt.Sprintf(" (moved %dx)", t.TimesMoved)
		}
		out = append(out, title)
	}
	return strings.Join(out, ", ")
}

func clock(d time.Duration) string { return ClockString(d) }
