-- Threshold rules: per bot, or for every bot (bot_id = 0).
CREATE TABLE alert_rules (
    id      INTEGER PRIMARY KEY,
    bot_id  INTEGER NOT NULL DEFAULT 0,
    metric  TEXT    NOT NULL,  -- p95 | err_rate | cpu | rss | updates | silence | revenue_drop
    op      TEXT    NOT NULL DEFAULT '>',
    value   REAL    NOT NULL,
    minutes INTEGER NOT NULL DEFAULT 5,
    enabled INTEGER NOT NULL DEFAULT 1
);
-- Every firing and recovery.
CREATE TABLE alerts_log (
    id      INTEGER PRIMARY KEY,
    rule_id INTEGER NOT NULL,
    bot_id  INTEGER NOT NULL,
    ts      INTEGER NOT NULL,
    state   TEXT    NOT NULL,  -- fired | ok
    value   REAL    NOT NULL,
    text    TEXT    NOT NULL
);
CREATE INDEX alerts_log_rule ON alerts_log (rule_id, bot_id, id);
