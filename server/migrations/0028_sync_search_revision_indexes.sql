-- Bound search-backfill revision lookups to the selected identity without
-- rewriting retained change, snapshot, proof or source bytes.
CREATE INDEX idx_sync_change_search_revision
 ON sync_changes(entity_id,entity_revision DESC) WHERE entity_type='search_doc';
CREATE INDEX idx_sync_snapshot_search_revision
 ON sync_snapshot_rows(entity_id,entity_revision DESC,snapshot_id) WHERE entity_type='search_doc';
