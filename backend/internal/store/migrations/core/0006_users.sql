-- Properties of a Telegram user, shared by all bots.
CREATE TABLE tg_users (
    user_id       INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL DEFAULT '',
    language_code TEXT    NOT NULL DEFAULT '',
    is_premium    INTEGER NOT NULL DEFAULT 0,
    updated_at    TEXT    NOT NULL
);
CREATE INDEX tg_users_username ON tg_users (username);

-- A user of one bot. Message texts are never stored.
CREATE TABLE bot_users (
    bot_id        INTEGER NOT NULL,
    user_id       INTEGER NOT NULL,
    first_seen    TEXT    NOT NULL,
    last_seen     TEXT    NOT NULL,
    source        TEXT    NOT NULL DEFAULT '',  -- parameter of the first /start
    started       INTEGER NOT NULL DEFAULT 0,   -- a /start was seen
    updates_total INTEGER NOT NULL DEFAULT 0,
    active_days   BLOB    NOT NULL DEFAULT x'00000000000000000000000000000000',
    active_anchor INTEGER NOT NULL DEFAULT 0,   -- day (since 1970, UTC) of bit 0
    blocked_at    TEXT,
    unblocked_at  TEXT,
    banned        INTEGER NOT NULL DEFAULT 0,
    ban_reason    TEXT    NOT NULL DEFAULT '',
    ban_until     TEXT,
    banned_at     TEXT,
    banned_by     TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (bot_id, user_id)
);
CREATE INDEX bot_users_last_seen ON bot_users (bot_id, last_seen);
CREATE INDEX bot_users_first_seen ON bot_users (bot_id, first_seen);
CREATE INDEX bot_users_source ON bot_users (bot_id, source);

CREATE TABLE bot_allow (
    bot_id  INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    note    TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (bot_id, user_id)
);

CREATE TABLE payments (
    id         INTEGER PRIMARY KEY,
    bot_id     INTEGER NOT NULL,
    ts         TEXT    NOT NULL,
    user_id    INTEGER NOT NULL,
    currency   TEXT    NOT NULL,
    amount     INTEGER NOT NULL,
    payload    TEXT    NOT NULL DEFAULT '',
    charge_id  TEXT    NOT NULL DEFAULT '',
    refunded_at TEXT
);
CREATE INDEX payments_bot_ts ON payments (bot_id, ts);
CREATE UNIQUE INDEX payments_charge ON payments (bot_id, charge_id) WHERE charge_id != '';
