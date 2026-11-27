-- Characters made in the app. characters.json keeps the shipped ones; a row
-- here holds the same JSON shape and is merged in at startup.
CREATE TABLE characters (
  id text PRIMARY KEY,
  spec jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
