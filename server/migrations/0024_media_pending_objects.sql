-- Before promotion, journal each sanitized object independently of raw uploads.
-- A pending row is removed only by its publishing transaction or by a future
-- reference-aware recovery process; no existing files are classified here.
CREATE TABLE media_pending_objects (
 id TEXT PRIMARY KEY NOT NULL,
 dataset_epoch TEXT NOT NULL,
 sha256 TEXT NOT NULL CHECK(length(sha256)=64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
 bytes INTEGER NOT NULL CHECK(bytes BETWEEN 1 AND 528777216),
 created_at TEXT NOT NULL,
 stage_name TEXT NOT NULL,
 stage_device INTEGER NOT NULL CHECK(stage_device>=0),
 stage_inode INTEGER NOT NULL CHECK(stage_inode>0),
 staging_device INTEGER NOT NULL CHECK(staging_device>=0),
 staging_inode INTEGER NOT NULL CHECK(staging_inode>0),
 object_device INTEGER NOT NULL CHECK(object_device>=0),
 object_inode INTEGER NOT NULL CHECK(object_inode>0),
 UNIQUE(staging_device,staging_inode,stage_name),
 UNIQUE(stage_device,stage_inode)
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_media_pending_epoch ON media_pending_objects(dataset_epoch,object_device,object_inode,id);
CREATE INDEX idx_media_pending_digest ON media_pending_objects(sha256,id);
CREATE TRIGGER media_pending_objects_no_update BEFORE UPDATE ON media_pending_objects
 BEGIN SELECT RAISE(ABORT,'pending media objects are immutable'); END;
CREATE TRIGGER media_pending_objects_no_replace BEFORE INSERT ON media_pending_objects
 WHEN EXISTS(SELECT 1 FROM media_pending_objects WHERE id=NEW.id)
 BEGIN SELECT RAISE(ABORT,'pending media objects cannot be replaced'); END;
