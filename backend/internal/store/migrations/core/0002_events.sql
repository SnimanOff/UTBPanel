-- Core events (bot_started, bot_crashed, limit_warning…), kept 7 days.
CREATE TABLE events (
    id       INTEGER PRIMARY KEY,
    ts       TEXT    NOT NULL,
    type     TEXT    NOT NULL,
    bot_id   INTEGER NOT NULL DEFAULT 0,
    severity TEXT    NOT NULL DEFAULT 'info', -- info | warn | error
    data     TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX events_ts ON events (ts);
