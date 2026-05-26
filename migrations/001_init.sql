CREATE TABLE IF NOT EXISTS notes (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at   INTEGER NOT NULL,
  raw_text     TEXT    NOT NULL,
  duration_sec INTEGER,
  audio_path   TEXT,
  status       TEXT    NOT NULL DEFAULT 'pending'
    CHECK(status IN ('pending','analyzed','discarded'))
);

CREATE INDEX IF NOT EXISTS idx_notes_created ON notes(created_at);
CREATE INDEX IF NOT EXISTS idx_notes_status  ON notes(status);

CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
  raw_text,
  content='notes',
  content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS notes_ai AFTER INSERT ON notes BEGIN
  INSERT INTO notes_fts(rowid, raw_text) VALUES (new.id, new.raw_text);
END;

CREATE TRIGGER IF NOT EXISTS notes_ad AFTER DELETE ON notes BEGIN
  INSERT INTO notes_fts(notes_fts, rowid, raw_text)
    VALUES ('delete', old.id, old.raw_text);
END;

CREATE TRIGGER IF NOT EXISTS notes_au AFTER UPDATE ON notes BEGIN
  INSERT INTO notes_fts(notes_fts, rowid, raw_text)
    VALUES ('delete', old.id, old.raw_text);
  INSERT INTO notes_fts(rowid, raw_text) VALUES (new.id, new.raw_text);
END;
