-- Broadcasts: the task and its progress. Recipients go in user_id order,
-- cursor is the last one handled, so a restarted core continues where it
-- stopped.
CREATE TABLE broadcasts (
    id            INTEGER PRIMARY KEY,
    bot_id        INTEGER NOT NULL,
    text          TEXT    NOT NULL,
    parse_mode    TEXT    NOT NULL DEFAULT 'HTML',
    photo         BLOB,                         -- uploaded once, then by file_id
    photo_name    TEXT    NOT NULL DEFAULT '',
    photo_file_id TEXT    NOT NULL DEFAULT '',
    buttons       TEXT    NOT NULL DEFAULT '[]', -- [{"text","url"}]
    audience      TEXT    NOT NULL DEFAULT 'all', -- all | active
    active_days   INTEGER NOT NULL DEFAULT 0,
    starts_at     TEXT    NOT NULL,
    state         TEXT    NOT NULL,             -- scheduled | running | paused | done | cancelled
    kind          TEXT    NOT NULL DEFAULT '',  -- '' or maintenance_warning
    created_by    TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    started_at    TEXT,
    finished_at   TEXT,
    cursor        INTEGER NOT NULL DEFAULT 0,
    total         INTEGER NOT NULL DEFAULT 0,
    sent          INTEGER NOT NULL DEFAULT 0,
    blocked       INTEGER NOT NULL DEFAULT 0,
    failed        INTEGER NOT NULL DEFAULT 0,
    errors        TEXT    NOT NULL DEFAULT '{}' -- Telegram description → count
);
CREATE INDEX broadcasts_bot ON broadcasts (bot_id, id);
CREATE INDEX broadcasts_state ON broadcasts (state);
