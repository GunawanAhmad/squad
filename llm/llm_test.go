package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"squad/llm"
)

func TestOpenCodeStream(t *testing.T) {
	var body map[string]any
	var session string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session = r.Header.Get("x-opencode-session")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"fake","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"fake","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"fake","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":""}}]},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"fake","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":\"go\"}"}}]},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"fake","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"fake","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m := llm.NewOpenCode("test-key", srv.URL)
	var streamed strings.Builder
	resp, err := m.Stream(context.Background(), llm.Request{
		Model:   "fake-model",
		System:  "sys prompt",
		Session: "s1",
		Messages: []llm.Message{
			{Role: "user", Content: "search go"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_0", Name: "web_search", Arguments: `{"query":"x"}`}}},
			{Role: "tool", ToolCallID: "call_0", Content: "old result"},
		},
		Tools: []llm.ToolSpec{{Name: "web_search", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}, func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}

	if streamed.String() != "Hello" || resp.Text != "Hello" {
		t.Fatalf("text = %q, streamed %q", resp.Text, streamed.String())
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" ||
		resp.ToolCalls[0].Name != "web_search" || resp.ToolCalls[0].Arguments != `{"query":"go"}` {
		t.Fatalf("tool calls: %+v", resp.ToolCalls)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
	if session != "s1" {
		t.Fatalf("session header = %q", session)
	}

	if body["model"] != "fake-model" || body["stream"] != true {
		t.Fatalf("request body: %v", body)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages in request: %v", msgs)
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "sys prompt" {
		t.Fatalf("system message: %v", first)
	}
	asst, _ := msgs[2].(map[string]any)
	if asst["role"] != "assistant" {
		t.Fatalf("assistant message: %v", asst)
	}
	calls, _ := asst["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("replayed tool calls: %v", asst)
	}
	tool, _ := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_0" || tool["content"] != "old result" {
		t.Fatalf("tool message: %v", tool)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools in request: %v", body["tools"])
	}
}

func TestFakeOrdersSteps(t *testing.T) {
	f := llm.NewFake(
		llm.FakeStep{Text: "first"},
		llm.FakeStep{ToolCalls: []llm.ToolCall{{ID: "1", Name: "t", Arguments: `{}`}}},
	)
	var texts []string
	for i := range 2 {
		if i == 0 {
			resp, err := f.Stream(context.Background(), llm.Request{Model: "m"}, func(s string) { texts = append(texts, s) })
			if err != nil || resp.Text != "first" {
				t.Fatalf("step 0: %+v %v", resp, err)
			}
		} else {
			resp, err := f.Stream(context.Background(), llm.Request{Model: "m"}, func(string) {})
			if err != nil || len(resp.ToolCalls) != 1 {
				t.Fatalf("step 1: %+v %v", resp, err)
			}
		}
	}
	if f.Remaining() != 0 || len(f.Requests()) != 2 {
		t.Fatalf("remaining=%d requests=%d", f.Remaining(), len(f.Requests()))
	}
	if _, err := f.Stream(context.Background(), llm.Request{Model: "m"}, func(string) {}); err == nil {
		t.Fatal("expected an error when the script is empty")
	}
}
