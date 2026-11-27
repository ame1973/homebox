package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncLocationUncovered = errors.New("current location projections do not cover source")

// A complete observation of retained Location source in one caller-owned
// transaction. This is not session authority, tombstone-history verification,
// coverage of other kinds, a snapshot producer or permission to enable sync.
type syncLocationCoverage struct {
	Live    int64
	Deleted int64
}

func verifySyncLocationCoverage(ctx context.Context, tx *sql.Tx) (syncLocationCoverage, error) {
	if ctx == nil || tx == nil {
		return syncLocationCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncLocationCoverage{}, err
	}
	var coverage syncLocationCoverage
	after, continued := "", false
	for {
		ids, err := readSyncCoverageIDs(ctx, tx, "location", after, continued)
		if err != nil {
			return syncLocationCoverage{}, err
		}
		for _, id := range ids {
			deleted, err := verifySyncLocationCoverageEntry(ctx, tx, id)
			if err != nil {
				return syncLocationCoverage{}, err
			}
			count := &coverage.Live
			if deleted {
				count = &coverage.Deleted
			}
			if *count >= maxSafeInteger {
				return syncLocationCoverage{}, ErrDomainState
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
 LEFT JOIN locations l ON l.id=c.entity_id AND l.deleted_at IS NULL
 WHERE c.entity_type='location' AND l.id IS NULL)`).Scan(&extra); err != nil {
		return syncLocationCoverage{}, err
	}
	if extra {
		return syncLocationCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncLocationCoverage{}, err
	}
	return coverage, nil
}

func readSyncCoverageIDs(ctx context.Context, tx *sql.Tx, kind, after string, continued bool) ([]string, error) {
	var query string
	switch kind {
	case "location":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM locations"
	case "location_event":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM location_events"
	case "inventory_event":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM inventory_events"
	case "category":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM categories"
	case "tag":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM tags"
	case "item":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM items"
	case "unit":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM item_units"
	case "barcode":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM item_barcodes"
	case "alias":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM item_aliases"
	case "attribute":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM item_attributes"
	case "purchase":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM purchases"
	case "purchase_line":
		query = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM purchase_lines"
	default:
		return nil, ErrDomainState
	}
	var args []any
	if continued {
		query += " WHERE id>?"
		args = append(args, after)
	}
	query += " ORDER BY id LIMIT 100"
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
		if !id.Valid || !validDomainID(id.String) || (continued || len(ids) > 0) && id.String <= previous {
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

func verifySyncLocationCoverageEntry(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	location, err := scanLocation(tx.QueryRowContext(ctx, locationSelect+" WHERE l.id=?", id))
	if errors.Is(err, ErrNotFound) {
		return false, ErrDomainState
	}
	if err != nil {
		return false, err
	}
	var deleted sql.NullString
	var isNull bool
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(deleted_at AS BLOB))<=40000 THEN deleted_at END,
 deleted_at IS NULL FROM locations WHERE id=?`, id).Scan(&deleted, &isNull)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDomainState
	}
	if err != nil {
		return false, err
	}
	if !isNull {
		if !deleted.Valid || !authString(deleted.String, 1, 10000) {
			return false, ErrDomainState
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM current_projections WHERE entity_type='location' AND entity_id=?)", id).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, ErrDomainState
		}
		return true, nil
	}
	if _, err = holdingLocationPath(ctx, tx, id, false); err != nil {
		return false, err
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='location' AND entity_id=?`, maxSyncPageBytes, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errSyncLocationUncovered
	}
	if err != nil {
		return false, err
	}
	if !payload.Valid {
		return false, errSyncPayloadTooLarge
	}
	if err = validateCurrentProjectionSource(ctx, tx, "location", id, location.Revision, []byte(payload.String)); err != nil {
		return false, err
	}
	return false, nil
}
