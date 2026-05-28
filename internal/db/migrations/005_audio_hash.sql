-- audio_hash: SHA-256 (hex) of the raw audio bytes downloaded from
-- Telegram. Used to detect accidental double-taps that send the same
-- voice message twice in a few seconds. Nullable for back-compat —
-- pre-existing notes never had their bytes hashed.
--
-- The (audio_hash, created_at) composite index makes the dedup lookup
-- "any note with this hash within the last N seconds" cheap.

ALTER TABLE notes ADD COLUMN audio_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_notes_audio_hash ON notes(audio_hash, created_at);
