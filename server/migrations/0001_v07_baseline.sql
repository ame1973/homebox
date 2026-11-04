-- HomeBox v0.7: NEW DATABASE bootstrap migration. Not ALTER migration for existing databases.
-- Requires SQLite >= 3.37, compiled with FTS5 and JSON1.
-- Each application connection MUST set PRAGMA foreign_keys=ON and busy_timeout.
PRAGMA foreign_keys=ON;
BEGIN IMMEDIATE;
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT;
CREATE TABLE locations (
 id TEXT PRIMARY KEY, parent_id TEXT REFERENCES locations(id) ON DELETE RESTRICT,
 code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'other',
 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
 sort_order INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 CHECK(parent_id IS NULL OR parent_id <> id)
) STRICT;
CREATE INDEX idx_locations_parent ON locations(parent_id,sort_order);
CREATE TABLE categories (id TEXT PRIMARY KEY,parent_id TEXT REFERENCES categories(id) ON DELETE RESTRICT,name TEXT NOT NULL) STRICT;
CREATE TABLE items (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, brand TEXT, model TEXT,
 category_id TEXT REFERENCES categories(id) ON DELETE SET NULL,
 tracking_mode TEXT NOT NULL CHECK(tracking_mode IN ('individual','quantity')),
 description TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
 unit_code TEXT NOT NULL DEFAULT 'pcs', status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX idx_items_category ON items(category_id,status);
CREATE TABLE item_units (
 id TEXT PRIMARY KEY,item_id TEXT NOT NULL REFERENCES items(id) ON DELETE RESTRICT,
 location_id TEXT REFERENCES locations(id) ON DELETE RESTRICT,
 asset_code TEXT NOT NULL UNIQUE, serial_number TEXT, notes TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'stored' CHECK(status IN ('stored','in_use','lent','lost','retired','disposed')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX idx_units_item ON item_units(item_id);
CREATE INDEX idx_units_location_status ON item_units(location_id,status);
CREATE INDEX idx_units_serial ON item_units(serial_number) WHERE serial_number IS NOT NULL;
CREATE TABLE stock_balances (
 item_id TEXT NOT NULL REFERENCES items(id) ON DELETE RESTRICT,
 location_id TEXT NOT NULL REFERENCES locations(id) ON DELETE RESTRICT,
 quantity_base INTEGER NOT NULL DEFAULT 0 CHECK(quantity_base>=0),updated_at TEXT NOT NULL,
 PRIMARY KEY(item_id,location_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_stock_location ON stock_balances(location_id);
CREATE TABLE item_barcodes (
 id TEXT PRIMARY KEY,item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 symbology TEXT NOT NULL, raw_value TEXT NOT NULL, normalized_value TEXT NOT NULL,
 created_at TEXT NOT NULL,UNIQUE(item_id,symbology,normalized_value)
) STRICT;
CREATE INDEX idx_barcode_lookup ON item_barcodes(symbology,normalized_value);
CREATE TABLE tags(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE item_tags(item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,PRIMARY KEY(item_id,tag_id)) STRICT, WITHOUT ROWID;
CREATE TABLE media (
 id TEXT PRIMARY KEY,storage_key TEXT NOT NULL UNIQUE,sha256 TEXT NOT NULL,
 mime_type TEXT NOT NULL,width INTEGER NOT NULL CHECK(width>0),height INTEGER NOT NULL CHECK(height>0),
 bytes INTEGER NOT NULL CHECK(bytes>0),perceptual_hash TEXT,created_at TEXT NOT NULL
) STRICT;
CREATE TABLE media_links (
 id TEXT PRIMARY KEY,media_id TEXT NOT NULL REFERENCES media(id) ON DELETE CASCADE,
 location_id TEXT REFERENCES locations(id) ON DELETE CASCADE,
 item_id TEXT REFERENCES items(id) ON DELETE CASCADE,
 item_unit_id TEXT REFERENCES item_units(id) ON DELETE CASCADE,
 is_cover INTEGER NOT NULL DEFAULT 0 CHECK(is_cover IN (0,1)),sort_order INTEGER NOT NULL DEFAULT 0,
 CHECK((location_id IS NOT NULL)+(item_id IS NOT NULL)+(item_unit_id IS NOT NULL)=1)
) STRICT;
CREATE INDEX idx_media_links_location ON media_links(location_id);
CREATE INDEX idx_media_links_item ON media_links(item_id);
CREATE INDEX idx_media_links_unit ON media_links(item_unit_id);
CREATE TABLE purchases (
 id TEXT PRIMARY KEY,merchant_name TEXT NOT NULL,purchased_at TEXT NOT NULL,
 currency TEXT NOT NULL CHECK(length(currency)=3),receipt_note TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL
) STRICT;
CREATE INDEX idx_purchases_date ON purchases(purchased_at,currency);
CREATE INDEX idx_purchases_merchant ON purchases(merchant_name,purchased_at);
CREATE TABLE purchase_lines (
 id TEXT PRIMARY KEY,purchase_id TEXT NOT NULL REFERENCES purchases(id) ON DELETE RESTRICT,
 item_id TEXT NOT NULL REFERENCES items(id) ON DELETE RESTRICT,
 item_unit_id TEXT REFERENCES item_units(id) ON DELETE RESTRICT,
 quantity_base INTEGER NOT NULL CHECK(quantity_base>0),
 paid_minor INTEGER NOT NULL CHECK(paid_minor>=0),
 note TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX idx_purchase_lines_item ON purchase_lines(item_id);
CREATE INDEX idx_purchase_lines_unit ON purchase_lines(item_unit_id);
CREATE INDEX idx_purchase_lines_purchase ON purchase_lines(purchase_id);
-- Source of truth for asset acquisition: purchase_lines allocated to one item_unit,
-- otherwise asset_profiles.manual_acquisition_minor as a fallback, NEVER both.
CREATE TABLE asset_profiles (
 item_unit_id TEXT PRIMARY KEY REFERENCES item_units(id) ON DELETE RESTRICT,
 in_service_at TEXT,retired_at TEXT,
 cost_basis TEXT NOT NULL DEFAULT 'purchase' CHECK(cost_basis IN ('purchase','manual')),
 manual_acquisition_minor INTEGER CHECK(manual_acquisition_minor>=0),
 currency TEXT NOT NULL CHECK(length(currency)=3),
 estimated_residual_minor INTEGER CHECK(estimated_residual_minor>=0),
 usage_unit TEXT NOT NULL DEFAULT 'times' CHECK(usage_unit IN ('times','minutes','cycles')),
 created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
 CHECK(retired_at IS NULL OR in_service_at IS NULL OR retired_at>=in_service_at),
 CHECK((cost_basis='manual' AND manual_acquisition_minor IS NOT NULL) OR
       (cost_basis='purchase' AND manual_acquisition_minor IS NULL))
) STRICT;
CREATE INDEX idx_asset_service ON asset_profiles(in_service_at,retired_at);
CREATE TABLE asset_usage_events (
 id TEXT PRIMARY KEY,item_unit_id TEXT NOT NULL REFERENCES asset_profiles(item_unit_id) ON DELETE RESTRICT,
 occurred_at TEXT NOT NULL,delta INTEGER NOT NULL CHECK(delta<>0),
 usage_unit TEXT NOT NULL CHECK(usage_unit IN ('times','minutes','cycles')),
 event_type TEXT NOT NULL CHECK(event_type IN ('use','correction')),
 note TEXT NOT NULL DEFAULT '',idempotency_key TEXT UNIQUE,created_at TEXT NOT NULL
) STRICT;
CREATE INDEX idx_usage_unit_date ON asset_usage_events(item_unit_id,occurred_at);
CREATE TABLE asset_cost_events (
 id TEXT PRIMARY KEY,item_unit_id TEXT NOT NULL REFERENCES asset_profiles(item_unit_id) ON DELETE RESTRICT,
 occurred_at TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('maintenance','repair','accessory','fee','refund','adjustment')),
 amount_minor INTEGER NOT NULL CHECK(amount_minor>0),
 direction TEXT NOT NULL CHECK(direction IN ('expense','credit')),
 currency TEXT NOT NULL CHECK(length(currency)=3),note TEXT NOT NULL DEFAULT '',
 idempotency_key TEXT UNIQUE,created_at TEXT NOT NULL
) STRICT;
CREATE INDEX idx_costs_unit_date ON asset_cost_events(item_unit_id,occurred_at);
CREATE TABLE asset_disposals (
 item_unit_id TEXT PRIMARY KEY REFERENCES asset_profiles(item_unit_id) ON DELETE RESTRICT,
 disposed_at TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('sold','donated','recycled','discarded','other')),
 realized_proceeds_minor INTEGER NOT NULL DEFAULT 0 CHECK(realized_proceeds_minor>=0),
 currency TEXT NOT NULL CHECK(length(currency)=3),note TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL
) STRICT;
CREATE INDEX idx_disposals_date ON asset_disposals(disposed_at);
CREATE TABLE inventory_events (
 id TEXT PRIMARY KEY,item_id TEXT NOT NULL REFERENCES items(id),item_unit_id TEXT REFERENCES item_units(id),
 from_location_id TEXT REFERENCES locations(id),to_location_id TEXT REFERENCES locations(id),
 action TEXT NOT NULL,quantity_base INTEGER NOT NULL,occurred_at TEXT NOT NULL,
 idempotency_key TEXT UNIQUE
) STRICT;
CREATE INDEX idx_inventory_events_item ON inventory_events(item_id,occurred_at);
CREATE TABLE location_events (
 id TEXT PRIMARY KEY,location_id TEXT NOT NULL REFERENCES locations(id),
 from_parent_id TEXT REFERENCES locations(id),to_parent_id TEXT REFERENCES locations(id),
 occurred_at TEXT NOT NULL
) STRICT;
-- Raw OCR is never inserted into published search automatically.
CREATE TABLE ai_jobs (
 id TEXT PRIMARY KEY,kind TEXT NOT NULL CHECK(kind IN ('ocr','embedding','classification','rebuild')),
 media_id TEXT REFERENCES media(id) ON DELETE CASCADE,
 item_id TEXT REFERENCES items(id) ON DELETE CASCADE,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','leased','retry_wait','completed','failed','cancelled')),
 priority INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
 lease_owner TEXT,lease_expires_at TEXT,available_at TEXT NOT NULL,
 model_id TEXT,model_revision TEXT,content_sha256 TEXT,params_hash TEXT,
 error_message TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
 CHECK(media_id IS NOT NULL OR item_id IS NOT NULL)
) STRICT;
CREATE INDEX idx_jobs_claim ON ai_jobs(status,available_at,priority DESC);
CREATE UNIQUE INDEX idx_jobs_dedupe ON ai_jobs(kind,media_id,model_id,model_revision,content_sha256,params_hash)
 WHERE media_id IS NOT NULL AND status IN ('pending','leased','retry_wait');
CREATE TABLE ai_review_entries (
 id TEXT PRIMARY KEY,item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 media_id TEXT REFERENCES media(id) ON DELETE SET NULL,
 source_job_id TEXT REFERENCES ai_jobs(id) ON DELETE SET NULL,
 field_key TEXT NOT NULL,
 raw_value TEXT NOT NULL,corrected_value TEXT,
 state TEXT NOT NULL DEFAULT 'needs_review' CHECK(state IN ('needs_review','accepted','corrected','rejected')),
 model_id TEXT,confidence REAL CHECK(confidence IS NULL OR (confidence>=0 AND confidence<=1)),
 reviewed_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
 CHECK((state='corrected' AND corrected_value IS NOT NULL) OR state<>'corrected')
) STRICT;
CREATE INDEX idx_reviews_pending ON ai_review_entries(state,created_at);
CREATE INDEX idx_reviews_item ON ai_review_entries(item_id,state);
CREATE TABLE image_embeddings (
 id TEXT PRIMARY KEY,media_id TEXT NOT NULL REFERENCES media(id) ON DELETE CASCADE,
 model_id TEXT NOT NULL,model_revision TEXT NOT NULL,preprocess_version TEXT NOT NULL,
 dim INTEGER NOT NULL CHECK(dim>0),vector BLOB NOT NULL,created_at TEXT NOT NULL,
 UNIQUE(media_id,model_id,model_revision,preprocess_version),
 CHECK(length(vector)=dim*4)
) STRICT;
CREATE INDEX idx_embedding_model ON image_embeddings(model_id,model_revision,preprocess_version);
-- Rebuildable effective search document: only curated fields and reviewed AI values.
CREATE TABLE search_documents (
 id INTEGER PRIMARY KEY,item_id TEXT NOT NULL UNIQUE REFERENCES items(id) ON DELETE CASCADE,
 title TEXT NOT NULL DEFAULT '',identifiers TEXT NOT NULL DEFAULT '',details TEXT NOT NULL DEFAULT '',
 reviewed_ai TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),updated_at TEXT NOT NULL
) STRICT;
CREATE VIRTUAL TABLE search_fts USING fts5(title,identifiers,details,reviewed_ai,content='search_documents',content_rowid='id',tokenize='unicode61');
CREATE TRIGGER search_docs_insert AFTER INSERT ON search_documents BEGIN
 INSERT INTO search_fts(rowid,title,identifiers,details,reviewed_ai) VALUES(new.id,new.title,new.identifiers,new.details,new.reviewed_ai);
END;
CREATE TRIGGER search_docs_delete AFTER DELETE ON search_documents BEGIN
 INSERT INTO search_fts(search_fts,rowid,title,identifiers,details,reviewed_ai) VALUES('delete',old.id,old.title,old.identifiers,old.details,old.reviewed_ai);
END;
CREATE TRIGGER search_docs_update AFTER UPDATE ON search_documents BEGIN
 INSERT INTO search_fts(search_fts,rowid,title,identifiers,details,reviewed_ai) VALUES('delete',old.id,old.title,old.identifiers,old.details,old.reviewed_ai);
 INSERT INTO search_fts(rowid,title,identifiers,details,reviewed_ai) VALUES(new.id,new.title,new.identifiers,new.details,new.reviewed_ai);
END;
CREATE TABLE analytics_cache (
 cache_key TEXT PRIMARY KEY,filter_hash TEXT NOT NULL,currency TEXT,
 data_json TEXT NOT NULL CHECK(json_valid(data_json)),
 source_revision INTEGER NOT NULL,computed_at TEXT NOT NULL,expires_at TEXT NOT NULL
) STRICT;
CREATE TABLE data_revision (scope TEXT PRIMARY KEY,revision INTEGER NOT NULL DEFAULT 0) STRICT;
INSERT INTO data_revision(scope,revision) VALUES ('catalog',0),('finance',0),('ai',0);
CREATE TABLE api_idempotency (
 key TEXT PRIMARY KEY,operation TEXT NOT NULL,request_sha256 TEXT NOT NULL,
 response_status INTEGER,response_json TEXT,created_at TEXT NOT NULL,expires_at TEXT NOT NULL
) STRICT;
INSERT INTO schema_migrations VALUES(1,'homebox_v0_7_bootstrap',strftime('%Y-%m-%dT%H:%M:%fZ','now'));
COMMIT;
