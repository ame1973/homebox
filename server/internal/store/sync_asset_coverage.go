package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncAssetUncovered = errors.New("current asset profiles and event projections do not cover source")

// Observe Asset profiles, Usage and Cost sources in one caller-owned transaction.
// These counts are not financial totals, disposal coverage or snapshot authority.
type syncAssetCoverage struct {
	Profiles int64
	Usage    int64
	Costs    int64
}

func verifySyncAssetCoverage(ctx context.Context, tx *sql.Tx) (syncAssetCoverage, error) {
	if ctx == nil || tx == nil {
		return syncAssetCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncAssetCoverage{}, err
	}
	var coverage syncAssetCoverage
	for _, kind := range []string{"asset", "usage", "cost"} {
		after, continued := "", false
		for {
			ids, err := readSyncCoverageIDs(ctx, tx, kind, after, continued)
			if err != nil {
				return syncAssetCoverage{}, err
			}
			for _, id := range ids {
				if err = verifySyncAssetCoverageEntry(ctx, tx, kind, id); err != nil {
					return syncAssetCoverage{}, err
				}
				count := &coverage.Profiles
				switch kind {
				case "usage":
					count = &coverage.Usage
				case "cost":
					count = &coverage.Costs
				}
				if *count >= maxSafeInteger {
					return syncAssetCoverage{}, ErrDomainState
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
 LEFT JOIN asset_profiles a ON c.entity_type='asset' AND a.item_unit_id=c.entity_id
 LEFT JOIN asset_usage_events u ON c.entity_type='usage' AND u.id=c.entity_id
 LEFT JOIN asset_cost_events x ON c.entity_type='cost' AND x.id=c.entity_id
 WHERE (c.entity_type='asset' AND a.item_unit_id IS NULL) OR (c.entity_type='usage' AND u.id IS NULL)
 OR (c.entity_type='cost' AND x.id IS NULL))`).Scan(&extra); err != nil {
		return syncAssetCoverage{}, err
	}
	if extra {
		return syncAssetCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncAssetCoverage{}, err
	}
	return coverage, nil
}

func verifySyncAssetCoverageEntry(ctx context.Context, tx *sql.Tx, kind, id string) error {
	var bounded bool
	var revision int64
	var err error
	switch kind {
	case "asset":
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(cost_basis AS BLOB))<=8 AND length(CAST(currency AS BLOB))<=3 AND
 length(CAST(usage_unit AS BLOB))<=7 AND (in_service_at IS NULL OR length(CAST(in_service_at AS BLOB))<=35) AND
 (retired_at IS NULL OR length(CAST(retired_at AS BLOB))<=35) FROM asset_profiles WHERE item_unit_id=?`, id).Scan(&bounded)
	case "usage":
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(item_unit_id AS BLOB))<=512 AND length(CAST(usage_unit AS BLOB))<=7 AND
 length(CAST(event_type AS BLOB))<=10 AND length(CAST(occurred_at AS BLOB))<=35 AND length(CAST(note AS BLOB))<=40000 AND
 (correction_of IS NULL OR length(CAST(correction_of AS BLOB))<=512) AND length(CAST(created_at AS BLOB))<=? AND
 (mutation_event_id IS NULL OR length(CAST(mutation_event_id AS BLOB))<=512) FROM asset_usage_events WHERE id=?`, maxSyncPageBytes, id).Scan(&bounded)
	case "cost":
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(item_unit_id AS BLOB))<=512 AND length(CAST(direction AS BLOB))<=7 AND
 length(CAST(currency AS BLOB))<=3 AND length(CAST(kind AS BLOB))<=11 AND length(CAST(occurred_at AS BLOB))<=35 AND
 length(CAST(note AS BLOB))<=40000 AND (correction_of IS NULL OR length(CAST(correction_of AS BLOB))<=512) AND
 length(CAST(created_at AS BLOB))<=? AND (mutation_event_id IS NULL OR length(CAST(mutation_event_id AS BLOB))<=512)
 FROM asset_cost_events WHERE id=?`, maxSyncPageBytes, id).Scan(&bounded)
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
	// ponytail: validate each real owner/correction chain per source; batch within
	// this transaction if measured snapshot latency requires it.
	switch kind {
	case "asset":
		var value Asset
		value, err = loadAsset(ctx, tx, id)
		revision = value.Revision
	case "usage":
		var value Usage
		value, err = loadUsage(ctx, tx, id)
		revision = value.Revision
	case "cost":
		var value Cost
		value, err = loadCost(ctx, tx, id)
		revision = value.Revision
	}
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type=? AND entity_id=?`, maxSyncPageBytes, kind, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncAssetUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, kind, id, revision, []byte(payload.String))
}
