-- Location metadata and independently retained current projections.
ALTER TABLE locations ADD COLUMN description TEXT NOT NULL DEFAULT '' CHECK(length(description)<=10000);
ALTER TABLE locations ADD COLUMN deleted_at TEXT;

CREATE UNIQUE INDEX idx_projection_revision_identity ON projection_revisions(entity_type,entity_id,revision);
CREATE TABLE current_projections (
 entity_type TEXT NOT NULL,
 entity_id TEXT NOT NULL,
 entity_revision INTEGER NOT NULL CHECK(entity_revision BETWEEN 1 AND 9007199254740991),
 projection_version INTEGER NOT NULL DEFAULT 1 CHECK(projection_version=1),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json) AND json_type(payload_json)='object'),
 updated_at TEXT NOT NULL,
 PRIMARY KEY(entity_type,entity_id),
 FOREIGN KEY(entity_type,entity_id,entity_revision) REFERENCES projection_revisions(entity_type,entity_id,revision)
 ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
) STRICT, WITHOUT ROWID;

-- Location backfill does not enable sync: the complete cross-domain backfill and
-- frozen-snapshot HTTP gates must be completed before publishing a full replica.
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'location',id,1 FROM locations WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location',l.id,r.revision,
 json_object('id',l.id,'parent_id',l.parent_id,'code',l.code,'name',l.name,
 'status',l.status,'kind',l.kind,'sort_order',l.sort_order,'description',l.description),l.updated_at
 FROM locations l JOIN projection_revisions r ON r.entity_type='location' AND r.entity_id=l.id;
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'location_event',id,1 FROM location_events WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'location_event',e.id,r.revision,
 json_object('id',e.id,'location_id',e.location_id,'from_parent_id',e.from_parent_id,
 'to_parent_id',e.to_parent_id,'occurred_at',e.occurred_at),e.occurred_at
 FROM location_events e JOIN projection_revisions r ON r.entity_type='location_event' AND r.entity_id=e.id;

-- Mutation audit is distinct from projected movement/usage/cost history. It
-- freezes the acknowledged result, contains no raw credentials, and is append-only.
CREATE TABLE mutation_events (
 id TEXT PRIMARY KEY,
 admin_id TEXT NOT NULL REFERENCES admin_accounts(id) ON DELETE RESTRICT,
 dataset_epoch TEXT NOT NULL,
 operation TEXT NOT NULL,
 idempotency_key TEXT NOT NULL UNIQUE REFERENCES api_idempotency(key) ON DELETE RESTRICT,
 occurred_at TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '' CHECK(length(reason)<=10000),
 result_json TEXT NOT NULL CHECK(json_valid(result_json))
) STRICT;
CREATE INDEX idx_mutation_events_time ON mutation_events(occurred_at,id);
CREATE TRIGGER mutation_events_no_update BEFORE UPDATE ON mutation_events
 BEGIN SELECT RAISE(ABORT,'mutation events are immutable'); END;
CREATE TRIGGER mutation_events_no_delete BEFORE DELETE ON mutation_events
 BEGIN SELECT RAISE(ABORT,'mutation events are immutable'); END;
CREATE TRIGGER sync_changes_no_update BEFORE UPDATE ON sync_changes
 BEGIN SELECT RAISE(ABORT,'sync payloads are frozen'); END;
CREATE TRIGGER location_events_no_update BEFORE UPDATE ON location_events
 BEGIN SELECT RAISE(ABORT,'location events are immutable'); END;
CREATE TRIGGER location_events_no_delete BEFORE DELETE ON location_events
 BEGIN SELECT RAISE(ABORT,'location events are immutable'); END;
