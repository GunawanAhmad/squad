package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"characters/assistant"
	"characters/store"
	"characters/tools"
)

// REST for the Today panel. These edits do not go through the model, so they
// share the tools' validation (tools.CreateTask, ApplyTaskPatch, ...): a date
// the model may not write is a date the panel may not write either.

func (s *server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	settings := assistant.LoadSettings(r.Context(), s.records)
	now := s.clock()
	day := assistant.LogicalDay(now, settings.Loc, settings.DayStartsAt)

	q := r.URL.Query()
	f := store.TaskFilter{Order: q.Get("order"), Limit: queryInt(r, "limit", 0)}
	if f.Limit < 0 || f.Limit > 500 {
		http.Error(w, "bad limit", http.StatusBadRequest)
		return
	}
	if v := q.Get("status"); v != "" {
		for _, st := range strings.Split(v, ",") {
			st = strings.TrimSpace(st)
			if st != "open" && st != "done" && st != "dropped" {
				http.Error(w, "bad status", http.StatusBadRequest)
				return
			}
			f.Status = append(f.Status, st)
		}
	}
	for name, dst := range map[string]**time.Time{
		"plannedOn":   &f.PlannedOn,
		"plannedFrom": &f.PlannedFrom,
		"plannedTo":   &f.PlannedTo,
		"openBefore":  &f.PlannedBefore,
	} {
		if v := q.Get(name); v != "" {
			d, err := tools.ParseDay(v, day)
			if err != nil {
				recordError(w, err)
				return
			}
			*dst = &d
		}
	}
	tasks, err := s.records.Tasks(r.Context(), f)
	if err != nil {
		serverError(w, "list tasks", err)
		return
	}
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out, "day": store.DayString(day)})
}

func (s *server) handlePatchTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	var patch tools.TaskPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	settings := assistant.LoadSettings(r.Context(), s.records)
	now := s.clock()
	day := assistant.LogicalDay(now, settings.Loc, settings.DayStartsAt)

	change, err := tools.ApplyTaskPatch(r.Context(), s.records, id, patch, now, day)
	if err != nil {
		recordError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, taskJSON(change.After))
}

func (s *server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	if err := s.records.DeleteTask(r.Context(), id); err != nil {
		recordError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListActivities(w http.ResponseWriter, r *http.Request) {
	settings := assistant.LoadSettings(r.Context(), s.records)
	now := s.clock()
	day := assistant.LogicalDay(now, settings.Loc, settings.DayStartsAt)

	f := store.ActivityFilter{Limit: queryInt(r, "limit", 0)}
	q := r.URL.Query()
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			d, err := tools.ParseDay(v, day)
			if err != nil {
				recordError(w, err)
				return
			}
			*dst = &d
		}
	}
	acts, err := s.records.Activities(r.Context(), f)
	if err != nil {
		serverError(w, "list activities", err)
		return
	}
	out := make([]map[string]any, 0, len(acts))
	for _, a := range acts {
		out = append(out, activityJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"activities": out, "day": store.DayString(day)})
}

func (s *server) handlePatchActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	var patch struct {
		Description *string `json:"description,omitempty"`
		Minutes     *int    `json:"minutes,omitempty"`
		ClearMin    bool    `json:"clearMinutes,omitempty"`
		Day         *string `json:"day,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	a, err := s.records.GetActivity(r.Context(), id)
	if err != nil {
		recordError(w, err)
		return
	}
	settings := assistant.LoadSettings(r.Context(), s.records)
	day := assistant.LogicalDay(s.clock(), settings.Loc, settings.DayStartsAt)
	if patch.Description != nil {
		if strings.TrimSpace(*patch.Description) == "" || len(*patch.Description) > 2000 {
			recordError(w, tools.ErrInvalid)
			return
		}
		a.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.ClearMin {
		a.Minutes = nil
	} else if patch.Minutes != nil {
		if *patch.Minutes < 1 || *patch.Minutes > 1440 {
			http.Error(w, "minutes must be between 1 and 1440", http.StatusBadRequest)
			return
		}
		a.Minutes = patch.Minutes
	}
	if patch.Day != nil {
		d, err := tools.ParseDay(*patch.Day, day)
		if err != nil {
			recordError(w, err)
			return
		}
		a.Day = d
	}
	updated, err := s.records.UpdateActivity(r.Context(), a)
	if err != nil {
		serverError(w, "update activity", err)
		return
	}
	writeJSON(w, http.StatusOK, activityJSON(updated))
}

func (s *server) handleDeleteActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	if err := s.records.DeleteActivity(r.Context(), id); err != nil {
		recordError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListReminders(w http.ResponseWriter, r *http.Request) {
	f := store.ReminderFilter{Limit: queryInt(r, "limit", 0)}
	q := r.URL.Query()
	if v := q.Get("status"); v != "" {
		for _, st := range strings.Split(v, ",") {
			st = strings.TrimSpace(st)
			if st != "pending" && st != "fired" && st != "cancelled" {
				http.Error(w, "bad status", http.StatusBadRequest)
				return
			}
			f.Status = append(f.Status, st)
		}
	}
	reminders, err := s.records.Reminders(r.Context(), f)
	if err != nil {
		serverError(w, "list reminders", err)
		return
	}
	settings := assistant.LoadSettings(r.Context(), s.records)
	out := make([]map[string]any, 0, len(reminders))
	for _, rem := range reminders {
		out = append(out, reminderJSON(rem, settings.Loc))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reminders": out})
}

func (s *server) handlePatchReminder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	var patch struct {
		Text   *string `json:"text,omitempty"`
		At     *string `json:"at,omitempty"`
		Repeat *string `json:"repeat,omitempty"`
		Cancel bool    `json:"cancel,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	rem, err := s.records.GetReminder(r.Context(), id)
	if err != nil {
		recordError(w, err)
		return
	}
	settings := assistant.LoadSettings(r.Context(), s.records)
	now := s.clock()
	if patch.Text != nil {
		text := strings.TrimSpace(*patch.Text)
		if text == "" || len(text) > 500 {
			recordError(w, tools.ErrInvalid)
			return
		}
		rem.Text = text
	}
	if patch.At != nil {
		at, err := tools.ParseReminderAt(*patch.At, now, settings.Loc)
		if err != nil {
			recordError(w, err)
			return
		}
		if !at.After(now) {
			http.Error(w, "that time is in the past", http.StatusBadRequest)
			return
		}
		rem.RemindAt = at
	}
	if patch.Repeat != nil {
		switch strings.ToLower(strings.TrimSpace(*patch.Repeat)) {
		case "", "daily", "weekdays":
			rem.Repeat = strings.ToLower(strings.TrimSpace(*patch.Repeat))
		default:
			http.Error(w, "repeat must be daily or weekdays", http.StatusBadRequest)
			return
		}
	}
	if patch.Cancel {
		rem.Status = "cancelled"
	}
	updated, err := s.records.UpdateReminder(r.Context(), rem)
	if err != nil {
		serverError(w, "update reminder", err)
		return
	}
	writeJSON(w, http.StatusOK, reminderJSON(updated, settings.Loc))
}

func (s *server) handleDeleteReminder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r)
	if !ok {
		return
	}
	if err := s.records.DeleteReminder(r.Context(), id); err != nil {
		recordError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsJSON(assistant.LoadSettings(r.Context(), s.records)))
}

func (s *server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TZ          *string `json:"tz,omitempty"`
		DayStartsAt *string `json:"dayStartsAt,omitempty"`
		MorningAt   *string `json:"morningAt,omitempty"`
		EveningAt   *string `json:"eveningAt,omitempty"`
		JobsModel   *string `json:"jobsModel,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	vals := map[string]string{}
	if req.TZ != nil {
		if _, err := time.LoadLocation(strings.TrimSpace(*req.TZ)); err != nil {
			http.Error(w, "unknown time zone", http.StatusBadRequest)
			return
		}
		vals[assistant.SettingTZ] = strings.TrimSpace(*req.TZ)
	}
	for name, pair := range map[string]struct {
		key string
		val *string
	}{
		"dayStartsAt": {assistant.SettingDayStartsAt, req.DayStartsAt},
		"morningAt":   {assistant.SettingMorningAt, req.MorningAt},
		"eveningAt":   {assistant.SettingEveningAt, req.EveningAt},
	} {
		if pair.val == nil {
			continue
		}
		d, err := assistant.ParseClock(*pair.val)
		if err != nil {
			http.Error(w, name+": "+err.Error(), http.StatusBadRequest)
			return
		}
		vals[pair.key] = assistant.ClockString(d)
	}
	if req.JobsModel != nil {
		vals[assistant.SettingJobsModel] = strings.TrimSpace(*req.JobsModel)
	}
	if err := s.records.SetSettings(r.Context(), vals); err != nil {
		serverError(w, "save settings", err)
		return
	}
	writeJSON(w, http.StatusOK, settingsJSON(assistant.LoadSettings(r.Context(), s.records)))
}

func settingsJSON(set assistant.Settings) map[string]any {
	return map[string]any{
		"tz":          set.TZ,
		"dayStartsAt": assistant.ClockString(set.DayStartsAt),
		"morningAt":   assistant.ClockString(set.MorningAt),
		"eveningAt":   assistant.ClockString(set.EveningAt),
		"jobsModel":   set.JobsModel,
	}
}

func taskJSON(t store.Task) map[string]any {
	out := map[string]any{
		"id": t.ID, "title": t.Title, "notes": t.Notes, "status": t.Status,
		"timesMoved": t.TimesMoved, "createdAt": t.CreatedAt, "updatedAt": t.UpdatedAt,
	}
	if t.PlannedOn != nil {
		out["plannedOn"] = store.DayString(*t.PlannedOn)
	}
	if t.DueOn != nil {
		out["dueOn"] = store.DayString(*t.DueOn)
	}
	if t.ProjectID != nil {
		out["projectId"] = *t.ProjectID
	}
	if t.CompletedAt != nil {
		out["completedAt"] = *t.CompletedAt
	}
	if t.ToolCallID != nil {
		out["toolCallId"] = *t.ToolCallID
	}
	return out
}

func activityJSON(a store.Activity) map[string]any {
	out := map[string]any{
		"id": a.ID, "day": store.DayString(a.Day), "description": a.Description, "createdAt": a.CreatedAt,
	}
	if a.Minutes != nil {
		out["minutes"] = *a.Minutes
	}
	if a.ProjectID != nil {
		out["projectId"] = *a.ProjectID
	}
	if a.ToolCallID != nil {
		out["toolCallId"] = *a.ToolCallID
	}
	return out
}

func reminderJSON(r store.Reminder, loc *time.Location) map[string]any {
	out := map[string]any{
		"id": r.ID, "text": r.Text, "remindAt": r.RemindAt, "status": r.Status, "createdAt": r.CreatedAt,
		"localTime": tools.FormatClock(r.RemindAt, loc),
	}
	if r.Repeat != "" {
		out["repeat"] = r.Repeat
	}
	if r.FiredAt != nil {
		out["firedAt"] = *r.FiredAt
	}
	if r.ToolCallID != nil {
		out["toolCallId"] = *r.ToolCallID
	}
	return out
}

func pathInt(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func recordError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tools.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, tools.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		serverError(w, "records", err)
	}
}
