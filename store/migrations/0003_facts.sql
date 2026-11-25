-- Phase 2: facts. Facts are superseded, never overwritten: the old row gets
-- valid_to and superseded_by, so "where did I live last year?" still works.
-- Only the user can hard-delete, and that still keeps the row (status
-- 'forgotten'); forgotten messages are hidden from search, context, fact
-- extraction and embeddings.

CREATE TABLE facts (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  character_id text NOT NULL,                -- the agent that was told; facts never cross agents
  text text NOT NULL CHECK (length(text) <= 300),
  category text NOT NULL DEFAULT 'general',  -- preference | person | circumstance | goal | work | health | general
  core boolean NOT NULL DEFAULT false,       -- always in context
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','forgotten')),
  source_kind text NOT NULL CHECK (source_kind IN ('user','tool','extraction')),
  source_message_id bigint REFERENCES messages(id),
  superseded_by bigint REFERENCES facts(id),
  valid_from date NOT NULL,
  valid_to date,
  last_used_at timestamptz,
  use_count int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  search tsvector GENERATED ALWAYS AS (to_tsvector('simple', text)) STORED
);
CREATE INDEX facts_search ON facts USING gin (search);
CREATE INDEX facts_active ON facts (character_id, status, core);
