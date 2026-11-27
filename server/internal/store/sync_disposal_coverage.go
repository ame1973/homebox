package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncDisposalUncovered = errors.New("current disposal projections do not cover source")

// Observe retained Disposal decisions in one caller-owned transaction. A void is
// not zero proceeds, historical tombstone proof or a complete snapshot.
type syncDisposalCoverage struct {
	Active int64
	Voided int64
}

func verifySyncDisposalCoverage(ctx context.Context, tx *sql.Tx) (syncDisposalCoverage, error) {
	if ctx == nil || tx == nil {
		return syncDisposalCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncDisposalCoverage{}, err
	}
	var coverage syncDisposalCoverage
	after, continued := "", false
	for {
		ids, err := readSyncCoverageIDs(ctx, tx, "disposal", after, continued)
		if err != nil {
			return syncDisposalCoverage{}, err
		}
		for _, id := range ids {
			active, err := verifySyncDisposalCoverageEntry(ctx, tx, id)
			if err != nil {
				return syncDisposalCoverage{}, err
			}
			count := &coverage.Voided
			if active {
				count = &coverage.Active
			}
			if *count >= maxSafeInteger {
				return syncDisposalCoverage{}, ErrDomainState
			}
			*count++
		}
		if len(ids) < 100 {
			break
		}
		after, continued = ids[len(ids)-1], true
	}
	var extra bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM current_projections c
 LEFT JOIN asset_disposals d ON d.item_unit_id=c.entity_id WHERE c.entity_type='disposal' AND d.item_unit_id IS NULL)
 OR EXISTS(SELECT 1 FROM asset_disposal_corrections c LEFT JOIN asset_disposals d ON d.item_unit_id=c.item_unit_id WHERE d.item_unit_id IS NULL)`).Scan(&extra); err != nil {
		return syncDisposalCoverage{}, err
	}
	if extra {
		return syncDisposalCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncDisposalCoverage{}, err
	}
	return coverage, nil
}

func verifySyncDisposalCoverageEntry(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var bounded bool
	err := tx.QueryRowContext(ctx, `SELECT length(CAST(kind AS BLOB))<=9 AND length(CAST(disposed_at AS BLOB))<=35 AND
 length(CAST(currency AS BLOB))<=3 AND length(CAST(note AS BLOB))<=40000 AND length(CAST(created_at AS BLOB))<=? AND
 (mutation_event_id IS NULL OR length(CAST(mutation_event_id AS BLOB))<=512) FROM asset_disposals WHERE item_unit_id=?`, maxSyncPageBytes, id).Scan(&bounded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDomainState
	}
	if err != nil {
		return false, err
	}
	if !bounded {
		return false, ErrDomainState
	}
	var oversized bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM asset_disposal_corrections WHERE item_unit_id=? AND
 (length(CAST(context_json AS BLOB))>? OR length(CAST(reason AS BLOB))>40000 OR length(CAST(action AS BLOB))>7 OR
 length(CAST(mutation_event_id AS BLOB))>512 OR (previous_correction_id IS NOT NULL AND length(CAST(previous_correction_id AS BLOB))>512) OR
 length(CAST(created_at AS BLOB))>35))`, id, maxSyncPageBytes).Scan(&oversized)
	if err != nil {
		return false, err
	}
	if oversized {
		return false, ErrDomainState
	}
	// ponytail: validate each full correction chain per Disposal; batch within
	// this transaction if measured snapshot latency requires it.
	record, err := loadDisposalRecord(ctx, tx, id)
	if errors.Is(err, ErrNotFound) {
		return false, ErrDomainState
	}
	if err != nil {
		return false, err
	}
	if record.Current == nil {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM current_projections WHERE entity_type='disposal' AND entity_id=?)", id).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, ErrDomainState
		}
		return false, nil
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='disposal' AND entity_id=?`, maxSyncPageBytes, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errSyncDisposalUncovered
	}
	if err != nil {
		return false, err
	}
	if !payload.Valid {
		return false, errSyncPayloadTooLarge
	}
	if err = validateCurrentProjectionSource(ctx, tx, "disposal", id, record.Revision, []byte(payload.String)); err != nil {
		return false, err
	}
	return true, nil
}
