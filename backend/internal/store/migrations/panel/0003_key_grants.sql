-- For an enrollment certificate: the kind of device key it may become.
ALTER TABLE keys ADD COLUMN grants TEXT NOT NULL DEFAULT '';
