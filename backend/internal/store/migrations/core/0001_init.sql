-- Bots run by the core. State is what the bot returns to after a core restart.
CREATE TABLE bots (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE,
    username        TEXT    NOT NULL DEFAULT '',
    tg_id           INTEGER NOT NULL DEFAULT 0,
    state           TEXT    NOT NULL DEFAULT 'stopped', -- running | stopped | paused | crashed | error
    port            INTEGER NOT NULL UNIQUE,
    current_version TEXT    NOT NULL DEFAULT '',
    settings        TEXT    NOT NULL DEFAULT '{}',      -- JSON
    created_at      TEXT    NOT NULL
);

-- Write-only secrets: AES-256-GCM with the master key, AAD = bot id + name.
CREATE TABLE secrets (
    bot_id     INTEGER NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    kind       TEXT    NOT NULL DEFAULT 'value', -- value | file
    ciphertext BLOB    NOT NULL,
    nonce      BLOB    NOT NULL,
    updated_at TEXT    NOT NULL,
    PRIMARY KEY (bot_id, name)
);

CREATE TABLE webhook_state (
    bot_id       INTEGER PRIMARY KEY REFERENCES bots (id) ON DELETE CASCADE,
    url          TEXT    NOT NULL DEFAULT '',
    set_at       TEXT,
    checked_at   TEXT,
    last_error   TEXT    NOT NULL DEFAULT '',
    hijacked_url TEXT    NOT NULL DEFAULT '',
    fixed_count  INTEGER NOT NULL DEFAULT 0
);

-- Core settings.
CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
