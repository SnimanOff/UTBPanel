CREATE TABLE sdk_metrics (
    bot_id INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('counter', 'value')),
    counter_value INTEGER NOT NULL DEFAULT 0,
    gauge_value REAL NOT NULL DEFAULT 0,
    selected INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bot_id, name)
);
CREATE TABLE sdk_metric_sources (
    bot_id INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    instance TEXT NOT NULL,
    source TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (bot_id, instance, source)
);
