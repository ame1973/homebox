package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncUnitUncovered = errors.New("current unit projections do not cover source")

// Observe all present Unit sources and their current projections in one
// caller-owned transaction. Retained counters alone are not deletion history.
func verifySyncUnitCoverage(ctx context.Context, tx *sql.Tx) (int64, error) {
	if ctx == nil || tx == nil {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int64
	after, continued := "", false
	for {
		ids, err := readSyncCoverageIDs(ctx, tx, "unit", after, continued)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if err = verifySyncUnitCoverageEntry(ctx, tx, id); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM current_projections c
 LEFT JOIN item_units u ON u.id=c.entity_id WHERE c.entity_type='unit' AND u.id IS NULL)`).Scan(&extra); err != nil {
		return 0, err
	}
	if extra {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func verifySyncUnitCoverageEntry(ctx context.Context, tx *sql.Tx, id string) error {
	unit, err := loadUnit(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if _, err = loadItem(ctx, tx, unit.ItemID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		return err
	}
	if unit.LocationID != nil {
		// ponytail: validate each actual ancestor path per Unit; batch within the
		// same transaction if measured snapshot latency requires it.
		if _, err = holdingLocationPath(ctx, tx, *unit.LocationID, false); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrDomainState
			}
			return err
		}
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='unit' AND entity_id=?`, maxSyncPageBytes, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncUnitUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, "unit", id, unit.Revision, []byte(payload.String))
}
