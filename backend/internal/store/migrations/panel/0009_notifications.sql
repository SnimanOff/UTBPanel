-- Notifications under the bell. Repeats with the same key within the
-- repeat window are glued into one row (count); a recovery closes them.
CREATE TABLE notifications (
    id       INTEGER PRIMARY KEY,
    ts       INTEGER NOT NULL,             -- first, unix seconds
    last_ts  INTEGER NOT NULL,
    bot_id   INTEGER NOT NULL DEFAULT 0,   -- 0 = the server
    kind     TEXT    NOT NULL,
    severity TEXT    NOT NULL,             -- error | warn | info | ok
    key      TEXT    NOT NULL,
    text     TEXT    NOT NULL,
    href     TEXT    NOT NULL DEFAULT '',
    count    INTEGER NOT NULL DEFAULT 1,
    read     INTEGER NOT NULL DEFAULT 0,
    resolved INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notifications_key ON notifications (key, last_ts);
CREATE INDEX notifications_bot ON notifications (bot_id, read, resolved);
