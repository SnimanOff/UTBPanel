-- The canary of a bot: a second instance with a new version for some users.
CREATE TABLE canaries (
    bot_id     INTEGER PRIMARY KEY,
    version    TEXT    NOT NULL,
    share      INTEGER NOT NULL DEFAULT 0,  -- % of users by hash(user_id)
    users      TEXT    NOT NULL DEFAULT '[]', -- user ids always on the canary
    auto_after INTEGER NOT NULL DEFAULT 0,  -- minutes until auto promote, 0 = off
    started_at TEXT    NOT NULL,
    actor      TEXT    NOT NULL DEFAULT ''
);
