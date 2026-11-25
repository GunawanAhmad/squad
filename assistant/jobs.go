package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"characters/llm"
	"characters/store"
)

// Background jobs: the day-close fact extraction and the rolling summary. They
// run on jobs_model, one at a time, never in the check-in's path, and their
// output is validated in Go before anything is applied.

const (
	maxCloseMessages = 60
	maxCloseFacts    = 80
	closeLookback    = 7 // days back to look for unclosed days
	closePerPass     = 3 // do not burst after time away
	summaryKeep      = 40
	maxSummaryLen    = 1200
)

// jobsMu is separate from the check-in lock: a slow close job must never delay
// a morning check-in.
type jobState struct {
	mu   sync.Mutex
	busy bool
}

func (j *jobState) acquire() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.busy {
		return false
	}
	j.busy = true
	return true
}

func (j *jobState) release() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.busy = false
}

// CloseJob is one (day, character) pair whose facts still need extracting. The
// day-close job runs per owner, so one character's failure never re-applies or
// blocks another's.
type CloseJob struct {
	Day         time.Time
	CharacterID string
}

// closeJobs claims the past (day, character) pairs that still need closing.
// Claiming is a checkins row keyed by (day, kind, character_id), so two polls
// cannot close the same pair twice and a failed character retries alone.
func (e *Engine) closeJobs(ctx context.Context, today time.Time, set Settings) ([]CloseJob, error) {
	var jobs []CloseJob
	for i := 1; i <= closeLookback && len(jobs) < closePerPass; i++ {
		day := today.AddDate(0, 0, -i)
		from := At(day, set.DayStartsAt, set.Loc)
		to := At(day.AddDate(0, 0, 1), set.DayStartsAt, set.Loc)
		chars, err := e.Store.DayCharacters(ctx, day, from, to)
		if err != nil {
			return jobs, err
		}
		for _, characterID := range chars {
			if len(jobs) >= closePerPass {
				break
			}
			c, err := e.Store.Checkin(ctx, day, "close", characterID)
			switch {
			case err == nil:
				retryable := c.Status == "pending" || (c.Status == "failed" && c.Attempts < maxAttempts)
				if retryable {
					jobs = append(jobs, CloseJob{Day: day, CharacterID: characterID})
				}
				continue
			case err != nil && !errors.Is(err, store.ErrNotFound):
				return jobs, err
			}
			claimed, err := e.Store.ClaimCheckin(ctx, day, "close", characterID)
			if err != nil {
				return jobs, err
			}
			if claimed {
				jobs = append(jobs, CloseJob{Day: day, CharacterID: characterID})
			}
		}
	}
	return jobs, nil
}

// ExecuteJobs runs the close jobs and the rolling summary in the background.
// The handler calls it on every poll; it is cheap when nothing is due.
func (e *Engine) ExecuteJobs(ctx context.Context, plan Plan) {
	if e.Store == nil || e.Runner == nil {
		return
	}
	if len(plan.Close) == 0 && !plan.Summary {
		return
	}
	if !e.jobs.acquire() {
		return
	}
	defer e.jobs.release()

	set := e.settings(ctx)
	for _, job := range plan.Close {
		e.closeDay(ctx, job, set)
	}
	if plan.Summary {
		e.maybeSummarize(ctx, set)
	}
}

// closeDay extracts durable facts from one character's finished day and applies
// them to that character only.
func (e *Engine) closeDay(ctx context.Context, job CloseJob, set Settings) {
	day, characterID := job.Day, job.CharacterID
	// A plan may have been made while another poll was already running this
	// pair. Re-read the row now that the jobs lock is held; a finished or
	// exhausted pair must not be extracted twice.
	if c, err := e.Store.Checkin(ctx, day, "close", characterID); err == nil {
		if c.Status == "sent" || c.Status == "skipped" {
			return
		}
		if c.Status == "failed" && c.Attempts >= maxAttempts {
			return
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("close %s/%s: checking status: %v", store.DayString(day), characterID, err)
		return
	}
	model, modelID, err := e.jobsModel(ctx)
	if err != nil {
		log.Printf("close %s/%s: %v", store.DayString(day), characterID, err)
		e.failClose(ctx, job)
		return
	}
	dayStart := At(day, set.DayStartsAt, set.Loc)
	nextStart := At(day.AddDate(0, 0, 1), set.DayStartsAt, set.Loc)

	msgs, err := e.Store.DayMessages(ctx, characterID, day, dayStart, nextStart)
	if err != nil {
		log.Printf("close %s/%s: reading messages: %v", store.DayString(day), characterID, err)
		e.failClose(ctx, job)
		return
	}
	if len(msgs) == 0 {
		// Nothing was said; the pair is closed with nothing extracted.
		if err := e.Store.FinishCheckin(ctx, day, "close", characterID, "sent", nil, nil); err != nil {
			log.Printf("close %s/%s: marking empty day: %v", store.DayString(day), characterID, err)
		}
		return
	}
	facts, err := e.Store.Facts(ctx, characterID, store.FactFilter{Status: []string{"active"}, Limit: maxCloseFacts})
	if err != nil {
		log.Printf("close %s/%s: reading facts: %v", store.DayString(day), characterID, err)
		e.failClose(ctx, job)
		return
	}

	prompt, truncated := closePrompt(characterID, day, set, msgs, facts)
	runCtx, _ := json.Marshal(map[string]any{"day": store.DayString(day), "character_id": characterID, "messages": len(msgs), "facts": len(facts), "truncated": truncated})
	runID, err := e.Store.StartRun(ctx, store.Run{Kind: "close", Model: modelID, Context: runCtx, Status: StatusRunning})
	if err != nil {
		log.Printf("close %s/%s: starting run: %v", store.DayString(day), characterID, err)
		e.failClose(ctx, job)
		return
	}
	finish := func(status, errMsg string, usage llm.Usage) {
		var in, out *int
		if usage.InputTokens > 0 || usage.OutputTokens > 0 {
			i, o := usage.InputTokens, usage.OutputTokens
			in, out = &i, &o
		}
		_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, status, errMsg, in, out)
	}

	raw, usage, err := CallStructured(ctx, StructuredSpec{
		Model: model, ModelID: modelID, Session: "day-close",
		System: "You extract durable facts about the user from one agent's conversations with them. You never give advice and never invent anything.",
		Prompt: prompt,
		Schema: `{"facts":[{"op":"ADD|SUPERSEDE|NONE","id":0,"text":"...","category":"preference|person|circumstance|goal|work|health|general","core":false,"source_message_id":0}]}`,
	})
	if err != nil {
		log.Printf("close %s/%s: %v", store.DayString(day), characterID, err)
		finish(StatusError, err.Error(), usage)
		e.failClose(ctx, job)
		return
	}

	ops, rejects := validateCloseDoc(raw, characterID, facts, msgs)
	if len(rejects) > 0 {
		e.recordRejects(ctx, runID, map[string]any{
			"day": store.DayString(day), "character_id": characterID, "rejected": rejects, "applied": len(ops),
		})
	}
	applied, err := e.Store.ApplyFactOps(ctx, characterID, day, ops)
	if err != nil {
		log.Printf("close %s/%s: applying facts: %v", store.DayString(day), characterID, err)
		finish(StatusError, err.Error(), usage)
		e.failClose(ctx, job)
		return
	}
	// Keep the final trace: how many items were applied and refused.
	e.recordRejects(ctx, runID, map[string]any{
		"day": store.DayString(day), "character_id": characterID, "rejected": rejects, "applied": applied,
	})
	finish(StatusOK, "", usage)
	if err := e.Store.FinishCheckin(ctx, day, "close", characterID, "sent", nil, &runID); err != nil {
		log.Printf("close %s/%s: marking sent: %v", store.DayString(day), characterID, err)
	}
}

func (e *Engine) failClose(ctx context.Context, job CloseJob) {
	if attempts, err := e.Store.FailCheckin(context.WithoutCancel(ctx), job.Day, "close", job.CharacterID); err != nil {
		log.Printf("close %s/%s: recording failure: %v", store.DayString(job.Day), job.CharacterID, err)
	} else if attempts >= maxAttempts {
		log.Printf("close %s/%s: giving up after %d attempts", store.DayString(job.Day), job.CharacterID, attempts)
	}
}

func (e *Engine) recordRejects(ctx context.Context, runID int64, doc map[string]any) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return
	}
	if err := e.Store.SetRunContext(context.WithoutCancel(ctx), runID, raw); err != nil {
		log.Printf("close: recording run context: %v", err)
	}
}

const closeSystemRules = `Rules:
- Only durable facts about the user: preferences, people, circumstances, goals, work, health.
- Not tasks, not what happened that day ("went to the gym"), not the assistant's words.
- If a new fact contradicts an existing one, SUPERSEDE that fact's id. Otherwise ADD.
- If nothing durable was said, return {"facts":[]}. Never invent or generalize.
- text: one short sentence, at most 300 characters.
- source_message_id: the #id of the message this fact came from, copied from the message lines; omit it if unclear.`

func closePrompt(characterID string, day time.Time, set Settings, msgs []store.DayMessage, facts []store.Fact) (string, bool) {
	var b strings.Builder
	truncated := false
	fmt.Fprintf(&b, "Close the day %s (%s) for the agent %q. Everything below is only what this agent was told.\n\n", store.DayString(day), day.Format("Monday"), characterID)
	if len(facts) > 0 {
		b.WriteString("Existing active facts:\n")
		for _, f := range facts {
			fmt.Fprintf(&b, "- #%d (%s) %s\n", f.ID, f.Category, f.Text)
		}
		b.WriteString("\n")
	}
	b.WriteString("Messages from that day:\n")
	used := 0
	for i, m := range msgs {
		if i == maxCloseMessages || used > 12000 {
			fmt.Fprintf(&b, "- (%d more messages)\n", len(msgs)-i)
			truncated = true
			break
		}
		who := m.CharacterID
		if m.Role == "user" {
			who = "user"
		}
		line := fmt.Sprintf("- [#%d] [%s] %s: %s\n", m.ID, m.CreatedAt.In(set.Loc).Format("15:04"), who, clip(m.Content, 700))
		b.WriteString(line)
		used += len(line)
	}
	b.WriteString("\n")
	b.WriteString(closeSystemRules)
	return b.String(), truncated
}

// validateCloseDoc checks every item; only clean items become store ops. The
// facts and messages passed in are already for one character, and the checks
// here repeat that: a forged cross-owner SUPERSEDE id or source message id is
// rejected and recorded, never applied.
func validateCloseDoc(raw json.RawMessage, characterID string, existing []store.Fact, msgs []store.DayMessage) ([]store.FactOp, []string) {
	var doc struct {
		Facts []struct {
			Op              string `json:"op"`
			ID              int64  `json:"id"`
			Text            string `json:"text"`
			Category        string `json:"category"`
			Core            bool   `json:"core"`
			SourceMessageID *int64 `json:"source_message_id"`
		} `json:"facts"`
	}
	var rejects []string
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, []string{"document: " + err.Error()}
	}
	validSource := map[int64]bool{}
	for _, m := range msgs {
		if m.CharacterID == characterID {
			validSource[m.ID] = true
		}
	}
	byID := map[int64]store.Fact{}
	byText := map[string]bool{}
	for _, f := range existing {
		if f.CharacterID != characterID {
			continue
		}
		byID[f.ID] = f
		byText[strings.ToLower(strings.TrimSpace(f.Text))] = true
	}

	var ops []store.FactOp
	for i, item := range doc.Facts {
		op := strings.ToUpper(strings.TrimSpace(item.Op))
		text := strings.TrimSpace(item.Text)
		switch op {
		case "NONE":
			continue
		case "ADD", "SUPERSEDE":
		default:
			rejects = append(rejects, fmt.Sprintf("item %d: op %q", i, item.Op))
			continue
		}
		if text == "" || len(text) > 300 {
			rejects = append(rejects, fmt.Sprintf("item %d: text is empty or longer than 300 characters", i))
			continue
		}
		category := strings.ToLower(strings.TrimSpace(item.Category))
		if category == "" {
			category = "general"
		}
		if !store.ValidFactCategory(category) {
			rejects = append(rejects, fmt.Sprintf("item %d: category %q", i, item.Category))
			continue
		}
		if byText[strings.ToLower(text)] {
			rejects = append(rejects, fmt.Sprintf("item %d: duplicate of an active fact", i))
			continue
		}
		if op == "SUPERSEDE" {
			if _, ok := byID[item.ID]; !ok {
				rejects = append(rejects, fmt.Sprintf("item %d: supersede of unknown or inactive fact %d", i, item.ID))
				continue
			}
		}
		if item.SourceMessageID != nil && *item.SourceMessageID <= 0 {
			item.SourceMessageID = nil
		}
		if item.SourceMessageID != nil && !validSource[*item.SourceMessageID] {
			rejects = append(rejects, fmt.Sprintf("item %d: source_message_id %d is not from this day", i, *item.SourceMessageID))
			continue
		}
		ops = append(ops, store.FactOp{Op: op, ID: item.ID, Text: text, Category: category, Core: item.Core, SourceMessageID: item.SourceMessageID})
		byText[strings.ToLower(text)] = true
		if op == "SUPERSEDE" {
			delete(byID, item.ID) // a second supersede of the same row is invalid
		}
	}
	return ops, rejects
}

// summaryNeeded is the cheap check Decide runs; maybeSummarize does the work.
func (e *Engine) summaryNeeded(ctx context.Context, day time.Time, set Settings) bool {
	conv, err := e.Store.GetConversation(ctx, DailyConversationID(day))
	if err != nil {
		return false
	}
	msgs, err := e.Store.Messages(ctx, conv.ID, 0, 0)
	if err != nil {
		return false
	}
	fresh := 0
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" || m.ForgottenAt != nil {
			continue
		}
		if conv.SummaryUpto != nil && m.ID <= *conv.SummaryUpto {
			continue
		}
		fresh++
	}
	window := e.Window
	if window <= 0 {
		window = DefaultWindow
	}
	return fresh > window+8
}

// ---- rolling summary ------------------------------------------------------

// maybeSummarize compresses the part of today's thread that fell out of the
// window. It runs on jobs_model and only stores a summary it validated.
func (e *Engine) maybeSummarize(ctx context.Context, set Settings) {
	model, modelID, err := e.jobsModel(ctx)
	if err != nil {
		log.Printf("summary: %v", err)
		return
	}
	now := e.now()
	convID := DailyConversationID(LogicalDay(now, set.Loc, set.DayStartsAt))
	conv, err := e.Store.GetConversation(ctx, convID)
	if err != nil {
		return // no thread yet
	}
	msgs, err := e.Store.Messages(ctx, convID, 0, 0)
	if err != nil {
		log.Printf("summary: reading: %v", err)
		return
	}
	var fresh []store.Message
	wasForgotten := map[int64]bool{}
	for _, m := range msgs {
		if m.ForgottenAt != nil {
			wasForgotten[m.ID] = true
			continue
		}
		if (m.Role == "user" || m.Role == "assistant") &&
			(conv.SummaryUpto == nil || m.ID > *conv.SummaryUpto) {
			fresh = append(fresh, m)
		}
	}
	window := e.Window
	if window <= 0 {
		window = DefaultWindow
	}
	if len(fresh) <= window+8 {
		return
	}
	older := fresh[:len(fresh)-window]
	if len(older) < 8 {
		return
	}

	var b strings.Builder
	b.WriteString("Summarize this part of today's conversation for later context. Keep names, decisions, numbers and dates. ")
	b.WriteString("Do not add advice or anything that was not said. At most 800 characters, plain text.\n\n")
	if conv.Summary != "" {
		b.WriteString("Existing summary:\n" + conv.Summary + "\n\n")
	}
	used := 0
	var lastID int64
	for _, m := range older {
		line := fmt.Sprintf("%s: %s\n", m.Role, clip(m.Content, 600))
		if used+len(line) > 12000 {
			break
		}
		b.WriteString(line)
		used += len(line)
		lastID = m.ID
	}
	if lastID == 0 {
		return
	}

	runID, err := e.Store.StartRun(ctx, store.Run{
		Kind: "summary", ConversationID: &convID, Model: modelID,
		Context: []byte(`{"section":"summary"}`), Status: StatusRunning,
	})
	if err != nil {
		log.Printf("summary: starting run: %v", err)
		return
	}
	resp, err := model.Stream(ctx, llm.Request{
		Model: modelID, Session: "summary",
		System:   "You compress conversations faithfully. You never add advice or invent details.",
		Messages: []llm.Message{{Role: "user", Content: b.String()}},
	}, func(string) {})
	var in, out *int
	if resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0 {
		i, o := resp.Usage.InputTokens, resp.Usage.OutputTokens
		in, out = &i, &o
	}
	if err != nil {
		_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusError, err.Error(), in, out)
		log.Printf("summary: %v", err)
		return
	}
	summary := strings.TrimSpace(resp.Text)
	if summary == "" || len(summary) > maxSummaryLen {
		msg := fmt.Sprintf("summary rejected: %d characters", len(summary))
		_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusError, msg, in, out)
		return
	}
	// A message may have been forgotten while the model was running. Never
	// store a summary that quotes something the user just deleted.
	nowMsgs, err := e.Store.Messages(ctx, convID, 0, 0)
	if err != nil {
		_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusError, err.Error(), in, out)
		return
	}
	for _, m := range nowMsgs {
		if !wasForgotten[m.ID] && m.ID <= lastID && m.ForgottenAt != nil {
			_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusError, "messages forgotten during summary", in, out)
			return
		}
	}
	// If a forget cleared the summary the prompt was built from, never write a
	// replacement that still quotes the old text.
	if latest, err := e.Store.GetConversation(ctx, convID); err != nil || latest.Summary != conv.Summary {
		_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusError, "summary changed during the run", in, out)
		return
	}
	if err := e.Store.SetSummary(ctx, convID, summary, lastID); err != nil {
		_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusError, err.Error(), in, out)
		return
	}
	_ = e.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusOK, "", in, out)
}

func (e *Engine) jobsModel(ctx context.Context) (llm.Model, string, error) {
	if e.Runner == nil {
		return nil, "", fmt.Errorf("no runner")
	}
	model, ok := e.Runner.Model(llm.OpenCodeProvider)
	if !ok {
		return nil, "", fmt.Errorf("no OpenCode model adapter")
	}
	id := e.Char.Model
	if set := e.settings(ctx); set.JobsModel != "" {
		id = set.JobsModel
	}
	if id == "" {
		return nil, "", fmt.Errorf("no jobs model configured")
	}
	return model, id, nil
}
