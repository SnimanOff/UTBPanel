-- Secrets an AI asked the owner for. The value never passes through the AI:
-- the owner enters it in the panel, the AI sees only the state.
CREATE TABLE secret_requests (
    id          TEXT    PRIMARY KEY,
    bot_id      INTEGER NOT NULL DEFAULT 0,  -- 0: BOT_TOKEN of a new bot
    name        TEXT    NOT NULL,
    hint        TEXT    NOT NULL DEFAULT '',
    bot_name    TEXT    NOT NULL DEFAULT '', -- the new bot
    group_id    INTEGER NOT NULL DEFAULT 0,
    caller      TEXT    NOT NULL,            -- the MCP device, JSON
    created_at  INTEGER NOT NULL,            -- unix seconds
    expires_at  INTEGER NOT NULL,
    state       TEXT    NOT NULL DEFAULT 'waiting', -- waiting | set | rejected | expired
    created_bot INTEGER NOT NULL DEFAULT 0,
    decided_by  TEXT    NOT NULL DEFAULT '',
    decided_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX secret_requests_bot ON secret_requests (bot_id, name, state);
