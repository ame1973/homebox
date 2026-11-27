package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncItemTagUncovered = errors.New("current item-tag projections do not cover source")

// Observe present Item/Tag memberships in one caller-owned transaction. This
// does not prove deleted membership history, session authority or a snapshot.
func verifySyncItemTagCoverage(ctx context.Context, tx *sql.Tx) (int64, error) {
	if ctx == nil || tx == nil {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int64
	afterItem, afterTag, continued := "", "", false
	for {
		pairs, err := readSyncItemPairSources(ctx, tx, "item_tag", afterItem, afterTag, continued)
		if err != nil {
			return 0, err
		}
		for _, pair := range pairs {
			if err = verifySyncItemTagCoverageEntry(ctx, tx, pair); err != nil {
				return 0, err
			}
			if count >= maxSafeInteger {
				return 0, ErrDomainState
			}
			count++
		}
		if len(pairs) < 100 {
			break
		}
		last := pairs[len(pairs)-1]
		afterItem, afterTag, continued = last.ItemID, last.RelatedID, true
	}
	var actual int64
	after, continued := "", false
	for {
		ids, err := readSyncPairProjectionIDs(ctx, tx, "item_tag", after, continued)
		if err != nil {
			return 0, err
		}
		for _, identity := range ids {
			parts, _ := projectionPairParts(identity) // The page reader validated canonical spelling.
			var exists bool
			if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM item_tags WHERE item_id=? AND tag_id=?)", parts[0], parts[1]).Scan(&exists); err != nil {
				return 0, err
			}
			if !exists || actual >= maxSafeInteger {
				return 0, ErrDomainState
			}
			actual++
		}
		if len(ids) < 100 {
			break
		}
		after, continued = ids[len(ids)-1], true
	}
	if actual != count {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func verifySyncItemTagCoverageEntry(ctx context.Context, tx *sql.Tx, pair syncItemPair) error {
	// ponytail: validate each actual owner per membership; batch within the same
	// transaction if measured snapshot latency requires it.
	if _, err := loadItem(ctx, tx, pair.ItemID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		return err
	}
	if _, err := loadTag(ctx, tx, pair.RelatedID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		return err
	}
	identity := projectionPair(pair.ItemID, pair.RelatedID)
	var revision int64
	err := tx.QueryRowContext(ctx, "SELECT revision FROM projection_revisions WHERE entity_type='item_tag' AND entity_id=?", identity).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if revision < 1 || revision > maxSafeInteger {
		return ErrDomainState
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='item_tag' AND entity_id=?`, maxSyncPageBytes, identity).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncItemTagUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, "item_tag", identity, revision, []byte(payload.String))
}
