package llm

import (
	"strings"
	"testing"
)

func TestClaudeTranscript(t *testing.T) {
	got := claudeTranscript([]Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "tool", Content: "hidden"},
		{Role: "user", Content: "three"},
	})
	for _, want := range []string{"<conversation>", "User: one", "You: two", "Reply to the user's latest message:\n\nthree", "</conversation>"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("tool result leaked into transcript:\n%s", got)
	}

	if got := claudeTranscript([]Message{{Role: "user", Content: "only"}}); got != "only" {
		t.Errorf("single message = %q", got)
	}
}
