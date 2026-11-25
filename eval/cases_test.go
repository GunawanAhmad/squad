package main

import (
	"testing"

	"squad/llm"
)

func TestMatchArgTaskByIDAndDateDefaults(t *testing.T) {
	lookup := func(id int64) (string, bool) {
		if id == 2 {
			return "finish the report", true
		}
		return "", false
	}
	if !matchArg("task", []byte(`"#2"`), "report", lookup) {
		t.Fatal("task id should resolve to its title")
	}
	if matchArg("task", []byte(`"#3"`), "report", lookup) {
		t.Fatal("unknown id matched")
	}
	if !matchArg("to", []byte(`"yesterday"`), "2026-11-30", nil) {
		t.Fatal("relative date should parse")
	}
	if !matchArg("plannedOn", []byte(`"besok"`), "2026-12-02", nil) {
		t.Fatal("Indonesian 'besok' should parse")
	}
}

func TestJudgeToolAcceptsOnlyOneRangeEnd(t *testing.T) {
	c := toolCase{Text: "what did I do yesterday?", Tool: "query_activities", Want: map[string]string{"from": "2026-11-30", "to": "2026-11-30"}}
	ok, fieldsOK, fields := judgeTool(c, []llm.ToolCall{{
		Name: "query_activities", Arguments: `{"from":"yesterday"}`,
	}}, nil)
	if !ok || fieldsOK != fields || fields != 2 {
		t.Fatalf("one range end should satisfy both: ok=%v fieldsOK=%d/%d", ok, fieldsOK, fields)
	}
}

func TestGroundingJudgeRejectsWrongNumbersAndUncitedItems(t *testing.T) {
	c := groundingCase{
		Tool:    "get_overview",
		Allowed: []string{"finish the report"},
	}
	tool := "open plans (1 total):\n- #1 finish the report — planned 2026-11-28, age 3d, moved 2x"
	seed := "plan: finish the report (status open, planned 2026-11-28, moved 2x)"
	if ok, why := judgeGroundingAnswer(c, tool, seed, "1. **#1 finish the report** is open; start there."); !ok {
		t.Fatalf("valid answer rejected: %s", why)
	}
	if ok, _ := judgeGroundingAnswer(c, tool, seed, "1. **#1 finish the report** is open.\n2. Do your best."); ok {
		t.Fatal("uncited second suggestion accepted")
	}
	if ok, _ := judgeGroundingAnswer(c, tool, seed, "1. **#1 finish the report** has 999 minutes."); ok {
		t.Fatal("number outside the tool result accepted")
	}
	if ok, _ := judgeGroundingAnswer(c, tool, seed, "Focus on finish the report."); !ok {
		t.Fatal("prose citation rejected")
	}
}
