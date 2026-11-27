package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncInventoryEventUncovered = errors.New("current inventory event projections do not cover source")

// Observe retained inventory events and their current projections in one caller-
// owned transaction. This does not establish complete history or a snapshot.
func verifySyncInventoryEventCoverage(ctx context.Context, tx *sql.Tx) (int64, error) {
	if ctx == nil || tx == nil {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int64
	after, continued := "", false
	for {
		ids, err := readSyncCoverageIDs(ctx, tx, "inventory_event", after, continued)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if err = verifySyncInventoryEventCoverageEntry(ctx, tx, id); err != nil {
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
 LEFT JOIN inventory_events e ON e.id=c.entity_id WHERE c.entity_type='inventory_event' AND e.id IS NULL)`).Scan(&extra); err != nil {
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

func verifySyncInventoryEventCoverageEntry(ctx context.Context, tx *sql.Tx, id string) error {
	var bounded bool
	err := tx.QueryRowContext(ctx, `SELECT length(CAST(item_id AS BLOB))<=512 AND
 length(CAST(action AS BLOB))<=40000 AND length(CAST(occurred_at AS BLOB))<=? AND
 length(CAST(note AS BLOB))<=40000 AND length(CAST(reason AS BLOB))<=40000 AND
 (item_unit_id IS NULL OR length(CAST(item_unit_id AS BLOB))<=512) AND
 (from_location_id IS NULL OR length(CAST(from_location_id AS BLOB))<=512) AND
 (to_location_id IS NULL OR length(CAST(to_location_id AS BLOB))<=512)
 FROM inventory_events WHERE id=?`, maxSyncPageBytes, id).Scan(&bounded)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if !bounded {
		return ErrDomainState
	}
	event, err := loadInventoryEvent(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	// ponytail: validate actual owners and Location paths per event; batch within
	// the transaction if measured snapshot latency requires it.
	if _, err = loadItem(ctx, tx, event.ItemID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		return err
	}
	if event.ItemUnitID != nil {
		unit, unitErr := loadUnit(ctx, tx, *event.ItemUnitID)
		if unitErr != nil && !errors.Is(unitErr, ErrNotFound) {
			return unitErr
		}
		// Deletion deliberately retains historical Unit IDs. A still-present Unit
		// must belong to the same Item, but absence is not proof of a tombstone.
		if unitErr == nil && unit.ItemID != event.ItemID {
			return ErrDomainState
		}
	}
	for _, location := range []*string{event.FromLocationID, event.ToLocationID} {
		if location != nil {
			if _, err = verifySyncLocationCoverageEntry(ctx, tx, *location); err != nil {
				return err
			}
		}
	}
	var revision int64
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT entity_revision,CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='inventory_event' AND entity_id=?`, maxSyncPageBytes, id).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncInventoryEventUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, "inventory_event", id, revision, []byte(payload.String))
}
