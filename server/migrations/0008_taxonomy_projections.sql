-- Taxonomy lookup paths and independent stable-ID/link projections.
CREATE INDEX idx_categories_parent ON categories(parent_id,id);
CREATE INDEX idx_item_tags_tag ON item_tags(tag_id,item_id);

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'category',id,1 FROM categories WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'category',c.id,r.revision,json_object('id',c.id,'parent_id',c.parent_id,'name',c.name),
 strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM categories c JOIN projection_revisions r ON r.entity_type='category' AND r.entity_id=c.id;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'tag',id,1 FROM tags WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'tag',t.id,r.revision,json_object('id',t.id,'name',t.name),
 strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM tags t JOIN projection_revisions r ON r.entity_type='tag' AND r.entity_id=t.id;

INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'item_tag',json_array(item_id,tag_id),1 FROM item_tags WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 SELECT 'item_tag',json_array(t.item_id,t.tag_id),r.revision,json_object('item_id',t.item_id,'tag_id',t.tag_id),
 strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM item_tags t JOIN projection_revisions r
 ON r.entity_type='item_tag' AND r.entity_id=json_array(t.item_id,t.tag_id);

-- Reserve existing item/search revisions without copying unverified legacy FTS
-- text into published projections. Dependent mutations rebuild from approved
-- source fields; full cross-domain backfill remains required before sync starts.
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'item',id,1 FROM items WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
INSERT INTO projection_revisions(entity_type,entity_id,revision)
 SELECT 'search_doc',json_array('item',item_id),revision FROM search_documents WHERE true
 ON CONFLICT(entity_type,entity_id) DO NOTHING;
