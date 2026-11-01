-- Maintenance of a bot: a manual pause (no schedule, no end) or scheduled
-- maintenance. While active, users get the text and only payments and
-- service updates are kept for the bot.
CREATE TABLE maintenance (
    bot_id     INTEGER PRIMARY KEY,
    kind       TEXT    NOT NULL,            -- pause | scheduled
    state      TEXT    NOT NULL,            -- planned | active
    starts_at  TEXT,
    ends_at    TEXT,                        -- estimated end, shown to users
    text       TEXT    NOT NULL DEFAULT '',
    version    TEXT    NOT NULL DEFAULT '', -- switch to this version at start
    auto_end   INTEGER NOT NULL DEFAULT 0,  -- end when the new version is ready
    warn_hours INTEGER NOT NULL DEFAULT 0,
    warned     INTEGER NOT NULL DEFAULT 0,
    overdue    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL
);
