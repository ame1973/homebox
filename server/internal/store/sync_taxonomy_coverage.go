package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncTaxonomyUncovered = errors.New("current taxonomy projections do not cover source")

// A read-only observation of present Category and Tag sources in one caller-owned
// transaction. Counter-only deleted identities are not tombstone-history proof.
type syncTaxonomyCoverage struct {
	Categories int64
	Tags       int64
}

func verifySyncTaxonomyCoverage(ctx context.Context, tx *sql.Tx) (syncTaxonomyCoverage, error) {
	if ctx == nil || tx == nil {
		return syncTaxonomyCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncTaxonomyCoverage{}, err
	}
	var coverage syncTaxonomyCoverage
	for _, kind := range []string{"category", "tag"} {
		after, continued := "", false
		for {
			ids, err := readSyncCoverageIDs(ctx, tx, kind, after, continued)
			if err != nil {
				return syncTaxonomyCoverage{}, err
			}
			for _, id := range ids {
				if err = verifySyncTaxonomyCoverageEntry(ctx, tx, kind, id); err != nil {
					return syncTaxonomyCoverage{}, err
				}
				count := &coverage.Categories
				if kind == "tag" {
					count = &coverage.Tags
				}
				if *count >= maxSafeInteger {
					return syncTaxonomyCoverage{}, ErrDomainState
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
 LEFT JOIN categories ca ON c.entity_type='category' AND ca.id=c.entity_id
 LEFT JOIN tags t ON c.entity_type='tag' AND t.id=c.entity_id
 WHERE (c.entity_type='category' AND ca.id IS NULL) OR (c.entity_type='tag' AND t.id IS NULL))`).Scan(&extra); err != nil {
		return syncTaxonomyCoverage{}, err
	}
	if extra {
		return syncTaxonomyCoverage{}, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return syncTaxonomyCoverage{}, err
	}
	return coverage, nil
}

func verifySyncTaxonomyCoverageEntry(ctx context.Context, tx *sql.Tx, kind, id string) error {
	var revision int64
	switch kind {
	case "category":
		value, err := loadCategory(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
		// ponytail: derive and validate each category path separately; batch
		// ancestry in the same transaction if measured snapshot latency requires it.
		if _, err = categoryPathText(ctx, tx, &value.ID); err != nil {
			return err
		}
		revision = value.Revision
	case "tag":
		value, err := loadTag(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
		revision = value.Revision
	default:
		return ErrDomainState
	}
	var payload sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type=? AND entity_id=?`, maxSyncPageBytes, kind, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncTaxonomyUncovered
	}
	if err != nil {
		return err
	}
	if !payload.Valid {
		return errSyncPayloadTooLarge
	}
	return validateCurrentProjectionSource(ctx, tx, kind, id, revision, []byte(payload.String))
}
