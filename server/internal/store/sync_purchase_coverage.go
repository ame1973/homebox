package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncPurchaseUncovered = errors.New("current purchase projections do not cover source")

// Observe present Purchase headers and retained Lines in one caller-owned
// transaction. Voided source is counted separately, not treated as a tombstone proof.
type syncPurchaseCoverage struct {
	Headers     int64
	ActiveLines int64
	VoidedLines int64
}

func verifySyncPurchaseCoverage(ctx context.Context, tx *sql.Tx) (syncPurchaseCoverage, error) {
	if ctx == nil || tx == nil {
		return syncPurchaseCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncPurchaseCoverage{}, err
	}
	var coverage syncPurchaseCoverage
	for _, kind := range []string{"purchase", "purchase_line"} {
		after, continued := "", false
		for {
			ids, err := readSyncCoverageIDs(ctx, tx, kind, after, continued)
			if err != nil {
				return syncPurchaseCoverage{}, err
			}
			for _, id := range ids {
				active, err := verifySyncPurchaseCoverageEntry(ctx, tx, kind, id)
				if err != nil {
					return syncPurchaseCoverage{}, err
				}
				count := &coverage.Headers
				if kind == "purchase_line" {
					count = &coverage.VoidedLines
					if active {
						count = &coverage.ActiveLines
					}
				}
				if *count >= maxSafeInteger {
					return syncPurchaseCoverage{}, ErrDomainState
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
 LEFT JOIN purchases p ON c.entity_type='purchase' AND p.id=c.entity_id
 LEFT JOIN purchase_lines l ON c.entity_type='purchase_line' AND l.id=c.entity_id
 WHERE (c.entity_type='purchase' AND p.id IS NULL) OR (c.entity_type='purchase_line' AND l.id IS NULL))
 OR EXISTS(SELECT 1 FROM purchase_line_corrections c LEFT JOIN purchase_lines l ON l.id=c.line_id WHERE l.id IS NULL)`).Scan(&extra); err != nil {
		return syncPurchaseCoverage{}, err
	}
	if extra {
		return syncPurchaseCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncPurchaseCoverage{}, err
	}
	return coverage, nil
}

func verifySyncPurchaseCoverageEntry(ctx context.Context, tx *sql.Tx, kind, id string) (bool, error) {
	var bounded bool
	var revision int64
	var err error
	switch kind {
	case "purchase":
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(merchant_name AS BLOB))<=1200 AND
 length(CAST(purchased_at AS BLOB))<=35 AND length(CAST(currency AS BLOB))<=3 AND
 length(CAST(receipt_note AS BLOB))<=40000 FROM purchases WHERE id=?`, id).Scan(&bounded)
	case "purchase_line":
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(purchase_id AS BLOB))<=512 AND length(CAST(item_id AS BLOB))<=512 AND
 (item_unit_id IS NULL OR length(CAST(item_unit_id AS BLOB))<=512) AND length(CAST(note AS BLOB))<=40000 AND
 (created_at IS NULL OR length(CAST(created_at AS BLOB))<=35) AND
 (mutation_event_id IS NULL OR length(CAST(mutation_event_id AS BLOB))<=512) FROM purchase_lines WHERE id=?`, id).Scan(&bounded)
	default:
		return false, ErrDomainState
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDomainState
	}
	if err != nil {
		return false, err
	}
	if !bounded {
		return false, ErrDomainState
	}
	if kind == "purchase" {
		purchase, err := loadPurchase(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return false, ErrDomainState
		}
		if err != nil {
			return false, err
		}
		revision = purchase.Revision
	} else {
		var oversized bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM purchase_line_corrections WHERE line_id=? AND
 (length(CAST(context_json AS BLOB))>? OR length(CAST(reason AS BLOB))>40000 OR
 length(CAST(mutation_event_id AS BLOB))>512 OR length(CAST(purchase_id AS BLOB))>512 OR
 (replacement_line_id IS NOT NULL AND length(CAST(replacement_line_id AS BLOB))>512) OR
 length(CAST(created_at AS BLOB))>35))`, id, maxSyncPageBytes).Scan(&oversized)
		if err != nil {
			return false, err
		}
		if oversized {
			return false, ErrDomainState
		}
		// ponytail: validate each actual owner and correction per Line; batch within
		// the transaction if measured snapshot latency requires it.
		line, err := loadPurchaseLine(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return false, ErrDomainState
		}
		if err != nil {
			return false, err
		}
		active, err := purchaseLineIsActive(ctx, tx, line)
		if err != nil {
			return false, err
		}
		if !active {
			var exists bool
			if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?)", id).Scan(&exists); err != nil {
				return false, err
			}
			if exists {
				return false, ErrDomainState
			}
			return false, nil
		}
		revision = line.Revision
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type=? AND entity_id=?`, maxSyncPageBytes, kind, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errSyncPurchaseUncovered
	}
	if err != nil {
		return false, err
	}
	if !payload.Valid {
		return false, errSyncPayloadTooLarge
	}
	if err = validateCurrentProjectionSource(ctx, tx, kind, id, revision, []byte(payload.String)); err != nil {
		return false, err
	}
	return true, nil
}
