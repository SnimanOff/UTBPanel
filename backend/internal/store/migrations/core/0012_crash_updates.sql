-- Updates that were with the bot when it fell (or answered 5xx). Only with
-- the bot's crash_updates setting on; the last 20 per bot, 7 days. They
-- hold users' messages and never go into transfer archives.
CREATE TABLE crash_updates (
    id     INTEGER PRIMARY KEY,
    bot_id INTEGER NOT NULL,
    ts     TEXT    NOT NULL,
    status INTEGER NOT NULL DEFAULT 0,
    reason TEXT    NOT NULL,              -- crash | 5xx | timeout
    body   BLOB    NOT NULL
);
CREATE INDEX crash_updates_bot ON crash_updates (bot_id, id);
