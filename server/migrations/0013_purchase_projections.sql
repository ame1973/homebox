-- Retain posted allocations and their exact original values. Legacy rows have
-- no invented creation instant or audit association; future producers supply both.
ALTER TABLE purchase_lines ADD COLUMN created_at TEXT
 CHECK(created_at IS NULL OR length(created_at) BETWEEN 20 AND 35);
ALTER TABLE purchase_lines ADD COLUMN mutation_event_id TEXT REFERENCES mutation_events(id) DEFERRABLE INITIALLY DEFERRED
 CHECK((created_at IS NULL)=(mutation_event_id IS NULL));
CREATE INDEX idx_purchase_lines_purchase_id ON purchase_lines(purchase_id,id);
CREATE INDEX idx_purchase_lines_item_purchase ON purchase_lines(item_id,purchase_id);
CREATE INDEX idx_purchase_lines_mutation ON purchase_lines(mutation_event_id,id);

CREATE TRIGGER purchase_lines_no_update BEFORE UPDATE ON purchase_lines
 BEGIN SELECT RAISE(ABORT,'purchase allocations are immutable'); END;
CREATE TRIGGER purchase_lines_no_delete BEFORE DELETE ON purchase_lines
 BEGIN SELECT RAISE(ABORT,'purchase allocations are immutable'); END;
CREATE TRIGGER purchases_no_delete BEFORE DELETE ON purchases
 BEGIN SELECT RAISE(ABORT,'purchase headers are retained history'); END;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'purchase',id,1 FROM purchases WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'purchase',p.id,r.revision,
 json_object('id',p.id,'merchant_name',p.merchant_name,'purchased_at',p.purchased_at,'currency',p.currency,'receipt_note',p.receipt_note),
 strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM purchases p JOIN projection_revisions r ON r.entity_type='purchase' AND r.entity_id=p.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'purchase_line',id,1 FROM purchase_lines WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'purchase_line',l.id,r.revision,
 json_object('id',l.id,'purchase_id',l.purchase_id,'item_id',l.item_id,'item_unit_id',l.item_unit_id,
 'quantity_base',l.quantity_base,'paid_minor',l.paid_minor,'note',l.note),
 strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM purchase_lines l JOIN projection_revisions r ON r.entity_type='purchase_line' AND r.entity_id=l.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

-- updated_at above is materialization time, not a guessed financial event date.
-- Go validates all retained source/owner/counter/UTF-8/bounds inside this checksum
-- transaction. Existing current/frozen bytes and counter-only identities remain.
-- Do not emit old allocations as new events, infer totals/receipt/asset bases,
-- copy legacy FTS/OCR caches or enable the complete frozen sync protocol here.
