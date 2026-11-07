-- Posted allocation source remains immutable. A correction retains its actual
-- original and optional new replacement under one real deferred audit event.
-- Existing source/current/frozen bytes, counters and dataset epoch stay exact.
CREATE TABLE purchase_line_corrections (
 mutation_event_id TEXT PRIMARY KEY NOT NULL REFERENCES mutation_events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 revision INTEGER NOT NULL CHECK(revision=1),
 purchase_id TEXT NOT NULL REFERENCES purchases(id) ON DELETE RESTRICT,
 line_id TEXT NOT NULL UNIQUE REFERENCES purchase_lines(id) ON DELETE RESTRICT,
 replacement_line_id TEXT UNIQUE REFERENCES purchase_lines(id) ON DELETE RESTRICT,
 action TEXT NOT NULL CHECK(action IN ('replace','void')),
 -- Bound native UTF-8 bytes here; Go validates the complete code-point bound,
 -- including text after embedded NUL (SQLite length(TEXT) stops there).
 reason TEXT NOT NULL CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 40000),
 created_at TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 35),
 context_json TEXT NOT NULL CHECK(json_valid(context_json) AND json_type(context_json)='object'),
 CHECK((action='void' AND replacement_line_id IS NULL) OR
       (action='replace' AND replacement_line_id IS NOT NULL AND replacement_line_id<>line_id))
) STRICT;
CREATE INDEX idx_purchase_line_corrections_owner ON purchase_line_corrections(purchase_id,mutation_event_id);
CREATE TRIGGER purchase_line_corrections_no_update BEFORE UPDATE ON purchase_line_corrections
 BEGIN SELECT RAISE(ABORT,'allocation corrections are immutable'); END;
CREATE TRIGGER purchase_line_corrections_no_delete BEFORE DELETE ON purchase_line_corrections
 BEGIN SELECT RAISE(ABORT,'allocation corrections are immutable'); END;

-- Never invent an old correction, receipt, before-image, event time or source
-- projection here. New producers must atomically publish the original Line's
-- tombstone, optional replacement and all actual financial/search dependencies.
