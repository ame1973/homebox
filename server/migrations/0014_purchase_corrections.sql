-- Financial header corrections preserve exact original and resulting source.
-- Old headers receive no invented correction, timestamp or audit association.
CREATE TABLE purchase_header_corrections (
 mutation_event_id TEXT PRIMARY KEY REFERENCES mutation_events(id) DEFERRABLE INITIALLY DEFERRED,
 purchase_id TEXT NOT NULL REFERENCES purchases(id) ON DELETE RESTRICT,
 context_json TEXT NOT NULL CHECK(json_valid(context_json))
) STRICT;
CREATE INDEX idx_purchase_header_corrections_owner ON purchase_header_corrections(purchase_id,mutation_event_id);
CREATE TRIGGER purchase_header_corrections_no_update BEFORE UPDATE ON purchase_header_corrections
 BEGIN SELECT RAISE(ABORT,'purchase correction history is immutable'); END;
CREATE TRIGGER purchase_header_corrections_no_delete BEFORE DELETE ON purchase_header_corrections
 BEGIN SELECT RAISE(ABORT,'purchase correction history is immutable'); END;
