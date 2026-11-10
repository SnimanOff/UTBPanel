-- An access may end: after expires_at (unix seconds) its sessions stop
-- working, its links are refused and sign-in is denied. NULL is for good.
ALTER TABLE web_access ADD COLUMN expires_at INTEGER;
