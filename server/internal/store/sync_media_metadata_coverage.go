package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncMediaMetadataUncovered = errors.New("current media metadata projections do not cover source")

// Observe present Media claims and independent links in one caller-owned SQL
// transaction. This does not open or verify files or establish deletion history.
type syncMediaMetadataCoverage struct {
	Media int64
	Links int64
}

func verifySyncMediaMetadataCoverage(ctx context.Context, tx *sql.Tx) (syncMediaMetadataCoverage, error) {
	if ctx == nil || tx == nil {
		return syncMediaMetadataCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncMediaMetadataCoverage{}, err
	}
	var coverage syncMediaMetadataCoverage
	for _, kind := range []string{"media", "media_link"} {
		after, continued := "", false
		for {
			ids, err := readSyncCoverageIDs(ctx, tx, kind, after, continued)
			if err != nil {
				return syncMediaMetadataCoverage{}, err
			}
			for _, id := range ids {
				if err = verifySyncMediaMetadataCoverageEntry(ctx, tx, kind, id); err != nil {
					return syncMediaMetadataCoverage{}, err
				}
				count := &coverage.Media
				if kind == "media_link" {
					count = &coverage.Links
				}
				if *count >= maxSafeInteger {
					return syncMediaMetadataCoverage{}, ErrDomainState
				}
				*count++
			}
			if len(ids) < 100 {
				break
			}
			after, continued = ids[len(ids)-1], true
		}
	}
	var extra bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM current_projections c
 LEFT JOIN media m ON c.entity_type='media' AND m.id=c.entity_id
 LEFT JOIN media_links l ON c.entity_type='media_link' AND l.id=c.entity_id
 WHERE (c.entity_type='media' AND m.id IS NULL) OR (c.entity_type='media_link' AND l.id IS NULL))
 OR EXISTS(SELECT 1 FROM media_thumbnails t LEFT JOIN media m ON m.id=t.media_id WHERE m.id IS NULL)`).Scan(&extra); err != nil {
		return syncMediaMetadataCoverage{}, err
	}
	if extra {
		return syncMediaMetadataCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncMediaMetadataCoverage{}, err
	}
	return coverage, nil
}

func verifySyncMediaMetadataCoverageEntry(ctx context.Context, tx *sql.Tx, kind, id string) error {
	var bounded bool
	var revision int64
	var err error
	switch kind {
	case "media":
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(m.sha256 AS BLOB))<=64 AND
 length(CAST(m.mime_type AS BLOB))<=400 AND (m.perceptual_hash IS NULL OR length(CAST(m.perceptual_hash AS BLOB))<=64) AND
 length(CAST(m.verification AS BLOB))<=10 AND length(CAST(m.storage_key AS BLOB))<=? AND length(CAST(m.created_at AS BLOB))<=? AND
 (t.sha256 IS NULL OR length(CAST(t.sha256 AS BLOB))<=64)
 FROM media m LEFT JOIN media_thumbnails t ON t.media_id=m.id WHERE m.id=?`, maxSyncPageBytes, maxSyncPageBytes, id).Scan(&bounded)
	case "media_link":
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(media_id AS BLOB))<=512 AND
 (item_id IS NULL OR length(CAST(item_id AS BLOB))<=512) AND
 (item_unit_id IS NULL OR length(CAST(item_unit_id AS BLOB))<=512) AND
 (location_id IS NULL OR length(CAST(location_id AS BLOB))<=512) AND
 length(CAST(description AS BLOB))<=40000 FROM media_links WHERE id=?`, id).Scan(&bounded)
	default:
		return ErrDomainState
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if !bounded {
		return ErrDomainState
	}
	if kind == "media" {
		value, err := loadMedia(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
		revision = value.Revision
	} else {
		// ponytail: revalidate each actual Media and owner per link; batch within
		// this transaction if measured snapshot latency requires it.
		value, err := loadMediaLink(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
		revision = value.Revision
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type=? AND entity_id=?`, maxSyncPageBytes, kind, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncMediaMetadataUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, kind, id, revision, []byte(payload.String))
}
