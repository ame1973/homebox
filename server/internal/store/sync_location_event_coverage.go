package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncLocationEventUncovered = errors.New("current location event projections do not cover source")

// Observe retained movement source and current projection coverage in one
// caller-owned transaction. This is not complete Location/source coverage,
// tombstone-history proof, session authority, retention or a snapshot producer.
func verifySyncLocationEventCoverage(ctx context.Context, tx *sql.Tx) (int64, error) {
	if ctx == nil || tx == nil {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int64
	after, continued := "", false
	for {
		ids, err := readSyncCoverageIDs(ctx, tx, "location_event", after, continued)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if err = verifySyncLocationEventCoverageEntry(ctx, tx, id); err != nil {
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
 LEFT JOIN location_events e ON e.id=c.entity_id WHERE c.entity_type='location_event' AND e.id IS NULL)`).Scan(&extra); err != nil {
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

func verifySyncLocationEventCoverageEntry(ctx context.Context, tx *sql.Tx, id string) error {
	var owner sql.NullString
	var fieldsBounded bool
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(location_id AS BLOB))<=512 THEN location_id END,
 length(CAST(occurred_at AS BLOB))<=40000 AND
 (from_parent_id IS NULL OR length(CAST(from_parent_id AS BLOB))<=512) AND
 (to_parent_id IS NULL OR length(CAST(to_parent_id AS BLOB))<=512)
 FROM location_events WHERE id=?`, id).Scan(&owner, &fieldsBounded)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if !owner.Valid || !validDomainID(owner.String) || !fieldsBounded {
		return ErrDomainState
	}
	if _, err = scanLocationMovement(tx.QueryRowContext(ctx, locationMovementSelect+" WHERE e.id=?", id), owner.String); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDomainState
		}
		return err
	}
	// The owner may be logically deleted; its retained source still needs its
	// own counter and, if live, a valid path and matching current projection.
	// ponytail: this repeats owner/path checks per event; batch them if measured
	// snapshot latency requires it, while preserving one-transaction validation.
	if _, err = verifySyncLocationCoverageEntry(ctx, tx, owner.String); err != nil {
		return err
	}
	var revision int64
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT entity_revision,CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='location_event' AND entity_id=?`, maxSyncPageBytes, id).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncLocationEventUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, "location_event", id, revision, []byte(payload.String))
}
