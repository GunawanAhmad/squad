# characters

A character chat app that is growing into a personal assistant: Vue frontend, Go
server, Postgres for every conversation.

## Run

```sh
cp .env.example .env   # fill in OPENCODE_API_KEY
make dev               # SearXNG + Postgres (compose), builds web/, serves 127.0.0.1:8080
```

`make test` runs `go vet` and `go test` on the Go packages, then builds and lints
the frontend. Store tests create a throwaway schema in the Compose Postgres; when
it is not reachable they skip with a message instead of failing.

Two routes to a model: the OpenCode (OpenAI-compatible) gateway, which supports
app tools, and the `claude` CLI, which uses its own WebSearch/WebFetch.

## Backups

Once per local day, on the first turn (Phase 1 will trigger the same hook from
the check-in engine's `Due()`), the server writes `backups/YYYY-MM-DD.dump`
with `pg_dump -Fc` through `docker compose exec` and keeps the newest 14 files.
An existing day's file is never overwritten; a failed dump leaves no file and is
retried on the next turn. `backups/` is not tracked by git: copy it off the
machine yourself.

### Restore into a scratch database (what was tested)

```sh
# 1. Create an empty database.
docker compose exec -T postgres createdb -U characters characters_scratch

# 2. Restore the custom-format dump into it.
docker compose exec -T postgres pg_restore -U characters -d characters_scratch \
  --no-owner --no-privileges < backups/2026-12-01.dump

# 3. Start the app against the scratch database (migrations see version 1 and
#    do nothing to a restored schema) and check a conversation.
DATABASE_URL='postgres://characters:characters@127.0.0.1:5432/characters_scratch?sslmode=disable' \
  ADDR=127.0.0.1:8099 ./characters
#    curl -s 'http://127.0.0.1:8099/api/conversations?characterId=general'

# 4. Clean up.
docker compose exec -T postgres dropdb -U characters characters_scratch
```

### Restore over the live database

Stop the app first, then:

```sh
docker compose exec -T postgres pg_restore -U characters -d characters \
  --clean --if-exists --no-owner --no-privileges < backups/2026-12-01.dump
```

`docker compose down -v` deletes the named volume, so use `down` (without `-v`)
rather than `down -v` unless you mean to start empty.
