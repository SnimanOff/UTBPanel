CREATE TABLE sdk_flags (
    bot_id INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    description TEXT NOT NULL,
    default_json TEXT NOT NULL,
    override_json TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    main_active INTEGER NOT NULL DEFAULT 0,
    canary_active INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(bot_id,name)
);
CREATE TABLE sdk_flag_changes (
    id INTEGER PRIMARY KEY,
    bot_id INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    ts INTEGER NOT NULL
);
CREATE INDEX sdk_flag_changes_bot ON sdk_flag_changes(bot_id,ts);
