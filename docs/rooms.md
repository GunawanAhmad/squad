# Rooms

Status: built 2026-11-27. Plan:
`docs/superpowers/plans/2026-11-27-rooms.md`.

## The idea

Talking to a character 1:1 is a private conversation, like talking to a person
directly. A character can't reach out to another character from it.

Characters only talk to each other in a **room**: a conversation you create,
with members you pick. Everyone in a room hears everything said there. Nobody
outside it hears anything. You are a member of every room, a participant
without memory.

Later, an office (a task board plus an activity feed) builds on rooms. It is not
part of this work.

## Rules

1. **1:1 chats don't change.** A room always has at least two characters.
2. **Members are fixed** when the room is created (v1).
   **New session** (the + button) starts the room over with an empty
   transcript. Old sessions stay stored, so each character still remembers
   them through retrieval and the share gate, the way it remembers an old 1:1
   chat. To make them truly forget, use the Memory screen.
3. **Who speaks next:**
   - When you send a message, every member you @mention answers, in the
     order you mention them (by id or name, case-insensitive). With no
     mention, the last character who spoke in the room answers. In a new
     room, the first member answers.
   - Members that a character's reply @mentions join the end of the queue
     (a self-mention doesn't count, and nobody is queued twice). When the
     queue is empty, the chain ends.
   - At most **4 character turns** per message you send, or one for each
     member you mentioned if that's more.
4. **What each character hears:** your messages and the other characters'
   final replies. It does not see another character's tool calls or web reads.
5. **Discretion.** Each character has `discretion` 0–10 in `characters.json`
   (default 5). In a room, before each turn, a separate **share gate** decides
   which of the character's private knowledge (its core facts and its
   pre-retrieval hits) it is willing to bring up in front of these members.
   Only approved items enter the prompt.
   - **0–3:** share what you know about the user freely, unless it's
     obviously private.
   - **4–7:** share everyday things. Keep health, money, relationships and
     anything said in confidence.
   - **8–10:** don't repeat what the user told you. Point the asker to the
     user.

   If the gate fails, nothing is shared (fail closed). Every decision is
   recorded in the run trace. `profile.md` is not gated: you wrote it for
   everyone.
6. **No `recall` in rooms.** It would bypass the gate. `remember` and `forget`
   still work, and so does the web-read rule (D3).
7. **Memory stays in the store.** Each character keeps its own copy of the
   room, so every existing per-character query and "forget" keeps working
   unchanged. Your copy (owner `me`) is the transcript the UI shows.
8. **Facts know who they're about and where they came from.** `about` is `me`
   or a character id. `told_by` is the character that said it, or empty
   when it came from you. The server takes `told_by` from the source
   message's speaker and never trusts the model for it.

## Built notes

Two privacy choices go slightly beyond the rules above, because both would
otherwise bypass the gate: a room turn does not load the Assistant's day
records, and it removes the tools that read them (`get_overview`,
`get_summary`, `query_activities`, `list_tasks`, `list_reminders`) along with
`recall`. Room turns also carry a short `<room>` note that tells the character
how @mention hands the turn on. On a room day the close job refuses a fact with
no `source_message_id`, so `told_by` is never guessed. The `remember` tool takes
an optional `about`, limited to the user and the people present, so a member can
record a fact about another member.

## Verified

`make test` covers the room store, turn-taking, fan-out, the gate, the API and
the close job over a room day. Two checks used real models:

- **Manual UI check:** created a room with Socrates and Ria, sent
  `@Ria ask Socrates what he thinks of me`, and saw Ria hand the turn to
  Socrates with `@Socrates` and Socrates hand it back with `@Ria` (three
  bubbles, then the chain ended). Reloading the room showed the same
  transcript.
- **Share-gate eval** (`make eval sets=gate models=… runs=2`): the fixed six
  candidates, low vs high discretion. Pass rate:

  | Model | Pass |
  |---|---|
  | `deepseek-v4.1-flash` | 2/2 |
  | `claude-haiku-5-5` | 2/2 |
  | `deepseek-v4-pro` | 2/2 |
