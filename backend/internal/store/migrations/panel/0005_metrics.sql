-- One row per bot per minute/hour/day; data holds every counter and
-- histogram (only non-zero values). bot_id 0 is the sum over all bots.
CREATE TABLE m_minute (bot_id INTEGER NOT NULL, ts INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY (bot_id, ts)) WITHOUT ROWID;
CREATE TABLE m_hour   (bot_id INTEGER NOT NULL, ts INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY (bot_id, ts)) WITHOUT ROWID;
CREATE TABLE m_day    (bot_id INTEGER NOT NULL, day INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY (bot_id, day)) WITHOUT ROWID;
-- Daily HyperLogLog sketches of unique users and chats.
CREATE TABLE m_uniq   (bot_id INTEGER NOT NULL, day INTEGER NOT NULL, kind TEXT NOT NULL, hll BLOB NOT NULL, PRIMARY KEY (bot_id, day, kind)) WITHOUT ROWID;
CREATE TABLE m_server (ts INTEGER PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE deploys (
    id      INTEGER PRIMARY KEY,
    bot_id  INTEGER NOT NULL,
    ts      INTEGER NOT NULL,
    version TEXT    NOT NULL,
    result  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX deploys_bot ON deploys (bot_id, ts);
