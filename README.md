# squad

A small crew of AI characters you can talk to, each with its own personality,
model, tools and memory. You pick a character, chat with it, and it remembers
what you told it, but only what you told *it*. As with real people, telling one
of them something doesn't mean the others know.

It runs on your own machine: a Go server, a Vue frontend, and a Postgres
database that keeps every conversation.

## The squad

Characters are defined in `characters.json`. Three ship today:

| Character | What it does | Default model |
|---|---|---|
| **Socrates** | Answers questions with questions until you find out what you actually believe. | `deepseek-v4.1-flash` |
| **Ria** | Researcher: searches the web, reads the pages, cites every source. | Claude `opus` via the `claude` CLI |
| **Assistant** | Keeps your day: morning and evening check-ins, plans, what you actually did, reminders. Answers anything else too. | `claude-haiku-5-5` |

Socrates and Ria are conversation partners. The Assistant is the only one with
a daily routine (see [The Assistant's day](#the-assistants-day)).

## How it works

### A turn

When you send a message, the server:

1. Builds the character's prompt from its persona, plus its memory (see below).
2. Sends the conversation to the character's model and streams the reply back
   to the browser.
3. If the model calls a tool (`web_search`, `remember`, `plan_tasks`, …), it runs
   the tool, feeds back the result, and loops until the model answers in text.
4. Saves both messages to Postgres.

### Two routes to a model

- **OpenCode** (an OpenAI-compatible gateway, `OPENCODE_API_KEY`). This route
  supports the app's own tools, so it is the default and the only route the
  Assistant can use.
- **The `claude` CLI** (`"provider": "claude"`). The CLI uses its own
  WebSearch/WebFetch and can't call the app's tools. A character on this route
  still gets its own memory in the prompt, but it can't `remember` or `forget`
  in the middle of a chat. The nightly close job (below) still learns from those
  chats.

A character with a `models` list gets a model picker in the chat screen.

### Tools

| Tools | Who has them | What they do |
|---|---|---|
| `web_search`, `fetch_url` | everyone | Search through a local SearXNG and read pages. |
| `remember`, `forget`, `recall` | everyone | Write, delete and search the character's *own* facts about you. |
| `plan_tasks`, `update_task`, `complete_task`, `list_tasks` | Assistant | Today's plan. |
| `log_activity`, `query_activities` | Assistant | What you actually did. |
| `remind_me`, `list_reminders`, `cancel_reminder` | Assistant | Reminders that pop up in its chat. |
| `skip_checkin`, `undo`, `get_overview`, `get_summary` | Assistant | Skip a check-in, undo its last write, look back over days. |

The server refuses to start if a character lists an unknown tool, or if a
character other than the Assistant lists a daily-routine tool.

One safety rule applies to every character: once it has read a web page or
search result in a turn, it can't `remember` or `forget` until your next
message. A page can't plant a "fact" about you.

### Memory

Every character has its own memory, made of four parts:

- **`memory/profile.md`**: a file *you* write, about yourself. Every character
  with memory reads it. Nobody else writes to it.
- **Facts**: short notes about you ("works night shifts", "moving to Jakarta in
  March"). A character saves them with `remember`, and the nightly close job
  adds them too (see the next section). Facts marked *core* go into every
  prompt, up to about 2,000 characters. The rest are found by search. Each fact
  also records who it is *about* (`me` or a character id) and who said it
  (`told_by`, taken from the source message's speaker, never from the model).
- **Pre-retrieval**: before each turn, the server searches the character's own
  past messages and facts using your message, and puts a few quoted hits in front
  of the model (at most 4 messages and 3 facts).
- **Search**: keyword search by default. Set `EMBEDDINGS_URL` to add vector
  search (Ollama `bge-m3` in Compose); results from both are merged.

Isolation is enforced in the store, not in the prompt: every fact and message
query takes a character id, and there is no "all characters" query. The only
place that crosses characters is the **Memory screen**. There, you pick a
character to see, search, edit, or forget its facts, or to forget a whole day of
its chats.

Set `"context": "none"` on a character to turn its memory off entirely: no
profile, no facts, no retrieval, and no extraction from its chats.

### The nightly close

After each day ends (the day starts at 04:00 by default), a background job
"closes" it one character at a time. For each character that chatted that day,
it reads only that character's messages and current facts, and asks a model what
is worth keeping. New facts are added, outdated ones are superseded, and every
change points back to the message it came from. The server checks all of this in
Go: a character can only change its own facts, quoting its own messages. If one
character's close fails, it retries alone on a later pass, up to 3 times.

### Rooms

A room is a group conversation: you and at least two characters. Everyone in it
hears your messages and the other characters' final replies, never their tool
calls or web reads. Each member keeps its own copy of the room in the store, so
per-character search, forget and "what does this character know" keep working
unchanged; your copy (`me`) is the transcript the UI shows.

- **Who answers.** The member you @mention, by id or name, case-insensitive.
  With no mention, the last character who spoke in the room answers; in a new
  room, the first member. A reply that @mentions another member hands the turn
  to them; a reply with no mention (or only itself) ends the chain. At most 4
  character turns per message you send.
- **Discretion.** Every character has a `discretion` from 0 to 10 (default 5).
  Before each room turn a separate **share gate** decides which of that
  character's facts it is willing to bring up in front of these members.
  0–3 shares freely unless something is obviously private; 4–7 keeps health,
  money, relationships and confidences; 8–10 does not repeat what you told it
  and points the asker to you. Only approved facts enter the prompt, and if the
  gate fails nothing is shared. `profile.md` is not gated: you wrote it for
  everyone. Rooms do not load the Assistant's day records, and they take away
  the tools that would read them (`recall`, `get_overview`, `get_summary`,
  `query_activities`, `list_tasks`, `list_reminders`): both would bypass the
  gate. `remember` and `forget` still work.
- **API.** `POST /api/rooms` with `{name, members}`, `GET /api/rooms`,
  `GET /api/rooms/{id}/messages` (the transcript, each row with a `speaker`),
  and `POST /api/rooms/{id}/messages` with `{content}`, which streams NDJSON
  `speaker`/`text`/`tool`/`error` events and then `done`.

### The Assistant's day

The Assistant has one conversation per day instead of separate chats.

- **Check-ins:** at 08:00 it asks what you want to get done today. At 21:00 it
  asks how the day went and lists what it noted. Times and time zone are
  settings (`/api/assistant/settings`, default `TZ` is `Asia/Makassar`).
- **Records:** plans, activities and reminders are stored as records, not just
  chat text, so it can answer "what did I do last Tuesday?" from data.
- **No unasked advice:** it asks and records; it gives advice only when you ask.

Nothing runs on a timer. The open browser tab polls `/api/assistant/due` every
60 s; the server decides what is due (a check-in, a reminder, a day to close)
and records that decision, so a missed poll, a restart, or two open tabs can't
send a check-in twice. If no tab is open, nothing fires until one is.

## Run it

Requirements: Go, Node/npm, Docker with Compose, and an OpenCode API key. To use
Ria's default route, you also need the `claude` CLI, logged in.

```sh
cp .env.example .env   # fill in OPENCODE_API_KEY
make dev               # compose services + hot reload on 127.0.0.1:8080
```

Open http://127.0.0.1:8080. The Go server proxies every non-`/api` request to
Vite, so that URL is always the live UI: saved `.vue`/`.ts`/`.css` files
hot-update, and a change to Go code, `store/migrations`, `characters.json` or
`.env` rebuilds and restarts the server in place. A failed build leaves the
previous server running and prints the compiler error. Ctrl+C stops the app and
Vite; Postgres, SearXNG and Ollama stay up.

`make ui` runs Vite alone on :5173, `make web` builds the UI into `web/`, and
`make build` compiles `./squad`, which embeds that build and needs no Vite.

### Configuration (`.env`)

| Variable | Default | Purpose |
|---|---|---|
| `OPENCODE_API_KEY` | (required) | Key for the OpenCode gateway. |
| `DATABASE_URL` | local Compose Postgres | Where conversations and memory live. |
| `SEARXNG_URL` | `http://127.0.0.1:8888` | Search backend for `web_search`. |
| `OPENCODE_BASE_URL` | OpenCode's endpoint | Point at another OpenAI-compatible gateway. |
| `ADDR` | `127.0.0.1:8080` | Listen address. |
| `TZ` | `Asia/Makassar` | Time zone for days and check-ins. |
| `EMBEDDINGS_URL`, `EMBEDDINGS_MODEL` | unset, `bge-m3` | Turn on vector search. |

Every Compose service binds to `127.0.0.1` only. Published Docker ports bypass
the host firewall, so don't change them to `0.0.0.0`.

## Add a character

Add an entry to `characters.json`:

```jsonc
{
  "id": "chef",                  // stable: memory and chats are keyed by it
  "name": "Chef",
  "persona": "You are …",        // the system prompt
  "model": "deepseek-v4.1-flash",
  "models": ["deepseek-v4.1-flash", "claude/sonnet"],  // optional picker; must include the default
  "provider": "",                // "" for OpenCode, "claude" for the CLI
  "tools": ["web_search", "fetch_url", "remember", "forget", "recall"],
  "context": "memory",           // or "none"
  "discretion": 5,                // 0-10: how much it shares about you in rooms
  "ui": { "tagline": "…", "description": "…", "accentColor": "#e8734a",
          "selectLine": "…", "stats": [...], "voice": {...},
          "portrait": { "idle": "/characters/chef/portrait.png", ... } }
}
```

Put the portrait images in `frontend/public/characters/<id>/`. That folder is
gitignored, so the images stay on your machine. A running `make dev` picks up
the new entry when `characters.json` changes; reload the browser for a new
portrait.
Don't rename an `id` later: its chats and facts stay under the old one.

## Code map

| Path | What's there |
|---|---|
| `main.go`, `handlers*.go` | HTTP API, character loading and validation, memory wiring. |
| `assistant/` | Turn loop, prompt building, pre-retrieval, check-ins, nightly close, embedding worker. |
| `llm/` | Model adapters: OpenCode, `claude` CLI, embeddings, and a fake model for tests. |
| `tools/` | The tool registry and every tool. |
| `store/` | Postgres access and numbered migrations (`store/migrations/`). |
| `eval/` | Model evals for tool use, memory, grounding and the room share gate. These spend real API credit. |
| `frontend/` | Vue app: character home, rooms, chat with a character rail, and the Memory screen. It builds into `web/`, which the binary embeds. |
| `docs/` | `assistant-roadmap.md` (design decisions and what was verified), `memory-research.md`. |

## Tests and evals

`make test` runs `go vet` and `go test`, then builds and lints the frontend.
Tests use a fake model, so they cost nothing. Store tests create a throwaway
schema in the Compose Postgres; when Postgres isn't reachable they skip with a
message instead of failing. Don't run `go test ./...`: `frontend/node_modules`
contains stray Go packages, so the Makefile filters them out.

`make eval models=… runs=…` scores real models on tool use, memory recall,
grounding and the share gate (`sets=gate`),
and spends API credit. `go run ./eval -dry-run -models=…` prints the call
estimate first.

## Backups

Once per local day, on the first turn, the server writes
`backups/YYYY-MM-DD.dump` with `pg_dump -Fc` through `docker compose exec`, and
keeps the newest 14 files. An existing day's file is never overwritten; a failed
dump leaves no file and is retried on the next turn. `backups/` is not tracked
by git, so copy it off the machine yourself. `memory/profile.md` isn't in the
dump either; back it up too.

The Postgres user and database are still named `characters`, from before the
rename.

### Restore into a scratch database

```sh
# 1. Create an empty database.
docker compose exec -T postgres createdb -U characters characters_scratch

# 2. Restore the custom-format dump into it.
docker compose exec -T postgres pg_restore -U characters -d characters_scratch \
  --no-owner --no-privileges < backups/2026-12-01.dump

# 3. Start the app against it. Migrations only apply versions newer than the
#    restored schema_version. Then check a conversation.
DATABASE_URL='postgres://characters:characters@127.0.0.1:5432/characters_scratch?sslmode=disable' \
  ADDR=127.0.0.1:8099 ./squad
#    curl -s 'http://127.0.0.1:8099/api/conversations?characterId=assistant'

# 4. Clean up.
docker compose exec -T postgres dropdb -U characters characters_scratch
```

### Restore over the live database

Stop the app first, then:

```sh
docker compose exec -T postgres pg_restore -U characters -d characters \
  --clean --if-exists --no-owner --no-privileges < backups/2026-12-01.dump
```

`docker compose down -v` deletes the database volume. Use `make down` (plain
`down`) unless you mean to start empty.
