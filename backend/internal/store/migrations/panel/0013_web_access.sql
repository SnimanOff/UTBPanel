-- Access over passkeys on the web listener: a person (the owner's phone or
-- someone with some bots), their passkeys, invitations and sessions.
CREATE TABLE web_access (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    role       TEXT    NOT NULL,             -- view | manage | full
    all_bots   INTEGER NOT NULL DEFAULT 0,
    bots       TEXT    NOT NULL DEFAULT '[]', -- JSON array of bot ids
    created_at INTEGER NOT NULL,
    created_by TEXT    NOT NULL DEFAULT ''
);

-- One-time links, 24 hours. Only the hash of the token is kept.
CREATE TABLE web_invites (
    token_hash BLOB    PRIMARY KEY,
    access_id  INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);

-- Passkeys. approved = 0 until the owner compares the code.
CREATE TABLE web_credentials (
    id         BLOB    PRIMARY KEY,
    access_id  INTEGER NOT NULL,
    public_key BLOB    NOT NULL, -- SPKI DER
    alg        INTEGER NOT NULL,
    sign_count INTEGER NOT NULL DEFAULT 0,
    code       TEXT    NOT NULL,
    approved   INTEGER NOT NULL DEFAULT 0,
    poll_hash  BLOB,             -- the waiting page asks with it
    device     TEXT    NOT NULL DEFAULT '',
    ip         TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    last_used  INTEGER
);
CREATE INDEX web_credentials_access ON web_credentials (access_id);

CREATE TABLE web_sessions (
    token_hash    BLOB    PRIMARY KEY,
    credential_id BLOB    NOT NULL,
    access_id     INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL
);
CREATE INDEX web_sessions_credential ON web_sessions (credential_id);
