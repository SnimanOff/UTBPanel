-- GitHub Releases as a source of a bot's versions. The token and the
-- webhook secret are secrets of kind "panel" (never given to the bot).
CREATE TABLE github_sources (
    bot_id      INTEGER PRIMARY KEY,
    repo        TEXT    NOT NULL,             -- owner/repo
    asset_mask  TEXT    NOT NULL DEFAULT '*linux_amd64*',
    auto_deploy INTEGER NOT NULL DEFAULT 0,
    etag        TEXT    NOT NULL DEFAULT '',
    last_tag    TEXT    NOT NULL DEFAULT '',  -- newest imported release
    checked_at  TEXT,
    last_error  TEXT    NOT NULL DEFAULT ''
);
