-- Phase 0: everything said, forever.

CREATE TABLE conversations (
  id text PRIMARY KEY,                       -- uuid, or 'daily:YYYY-MM-DD' for the assistant
  character_id text NOT NULL,
  day date,                                  -- set for daily threads
  summary text, summary_upto bigint,         -- rolling summary of messages past the window
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runs (                          -- trace: one per turn or job
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind text NOT NULL, conversation_id text REFERENCES conversations(id), model text NOT NULL,
  context jsonb,                             -- which sections/ids went into the prompt
  status text NOT NULL, error text, input_tokens int, output_tokens int,
  started_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz
);

CREATE TABLE messages (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  conversation_id text NOT NULL REFERENCES conversations(id),
  role text NOT NULL,                        -- user | assistant | tool | trigger
  content text NOT NULL,
  tool_calls jsonb, tool_call_id text,
  run_id bigint REFERENCES runs(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  forgotten_at timestamptz,                  -- set by "forget": excluded from search, context and fact extraction
  search tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED  -- 'simple': no stemming, safe for mixed Indonesian/English
);
CREATE INDEX messages_search ON messages USING gin (search);
CREATE INDEX messages_conv ON messages (conversation_id, id);

CREATE TABLE tool_calls (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  run_id bigint NOT NULL REFERENCES runs(id),
  name text NOT NULL, args jsonb NOT NULL, result text,
  undo jsonb,                                -- inverse operation, for write tools
  status text NOT NULL,                      -- ok | error | undone
  created_at timestamptz NOT NULL DEFAULT now()
);
