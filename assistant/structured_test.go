package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"characters/llm"
)

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", `{"facts":[]}`, `{"facts":[]}`},
		{"fenced", "```json\n{\"facts\":[{\"op\":\"ADD\"}]}\n```", `{"facts":[{"op":"ADD"}]}`},
		{"prose", `Sure! Here you go: {"facts":[]} Hope that helps.`, `{"facts":[]}`},
		{"nested", `{"a":{"b":1}}`, `{"a":{"b":1}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ExtractJSONObject(c.in)
			if err != nil {
				t.Fatal(err)
			}
			var a, b any
			if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(c.want), &b) != nil {
				t.Fatal("test data is invalid")
			}
			if gotA, _ := json.Marshal(a); string(gotA) != string(mustJSON(t, b)) {
				t.Fatalf("got %s, want %s", gotA, c.want)
			}
		})
	}
	for _, bad := range []string{"no json here", "{\"unterminated\":", "[1,2,3]"} {
		if _, err := ExtractJSONObject(bad); err == nil {
			t.Fatalf("%q should not parse", bad)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCallStructured(t *testing.T) {
	fake := llm.NewFake(llm.FakeStep{
		Text:  "```json\n{\"facts\":[{\"op\":\"ADD\",\"text\":\"lives in Jakarta\"}]}\n```",
		Usage: llm.Usage{InputTokens: 10, OutputTokens: 5},
	})
	raw, usage, err := CallStructured(context.Background(), StructuredSpec{
		Model: fake, ModelID: "fake", Prompt: "extract facts", Schema: `{"facts":[...]}`, Session: "job",
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 10 || !strings.Contains(string(raw), "Jakarta") {
		t.Fatalf("raw=%s usage=%+v", raw, usage)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Messages[0].Content, "Reply with exactly one JSON object") ||
		!strings.Contains(reqs[0].Messages[0].Content, `{"facts":[...]}`) {
		t.Fatalf("prompt: %+v", reqs)
	}

	// A model error is returned with no partial result.
	bad := llm.NewFake(llm.FakeStep{Err: errors.New("gateway down")})
	if _, _, err := CallStructured(context.Background(), StructuredSpec{Model: bad, ModelID: "fake", Prompt: "x"}); err == nil {
		t.Fatal("model error swallowed")
	}
	// A reply without JSON is an error, not an empty object.
	silent := llm.NewFake(llm.FakeStep{Text: "I could not do that."})
	if _, _, err := CallStructured(context.Background(), StructuredSpec{Model: silent, ModelID: "fake", Prompt: "x"}); err == nil {
		t.Fatal("non-JSON reply accepted")
	}
}

func TestRejects(t *testing.T) {
	var r Rejects
	if r.JSON() != nil {
		t.Fatal("empty rejects should be nil")
	}
	r.Add("fact %d: text too long", 3)
	r.Add("supersede %d: not active", 4)
	raw := r.JSON()
	if !strings.Contains(string(raw), "text too long") || !strings.Contains(string(raw), "not active") {
		t.Fatalf("rejects: %s", raw)
	}
}
