-- Rebuild the complete parent/child group with foreign keys still enabled.
-- Text ordering of retained RFC3339 offsets is not chronological ordering. Go
-- validates actual service intervals inside this same checksum transaction.
CREATE TABLE asset_profiles_v15 (
 item_unit_id TEXT PRIMARY KEY REFERENCES item_units(id) ON DELETE RESTRICT,
 in_service_at TEXT,
 retired_at TEXT,
 cost_basis TEXT NOT NULL DEFAULT 'purchase' CHECK(cost_basis IN ('purchase','manual')),
 manual_acquisition_minor INTEGER CHECK(manual_acquisition_minor>=0),
 currency TEXT NOT NULL CHECK(length(currency)=3),
 estimated_residual_minor INTEGER CHECK(estimated_residual_minor>=0),
 usage_unit TEXT NOT NULL DEFAULT 'times' CHECK(usage_unit IN ('times','minutes','cycles')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK((cost_basis='manual' AND manual_acquisition_minor IS NOT NULL) OR
       (cost_basis='purchase' AND manual_acquisition_minor IS NULL))
) STRICT;
CREATE TABLE asset_usage_events_v15 (
 id TEXT PRIMARY KEY,
 item_unit_id TEXT NOT NULL REFERENCES asset_profiles_v15(item_unit_id) ON DELETE RESTRICT,
 occurred_at TEXT NOT NULL,
 delta INTEGER NOT NULL CHECK(delta<>0),
 usage_unit TEXT NOT NULL CHECK(usage_unit IN ('times','minutes','cycles')),
 event_type TEXT NOT NULL CHECK(event_type IN ('use','correction')),
 note TEXT NOT NULL DEFAULT '',
 idempotency_key TEXT UNIQUE,
 created_at TEXT NOT NULL,
 correction_of TEXT REFERENCES asset_usage_events_v15(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
  CHECK(correction_of IS NULL OR correction_of<>id),
 mutation_event_id TEXT REFERENCES mutation_events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
  CHECK(mutation_event_id IS NULL OR length(created_at) BETWEEN 20 AND 35)
) STRICT;
CREATE TABLE asset_cost_events_v15 (
 id TEXT PRIMARY KEY,
 item_unit_id TEXT NOT NULL REFERENCES asset_profiles_v15(item_unit_id) ON DELETE RESTRICT,
 occurred_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('maintenance','repair','accessory','fee','refund','adjustment')),
 amount_minor INTEGER NOT NULL CHECK(amount_minor>0),
 direction TEXT NOT NULL CHECK(direction IN ('expense','credit')),
 currency TEXT NOT NULL CHECK(length(currency)=3),
 note TEXT NOT NULL DEFAULT '',
 idempotency_key TEXT UNIQUE,
 created_at TEXT NOT NULL,
 correction_of TEXT REFERENCES asset_cost_events_v15(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
  CHECK(correction_of IS NULL OR correction_of<>id),
 mutation_event_id TEXT REFERENCES mutation_events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
  CHECK(mutation_event_id IS NULL OR length(created_at) BETWEEN 20 AND 35)
) STRICT;
CREATE TABLE asset_disposals_v15 (
 item_unit_id TEXT PRIMARY KEY REFERENCES asset_profiles_v15(item_unit_id) ON DELETE RESTRICT,
 disposed_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('sold','donated','recycled','discarded','other')),
 realized_proceeds_minor INTEGER NOT NULL DEFAULT 0 CHECK(realized_proceeds_minor>=0),
 currency TEXT NOT NULL CHECK(length(currency)=3),
 note TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 mutation_event_id TEXT REFERENCES mutation_events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
  CHECK(mutation_event_id IS NULL OR length(created_at) BETWEEN 20 AND 35)
) STRICT;

INSERT INTO asset_profiles_v15
 SELECT item_unit_id,in_service_at,retired_at,cost_basis,manual_acquisition_minor,currency,
 estimated_residual_minor,usage_unit,created_at,updated_at FROM asset_profiles;
INSERT INTO asset_usage_events_v15
 SELECT id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,idempotency_key,created_at,NULL,NULL
 FROM asset_usage_events;
INSERT INTO asset_cost_events_v15
 SELECT id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,idempotency_key,created_at,NULL,NULL
 FROM asset_cost_events;
INSERT INTO asset_disposals_v15
 SELECT item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,note,created_at,NULL
 FROM asset_disposals;

DROP TABLE asset_usage_events;
DROP TABLE asset_cost_events;
DROP TABLE asset_disposals;
DROP TABLE asset_profiles;
ALTER TABLE asset_profiles_v15 RENAME TO asset_profiles;
ALTER TABLE asset_usage_events_v15 RENAME TO asset_usage_events;
ALTER TABLE asset_cost_events_v15 RENAME TO asset_cost_events;
ALTER TABLE asset_disposals_v15 RENAME TO asset_disposals;

CREATE INDEX idx_asset_service ON asset_profiles(in_service_at,retired_at);
CREATE INDEX idx_usage_unit_date ON asset_usage_events(item_unit_id,occurred_at);
CREATE INDEX idx_usage_unit_id ON asset_usage_events(item_unit_id,id);
CREATE INDEX idx_usage_correction ON asset_usage_events(correction_of,id);
CREATE INDEX idx_usage_mutation ON asset_usage_events(mutation_event_id,id);
CREATE INDEX idx_costs_unit_date ON asset_cost_events(item_unit_id,occurred_at);
CREATE INDEX idx_costs_unit_id ON asset_cost_events(item_unit_id,id);
CREATE INDEX idx_costs_correction ON asset_cost_events(correction_of,id);
CREATE INDEX idx_costs_mutation ON asset_cost_events(mutation_event_id,id);
CREATE INDEX idx_disposals_date ON asset_disposals(disposed_at);
CREATE INDEX idx_disposals_mutation ON asset_disposals(mutation_event_id,item_unit_id);

CREATE TRIGGER asset_profiles_no_delete BEFORE DELETE ON asset_profiles
 BEGIN SELECT RAISE(ABORT,'asset profiles are retained financial authority'); END;
CREATE TRIGGER asset_profiles_owner_immutable BEFORE UPDATE OF item_unit_id ON asset_profiles
 WHEN NEW.item_unit_id<>OLD.item_unit_id
 BEGIN SELECT RAISE(ABORT,'asset profile owners are immutable'); END;
CREATE TRIGGER asset_usage_events_no_update BEFORE UPDATE ON asset_usage_events
 BEGIN SELECT RAISE(ABORT,'asset usage events are immutable'); END;
CREATE TRIGGER asset_usage_events_no_delete BEFORE DELETE ON asset_usage_events
 BEGIN SELECT RAISE(ABORT,'asset usage events are immutable'); END;
CREATE TRIGGER asset_cost_events_no_update BEFORE UPDATE ON asset_cost_events
 BEGIN SELECT RAISE(ABORT,'asset cost events are immutable'); END;
CREATE TRIGGER asset_cost_events_no_delete BEFORE DELETE ON asset_cost_events
 BEGIN SELECT RAISE(ABORT,'asset cost events are immutable'); END;
CREATE TRIGGER asset_disposals_no_update BEFORE UPDATE ON asset_disposals
 BEGIN SELECT RAISE(ABORT,'asset disposal records are immutable'); END;
CREATE TRIGGER asset_disposals_no_delete BEFORE DELETE ON asset_disposals
 BEGIN SELECT RAISE(ABORT,'asset disposal records are immutable'); END;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'asset',item_unit_id,1 FROM asset_profiles WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'asset',a.item_unit_id,r.revision,
 json_object('item_unit_id',a.item_unit_id,'cost_basis',a.cost_basis,'currency',a.currency,
 'in_service_at',a.in_service_at,'retired_at',a.retired_at,'manual_acquisition_minor',a.manual_acquisition_minor,
 'estimated_residual_minor',a.estimated_residual_minor,'usage_unit',a.usage_unit),strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM asset_profiles a JOIN projection_revisions r ON r.entity_type='asset' AND r.entity_id=a.item_unit_id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'usage',id,1 FROM asset_usage_events WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'usage',u.id,r.revision,
 json_object('id',u.id,'item_unit_id',u.item_unit_id,'delta',u.delta,'usage_unit',u.usage_unit,
 'event_type',u.event_type,'occurred_at',u.occurred_at,'note',u.note,'correction_of',u.correction_of),strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM asset_usage_events u JOIN projection_revisions r ON r.entity_type='usage' AND r.entity_id=u.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'cost',id,1 FROM asset_cost_events WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'cost',c.id,r.revision,
 json_object('id',c.id,'item_unit_id',c.item_unit_id,'amount_minor',c.amount_minor,'direction',c.direction,
 'currency',c.currency,'kind',c.kind,'occurred_at',c.occurred_at,'note',c.note,'correction_of',c.correction_of),strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM asset_cost_events c JOIN projection_revisions r ON r.entity_type='cost' AND r.entity_id=c.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'disposal',item_unit_id,1 FROM asset_disposals WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'disposal',d.item_unit_id,r.revision,
 json_object('item_unit_id',d.item_unit_id,'disposed_at',d.disposed_at,'realized_proceeds_minor',d.realized_proceeds_minor,
 'currency',d.currency,'kind',d.kind,'note',d.note),strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM asset_disposals d JOIN projection_revisions r ON r.entity_type='disposal' AND r.entity_id=d.item_unit_id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

-- Legacy fields/keys remain exact. NULL new provenance means unknown, not a
-- guessed original correction, created instant or audit. New producers leave raw
-- keys NULL and bind their real deferred audit; scoped receipts own replay.
-- Validate all UTF-8/bounds/instants/owners/retained counters after SQL and before
-- committing the ledger. Keep existing current/frozen bytes and certified epoch.
-- This backfill emits no old events/receipts/FTS and does not enable complete sync.
