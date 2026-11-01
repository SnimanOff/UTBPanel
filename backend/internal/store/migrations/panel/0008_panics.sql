-- Panic groups: one row per kind and place in the bot's code.
CREATE TABLE panic_groups (
    id           INTEGER PRIMARY KEY,
    bot_id       INTEGER NOT NULL,
    signature    TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    frame        TEXT    NOT NULL DEFAULT '',
    count        INTEGER NOT NULL DEFAULT 0,
    first_at     INTEGER NOT NULL,             -- unix seconds
    last_at      INTEGER NOT NULL,
    versions     TEXT    NOT NULL DEFAULT '[]',
    last_version TEXT    NOT NULL DEFAULT '',
    last_stack   TEXT    NOT NULL DEFAULT '',
    UNIQUE (bot_id, signature)
);
-- Every occurrence, for the 24 h badge and charts (kept 30 days).
CREATE TABLE panic_hits (
    group_id INTEGER NOT NULL,
    bot_id   INTEGER NOT NULL,
    ts       INTEGER NOT NULL
);
CREATE INDEX panic_hits_bot ON panic_hits (bot_id, ts);
