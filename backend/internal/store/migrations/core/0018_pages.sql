-- Static pages: one HTML file (with its inline CSS and JS) served by the
-- edge at https://<sub>.<DOMAIN>:<webhook port>/ (see core/pages.go).
CREATE TABLE pages (
    sub        TEXT PRIMARY KEY,
    html       BLOB NOT NULL,
    updated_at TEXT NOT NULL
);
