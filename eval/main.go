// Command eval runs the model evals from docs/assistant-roadmap.md §7:
// tool calls, time queries and "no unsolicited advice" check-ins.
//
//	make eval models=deepseek-v4.1-flash,...       (spends real API credit)
//	go run ./eval -dry-run                         (plan and call estimate only)
//
// It writes nothing to the app database: every run gets a throwaway schema.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"characters/assistant"
	"characters/llm"
	"characters/store"
	"characters/store/storetest"
	"characters/tools"
)

type config struct {
	models     []string
	judgeModel string
	runs       int
	sets       []string
	dryRun     bool
	maxSteps   int
}

var assistantTools = []string{
	"plan_tasks", "update_task", "complete_task", "list_tasks",
	"log_activity", "query_activities",
	"remind_me", "list_reminders", "cancel_reminder",
	"skip_checkin", "undo",
}

func main() {
	var (
		models     = flag.String("models", "", "comma-separated OpenCode model ids (required unless -dry-run)")
		judgeModel = flag.String("judge", "", "model id for the no-advice judge (default: each model judges itself)")
		runs       = flag.Int("runs", 3, "runs per case, as in the roadmap")
		sets       = flag.String("sets", "tools,time,advice", "comma-separated: tools,time,advice")
		dryRun     = flag.Bool("dry-run", false, "print the plan and the estimated number of model calls, call nothing")
		maxSteps   = flag.Int("max-steps", 5, "tool loop steps per time query")
	)
	flag.Parse()

	cfg := config{
		judgeModel: *judgeModel,
		runs:       *runs,
		sets:       splitList(*sets),
		dryRun:     *dryRun,
		maxSteps:   *maxSteps,
	}
	for _, m := range splitList(*models) {
		cfg.models = append(cfg.models, m)
	}
	if cfg.runs < 1 {
		fail("runs must be at least 1")
	}
	if len(cfg.models) == 0 && !cfg.dryRun {
		fail("-models is required (for example -models=deepseek-v4.1-flash)")
	}

	if cfg.dryRun {
		printEstimate(cfg)
		return
	}

	loadDotEnv(".env")
	dsn, ok := storetest.DSN()
	if !ok {
		fail("DATABASE_URL is not set (needed for the throwaway eval schemas)")
	}

	key := os.Getenv("OPENCODE_API_KEY")
	if key == "" {
		fail("OPENCODE_API_KEY is not set")
	}
	base := envOr("OPENCODE_BASE_URL", "https://opencode.ai/inference/go/openai/v1")
	client := llm.NewOpenCode(key, base)

	persona := loadAssistantPersona()
	reg := tools.New()
	specs := reg.Specs(assistantTools)

	for _, model := range cfg.models {
		fmt.Printf("\n=== %s ===\n", model)
		judge := model
		if cfg.judgeModel != "" {
			judge = cfg.judgeModel
		}

		if contains(cfg.sets, "tools") {
			res, err := runToolSet(client, reg, specs, persona, model, dsn, cfg.runs)
			if err != nil {
				fmt.Printf("tools: %v\n", err)
			} else {
				printToolResult(model, res)
			}
		}
		if contains(cfg.sets, "time") {
			res, err := runTimeSet(client, reg, specs, persona, model, dsn, cfg.runs, cfg.maxSteps)
			if err != nil {
				fmt.Printf("time: %v\n", err)
			} else {
				printPassResult(model, "time queries", res)
			}
		}
		if contains(cfg.sets, "advice") {
			res, err := runAdviceSet(client, reg, specs, persona, model, judge, dsn, cfg.runs, cfg.maxSteps)
			if err != nil {
				fmt.Printf("advice: %v\n", err)
			} else {
				printPassResult(model, "no unsolicited advice", res)
			}
		}
	}
}

func printEstimate(cfg config) {
	toolCalls := len(toolCases) * cfg.runs
	timeCalls := len(timeCases) * cfg.runs * 3 // a tool call plus a final answer, rounded up
	adviceGen := len(adviceCases) * cfg.runs * 2
	adviceJudge := len(adviceCases) * cfg.runs
	perModel := 0
	models := cfg.models
	if len(models) == 0 {
		models = []string{"<each model>"}
	}
	if contains(cfg.sets, "tools") {
		perModel += toolCalls
	}
	if contains(cfg.sets, "time") {
		perModel += timeCalls
	}
	if contains(cfg.sets, "advice") {
		perModel += adviceGen + adviceJudge
	}
	fmt.Println("Eval plan (no calls made):")
	fmt.Printf("  tool calls: %d cases x %d runs = %d calls\n", len(toolCases), cfg.runs, toolCalls)
	fmt.Printf("  time queries: %d cases x %d runs x ~3 = %d calls\n", len(timeCases), cfg.runs, timeCalls)
	fmt.Printf("  no-advice: %d cases x %d runs x (generate + judge) = %d calls\n", len(adviceCases), cfg.runs, adviceGen+adviceJudge)
	fmt.Printf("  per model: ~%d calls\n", perModel)
	fmt.Printf("  models (%d): %s\n", len(models), strings.Join(models, ", "))
	fmt.Printf("  total: ~%d calls\n", perModel*len(models))
}

// ---- tool calls -----------------------------------------------------------

type toolResult struct {
	pass, total      int
	fieldsOK, fields int
	perCase          map[string][2]int
	firstFailure     []string
}

func runToolSet(client llm.Model, reg *tools.Registry, specs []llm.ToolSpec, persona, model, dsn string, runs int) (toolResult, error) {
	ctx := context.Background()
	db, cleanup, err := storetest.NewSchema(ctx, dsn)
	if err != nil {
		return toolResult{}, err
	}
	defer cleanup()
	if err := seed(ctx, db, defaultSeed()); err != nil {
		return toolResult{}, err
	}
	system := systemPrompt(ctx, db, persona)

	out := toolResult{perCase: map[string][2]int{}}
	for _, c := range toolCases {
		for i := 0; i < runs; i++ {
			resp, err := client.Stream(ctx, llm.Request{
				Model: model, System: system, Tools: specs, Session: "eval-tools",
				Messages: []llm.Message{{Role: "user", Content: c.Text}},
			}, func(string) {})
			out.total++
			out.perCase[c.Text] = add2(out.perCase[c.Text], 0, 0)
			if err != nil {
				if len(out.firstFailure) < 5 {
					out.firstFailure = append(out.firstFailure, fmt.Sprintf("%q: %v", c.Text, err))
				}
				count := out.perCase[c.Text]
				out.perCase[c.Text] = [2]int{count[0], count[1] + 1}
				continue
			}

			ok, fieldsOK, fields := judgeTool(c, resp.ToolCalls)
			out.fieldsOK += fieldsOK
			out.fields += fields
			count := out.perCase[c.Text]
			out.perCase[c.Text] = [2]int{count[0] + boolInt(ok), count[1] + 1}
			if ok {
				out.pass++
			} else if len(out.firstFailure) < 5 {
				out.firstFailure = append(out.firstFailure, describeToolFailure(c, resp.ToolCalls))
			}
		}
	}
	return out, nil
}

// judgeTool compares the model's first tool call with the case. A case with an
// empty Tool must produce no tool call at all.
func judgeTool(c toolCase, calls []llm.ToolCall) (ok bool, fieldsOK, fields int) {
	if c.Tool == "" {
		return len(calls) == 0, 0, 0
	}
	if len(calls) == 0 {
		return false, 0, len(c.Want)
	}
	call := calls[0]
	if call.Name != c.Tool {
		return false, 0, len(c.Want)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return false, 0, len(c.Want)
	}
	ok = true
	for key, want := range c.Want {
		fields++
		got, present := args[key]
		if present && matchArg(key, got, want) {
			fieldsOK++
		} else {
			ok = false
		}
	}
	return ok, fieldsOK, fields
}

func describeToolFailure(c toolCase, calls []llm.ToolCall) string {
	want := c.Tool
	if want == "" {
		want = "no tool"
	}
	if len(calls) == 0 {
		return fmt.Sprintf("%q: wanted %s, got no tool", c.Text, want)
	}
	return fmt.Sprintf("%q: wanted %s %v, got %s %s", c.Text, want, c.Want, calls[0].Name, calls[0].Arguments)
}

// ---- time queries ---------------------------------------------------------

func runTimeSet(client llm.Model, reg *tools.Registry, specs []llm.ToolSpec, persona, model, dsn string, runs, maxSteps int) (passResult, error) {
	ctx := context.Background()
	db, cleanup, err := storetest.NewSchema(ctx, dsn)
	if err != nil {
		return passResult{}, err
	}
	defer cleanup()
	if err := seed(ctx, db, defaultSeed()); err != nil {
		return passResult{}, err
	}
	system := systemPrompt(ctx, db, persona)
	env := tools.Env{
		CharacterID: "assistant", Allowed: assistantTools,
		Now: evalNow, DB: db, Day: evalDay, Loc: evalLoc,
	}

	out := passResult{perCase: map[string][2]int{}}
	for _, c := range timeCases {
		for i := 0; i < runs; i++ {
			answer, _, err := runTurn(ctx, client, model, system, specs, reg, env, c.Question, maxSteps)
			out.total++
			out.perCase[c.Question] = add2(out.perCase[c.Question], 0, 0)
			count := out.perCase[c.Question]
			if err != nil {
				if len(out.firstFailure) < 5 {
					out.firstFailure = append(out.firstFailure, fmt.Sprintf("%q: %v", c.Question, err))
				}
				out.perCase[c.Question] = [2]int{count[0], count[1] + 1}
				continue
			}
			if ok, why := judgeTimeAnswer(c, answer); ok {
				out.pass++
				out.perCase[c.Question] = [2]int{count[0] + 1, count[1] + 1}
			} else {
				out.perCase[c.Question] = [2]int{count[0], count[1] + 1}
				if len(out.firstFailure) < 5 {
					out.firstFailure = append(out.firstFailure, fmt.Sprintf("%q (%s): %s", c.Question, why, truncate(answer, 200)))
				}
			}
		}
	}
	return out, nil
}

func judgeTimeAnswer(c timeCase, answer string) (bool, string) {
	lower := strings.ToLower(answer)
	if strings.TrimSpace(lower) == "" {
		return false, "empty answer"
	}
	for _, must := range c.MustAll {
		if !strings.Contains(lower, strings.ToLower(must)) {
			return false, "missing " + must
		}
	}
	for _, bad := range c.MustNot {
		if strings.Contains(lower, strings.ToLower(bad)) {
			return false, "invented " + bad
		}
	}
	for _, group := range c.MustAny {
		found := false
		for _, opt := range group {
			if strings.Contains(lower, strings.ToLower(opt)) {
				found = true
				break
			}
		}
		if !found {
			return false, "missing total " + strings.Join(group, "/")
		}
	}
	return true, ""
}

// runTurn is the app's loop without persistence: call, run tools, repeat.
func runTurn(ctx context.Context, client llm.Model, model, system string, specs []llm.ToolSpec, reg *tools.Registry, env tools.Env, question string, maxSteps int) (string, []llm.ToolCall, error) {
	msgs := []llm.Message{{Role: "user", Content: question}}
	for step := 0; step < maxSteps; step++ {
		resp, err := client.Stream(ctx, llm.Request{
			Model: model, System: system, Messages: msgs, Tools: specs, Session: "eval-turn",
		}, func(string) {})
		if err != nil {
			return "", nil, err
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Text, ToolCalls: resp.ToolCalls, Reasoning: resp.Reasoning})
		if len(resp.ToolCalls) == 0 {
			return resp.Text, resp.ToolCalls, nil
		}
		for _, tc := range resp.ToolCalls {
			out := "error: no result"
			res, err := reg.Run(ctx, env, tc.Name, tc.Arguments)
			if err != nil {
				out = "error: " + err.Error()
			} else {
				out = res.Text
			}
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Content: out})
		}
	}
	return "", nil, fmt.Errorf("step limit")
}

// ---- no unsolicited advice ------------------------------------------------

type passResult struct {
	pass, total  int
	perCase      map[string][2]int
	firstFailure []string
}

func runAdviceSet(client llm.Model, reg *tools.Registry, specs []llm.ToolSpec, persona, model, judgeModel, dsn string, runs, maxSteps int) (passResult, error) {
	ctx := context.Background()
	out := passResult{perCase: map[string][2]int{}}
	settings := assistant.Settings{
		TZ: "Asia/Makassar", Loc: evalLoc, DayStartsAt: 4 * time.Hour,
		MorningAt: 8 * time.Hour, EveningAt: 21 * time.Hour,
	}

	for _, c := range adviceCases {
		for i := 0; i < runs; i++ {
			// A fresh schema per scenario: the check-in turn may record things.
			db, cleanup, err := storetest.NewSchema(ctx, dsn)
			if err != nil {
				return out, err
			}
			seedData := defaultSeed()
			if c.Setup != nil {
				c.Setup(&seedData)
			}
			if err := seed(ctx, db, seedData); err != nil {
				cleanup()
				return out, err
			}
			now := c.NowLocal.UTC()
			day := assistant.LogicalDay(now, evalLoc, settings.DayStartsAt)
			rec := assistant.LoadRecords(ctx, db, day, settings)
			cctx := assistant.BuildContext(
				assistant.Character{ID: "assistant", Name: "Assistant", Persona: persona, Tools: assistantTools, Daily: true},
				"", nil, 40, now, settings, &rec,
			)
			env := tools.Env{
				CharacterID: "assistant", Allowed: assistantTools, Now: now,
				DB: db, Day: day, Loc: settings.Loc,
			}
			answer, _, err := runTurn(ctx, client, model, cctx.System, specs, reg, env, assistant.TriggerInstruction(c.Kind), maxSteps)
			cleanup()
			out.total++
			key := c.Name + " (" + c.Kind + ")"
			out.perCase[key] = add2(out.perCase[key], 0, 0)
			count := out.perCase[key]
			if err != nil {
				if len(out.firstFailure) < 5 {
					out.firstFailure = append(out.firstFailure, fmt.Sprintf("%s: %v", key, err))
				}
				out.perCase[key] = [2]int{count[0], count[1] + 1}
				continue
			}
			pass, why, err := judgeAdvice(ctx, client, judgeModel, answer)
			if err != nil {
				if len(out.firstFailure) < 5 {
					out.firstFailure = append(out.firstFailure, fmt.Sprintf("%s: judge: %v", key, err))
				}
				out.perCase[key] = [2]int{count[0], count[1] + 1}
				continue
			}
			if pass {
				out.pass++
				out.perCase[key] = [2]int{count[0] + 1, count[1] + 1}
			} else {
				out.perCase[key] = [2]int{count[0], count[1] + 1}
				if len(out.firstFailure) < 5 {
					out.firstFailure = append(out.firstFailure, fmt.Sprintf("%s (%s): %s", key, why, truncate(answer, 200)))
				}
			}
		}
	}
	return out, nil
}

func judgeAdvice(ctx context.Context, client llm.Model, model, answer string) (bool, string, error) {
	prompt := "You review one check-in message from a personal assistant to its user.\n" +
		"Rules for a check-in: it may only ask a question and reflect back what the user said earlier. " +
		"It must not give advice, recommendations, tips, encouragement, opinions or next steps.\n\n" +
		"Message:\n\"\"\"\n" + answer + "\n\"\"\"\n\n" +
		"Reply with JSON only: {\"pass\": true} if it only asks/reflects and contains no advice, " +
		"{\"pass\": false} otherwise. Add \"why\" with at most 15 words."
	resp, err := client.Stream(ctx, llm.Request{
		Model: model, Session: "eval-judge",
		Messages: []llm.Message{{Role: "user", Content: prompt}},
	}, func(string) {})
	if err != nil {
		return false, "", err
	}
	var verdict struct {
		Pass bool   `json:"pass"`
		Why  string `json:"why"`
	}
	raw := extractJSON(resp.Text)
	if raw == "" || json.Unmarshal([]byte(raw), &verdict) != nil {
		return false, "judge did not answer JSON", nil
	}
	return verdict.Pass, verdict.Why, nil
}

// ---- shared helpers -------------------------------------------------------

var jsonObject = regexp.MustCompile(`\{[^{}]*\}`)

func extractJSON(s string) string { return jsonObject.FindString(s) }

func defaultSeed() seedData {
	return seedData{
		tasks: []seedTask{
			{Title: "ship it", PlannedOn: evalDay.AddDate(0, 0, -1), Status: "done"},
			{Title: "finish the report", PlannedOn: evalDay.AddDate(0, 0, -2), Status: "open", TimesMoved: 2},
			{Title: "newsletter", PlannedOn: evalDay, Status: "dropped"},
			{Title: "write tests", PlannedOn: evalDay, Status: "open"},
		},
		activities: []seedActivity{
			{Description: "api refactor", Minutes: intPtr(150), DayOffset: -1},
			{Description: "gym", DayOffset: -1},
			{Description: "prod bug", Minutes: intPtr(30), DayOffset: -2},
			{Description: "wrote tests", Minutes: intPtr(45)},
		},
	}
}

func seed(ctx context.Context, db *store.Store, s seedData) error {
	for _, t := range s.tasks {
		planned := t.PlannedOn
		task := store.Task{Title: t.Title, PlannedOn: &planned, Status: t.Status, TimesMoved: t.TimesMoved}
		if t.Status == "done" {
			done := evalNow
			task.CompletedAt = &done
		}
		if _, err := db.CreateTask(ctx, task); err != nil {
			return err
		}
	}
	for _, a := range s.activities {
		day := evalDay.AddDate(0, 0, a.DayOffset)
		if _, err := db.AddActivity(ctx, store.Activity{Day: day, Description: a.Description, Minutes: a.Minutes}); err != nil {
			return err
		}
	}
	if s.reminderText != "" {
		at := evalNow.Add(6 * time.Hour)
		if _, err := db.AddReminder(ctx, store.Reminder{Text: s.reminderText, RemindAt: at}); err != nil {
			return err
		}
	}
	return nil
}

func systemPrompt(ctx context.Context, db *store.Store, persona string) string {
	settings := assistant.Settings{
		TZ: "Asia/Makassar", Loc: evalLoc, DayStartsAt: 4 * time.Hour,
		MorningAt: 8 * time.Hour, EveningAt: 21 * time.Hour,
	}
	rec := assistant.LoadRecords(ctx, db, evalDay, settings)
	return assistant.BuildContext(
		assistant.Character{ID: "assistant", Name: "Assistant", Persona: persona, Tools: assistantTools, Daily: true},
		"", nil, 40, evalNow, settings, &rec,
	).System
}

func loadAssistantPersona() string {
	raw, err := os.ReadFile("characters.json")
	if err != nil {
		fail("characters.json: " + err.Error())
	}
	var list []struct {
		ID      string `json:"id"`
		Persona string `json:"persona"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		fail("characters.json: " + err.Error())
	}
	for _, c := range list {
		if c.ID == "assistant" {
			return c.Persona
		}
	}
	fail("characters.json has no assistant")
	return ""
}

func printToolResult(model string, r toolResult) {
	rate := percent(r.pass, r.total)
	fieldRate := percent(r.fieldsOK, r.fields)
	fmt.Printf("tool calls:        %d/%d pass (%.1f%%), fields %d/%d (%.1f%%)\n", r.pass, r.total, rate, r.fieldsOK, r.fields, fieldRate)
	printFailures(r.firstFailure)
}

func printPassResult(model, name string, r passResult) {
	fmt.Printf("%-18s %d/%d pass (%.1f%%)\n", name+":", r.pass, r.total, percent(r.pass, r.total))
	printFailures(r.firstFailure)
}

func printFailures(failures []string) {
	for _, f := range failures {
		fmt.Printf("  ! %s\n", f)
	}
}

func percent(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

func add2(v [2]int, a, b int) [2]int { return [2]int{v[0] + a, v[1] + b} }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "eval:", msg)
	os.Exit(1)
}

// loadDotEnv sets KEY=VALUE lines from path; real env vars win. Same rules as
// the app's loader, without importing package main.
func loadDotEnv(path string) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
}
