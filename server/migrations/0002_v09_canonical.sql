-- HomeBox canonical v0.9 additive migration (after v0.7 baseline)
-- Migration runner MUST run entire file in a single connection with foreign_keys=ON.
-- A production migration runner validates schema version, backups and checksum BEFORE running.
PRAGMA foreign_keys=ON;
BEGIN IMMEDIATE;
-- Precision metadata required by canonical quantity_base projection.
ALTER TABLE items ADD COLUMN base_unit TEXT NOT NULL DEFAULT 'pcs';
ALTER TABLE items ADD COLUMN scale_factor INTEGER NOT NULL DEFAULT 1 CHECK(scale_factor>=1);
-- Existing non-pcs legacy rows require explicit operator-verified unit mapping before enabling offline sync.
CREATE TABLE item_aliases (id TEXT PRIMARY KEY,item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,alias TEXT NOT NULL,created_at TEXT NOT NULL,UNIQUE(item_id,alias)) STRICT;
CREATE INDEX idx_alias_item ON item_aliases(item_id);
CREATE TABLE item_attributes (id TEXT PRIMARY KEY,item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,attribute_key TEXT NOT NULL,attribute_value TEXT NOT NULL,created_at TEXT NOT NULL) STRICT;
CREATE INDEX idx_attr_item ON item_attributes(item_id);
CREATE TABLE ocr_results (id TEXT PRIMARY KEY,media_id TEXT NOT NULL REFERENCES media(id) ON DELETE CASCADE,job_id TEXT REFERENCES ai_jobs(id) ON DELETE SET NULL,raw_text TEXT NOT NULL,lang TEXT,engine TEXT NOT NULL,engine_version TEXT NOT NULL,created_at TEXT NOT NULL) STRICT;
CREATE TABLE unit_search_documents (id INTEGER PRIMARY KEY,unit_id TEXT NOT NULL UNIQUE REFERENCES item_units(id) ON DELETE CASCADE,title TEXT NOT NULL DEFAULT '',identifiers TEXT NOT NULL DEFAULT '',details TEXT NOT NULL DEFAULT '',reviewed_ai TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),updated_at TEXT NOT NULL) STRICT;
CREATE VIRTUAL TABLE unit_search_fts USING fts5(title,identifiers,details,reviewed_ai,content='unit_search_documents',content_rowid='id',tokenize='unicode61');
CREATE TRIGGER unit_search_ins AFTER INSERT ON unit_search_documents BEGIN INSERT INTO unit_search_fts(rowid,title,identifiers,details,reviewed_ai) VALUES(new.id,new.title,new.identifiers,new.details,new.reviewed_ai); END;
CREATE TRIGGER unit_search_del AFTER DELETE ON unit_search_documents BEGIN INSERT INTO unit_search_fts(unit_search_fts,rowid,title,identifiers,details,reviewed_ai) VALUES('delete',old.id,old.title,old.identifiers,old.details,old.reviewed_ai); END;
CREATE TRIGGER unit_search_upd AFTER UPDATE ON unit_search_documents BEGIN INSERT INTO unit_search_fts(unit_search_fts,rowid,title,identifiers,details,reviewed_ai) VALUES('delete',old.id,old.title,old.identifiers,old.details,old.reviewed_ai); INSERT INTO unit_search_fts(rowid,title,identifiers,details,reviewed_ai) VALUES(new.id,new.title,new.identifiers,new.details,new.reviewed_ai); END;
CREATE TABLE admin_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL) STRICT;
CREATE TABLE sessions (id TEXT PRIMARY KEY,admin_id TEXT NOT NULL REFERENCES admin_accounts(id) ON DELETE CASCADE,token_hash TEXT NOT NULL UNIQUE,expires_at TEXT NOT NULL,revoked_at TEXT,created_at TEXT NOT NULL) STRICT;
CREATE INDEX idx_sessions_expiry ON sessions(expires_at);
CREATE TABLE security_audit (id TEXT PRIMARY KEY,event_type TEXT NOT NULL,admin_id TEXT REFERENCES admin_accounts(id),occurred_at TEXT NOT NULL,details_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(details_json))) STRICT;
CREATE TABLE app_settings (key TEXT PRIMARY KEY,value_json TEXT NOT NULL CHECK(json_valid(value_json)),updated_at TEXT NOT NULL) STRICT;
CREATE TABLE ai_model_registry (model_key TEXT PRIMARY KEY,model_id TEXT NOT NULL,revision TEXT NOT NULL,preprocess_version TEXT NOT NULL,dimension INTEGER NOT NULL CHECK(dimension>0),sha256 TEXT NOT NULL,active INTEGER NOT NULL DEFAULT 0 CHECK(active IN (0,1))) STRICT;
CREATE TABLE backup_sets (id TEXT PRIMARY KEY,manifest_sha256 TEXT NOT NULL,created_at TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('pending','verified','failed')),storage_ref TEXT NOT NULL) STRICT;
CREATE TABLE release_history (id TEXT PRIMARY KEY,app_version TEXT NOT NULL,image_digest TEXT NOT NULL,schema_version INTEGER NOT NULL,deployed_at TEXT NOT NULL,status TEXT NOT NULL) STRICT;
-- Per-store revisions are monotone integers allocated by application transaction.
CREATE TABLE projection_revisions (entity_type TEXT NOT NULL,entity_id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),PRIMARY KEY(entity_type,entity_id)) STRICT, WITHOUT ROWID;
CREATE TABLE sync_state (singleton INTEGER PRIMARY KEY CHECK(singleton=1),server_instance_id TEXT NOT NULL,dataset_epoch TEXT NOT NULL,projection_version INTEGER NOT NULL DEFAULT 1 CHECK(projection_version=1),min_retained_seq INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL) STRICT;
INSERT INTO sync_state VALUES(1,lower(hex(randomblob(16))),lower(hex(randomblob(16))),1,0,strftime('%Y-%m-%dT%H:%M:%fZ','now'));
-- seq is durable, AUTOINCREMENT prevents reused seq values when the log is pruned.
CREATE TABLE sync_changes (seq INTEGER PRIMARY KEY AUTOINCREMENT,entity_type TEXT NOT NULL,entity_id TEXT NOT NULL,operation TEXT NOT NULL CHECK(operation IN ('upsert','delete')),entity_revision INTEGER NOT NULL CHECK(entity_revision>0),projection_json TEXT CHECK(projection_json IS NULL OR json_valid(projection_json)),created_at TEXT NOT NULL,CHECK((operation='delete' AND projection_json IS NULL) OR (operation='upsert' AND projection_json IS NOT NULL))) STRICT;
CREATE INDEX idx_sync_changes_entity ON sync_changes(entity_type,entity_id,seq);
-- Frozen, fully materialized manifests. Each snapshot captures a complete projection at the same high watermark.
CREATE TABLE sync_snapshots (id TEXT PRIMARY KEY,server_instance_id TEXT NOT NULL,dataset_epoch TEXT NOT NULL,projection_version INTEGER NOT NULL CHECK(projection_version=1),high_watermark INTEGER NOT NULL CHECK(high_watermark>=0),created_at TEXT NOT NULL,expires_at TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'ready' CHECK(status IN ('ready','expired'))) STRICT;
CREATE INDEX idx_snapshots_expiry ON sync_snapshots(expires_at);
CREATE TABLE sync_snapshot_rows (snapshot_id TEXT NOT NULL REFERENCES sync_snapshots(id) ON DELETE CASCADE,ordinal INTEGER NOT NULL CHECK(ordinal>=0),entity_type TEXT NOT NULL,entity_id TEXT NOT NULL,entity_revision INTEGER NOT NULL CHECK(entity_revision>0),payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),PRIMARY KEY(snapshot_id,ordinal),UNIQUE(snapshot_id,entity_type,entity_id)) STRICT, WITHOUT ROWID;
-- Optional change dedupe and transactional service guarantee: one revision / change per projection in a transaction.
-- Domain service MUST perform business mutation, projection materialization, revision increment and log append atomically.
-- Generic direct SQL writes outside the service boundary are forbidden; run mutation coverage tests.
INSERT INTO schema_migrations(version,name,applied_at) VALUES(2,'homebox_v0_9_canonical',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
COMMIT;
