-- Per-note transcription quality signals. All three are nullable so
-- existing rows (created before this migration) continue to work; the
-- bot treats NULL as "unknown" rather than "perfect" or "bad".
--
-- confidence_overall: mean of segments[].avg_logprob (closer to 0 = more
--   confident; whisper returns negative numbers).
-- confidence_min: worst segment's avg_logprob — useful to flag a note
--   that was mostly fine but had one mumbled span.
-- suspect_hallucination: 1 if segments[0].no_speech_prob exceeded
--   HALLUCINATION_THRESHOLD at insert time. Whisper's known failure mode
--   is to invent plausible text on silence / music.
--
-- SQLite quirk: ADD COLUMN is forward-only. DROP COLUMN is expensive
-- (rebuild table). Rolling back this migration means just dropping the
-- new columns from any future migration — old rows keep the columns at
-- their default NULL/0.

ALTER TABLE notes ADD COLUMN confidence_overall REAL;
ALTER TABLE notes ADD COLUMN confidence_min REAL;
ALTER TABLE notes ADD COLUMN suspect_hallucination INTEGER NOT NULL DEFAULT 0;
