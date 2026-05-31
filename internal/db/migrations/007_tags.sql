-- v0.5.0 — note tags. A many-to-many overlay between notes and free-form
-- category labels.
--
-- Tags live on the analysis side: Claude (via the MCP tag tools) labels
-- notes with categories that are NOT in the words themselves (#идея, #todo,
-- #философия), and the bot surfaces them in the list views. A separate
-- table (rather than a column on notes) keeps the rollback a plain
-- DROP TABLE and avoids touching the FTS5 triggers.
--
-- ON DELETE CASCADE: a permanent note delete (v0.5.0) drops the note's tags
-- in the same statement — foreign_keys is ON via the Open() DSN.
CREATE TABLE IF NOT EXISTS note_tags (
  note_id INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
  tag     TEXT    NOT NULL,
  PRIMARY KEY (note_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_note_tags_tag ON note_tags(tag);
