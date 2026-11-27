package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"squad/store"
)

// handleDue is the lazy scheduler (D4). The poll gets its answer from the
// database immediately; the check-in turn runs in the background so a slow
// model never stalls the page. Two tabs polling at once cannot double-send:
// the checkins primary key decides, and only one turn runs at a time.
func (s *Server) handleDue(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.Error(w, "assistant is not configured", http.StatusServiceUnavailable)
		return
	}
	now := s.clock()
	plan, err := s.engine.Decide(r.Context(), now)
	if err != nil {
		serverError(w, "due", err)
		return
	}

	reminders := make([]map[string]any, 0, len(plan.Reminders))
	for _, rem := range plan.Reminders {
		reminders = append(reminders, map[string]any{
			"id": rem.ID, "text": rem.Text, "at": rem.At, "missed": rem.Missed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"day":            store.DayString(plan.Day),
		"conversationId": plan.ConversationID,
		"reminders":      reminders,
		"checkin":        plan.Checkin,
		"busy":           s.engine.Busy(),
	})

	if plan.Checkin != "" {
		go func() {
			// The request context ends with the response; the turn must not.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 4*time.Minute)
			defer cancel()
			if res := s.engine.Execute(ctx, plan); res.Err != nil {
				log.Printf("check-in %s: %v", plan.Checkin, res.Err)
			}
		}()
	}
	// Close jobs and the summary run separately: a slow job must never delay
	// the check-in.
	if len(plan.Close) > 0 || plan.Summary {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
			defer cancel()
			s.engine.ExecuteJobs(ctx, plan)
		}()
	}
}
