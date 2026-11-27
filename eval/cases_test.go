package main

import (
	"testing"

	"squad/assistant"
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

func TestJudgeGateUsesDiscretionBands(t *testing.T) {
	f := func(ids ...int64) []assistant.Retrieved {
		var out []assistant.Retrieved
		for _, id := range ids {
			out = append(out, assistant.Retrieved{Kind: "fact", ID: id})
		}
		return out
	}
	if ok, why := judgeGate(f(1, 2, 3), f(1)); !ok {
		t.Fatalf("quiet gate rejected: %s", why)
	}
	if ok, _ := judgeGate(f(1, 2, 3), f(4)); ok {
		t.Fatal("sensitive fact at discretion 9 accepted")
	}
	if ok, _ := judgeGate(f(1, 2, 3), f(1, 2, 3)); ok {
		t.Fatal("discretion 9 sharing as much as discretion 2 accepted")
	}
	if ok, _ := judgeGate(f(1, 2), f(1)); ok {
		t.Fatal("discretion 2 sharing fewer than three facts accepted")
	}
	if ok, _ := judgeGate(f(1, 2, 3), f(1, 2, 3, 4)); ok {
		t.Fatal("sensitive fact among many accepted")
	}
}
