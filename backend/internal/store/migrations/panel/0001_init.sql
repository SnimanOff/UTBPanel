-- Access keys (gate.exe certificates).
CREATE TABLE keys (
    serial     TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,          -- owner | child | enrollment
    device     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    expires_at TEXT,
    last_seen  TEXT,
    last_ip    TEXT NOT NULL DEFAULT '',
    revoked_at TEXT,
    used_at    TEXT                    -- enrollment certificates are single-use
);
CREATE INDEX keys_name ON keys (name);

-- Devices waiting for the owner to compare the code.
CREATE TABLE pending_devices (
    id                 INTEGER PRIMARY KEY,
    enrollment_serial  TEXT NOT NULL REFERENCES keys (serial),
    name               TEXT NOT NULL,
    kind               TEXT NOT NULL,  -- owner | child
    ip                 TEXT NOT NULL,
    code               TEXT NOT NULL,
    csr                BLOB NOT NULL,
    state              TEXT NOT NULL DEFAULT 'pending', -- pending | approved | rejected
    cert               BLOB,
    created_at         TEXT NOT NULL
);

CREATE TABLE audit (
    id      INTEGER PRIMARY KEY,
    ts      TEXT    NOT NULL,
    key     TEXT    NOT NULL,
    ip      TEXT    NOT NULL DEFAULT '',
    source  TEXT    NOT NULL,           -- ui | mcp | cli | autorollback | schedule
    action  TEXT    NOT NULL,
    bot_id  INTEGER,
    object  TEXT    NOT NULL DEFAULT '',
    details TEXT    NOT NULL DEFAULT '{}',
    result  TEXT    NOT NULL DEFAULT 'ok'
);
CREATE INDEX audit_ts ON audit (ts);
CREATE INDEX audit_bot ON audit (bot_id, ts);

CREATE TABLE groups (
    id       INTEGER PRIMARY KEY,
    name     TEXT    NOT NULL UNIQUE,
    position INTEGER NOT NULL DEFAULT 0,
    mcp_mode TEXT    NOT NULL DEFAULT 'read' -- off | read | full
);

CREATE TABLE bot_meta (
    bot_id   INTEGER PRIMARY KEY,
    group_id INTEGER REFERENCES groups (id) ON DELETE SET NULL,
    position INTEGER NOT NULL DEFAULT 0
);

-- Panel settings: owner Telegram ID and others.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
