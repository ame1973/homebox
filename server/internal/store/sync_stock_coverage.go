package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncStockUncovered = errors.New("current stock projections do not cover source")

// Observe present Stock source and current projections in one caller-owned
// transaction. Counts distinguish zero balances; they do not total mixed units.
type syncStockCoverage struct {
	Nonzero int64
	Zero    int64
}

type syncItemPair struct {
	ItemID    string
	RelatedID string
}

func verifySyncStockCoverage(ctx context.Context, tx *sql.Tx) (syncStockCoverage, error) {
	if ctx == nil || tx == nil {
		return syncStockCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncStockCoverage{}, err
	}
	var coverage syncStockCoverage
	afterItem, afterLocation, continued := "", "", false
	for {
		pairs, err := readSyncItemPairSources(ctx, tx, "stock", afterItem, afterLocation, continued)
		if err != nil {
			return syncStockCoverage{}, err
		}
		for _, pair := range pairs {
			nonzero, err := verifySyncStockCoverageEntry(ctx, tx, pair)
			if err != nil {
				return syncStockCoverage{}, err
			}
			count := &coverage.Zero
			if nonzero {
				count = &coverage.Nonzero
			}
			if *count >= maxSafeInteger {
				return syncStockCoverage{}, ErrDomainState
			}
			*count++
		}
		if len(pairs) < 100 {
			break
		}
		last := pairs[len(pairs)-1]
		afterItem, afterLocation, continued = last.ItemID, last.RelatedID, true
	}
	if coverage.Nonzero > maxSafeInteger-coverage.Zero {
		return syncStockCoverage{}, ErrDomainState
	}
	var actual int64
	after, continued := "", false
	for {
		ids, err := readSyncPairProjectionIDs(ctx, tx, "stock", after, continued)
		if err != nil {
			return syncStockCoverage{}, err
		}
		for _, identity := range ids {
			parts, _ := projectionPairParts(identity) // The page reader validated canonical spelling.
			var exists bool
			if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM stock_balances WHERE item_id=? AND location_id=?)", parts[0], parts[1]).Scan(&exists); err != nil {
				return syncStockCoverage{}, err
			}
			if !exists || actual >= maxSafeInteger {
				return syncStockCoverage{}, ErrDomainState
			}
			actual++
		}
		if len(ids) < 100 {
			break
		}
		after, continued = ids[len(ids)-1], true
	}
	if actual != coverage.Nonzero+coverage.Zero {
		return syncStockCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncStockCoverage{}, err
	}
	return coverage, nil
}

// Source tuples use SQLite bytewise ordering, independently of JSON pair order.
func readSyncItemPairSources(ctx context.Context, tx *sql.Tx, kind, afterItem, afterRelated string, continued bool) ([]syncItemPair, error) {
	var table, related string
	switch kind {
	case "stock":
		table, related = "stock_balances", "location_id"
	case "item_tag":
		table, related = "item_tags", "tag_id"
	default:
		return nil, ErrDomainState
	}
	query := "SELECT CASE WHEN length(CAST(item_id AS BLOB))<=512 THEN item_id END, " +
		"CASE WHEN length(CAST(" + related + " AS BLOB))<=512 THEN " + related + " END FROM " + table
	var args []any
	if continued {
		query += " WHERE (item_id," + related + ")>(?,?)"
		args = append(args, afterItem, afterRelated)
	}
	query += " ORDER BY item_id," + related + " LIMIT 100"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	pairs := make([]syncItemPair, 0, 100)
	previous := syncItemPair{ItemID: afterItem, RelatedID: afterRelated}
	for rows.Next() {
		var item, other sql.NullString
		if err = rows.Scan(&item, &other); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if !item.Valid || !other.Valid || !validDomainID(item.String) || !validDomainID(other.String) ||
			(continued || len(pairs) > 0) && (item.String < previous.ItemID || item.String == previous.ItemID && other.String <= previous.RelatedID) {
			return nil, errors.Join(ErrDomainState, rows.Close())
		}
		pair := syncItemPair{ItemID: item.String, RelatedID: other.String}
		pairs = append(pairs, pair)
		previous = pair
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return pairs, nil
}

func verifySyncStockCoverageEntry(ctx context.Context, tx *sql.Tx, pair syncItemPair) (bool, error) {
	stock, err := loadStock(ctx, tx, pair.ItemID, pair.RelatedID)
	if errors.Is(err, ErrNotFound) {
		return false, ErrDomainState
	}
	if err != nil {
		return false, err
	}
	if _, err = loadItem(ctx, tx, pair.ItemID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, ErrDomainState
		}
		return false, err
	}
	// ponytail: validate each actual location path per balance; batch within the
	// same transaction if measured snapshot latency requires it.
	if _, err = holdingLocationPath(ctx, tx, pair.RelatedID, false); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, ErrDomainState
		}
		return false, err
	}
	identity := projectionPair(pair.ItemID, pair.RelatedID)
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='stock' AND entity_id=?`, maxSyncPageBytes, identity).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errSyncStockUncovered
	}
	if err != nil {
		return false, err
	}
	if !payload.Valid {
		return false, errSyncPayloadTooLarge
	}
	if err = validateCurrentProjectionSource(ctx, tx, "stock", identity, stock.Revision, []byte(payload.String)); err != nil {
		return false, err
	}
	return stock.QuantityBase > 0, nil
}
