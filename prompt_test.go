package main

import (
	"strings"
	"testing"
)

func TestSystemPromptHasNow(t *testing.T) {
	t.Setenv("TZ", "Asia/Makassar")
	c := Character{ID: "test-missing-memory", Persona: "persona text", Model: "m"}
	got := loadSystemPrompt(c)
	if !strings.Contains(got, "persona text") || !strings.Contains(got, "<now>") || !strings.Contains(got, "Asia/Makassar") {
		t.Fatalf("system prompt missing persona or clock:\n%s", got)
	}
}
