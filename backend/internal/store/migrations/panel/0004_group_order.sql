-- Bots without a row here are in "No group".
CREATE INDEX bot_meta_group ON bot_meta (group_id, position);
