-- Preserve legacy movement bytes while adding exact producer context. Older
-- events remain retained history, not fictional new frozen sync publications.
ALTER TABLE inventory_events ADD COLUMN note TEXT NOT NULL DEFAULT '' CHECK(length(note)<=10000);
ALTER TABLE inventory_events ADD COLUMN reason TEXT NOT NULL DEFAULT '' CHECK(length(reason)<=10000);
ALTER TABLE inventory_events ADD COLUMN mutation_event_id TEXT REFERENCES mutation_events(id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX idx_inventory_events_timeline ON inventory_events(occurred_at,id);
CREATE INDEX idx_inventory_events_mutation ON inventory_events(mutation_event_id,id);

-- A value-equal adjustment has a receipt, not a movement. Preserve its exact
-- optional note in immutable audit too, without inventing a physical event.
ALTER TABLE mutation_events ADD COLUMN note TEXT NOT NULL DEFAULT '' CHECK(length(note)<=10000);

CREATE TRIGGER inventory_events_no_update BEFORE UPDATE ON inventory_events
 BEGIN SELECT RAISE(ABORT,'inventory events are immutable'); END;
CREATE TRIGGER inventory_events_no_delete BEFORE DELETE ON inventory_events
 BEGIN SELECT RAISE(ABORT,'inventory events are immutable'); END;
