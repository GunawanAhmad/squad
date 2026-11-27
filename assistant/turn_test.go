package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"squad/llm"
	"squad/store/storetest"
	"squad/tools"
)

var (
	testNow = time.Date(2026, 12, 1, 9, 30, 0, 0, time.UTC)
	testLoc = mustLoc("Asia/Makassar")

	testChar = Character{
		ID: "ria", Name: "Ria", Provider: "opencode", Model: "fake",
		Persona: "You are Ria.", Tools: []string{"echo"},
	}
)

func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// echoTool is a deterministic stand-in for the web tools; "boom" makes it fail.
func newTestRegistry() *tools.Registry {
	r := tools.New()
	r.Add(&tools.Tool{
		Name:        "echo",
		Description: "echo the arguments",
		Parameters:  map[string]any{"type": "object"},
		Run: func(_ context.Context, _ tools.Env, raw string) (tools.Result, error) {
			if strings.Contains(raw, "boom") {
				return tools.Result{}, errors.New("boom")
			}
			return tools.Result{Text: "ok: " + raw}, nil
		},
	})
	return r
}

func testRunner(st Store, steps ...llm.FakeStep) (*Runner, *llm.Fake) {
	fake := llm.NewFake(steps...)
	r := New(Config{
		Store: st,
		Tools: newTestRegistry(),
		Models: map[string]llm.Model{
			string(llm.OpenCodeProvider): fake,
			string(llm.ClaudeProvider):   fake,
		},
		Memory:   func(string) string { return "<memory>test notes</memory>" },
		Now:      func() time.Time { return testNow },
		Location: testLoc,
		Window:   40,
		MaxSteps: 10,
	})
	return r, fake
}

func collectEvents() (func(Event), *[]Event) {
	var mu sync.Mutex
	var evs []Event
	return func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		evs = append(evs, e)
	}, &evs
}

func TestTurnText(t *testing.T) {
	st := newFakeStore()
	r, fake := testRunner(st, llm.FakeStep{Text: "Hello there.", Usage: llm.Usage{InputTokens: 12, OutputTokens: 4}})
	emit, events := collectEvents()

	res, err := r.Turn(context.Background(), TurnRequest{
		ConversationID: "c1", Character: testChar, Input: "hi",
	}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK || res.RunID == 0 || res.UserMessageID == 0 || res.AssistantMessageID == 0 {
		t.Fatalf("result: %+v", res)
	}
	if res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 4 {
		t.Fatalf("usage: %+v", res.Usage)
	}

	if len(*events) != 1 || (*events)[0].Type != "text" || (*events)[0].Delta != "Hello there." {
		t.Fatalf("events: %+v", *events)
	}

	msgs := st.messages()
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "hi" ||
		msgs[1].Role != "assistant" || msgs[1].Content != "Hello there." {
		t.Fatalf("messages: %+v", msgs)
	}
	if msgs[0].RunID == nil || *msgs[0].RunID != res.RunID || msgs[1].RunID == nil || *msgs[1].RunID != res.RunID {
		t.Fatalf("run ids: %+v", msgs)
	}

	runs := st.runList()
	if len(runs) != 1 || runs[0].Status != StatusOK || runs[0].Model != "fake" || runs[0].EndedAt == nil {
		t.Fatalf("runs: %+v", runs)
	}
	if runs[0].InputTokens == nil || *runs[0].InputTokens != 12 {
		t.Fatalf("run usage: %+v", runs[0])
	}
	if !strings.Contains(string(runs[0].Context), `"sections"`) || !strings.Contains(string(runs[0].Context), `"message_ids"`) {
		t.Fatalf("run context: %s", runs[0].Context)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: %d", len(reqs))
	}
	req := reqs[0]
	if !strings.Contains(req.System, "You are Ria.") || !strings.Contains(req.System, "test notes") ||
		!strings.Contains(req.System, "Tuesday") {
		t.Fatalf("system: %s", req.System)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != "hi" {
		t.Fatalf("request messages: %+v", req.Messages)
	}
	if req.Session != "c1" {
		t.Fatalf("session: %q", req.Session)
	}
}

func TestTurnToolCallThenText(t *testing.T) {
	st := newFakeStore()
	r, fake := testRunner(st,
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "echo", Arguments: `{"x":1}`}}},
		llm.FakeStep{Text: "Done."},
	)
	emit, events := collectEvents()

	res, err := r.Turn(context.Background(), TurnRequest{
		ConversationID: "c1", Character: testChar, Input: "run it",
	}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status: %s", res.Status)
	}

	if len(*events) != 2 || (*events)[0].Type != "tool" || (*events)[0].Name != "echo" || (*events)[1].Type != "text" {
		t.Fatalf("events: %+v", *events)
	}

	calls := st.toolCalls()
	if len(calls) != 1 || calls[0].Name != "echo" || calls[0].Status != "ok" || calls[0].Result != `ok: {"x":1}` {
		t.Fatalf("tool calls: %+v", calls)
	}

	msgs := st.messages()
	if len(msgs) != 4 {
		t.Fatalf("messages: %+v", msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) == 0 || msgs[1].Content != "" {
		t.Fatalf("assistant tool call message: %+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" || msgs[2].Content != `ok: {"x":1}` {
		t.Fatalf("tool result message: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "Done." {
		t.Fatalf("final message: %+v", msgs[3])
	}

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	second := reqs[1].Messages
	if len(second) != 3 || second[1].Role != "assistant" || len(second[1].ToolCalls) != 1 ||
		second[2].Role != "tool" || second[2].ToolCallID != "call_1" {
		t.Fatalf("second request: %+v", second)
	}
}

func TestTurnToolError(t *testing.T) {
	st := newFakeStore()
	r, fake := testRunner(st,
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "c", Name: "echo", Arguments: `{"q":"boom"}`}}},
		llm.FakeStep{Text: "I could not run that."},
	)
	emit, _ := collectEvents()

	res, err := r.Turn(context.Background(), TurnRequest{
		ConversationID: "c1", Character: testChar, Input: "go",
	}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status: %s", res.Status)
	}

	calls := st.toolCalls()
	if len(calls) != 1 || calls[0].Status != "error" || !strings.Contains(calls[0].Result, "error: boom") {
		t.Fatalf("tool calls: %+v", calls)
	}
	msgs := st.messages()
	if msgs[2].Role != "tool" || msgs[2].Content != "error: boom" {
		t.Fatalf("tool message: %+v", msgs[2])
	}
	second := fake.Requests()[1].Messages
	if second[2].Content != "error: boom" {
		t.Fatalf("model did not get the tool error: %+v", second)
	}
}

func TestTurnStepLimit(t *testing.T) {
	st := newFakeStore()
	steps := make([]llm.FakeStep, 15)
	for i := range steps {
		steps[i] = llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "echo", Arguments: `{}`}}}
	}
	r, _ := testRunner(st, steps...)
	emit, events := collectEvents()

	res, err := r.Turn(context.Background(), TurnRequest{
		ConversationID: "c1", Character: testChar, Input: "loop",
	}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLimited {
		t.Fatalf("status: %s", res.Status)
	}
	if got := len(st.toolCalls()); got != 10 {
		t.Fatalf("tool calls = %d, want 10", got)
	}
	last := (*events)[len(*events)-1]
	if last.Type != "text" || last.Delta != stopNote {
		t.Fatalf("last event: %+v", last)
	}
}

func TestTurnCancel(t *testing.T) {
	st := newFakeStore()
	started := make(chan struct{}, 1)
	r, _ := testRunner(st, llm.FakeStep{Block: true, Started: started})
	emit, _ := collectEvents()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Turn(ctx, TurnRequest{ConversationID: "c1", Character: testChar, Input: "wait"}, emit)
		done <- err
	}()

	<-started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	runs := st.runList()
	if len(runs) != 1 || runs[0].Status != StatusCancelled || runs[0].EndedAt == nil {
		t.Fatalf("runs: %+v", runs)
	}
	if len(st.messages()) != 1 { // the user message is kept
		t.Fatalf("messages: %+v", st.messages())
	}
}

func TestConcurrentTurnsSameThreadSerialized(t *testing.T) {
	st := newFakeStore()
	r, fake := testRunner(st,
		llm.FakeStep{Text: "one", Delay: 30 * time.Millisecond},
		llm.FakeStep{Text: "two", Delay: 30 * time.Millisecond},
	)
	emit, _ := collectEvents()

	var wg sync.WaitGroup
	for _, input := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Turn(context.Background(), TurnRequest{
				ConversationID: "c1", Character: testChar, Input: input,
			}, emit); err != nil {
				t.Errorf("turn %s: %v", input, err)
			}
		}()
	}
	wg.Wait()

	if got := fake.MaxActive(); got != 1 {
		t.Fatalf("overlapping model calls = %d, want 1", got)
	}
	if runs := st.runList(); len(runs) != 2 || runs[0].Status != StatusOK || runs[1].Status != StatusOK {
		t.Fatalf("runs: %+v", runs)
	}
	if msgs := st.messages(); len(msgs) != 4 {
		t.Fatalf("messages: %+v", msgs)
	}

	// The second turn must see the first turn's messages.
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	lens := []int{len(reqs[0].Messages), len(reqs[1].Messages)}
	if !(lens[0] == 1 && lens[1] == 3) {
		t.Fatalf("history lengths = %v, want [1 3]", lens)
	}
}

// TestTurnPersistsToPostgres runs the real store, so the trace and tool_call
// rows are verified against the schema (skipped when Postgres is down).
func TestTurnPersistsToPostgres(t *testing.T) {
	st := storetest.New(t)
	r, _ := testRunner(st,
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "call_pg", Name: "echo", Arguments: `{"q":"x"}`}}},
		llm.FakeStep{Text: "stored"},
	)
	emit, _ := collectEvents()

	res, err := r.Turn(context.Background(), TurnRequest{
		ConversationID: "pg-conv", Character: testChar, Input: "hello",
	}, emit)
	if err != nil {
		t.Fatal(err)
	}

	msgs, err := st.Messages(context.Background(), "pg-conv", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages: %+v", msgs)
	}

	runs, err := st.Runs(context.Background(), "pg-conv")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != StatusOK || runs[0].EndedAt == nil {
		t.Fatalf("runs: %+v", runs)
	}
	calls, err := st.ToolCalls(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Name != "echo" || calls[0].Status != "ok" {
		t.Fatalf("tool calls: %+v", calls)
	}
}

func TestTurnRecordsUndoAndToolCallID(t *testing.T) {
	st := newFakeStore()
	reg := tools.New()
	var seen int64
	reg.Add(&tools.Tool{
		Name: "write", Description: "a write tool", Parameters: map[string]any{"type": "object"},
		Run: func(_ context.Context, env tools.Env, _ string) (tools.Result, error) {
			seen = env.ToolCallID // the row must exist before the tool runs
			return tools.Result{Text: "wrote it", Undo: []tools.UndoOp{{Op: "delete_task", TaskID: 1}}}, nil
		},
	})
	fake := llm.NewFake(
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "write", Arguments: "{}"}}},
		llm.FakeStep{Text: "done"},
	)
	r := New(Config{Store: st, Tools: reg, Models: map[string]llm.Model{
		string(llm.OpenCodeProvider): fake, string(llm.ClaudeProvider): fake,
	}, Location: testLoc, Now: func() time.Time { return testNow }})

	char := Character{ID: "assistant", Provider: "opencode", Model: "fake", Tools: []string{"write"}}
	emit, events := collectEvents()
	if _, err := r.Turn(context.Background(), TurnRequest{ConversationID: "c", Character: char, Input: "do it"}, emit); err != nil {
		t.Fatal(err)
	}

	calls := st.toolCalls()
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	if seen != calls[0].ID || calls[0].ID == 0 {
		t.Fatalf("tool did not see its call id: env=%d row=%d", seen, calls[0].ID)
	}
	if string(calls[0].Undo) != `[{"op":"delete_task","task_id":1}]` {
		t.Fatalf("stored undo: %s", calls[0].Undo)
	}
	msgs := st.messages()
	if !strings.Contains(msgs[2].Content, "(undo: tool_call_id=1)") {
		t.Fatalf("tool result: %q", msgs[2].Content)
	}
	if (*events)[0].ID != calls[0].ID {
		t.Fatalf("tool event has no call id: %+v", (*events)[0])
	}
}

func TestOnlyTheAssistantGetsDailyRecords(t *testing.T) {
	st := storetest.New(t)
	r, fake := testRunner(st, llm.FakeStep{Text: "persona reply"}, llm.FakeStep{Text: "assistant reply"})
	ctx := context.Background()

	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "persona", Character: testChar, Input: "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	assistantChar := testChar
	assistantChar.ID, assistantChar.Daily = "assistant", true
	if _, err := r.Turn(ctx, TurnRequest{ConversationID: "daily", Character: assistantChar, Input: "hi"}, nil); err != nil {
		t.Fatal(err)
	}

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: %d", len(reqs))
	}
	if strings.Contains(reqs[0].System, "<today>") {
		t.Fatalf("persona got the user's day:\n%s", reqs[0].System)
	}
	if !strings.Contains(reqs[1].System, "<today>") || !strings.Contains(reqs[1].System, "The current day runs 04:00") {
		t.Fatalf("assistant context missing today section:\n%s", reqs[1].System)
	}
}

// D3 applies to personas too: they now have both web tools and their own
// memory tools, so a web read in a turn locks remember/forget for that turn.
// recall is a read and stays available.
func TestPersonaRefusesMemoryWritesAfterWebRead(t *testing.T) {
	st := newFakeStore()
	ran := map[string]int{}
	reg := tools.New()
	for _, name := range []string{"web_search", "remember", "forget", "recall"} {
		name := name
		reg.Add(&tools.Tool{
			Name: name, Description: name, Parameters: map[string]any{"type": "object"},
			Run: func(context.Context, tools.Env, string) (tools.Result, error) {
				ran[name]++
				return tools.Result{Text: "ok"}, nil
			},
		})
	}
	fake := llm.NewFake(
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "a", Name: "remember", Arguments: `{}`}}},
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "b", Name: "web_search", Arguments: `{}`}}},
		llm.FakeStep{ToolCalls: []llm.ToolCall{
			{ID: "c", Name: "remember", Arguments: `{}`},
			{ID: "d", Name: "forget", Arguments: `{}`},
			{ID: "e", Name: "recall", Arguments: `{}`},
		}},
		llm.FakeStep{Text: "done"},
	)
	r := New(Config{
		Store: st, Tools: reg,
		Models:   map[string]llm.Model{string(llm.OpenCodeProvider): fake},
		Memory:   func(string) string { return "" },
		Now:      func() time.Time { return testNow },
		Location: testLoc, Window: 40, MaxSteps: 10,
	})
	// A persona, not the daily assistant: no day thread, no day records.
	char := testChar
	if char.Daily {
		t.Fatal("test character should be a persona")
	}
	char.Context = "memory"
	char.Tools = []string{"web_search", "remember", "forget", "recall"}
	emit, _ := collectEvents()
	if _, err := r.Turn(context.Background(), TurnRequest{ConversationID: "c1", Character: char, Input: "go"}, emit); err != nil {
		t.Fatal(err)
	}
	if ran["remember"] != 1 || ran["forget"] != 0 || ran["web_search"] != 1 || ran["recall"] != 1 {
		t.Fatalf("ran: %v", ran)
	}
	calls := st.toolCalls()
	if len(calls) != 5 {
		t.Fatalf("tool calls: %+v", calls)
	}
	if calls[2].Status != "error" || !strings.Contains(calls[2].Result, "refused") ||
		calls[3].Status != "error" || !strings.Contains(calls[3].Result, "refused") {
		t.Fatalf("memory writes after a web read were allowed: %+v", calls)
	}
	if calls[4].Name != "recall" || calls[4].Status != "ok" {
		t.Fatalf("recall should still work after a web read: %+v", calls[4])
	}
}

func TestTurnStoresSpeaker(t *testing.T) {
	st := newFakeStore()
	r, fake := testRunner(st, llm.FakeStep{Text: "They mention a thesis."})
	res, err := r.Turn(context.Background(), TurnRequest{
		ConversationID: "c1", Character: testChar, Input: "what's up with the user?",
		Speaker: "ria", Names: map[string]string{"me": "the user", "ria": "Ria"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "They mention a thesis." {
		t.Fatalf("result text: %q", res.Text)
	}
	msgs := st.messages()
	if len(msgs) != 2 || msgs[0].Speaker != "ria" {
		t.Fatalf("stored input: %+v", msgs)
	}
	if strings.HasPrefix(msgs[0].Content, "[") {
		t.Fatalf("stored content must stay raw: %q", msgs[0].Content)
	}
	reqs := fake.Requests()
	last := reqs[0].Messages[len(reqs[0].Messages)-1]
	if last.Content != "[Ria]: what's up with the user?" {
		t.Fatalf("live input label: %q", last.Content)
	}
}

func TestTurnTextIsTerminalAnswer(t *testing.T) {
	cases := []struct {
		name  string
		steps []llm.FakeStep
		want  string
	}{
		{"preamble before a tool is not the reply", []llm.FakeStep{
			{Text: "Let me look.", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "echo", Arguments: `{}`}}},
			{Text: ""},
		}, ""},
		{"the closing step is the reply", []llm.FakeStep{
			{Text: "Let me look.", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "echo", Arguments: `{}`}}},
			{Text: "Final answer."},
		}, "Final answer."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			r, _ := testRunner(st, tc.steps...)
			res, err := r.Turn(context.Background(), TurnRequest{
				ConversationID: "c1", Character: testChar, Input: "hi",
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != tc.want {
				t.Fatalf("result text = %q, want %q", res.Text, tc.want)
			}
		})
	}
}
