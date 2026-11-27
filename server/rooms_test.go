package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"squad/llm"
)

func createRoom(t *testing.T, h http.Handler, body string) *http.Response {
	t.Helper()
	w := doJSON(t, h, "POST", "/api/rooms", body)
	return w.Result()
}

func TestCreateRoomValidation(t *testing.T) {
	s, _, _ := testServer()
	h := s.handler()

	cases := []struct {
		name string
		body string
		want int
	}{
		{"valid", `{"name":"office","members":["ria","claude-char"]}`, http.StatusCreated},
		{"unknown character", `{"name":"office","members":["ria","bob"]}`, http.StatusBadRequest},
		{"one member", `{"name":"office","members":["ria"]}`, http.StatusBadRequest},
		{"duplicate members", `{"name":"office","members":["ria","ria"]}`, http.StatusBadRequest},
		{"me as a member", `{"name":"office","members":["ria","me"]}`, http.StatusBadRequest},
		{"empty name", `{"name":"  ","members":["ria","claude-char"]}`, http.StatusBadRequest},
		{"long name", `{"name":"` + strings.Repeat("x", 61) + `","members":["ria","claude-char"]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(t, h, "POST", "/api/rooms", tc.body)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}
			if tc.want == http.StatusCreated {
				var room struct {
					ID      string   `json:"id"`
					Name    string   `json:"name"`
					Members []string `json:"members"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
					t.Fatal(err)
				}
				if len(room.ID) != 8 || room.Name != "office" || len(room.Members) != 2 {
					t.Fatalf("room: %+v", room)
				}
			}
		})
	}

	// The list includes the room that was created.
	w := doJSON(t, h, "GET", "/api/rooms", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("rooms: %+v, %v", list, err)
	}
}

func TestRoomMessagesStream(t *testing.T) {
	s, _, _ := testServer(
		// Ria's reply: no mention, so the chain ends after one character.
		llm.FakeStep{Text: "Hello there."},
	)
	h := s.handler()

	w := doJSON(t, h, "POST", "/api/rooms", `{"name":"office","members":["ria","claude-char"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create room: %d %s", w.Code, w.Body)
	}
	var room struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}

	res := doJSON(t, h, "POST", "/api/rooms/"+room.ID+"/messages", `{"content":"@Ria hi"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("post: %d %s", res.Code, res.Body)
	}
	events := decodeEvents(t, res.Body.Bytes())
	var sawSpeaker bool
	for _, e := range events {
		if e["type"] == "speaker" && e["name"] == "ria" {
			sawSpeaker = true
		}
	}
	if !sawSpeaker {
		t.Fatalf("no speaker event: %v", events)
	}
	if len(events) == 0 || events[len(events)-1]["type"] != "done" {
		t.Fatalf("stream does not end with done: %v", events)
	}

	w = doJSON(t, h, "GET", "/api/rooms/"+room.ID+"/messages", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Speaker string `json:"speaker"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages: %+v", got.Messages)
	}
	if got.Messages[0].Content != "@Ria hi" || got.Messages[0].Speaker != "me" {
		t.Fatalf("user message: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != "assistant" || got.Messages[1].Content != "Hello there." || got.Messages[1].Speaker != "ria" {
		t.Fatalf("ria's reply: %+v", got.Messages[1])
	}

	// A new session empties the transcript; an unknown room is a 404.
	if w := doJSON(t, h, "POST", "/api/rooms/"+room.ID+"/sessions", ""); w.Code != http.StatusCreated {
		t.Fatalf("new session: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, h, "GET", "/api/rooms/"+room.ID+"/messages", "")
	got.Messages = nil
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Messages) != 0 {
		t.Fatalf("after new session: %+v, %v", got.Messages, err)
	}
	if w := doJSON(t, h, "POST", "/api/rooms/deadbeef/sessions", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown room session: %d", w.Code)
	}

	// An unknown room is a 404, in both directions.
	if w := doJSON(t, h, "GET", "/api/rooms/deadbeef/messages", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown room get: %d", w.Code)
	}
	if w := doJSON(t, h, "POST", "/api/rooms/deadbeef/messages", `{"content":"hi"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown room post: %d", w.Code)
	}
}
