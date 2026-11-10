-- SQLite REPLACE implicitly deletes conflicting rows. With recursive triggers
-- disabled, that delete bypasses the existing immutable-history triggers.
-- Reject every declared identity conflict before SQLite can perform the delete.
-- The serving connection also enables recursive triggers to guard explicit
-- hidden-rowid conflicts, including SQLite's unspecified-rowid sentinel -1.

CREATE TRIGGER mutation_events_no_replace BEFORE INSERT ON mutation_events
 WHEN EXISTS(SELECT 1 FROM mutation_events WHERE id=NEW.id OR idempotency_key=NEW.idempotency_key OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'mutation events cannot be replaced'); END;
CREATE TRIGGER location_events_no_replace BEFORE INSERT ON location_events
 WHEN EXISTS(SELECT 1 FROM location_events WHERE id=NEW.id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'location events cannot be replaced'); END;
CREATE TRIGGER inventory_events_no_replace BEFORE INSERT ON inventory_events
 WHEN EXISTS(SELECT 1 FROM inventory_events WHERE id=NEW.id OR idempotency_key=NEW.idempotency_key OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'inventory events cannot be replaced'); END;
CREATE TRIGGER purchases_no_replace BEFORE INSERT ON purchases
 WHEN EXISTS(SELECT 1 FROM purchases WHERE id=NEW.id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'purchase headers cannot be replaced'); END;
CREATE TRIGGER purchase_lines_no_replace BEFORE INSERT ON purchase_lines
 WHEN EXISTS(SELECT 1 FROM purchase_lines WHERE id=NEW.id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'purchase allocations cannot be replaced'); END;
CREATE TRIGGER purchase_header_corrections_no_replace BEFORE INSERT ON purchase_header_corrections
 WHEN EXISTS(SELECT 1 FROM purchase_header_corrections WHERE mutation_event_id=NEW.mutation_event_id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'purchase corrections cannot be replaced'); END;
CREATE TRIGGER purchase_line_corrections_no_replace BEFORE INSERT ON purchase_line_corrections
 WHEN EXISTS(SELECT 1 FROM purchase_line_corrections WHERE mutation_event_id=NEW.mutation_event_id OR line_id=NEW.line_id OR replacement_line_id=NEW.replacement_line_id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'allocation corrections cannot be replaced'); END;
CREATE TRIGGER asset_profiles_no_replace BEFORE INSERT ON asset_profiles
 WHEN EXISTS(SELECT 1 FROM asset_profiles WHERE item_unit_id=NEW.item_unit_id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'asset profiles cannot be replaced'); END;
CREATE TRIGGER asset_usage_events_no_replace BEFORE INSERT ON asset_usage_events
 WHEN EXISTS(SELECT 1 FROM asset_usage_events WHERE id=NEW.id OR idempotency_key=NEW.idempotency_key OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'asset usage events cannot be replaced'); END;
CREATE TRIGGER asset_cost_events_no_replace BEFORE INSERT ON asset_cost_events
 WHEN EXISTS(SELECT 1 FROM asset_cost_events WHERE id=NEW.id OR idempotency_key=NEW.idempotency_key OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'asset cost events cannot be replaced'); END;
CREATE TRIGGER asset_disposals_no_replace BEFORE INSERT ON asset_disposals
 WHEN EXISTS(SELECT 1 FROM asset_disposals WHERE item_unit_id=NEW.item_unit_id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'asset disposal records cannot be replaced'); END;
CREATE TRIGGER asset_profile_changes_no_replace BEFORE INSERT ON asset_profile_changes
 WHEN EXISTS(SELECT 1 FROM asset_profile_changes WHERE mutation_event_id=NEW.mutation_event_id OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'asset profile history cannot be replaced'); END;
CREATE TRIGGER asset_disposal_corrections_no_replace BEFORE INSERT ON asset_disposal_corrections
 WHEN EXISTS(SELECT 1 FROM asset_disposal_corrections
  WHERE mutation_event_id=NEW.mutation_event_id OR previous_correction_id=NEW.previous_correction_id
   OR (item_unit_id=NEW.item_unit_id AND subject_revision=NEW.subject_revision)
   OR (item_unit_id=NEW.item_unit_id AND previous_correction_id IS NULL AND NEW.previous_correction_id IS NULL)
   OR (NEW.rowid<>-1 AND rowid=NEW.rowid))
 BEGIN SELECT RAISE(ABORT,'disposal financial corrections cannot be replaced'); END;

-- Sequence identity is the INTEGER PRIMARY KEY, so this also covers rowid.
-- An invalid retained sequence -1 makes automatic insertion fail closed because
-- SQLite exposes -1 in BEFORE INSERT for an unspecified automatic sequence.
-- Explicit retention remains permitted; committed payload bytes cannot be replaced.
CREATE TRIGGER sync_changes_no_replace BEFORE INSERT ON sync_changes
 WHEN EXISTS(SELECT 1 FROM sync_changes WHERE seq=NEW.seq)
 BEGIN SELECT RAISE(ABORT,'sync payloads are frozen'); END;
