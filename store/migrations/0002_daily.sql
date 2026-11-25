-- Phase 1: the daily loop. Anything you would ask "how many / how long / which /
-- what's left" about is a row here. tool_call_id is the write that created the
-- row: the Today panel uses it to offer the same undo the chat has.

CREATE TABLE projects (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX projects_name ON projects (lower(name));

CREATE TABLE tasks (                         -- "what I want to do"
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  title text NOT NULL, notes text,
  project_id bigint REFERENCES projects(id),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','dropped')),
  planned_on date,                           -- day you intend to do it
  due_on date,
  times_moved int NOT NULL DEFAULT 0,        -- planned_on pushed forward; for "what keeps slipping" when asked
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  source_message_id bigint REFERENCES messages(id),
  tool_call_id bigint REFERENCES tool_calls(id)   -- the write that created it (undo)
);
CREATE INDEX tasks_open ON tasks (planned_on) WHERE status = 'open';

CREATE TABLE activities (                    -- "what I did"
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  day date NOT NULL,
  description text NOT NULL,
  minutes int CHECK (minutes BETWEEN 1 AND 1440),  -- NULL when you didn't say; never guessed
  project_id bigint REFERENCES projects(id), task_id bigint REFERENCES tasks(id),
  source_message_id bigint REFERENCES messages(id),
  tool_call_id bigint REFERENCES tool_calls(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX activities_day ON activities (day);

CREATE TABLE reminders (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  text text NOT NULL,
  remind_at timestamptz NOT NULL,            -- next firing
  repeat text CHECK (repeat IN ('daily','weekdays')),  -- NULL = once
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','fired','cancelled')),
  fired_at timestamptz,
  source_message_id bigint REFERENCES messages(id),
  tool_call_id bigint REFERENCES tool_calls(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reminders_due ON reminders (remind_at) WHERE status = 'pending';

CREATE TABLE checkins (                     -- also records which days are closed
  day date NOT NULL,
  kind text NOT NULL,                        -- morning | evening | close (day-close job, Phase 2)
  character_id text NOT NULL,                -- the close job closes one (day, character) at a time
  status text NOT NULL CHECK (status IN ('pending','sent','failed','skipped')),  -- 'sent' = done, for close
  attempts int NOT NULL DEFAULT 0,
  message_id bigint REFERENCES messages(id), run_id bigint REFERENCES runs(id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (day, kind, character_id)      -- idempotency: two tabs polling can't double-send
);

CREATE TABLE settings (key text PRIMARY KEY, value text NOT NULL);
  -- tz (default from TZ env, else Asia/Makassar), day_starts_at '04:00', morning_at, evening_at, jobs_model
