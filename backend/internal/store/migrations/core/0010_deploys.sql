-- Every version switch of a bot: what was running, the database backup
-- made before it and PRAGMA schema_version at that moment (autorollback
-- restores the backup only if the new version changed the schema).
CREATE TABLE deploys (
    id            INTEGER PRIMARY KEY,
    bot_id        INTEGER NOT NULL,
    from_version  TEXT    NOT NULL DEFAULT '',
    to_version    TEXT    NOT NULL,
    backup        TEXT    NOT NULL DEFAULT '',
    schema_before INTEGER NOT NULL DEFAULT 0,
    actor         TEXT    NOT NULL DEFAULT '',  -- source:name
    started_at    TEXT    NOT NULL,
    finished_at   TEXT,
    result        TEXT    NOT NULL DEFAULT 'running', -- running | ok | failed | rolled_back
    error         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX deploys_bot ON deploys (bot_id, id);
