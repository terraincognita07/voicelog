-- Archive of previous transcriptions. Populated only by the
-- retranscribe MCP tool (issue #14): when a note is re-run through
-- whisper, the old raw_text is stashed here before the notes row is
-- updated. Lets Claude compare old vs new wording, and gives the user
-- a manual recovery path if the re-transcription is somehow worse.
--
-- Not indexed by FTS5 on purpose — search_notes hits the current
-- transcription only. Surfacing old wording via search would inflate
-- result counts with stale text.

CREATE TABLE IF NOT EXISTS notes_history (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  note_id     INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
  raw_text    TEXT    NOT NULL,
  replaced_at INTEGER NOT NULL,
  model       TEXT
);

CREATE INDEX IF NOT EXISTS idx_notes_history_note ON notes_history(note_id);
