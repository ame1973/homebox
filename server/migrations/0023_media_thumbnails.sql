-- Existing media remain unverified claims and do not acquire thumbnails.
-- Original file references remain in media; JPEG thumbnail object names are
-- derived from this independently retained digest, not supplied file paths.
CREATE TABLE media_thumbnails (
 media_id TEXT PRIMARY KEY NOT NULL REFERENCES media(id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL CHECK(length(sha256)=64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
 width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 512),
 height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 512),
 bytes INTEGER NOT NULL CHECK(bytes BETWEEN 1 AND 528777216)
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_media_thumbnails_sha256 ON media_thumbnails(sha256,media_id);

CREATE TRIGGER media_thumbnails_verified_insert BEFORE INSERT ON media_thumbnails
 WHEN NOT EXISTS(SELECT 1 FROM media WHERE id=NEW.media_id AND verification='verified')
 BEGIN SELECT RAISE(ABORT,'thumbnail requires verified media'); END;
CREATE TRIGGER media_thumbnails_verified_update BEFORE UPDATE ON media_thumbnails
 WHEN NOT EXISTS(SELECT 1 FROM media WHERE id=NEW.media_id AND verification='verified')
 BEGIN SELECT RAISE(ABORT,'thumbnail requires verified media'); END;
CREATE TRIGGER media_thumbnails_owner_immutable BEFORE UPDATE OF media_id ON media_thumbnails
 WHEN NEW.media_id<>OLD.media_id
 BEGIN SELECT RAISE(ABORT,'thumbnail owner is immutable'); END;
CREATE TRIGGER media_thumbnails_no_replace BEFORE INSERT ON media_thumbnails
 WHEN EXISTS(SELECT 1 FROM media_thumbnails WHERE media_id=NEW.media_id)
 BEGIN SELECT RAISE(ABORT,'thumbnail cannot be replaced'); END;
CREATE TRIGGER media_thumbnails_verified_parent BEFORE UPDATE OF verification ON media
 WHEN NEW.verification<>'verified' AND EXISTS(SELECT 1 FROM media_thumbnails WHERE media_id=OLD.id)
 BEGIN SELECT RAISE(ABORT,'unverified media cannot retain thumbnails'); END;
