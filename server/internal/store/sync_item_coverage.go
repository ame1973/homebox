package store

import (
	"context"
	"database/sql"
	"errors"
)

var (
	errSyncItemUncovered       = errors.New("current item projections do not cover source")
	errSyncItemSearchUncovered = errors.New("current item search projections do not cover source")
)

// Observe present Item source and its approved current search in one transaction.
// This does not certify other entities, orphan search rows, session or retention.
func verifySyncItemCoverage(ctx context.Context, tx *sql.Tx) (int64, error) {
	if ctx == nil || tx == nil {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	dataset, err := readSyncDataset(ctx, tx)
	if err != nil {
		return 0, err
	}
	if _, _, err = readSyncWatermark(ctx, tx, dataset.Floor); err != nil {
		return 0, err
	}
	var count int64
	after, continued := "", false
	for {
		ids, err := readSyncCoverageIDs(ctx, tx, "item", after, continued)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if err = verifySyncItemCoverageEntry(ctx, tx, dataset, id); err != nil {
				return 0, err
			}
			if count >= maxSafeInteger {
				return 0, ErrDomainState
			}
			count++
		}
		if len(ids) < 100 {
			break
		}
		after, continued = ids[len(ids)-1], true
	}
	var extra bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM current_projections c
 LEFT JOIN items i ON i.id=c.entity_id WHERE c.entity_type='item' AND i.id IS NULL)`).Scan(&extra); err != nil {
		return 0, err
	}
	if extra {
		return 0, ErrDomainState
	}
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func verifySyncItemCoverageEntry(ctx context.Context, tx *sql.Tx, dataset syncDataset, id string) error {
	item, err := loadItem(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if item.CategoryID != nil {
		if err = verifySyncTaxonomyCoverageEntry(ctx, tx, "category", *item.CategoryID); err != nil {
			return err
		}
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='item' AND entity_id=?`, maxSyncPageBytes, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncItemUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	if err = validateCurrentProjectionSource(ctx, tx, "item", id, item.Revision, []byte(payload.String)); err != nil {
		return err
	}
	// ponytail: approved source and retained search history are read per Item;
	// batch within the same transaction if measured snapshot latency requires it.
	search, err := readSyncSearchCoverageEntry(ctx, tx, dataset, "item", id)
	if err != nil {
		return err
	}
	if !search.Current {
		return errSyncItemSearchUncovered
	}
	if search.EntityID != id || search.ItemID != id || search.ItemRevision != item.Revision {
		return ErrDomainState
	}
	return nil
}
