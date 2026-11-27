package assistant

import (
	"context"
	"strings"
	"testing"

	"squad/llm"
)

func gateInput(fake *llm.Fake, candidates ...Retrieved) GateInput {
	return GateInput{
		Model: fake, ModelID: "fake",
		Character:  Character{ID: "socrates", Name: "Socrates", Discretion: 8},
		Listeners:  []string{"Ria"},
		Question:   "what is the user struggling with?",
		Candidates: candidates,
	}
}

func TestGateSharesOnlyApproved(t *testing.T) {
	fake := llm.NewFake(llm.FakeStep{Text: `{"share":["f1"]}`})
	res, _ := Gate(context.Background(), gateInput(fake,
		Retrieved{Kind: "fact", ID: 1, Text: "behind on thesis"},
		Retrieved{Kind: "fact", ID: 2, Text: "supervisor annoyed"},
	))
	if len(res.Shared) != 1 || res.Shared[0].ID != 1 {
		t.Fatalf("shared: %+v", res.Shared)
	}
	if len(res.Kept) != 1 || res.Kept[0].ID != 2 {
		t.Fatalf("kept: %+v", res.Kept)
	}
	if res.Err != "" {
		t.Fatalf("err: %s", res.Err)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests: %d", len(reqs))
	}
	prompt := reqs[0].System + "\n" + reqs[0].Messages[0].Content
	if !strings.Contains(prompt, discretionBand(8)) {
		t.Fatalf("prompt lacks the discretion band:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Ria") {
		t.Fatalf("prompt lacks the listener:\n%s", prompt)
	}
}

func TestGateRejectsUnknownIDs(t *testing.T) {
	fake := llm.NewFake(llm.FakeStep{Text: `{"share":["f999","m1"]}`})
	res, _ := Gate(context.Background(), gateInput(fake,
		Retrieved{Kind: "fact", ID: 1, Text: "behind on thesis"},
	))
	if len(res.Shared) != 0 {
		t.Fatalf("forged ids shared: %+v", res.Shared)
	}
	if len(res.Kept) != 1 || res.Kept[0].ID != 1 {
		t.Fatalf("kept: %+v", res.Kept)
	}
}

func TestGateFailsClosed(t *testing.T) {
	fake := llm.NewFake(llm.FakeStep{Text: "sure!"})
	res, _ := Gate(context.Background(), gateInput(fake,
		Retrieved{Kind: "fact", ID: 1, Text: "behind on thesis"},
		Retrieved{Kind: "fact", ID: 2, Text: "supervisor annoyed"},
	))
	if res.Shared != nil {
		t.Fatalf("shared on failure: %+v", res.Shared)
	}
	if len(res.Kept) != 2 {
		t.Fatalf("kept on failure: %+v", res.Kept)
	}
	if res.Err == "" {
		t.Fatal("want an error on the result")
	}
}

func TestGateNoCandidatesNoCall(t *testing.T) {
	fake := llm.NewFake(llm.FakeStep{Text: `{"share":[]}`})
	res, _ := Gate(context.Background(), gateInput(fake))
	if len(fake.Requests()) != 0 {
		t.Fatalf("model called with no candidates")
	}
	if len(res.Shared) != 0 || len(res.Kept) != 0 || res.Err != "" {
		t.Fatalf("result: %+v", res)
	}
}

func TestDiscretionBand(t *testing.T) {
	bandA := "share what you know about the user freely, unless it's obviously private."
	bandB := "share everyday things. Keep health, money, relationships and anything said in confidence."
	bandC := "don't repeat what the user told you. Point the asker to the user."
	cases := []struct {
		d    int
		want string
	}{
		{0, bandA}, {3, bandA},
		{4, bandB}, {7, bandB},
		{8, bandC}, {10, bandC},
	}
	for _, tc := range cases {
		if got := discretionBand(tc.d); got != tc.want {
			t.Fatalf("discretionBand(%d) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
