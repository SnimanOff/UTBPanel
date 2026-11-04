-- Message ids of a broadcast, for editing and recalling it. Telegram lets a
-- bot delete its messages for 48 hours, so rows live as long.
CREATE TABLE broadcast_messages (
    broadcast_id INTEGER NOT NULL,
    user_id      INTEGER NOT NULL,
    message_id   INTEGER NOT NULL,
    sent_at      INTEGER NOT NULL, -- unix seconds
    PRIMARY KEY (broadcast_id, user_id)
) WITHOUT ROWID;
CREATE INDEX broadcast_messages_sent ON broadcast_messages (sent_at);

-- Editing or recalling after sending: '' | editing | deleting | edited | deleted.
ALTER TABLE broadcasts ADD COLUMN revision TEXT NOT NULL DEFAULT '';
ALTER TABLE broadcasts ADD COLUMN rev_cursor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE broadcasts ADD COLUMN rev_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE broadcasts ADD COLUMN rev_failed INTEGER NOT NULL DEFAULT 0;
