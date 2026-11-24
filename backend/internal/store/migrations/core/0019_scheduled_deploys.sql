CREATE TABLE scheduled_deploys (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id         INTEGER NOT NULL UNIQUE REFERENCES bots (id) ON DELETE CASCADE,
    version        TEXT    NOT NULL,
    starts_at      TEXT    NOT NULL,
    config_version TEXT    NOT NULL,
    status         TEXT    NOT NULL DEFAULT 'scheduled',
    error          TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX scheduled_deploys_due ON scheduled_deploys (starts_at) WHERE status = 'scheduled';
