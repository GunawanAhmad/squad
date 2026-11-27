-- Rooms: several characters and the user in one conversation. A room is
-- stored as one conversation per member plus one for the user ('me'), so every
-- existing per-character query, isolation rule and forget path keeps working.
CREATE TABLE rooms (
  id text PRIMARY KEY,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
  members text[] NOT NULL CHECK (cardinality(members) >= 2),
  created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE conversations ADD COLUMN room_id text REFERENCES rooms(id);
ALTER TABLE messages ADD COLUMN speaker text; -- NULL: the conversation's owner, or the user in a 1:1 chat
ALTER TABLE facts ADD COLUMN about text NOT NULL DEFAULT 'me', ADD COLUMN told_by text;
