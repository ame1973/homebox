package store

import (
	"context"
	"database/sql"
	"errors"
)

var errSyncReviewUncovered = errors.New("current AI review projections do not cover source")

// Observe present review sources and current projections in one caller-owned
// transaction. This is not human/model provenance, history or sync readiness.
func verifySyncReviewCoverage(ctx context.Context, tx *sql.Tx) (int64, error) {
	if ctx == nil || tx == nil {
		return 0, ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var count int64
	after, continued := "", false
	for {
		ids, err := readSyncCoverageIDs(ctx, tx, "ai_review", after, continued)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			// ponytail: validate each selected source and owner independently; batch
			// within this transaction if measured snapshot latency requires it.
			source, err := loadSyncReviewSource(ctx, tx, id)
			if errors.Is(err, ErrNotFound) {
				return 0, ErrDomainState
			}
			if err != nil {
				return 0, err
			}
			var payload sql.NullString
			err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload_json AS BLOB))<=? THEN payload_json END
 FROM current_projections WHERE entity_type='ai_review' AND entity_id=?`, maxSyncPageBytes, id).Scan(&payload)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, errSyncReviewUncovered
			}
			if err != nil {
				return 0, err
			}
			if !payload.Valid {
				return 0, errSyncPayloadTooLarge
			}
			if err = validateCurrentProjectionSource(ctx, tx, "ai_review", id, source.Revision, []byte(payload.String)); err != nil {
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
 LEFT JOIN ai_review_entries r ON r.id=c.entity_id WHERE c.entity_type='ai_review' AND r.id IS NULL)`).Scan(&extra); err != nil {
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
