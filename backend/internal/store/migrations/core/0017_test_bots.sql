-- A test bot is a separate bot attached to one bot for test runs of its new
-- versions (see core/trial.go): test_for is the id of that bot, 0 for an
-- ordinary bot. Test bots are hidden from the bot list and are deleted with
-- their bot.
ALTER TABLE bots ADD COLUMN test_for INTEGER NOT NULL DEFAULT 0;
CREATE INDEX bots_test_for ON bots (test_for) WHERE test_for != 0;

-- Recall a broadcast automatically this many hours after it started
-- (0 = never). Telegram lets a bot delete its messages for 48 hours.
ALTER TABLE broadcasts ADD COLUMN delete_after INTEGER NOT NULL DEFAULT 0;
