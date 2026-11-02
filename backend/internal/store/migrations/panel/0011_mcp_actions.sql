-- Actions of the confirm level an AI asked for through MCP. The owner
-- confirms or rejects them; the AI follows with wait_action.
CREATE TABLE mcp_actions (
    id         TEXT    PRIMARY KEY,
    op         TEXT    NOT NULL,              -- operationId
    bot_id     INTEGER NOT NULL DEFAULT 0,
    args       TEXT    NOT NULL,              -- tool arguments, JSON
    summary    TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '{}', -- diff, version: what will be done
    caller     TEXT    NOT NULL,              -- the MCP device, JSON
    created_at INTEGER NOT NULL,              -- unix seconds
    expires_at INTEGER NOT NULL,
    state      TEXT    NOT NULL DEFAULT 'pending', -- pending | running | done | failed | rejected | expired
    result     TEXT    NOT NULL DEFAULT '',   -- the API answer, JSON
    decided_by TEXT    NOT NULL DEFAULT '',
    decided_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX mcp_actions_state ON mcp_actions (state, expires_at);
