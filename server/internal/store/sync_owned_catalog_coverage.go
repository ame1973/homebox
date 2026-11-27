package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncOwnedCatalogUncovered = errors.New("current owned catalog projections do not cover source")

// Observe present independently identified Barcode, Alias and Attribute rows in
// one caller-owned transaction; counter-only rows are not deletion-history proof.
type syncOwnedCatalogCoverage struct {
	Barcodes   int64
	Aliases    int64
	Attributes int64
}

func verifySyncOwnedCatalogCoverage(ctx context.Context, tx *sql.Tx) (syncOwnedCatalogCoverage, error) {
	if ctx == nil || tx == nil {
		return syncOwnedCatalogCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncOwnedCatalogCoverage{}, err
	}
	var coverage syncOwnedCatalogCoverage
	for _, kind := range []string{"barcode", "alias", "attribute"} {
		after, continued := "", false
		for {
			ids, err := readSyncCoverageIDs(ctx, tx, kind, after, continued)
			if err != nil {
				return syncOwnedCatalogCoverage{}, err
			}
			for _, id := range ids {
				if err = verifySyncOwnedCatalogCoverageEntry(ctx, tx, kind, id); err != nil {
					return syncOwnedCatalogCoverage{}, err
				}
				count := &coverage.Barcodes
				switch kind {
				case "alias":
					count = &coverage.Aliases
				case "attribute":
					count = &coverage.Attributes
				}
				if *count >= maxSafeInteger {
					return syncOwnedCatalogCoverage{}, ErrDomainState
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
 LEFT JOIN item_barcodes b ON c.entity_type='barcode' AND b.id=c.entity_id
 LEFT JOIN item_aliases a ON c.entity_type='alias' AND a.id=c.entity_id
 LEFT JOIN item_attributes v ON c.entity_type='attribute' AND v.id=c.entity_id
 WHERE (c.entity_type='barcode' AND b.id IS NULL) OR (c.entity_type='alias' AND a.id IS NULL)
 OR (c.entity_type='attribute' AND v.id IS NULL))`).Scan(&extra); err != nil {
		return syncOwnedCatalogCoverage{}, err
	}
	if extra {
		return syncOwnedCatalogCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncOwnedCatalogCoverage{}, err
	}
	return coverage, nil
}

func verifySyncOwnedCatalogCoverageEntry(ctx context.Context, tx *sql.Tx, kind, id string) error {
	var owner string
	var revision int64
	var err error
	switch kind {
	case "barcode":
		var value Barcode
		value, err = loadBarcode(ctx, tx, id)
		owner, revision = value.ItemID, value.Revision
	case "alias":
		var value Alias
		value, err = loadAlias(ctx, tx, id)
		owner, revision = value.ItemID, value.Revision
	case "attribute":
		var value Attribute
		value, err = loadAttribute(ctx, tx, id)
		owner, revision = value.ItemID, value.Revision
	default:
		return ErrDomainState
	}
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	// ponytail: revalidate each owner independently; batch within the same
	// transaction if measured snapshot latency requires it.
	if _, err = loadItem(ctx, tx, owner); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		return err
	}
	var payload sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type=? AND entity_id=?`, maxSyncPageBytes, kind, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncOwnedCatalogUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, kind, id, revision, []byte(payload.String))
}
