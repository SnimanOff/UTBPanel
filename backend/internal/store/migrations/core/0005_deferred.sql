-- Payments and service updates that came during maintenance, pause or while
-- the bot was down. No expiry: a payment cannot be thrown away.
CREATE TABLE deferred (
    id         INTEGER PRIMARY KEY,
    bot_id     INTEGER NOT NULL,
    body       BLOB    NOT NULL,
    created_at TEXT    NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    last_error TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX deferred_bot ON deferred (bot_id, id);
