-- Server-side preferences (single-user app). Defaults live in code: an
-- absent key means the default, so no seed rows are needed and new
-- settings need no migration.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
