-- Movement history retains stable Unit identities after an explicitly authorized
-- record deletion. A history reference is not a live holding: keeping this FK
-- would make a newly created Unit undeletable because its own creation event
-- already references it. Financial/profile FKs remain restrictive and services
-- independently reject those references before deleting any Unit.
CREATE TABLE inventory_events_v12 (
 id TEXT PRIMARY KEY,
 item_id TEXT NOT NULL REFERENCES items(id),
 item_unit_id TEXT,
 from_location_id TEXT REFERENCES locations(id),
 to_location_id TEXT REFERENCES locations(id),
 action TEXT NOT NULL,
 quantity_base INTEGER NOT NULL,
 occurred_at TEXT NOT NULL,
 idempotency_key TEXT UNIQUE,
 note TEXT NOT NULL DEFAULT '' CHECK(length(note)<=10000),
 reason TEXT NOT NULL DEFAULT '' CHECK(length(reason)<=10000),
 mutation_event_id TEXT REFERENCES mutation_events(id) DEFERRABLE INITIALLY DEFERRED
) STRICT;
INSERT INTO inventory_events_v12
 SELECT id,item_id,item_unit_id,from_location_id,to_location_id,action,quantity_base,occurred_at,idempotency_key,note,reason,mutation_event_id
 FROM inventory_events;
DROP TABLE inventory_events;
ALTER TABLE inventory_events_v12 RENAME TO inventory_events;
CREATE INDEX idx_inventory_events_item ON inventory_events(item_id,occurred_at);
CREATE INDEX idx_inventory_events_timeline ON inventory_events(occurred_at,id);
CREATE INDEX idx_inventory_events_mutation ON inventory_events(mutation_event_id,id);
CREATE TRIGGER inventory_events_no_update BEFORE UPDATE ON inventory_events
 BEGIN SELECT RAISE(ABORT,'inventory events are immutable'); END;
CREATE TRIGGER inventory_events_no_delete BEFORE DELETE ON inventory_events
 BEGIN SELECT RAISE(ABORT,'inventory events are immutable'); END;
