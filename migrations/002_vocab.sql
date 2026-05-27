CREATE TABLE IF NOT EXISTS vocabulary (
  term_lower TEXT PRIMARY KEY,
  term       TEXT NOT NULL,
  added_at   INTEGER NOT NULL
);
