-- Copy of core events; the id is the core's id, so re-fetching is harmless.
CREATE TABLE core_events (
    id       INTEGER PRIMARY KEY,
    ts       TEXT    NOT NULL,
    type     TEXT    NOT NULL,
    bot_id   INTEGER NOT NULL DEFAULT 0,
    severity TEXT    NOT NULL DEFAULT 'info',
    data     TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX core_events_ts ON core_events (ts);
CREATE INDEX core_events_bot ON core_events (bot_id, ts);
