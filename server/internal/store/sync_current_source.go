package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
)

// Compare exact persisted current bytes and their counter with selected source
// fields in the same transaction. This does not certify every dependent owner,
// parent, history or cross-domain invariant. The caller must separately establish
// those, session authority, complete source coverage and retention. Never compare
// historical frozen deltas with today's changing source. SQL, source loading and
// search aggregation can allocate beyond the payload budget.
func validateCurrentProjectionSource(ctx context.Context, tx *sql.Tx, kind, identity string, revision int64, payload []byte) error {
	if ctx == nil || tx == nil {
		return ErrDomainState
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(payload) > maxSyncPageBytes {
		return errSyncPayloadTooLarge
	}
	if err := validateSyncRecord(kind, identity, "upsert", revision, 1, payload); err != nil {
		return err
	}
	var storedRevision, version int64
	var counter sql.NullInt64
	var stored, updated sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT c.entity_revision,c.projection_version,
 CASE WHEN length(CAST(c.payload_json AS BLOB))<=? THEN c.payload_json END,
 CASE WHEN length(CAST(c.updated_at AS BLOB))<=40000 THEN c.updated_at END,r.revision
 FROM current_projections c LEFT JOIN projection_revisions r ON r.entity_type=c.entity_type AND r.entity_id=c.entity_id
 WHERE c.entity_type=? AND c.entity_id=?`, maxSyncPageBytes, kind, identity).
		Scan(&storedRevision, &version, &stored, &updated, &counter)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if !stored.Valid {
		return errSyncPayloadTooLarge
	}
	if storedRevision != revision || version != 1 || !counter.Valid || counter.Int64 != revision ||
		!updated.Valid || !authString(updated.String, 1, 10000) || stored.String != string(payload) {
		return ErrDomainState
	}
	if kind == "search_doc" {
		return validateSearchProjectionSource(ctx, tx, identity, revision, payload)
	}
	actual, err := readProjectionSource(ctx, tx, projectionKey{kind, identity})
	if errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if len(actual) > maxSyncPageBytes {
		return errSyncPayloadTooLarge
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = validateSyncRecord(kind, identity, "upsert", revision, 1, actual); err != nil {
		return err
	}
	// Version-one payloads are flat scalar objects. Both were losslessly
	// validated, including safe integer bounds, before semantic comparison.
	var supplied, source map[string]any
	if json.Unmarshal(payload, &supplied) != nil || json.Unmarshal(actual, &source) != nil || !reflect.DeepEqual(supplied, source) {
		return ErrDomainState
	}
	return ctx.Err()
}
