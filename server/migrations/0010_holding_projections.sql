-- Independent physical holding projections; source/history and counts stay exact.
CREATE INDEX idx_units_item_id ON item_units(item_id,id);

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'unit',id,1 FROM item_units WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'unit',u.id,r.revision,
 json_object('id',u.id,'item_id',u.item_id,'location_id',u.location_id,'asset_code',u.asset_code,
 'status',u.status,'serial_number',u.serial_number,'notes',u.notes),u.updated_at
 FROM item_units u JOIN projection_revisions r ON r.entity_type='unit' AND r.entity_id=u.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'stock',json_array(item_id,location_id),1 FROM stock_balances WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'stock',json_array(b.item_id,b.location_id),r.revision,
 json_object('item_id',b.item_id,'location_id',b.location_id,'quantity_base',b.quantity_base,
 'unit_code',i.unit_code,'base_unit',i.base_unit,'scale_factor',i.scale_factor),b.updated_at
 FROM stock_balances b JOIN items i ON i.id=b.item_id
 JOIN projection_revisions r ON r.entity_type='stock' AND r.entity_id=json_array(b.item_id,b.location_id) WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

-- The runner validates bounded UTF-8, exact safe counts/measurement, tracking and
-- real retained identity/counters inside this transaction before ledger commit.
-- Do not invent inventory events, touch Item/location revisions, publish legacy
-- FTS/OCR caches, rescale source or claim that this partial backfill enables sync.
