-- Addresses each bot connects to; a new one raises a notification until
-- the owner marks it as known.
CREATE TABLE known_hosts (
    bot_id     INTEGER NOT NULL,
    host       TEXT    NOT NULL,
    first_seen TEXT    NOT NULL,
    known      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bot_id, host)
);
