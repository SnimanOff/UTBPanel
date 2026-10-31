-- Every changing operation of the core, with who asked for it.
CREATE TABLE journal (
    id      INTEGER PRIMARY KEY,
    ts      TEXT    NOT NULL,
    source  TEXT    NOT NULL, -- ui | mcp | cli | autorollback | schedule | core
    actor   TEXT    NOT NULL DEFAULT '', -- key name for the panel, "ssh" for the CLI
    ip      TEXT    NOT NULL DEFAULT '',
    command TEXT    NOT NULL,
    bot_id  INTEGER NOT NULL DEFAULT 0,
    args    TEXT    NOT NULL DEFAULT '{}',
    result  TEXT    NOT NULL DEFAULT 'ok'
);
CREATE INDEX journal_ts ON journal (ts);
