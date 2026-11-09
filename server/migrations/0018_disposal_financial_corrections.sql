-- Retain the original disposal unchanged. This journal alone supplies subsequent
-- financial decisions; its subject revision is independent of entry revision one.
CREATE TABLE asset_disposal_corrections (
 mutation_event_id TEXT PRIMARY KEY NOT NULL
  REFERENCES mutation_events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 revision INTEGER NOT NULL CHECK(revision=1),
 item_unit_id TEXT NOT NULL REFERENCES asset_disposals(item_unit_id) ON DELETE RESTRICT,
 previous_correction_id TEXT UNIQUE
  REFERENCES asset_disposal_corrections(mutation_event_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
  CHECK(previous_correction_id IS NULL OR previous_correction_id<>mutation_event_id),
 subject_revision INTEGER NOT NULL CHECK(subject_revision BETWEEN 2 AND 9007199254740991),
 action TEXT NOT NULL CHECK(action IN ('replace','void')),
 -- SQLite length(TEXT) stops at NUL. Go also checks complete UTF-8/code points.
 reason TEXT NOT NULL CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 40000),
 created_at TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 35),
 context_json TEXT NOT NULL CHECK(json_valid(context_json) AND json_type(context_json)='object'),
 UNIQUE(item_unit_id,subject_revision)
) STRICT;
CREATE UNIQUE INDEX idx_disposal_corrections_root
 ON asset_disposal_corrections(item_unit_id) WHERE previous_correction_id IS NULL;
CREATE INDEX idx_disposal_corrections_owner
 ON asset_disposal_corrections(item_unit_id,mutation_event_id);
CREATE TRIGGER asset_disposal_corrections_no_update BEFORE UPDATE ON asset_disposal_corrections
 BEGIN SELECT RAISE(ABORT,'disposal financial corrections are immutable'); END;
CREATE TRIGGER asset_disposal_corrections_no_delete BEFORE DELETE ON asset_disposal_corrections
 BEGIN SELECT RAISE(ABORT,'disposal financial corrections are immutable'); END;

-- No invented legacy decisions, initial revisions, provenance or dates. Preserve
-- every original source, counter, current/frozen byte and certified dataset epoch.
-- Financial current upserts/tombstones retain the existing disposal replica kind.
