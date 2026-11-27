package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncApprovedSearchUncovered = errors.New("approved current search projections do not cover source")

// A read-only observation of Item/Unit search sources and current search rows in
// one caller-owned transaction. It is not historical proof, session authority,
// full cross-domain coverage, retention or a snapshot producer.
type syncApprovedSearchCoverage struct {
	Items int64
	Units int64
}

func verifySyncApprovedSearchCoverage(ctx context.Context, tx *sql.Tx) (syncApprovedSearchCoverage, error) {
	if ctx == nil || tx == nil {
		return syncApprovedSearchCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncApprovedSearchCoverage{}, err
	}
	dataset, err := readSyncDataset(ctx, tx)
	if err != nil {
		return syncApprovedSearchCoverage{}, err
	}
	if _, _, err = readSyncWatermark(ctx, tx, dataset.Floor); err != nil {
		return syncApprovedSearchCoverage{}, err
	}
	var coverage syncApprovedSearchCoverage
	for _, kind := range []string{"item", "unit"} {
		after, continued := "", false
		for {
			ids, err := readSyncCoverageIDs(ctx, tx, kind, after, continued)
			if err != nil {
				return syncApprovedSearchCoverage{}, err
			}
			for _, id := range ids {
				// ponytail: approved text and retained history are checked per source;
				// batch within this transaction if measured snapshot latency requires it.
				entry, err := readSyncSearchCoverageEntry(ctx, tx, dataset, kind, id)
				if err != nil {
					return syncApprovedSearchCoverage{}, err
				}
				if !entry.Current {
					return syncApprovedSearchCoverage{}, errSyncApprovedSearchUncovered
				}
				count := &coverage.Items
				if kind == "unit" {
					count = &coverage.Units
				}
				if *count >= maxSafeInteger {
					return syncApprovedSearchCoverage{}, ErrDomainState
				}
				*count++
			}
			if len(ids) < 100 {
				break
			}
			after, continued = ids[len(ids)-1], true
		}
	}
	var actual syncApprovedSearchCoverage
	after, continued := "", false
	for {
		ids, err := readSyncPairProjectionIDs(ctx, tx, "search_doc", after, continued)
		if err != nil {
			return syncApprovedSearchCoverage{}, err
		}
		for _, identity := range ids {
			parts, _ := projectionPairParts(identity) // The page reader validated canonical spelling.
			query, count := "SELECT EXISTS(SELECT 1 FROM items WHERE id=?)", &actual.Items
			if parts[0] == "unit" {
				query, count = "SELECT EXISTS(SELECT 1 FROM item_units WHERE id=?)", &actual.Units
			}
			var exists bool
			if err = tx.QueryRowContext(ctx, query, parts[1]).Scan(&exists); err != nil {
				return syncApprovedSearchCoverage{}, err
			}
			if !exists || *count >= maxSafeInteger {
				return syncApprovedSearchCoverage{}, ErrDomainState
			}
			*count++
		}
		if len(ids) < 100 {
			break
		}
		after, continued = ids[len(ids)-1], true
	}
	if actual != coverage {
		return syncApprovedSearchCoverage{}, ErrDomainState
	}
	if err = ctx.Err(); err != nil {
		return syncApprovedSearchCoverage{}, err
	}
	return coverage, nil
}

func readSyncPairProjectionIDs(ctx context.Context, tx *sql.Tx, kind, after string, continued bool) ([]string, error) {
	if kind != "search_doc" && kind != "stock" {
		return nil, ErrDomainState
	}
	query := `SELECT CASE WHEN length(CAST(entity_id AS BLOB))<=? THEN entity_id END
 FROM current_projections WHERE entity_type=?`
	args := []any{maxSyncIdentityBytes, kind}
	if continued {
		query += " AND entity_id>?"
		args = append(args, after)
	}
	query += " ORDER BY entity_id LIMIT 100"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, 100)
	previous := after
	for rows.Next() {
		var id sql.NullString
		if err = rows.Scan(&id); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if !id.Valid || !validProjectionIdentity(kind, id.String) || (continued || len(ids) > 0) && id.String <= previous {
			return nil, errors.Join(ErrDomainState, rows.Close())
		}
		ids = append(ids, id.String)
		previous = id.String
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return ids, nil
}
