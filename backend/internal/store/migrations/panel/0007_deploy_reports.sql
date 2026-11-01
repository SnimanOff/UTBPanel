-- An hour after a deploy: equal periods before and after it, side by side.
CREATE TABLE deploy_reports (
    deploy_id INTEGER PRIMARY KEY,          -- deploys.id (the chart mark)
    bot_id    INTEGER NOT NULL,
    ts        INTEGER NOT NULL,             -- the deploy, unix seconds
    version   TEXT    NOT NULL,
    before    TEXT    NOT NULL,             -- Summary as JSON
    after     TEXT    NOT NULL,
    worse     TEXT    NOT NULL DEFAULT '[]' -- what got noticeably worse
);
CREATE INDEX deploy_reports_bot ON deploy_reports (bot_id, ts);
