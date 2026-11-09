-- Retained media are claims until an actual image is verified and sanitized.
-- Neither this migration nor its source projections inspect or rewrite files.
ALTER TABLE media ADD COLUMN verification TEXT NOT NULL DEFAULT 'unverified'
 CHECK(verification IN ('unverified','verified'));
ALTER TABLE media_links ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- Stable independent link IDs support complete owner and Media membership reads.
CREATE INDEX idx_media_links_media_id ON media_links(media_id,id);
CREATE INDEX idx_media_links_item_id ON media_links(item_id,id);
CREATE INDEX idx_media_links_unit_id ON media_links(item_unit_id,id);
CREATE INDEX idx_media_links_location_id ON media_links(location_id,id);

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'media',id,1 FROM media WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'media',m.id,r.revision,
 json_object('id',m.id,'sha256',m.sha256,'thumb_url',NULL,'width',m.width,'height',m.height,
 'thumb_sha256',NULL,'mime_type',m.mime_type,'bytes',m.bytes,'perceptual_hash',m.perceptual_hash),
 strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM media m JOIN projection_revisions r ON r.entity_type='media' AND r.entity_id=m.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'media_link',id,1 FROM media_links WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'media_link',l.id,r.revision,
 json_object('id',l.id,'media_id',l.media_id,
 'owner_type',CASE WHEN l.item_id IS NOT NULL THEN 'item' WHEN l.item_unit_id IS NOT NULL THEN 'unit' ELSE 'location' END,
 'owner_id',COALESCE(l.item_id,l.item_unit_id,l.location_id),
 'is_cover',json(CASE WHEN l.is_cover=1 THEN 'true' ELSE 'false' END),
 'sort_order',l.sort_order,'description',l.description),strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM media_links l JOIN projection_revisions r ON r.entity_type='media_link' AND r.entity_id=l.id WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;

-- The runner validates source, independent counters and actual owners before
-- ledger commit. Preserve existing frozen/current bytes and counter-only IDs;
-- do not invent image verification, thumbnails, events or full offline sync.
