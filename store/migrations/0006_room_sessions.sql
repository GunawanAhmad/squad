-- A room's current session. "New session" bumps it and gives every participant
-- an empty copy; session 0 keeps the original conversation ids.
ALTER TABLE rooms ADD COLUMN session int NOT NULL DEFAULT 0;
