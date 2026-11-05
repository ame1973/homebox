-- Independent item-owned catalog projections and stable owner/ID paging paths.
CREATE INDEX idx_barcodes_item_id ON item_barcodes(item_id,id);
CREATE INDEX idx_aliases_item_id ON item_aliases(item_id,id);
CREATE INDEX idx_attributes_item_id ON item_attributes(item_id,id);

-- Retain source text and pre-existing counters, including historical barcode
-- normalization. New writes use the current validator; migration never repairs
-- old product strings or publishes legacy search/OCR caches as approved text.
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'barcode',id,1 FROM item_barcodes WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'barcode',b.id,r.revision,
 json_object('id',b.id,'item_id',b.item_id,'symbology',b.symbology,
 'raw_value',b.raw_value,'normalized_value',b.normalized_value),b.created_at
 FROM item_barcodes b JOIN projection_revisions r ON r.entity_type='barcode' AND r.entity_id=b.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'alias',id,1 FROM item_aliases WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'alias',a.id,r.revision,json_object('id',a.id,'item_id',a.item_id,'alias',a.alias),a.created_at
 FROM item_aliases a JOIN projection_revisions r ON r.entity_type='alias' AND r.entity_id=a.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'attribute',id,1 FROM item_attributes WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'attribute',a.id,r.revision,
 json_object('id',a.id,'item_id',a.item_id,'attribute_key',a.attribute_key,'attribute_value',a.attribute_value),a.created_at
 FROM item_attributes a JOIN projection_revisions r ON r.entity_type='attribute' AND r.entity_id=a.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

-- No fabricated domain events/outbox changes, revision bumps of unrelated
-- owners, or claim that this partial materialization enables full frozen sync.
-- The runner validates UTF-8/field/counter bounds transactionally before ledger.
