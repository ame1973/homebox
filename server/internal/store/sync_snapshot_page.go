package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
)

var errSyncSnapshotUnproven = errors.New("frozen snapshot proof is not established")

type syncSnapshotPage struct {
	DatasetEpoch      string
	ServerInstanceID  string
	ProjectionVersion int64
	HighWatermark     int64
	Records           []syncRecord
	NextCursor        string
	DeltaCursor       string
	Complete          bool
}

// This is a private reader, not an HTTP or replica boundary. It can only trust
// a seal created by a future transaction that validates all eligible source,
// including reviewed search provenance. No such producer or coverage gate exists.
func (a *Auth) readSyncSnapshotPage(ctx context.Context, token, signed string, limits syncPageLimits) (syncSnapshotPage, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil || !limits.valid() {
		return syncSnapshotPage{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return syncSnapshotPage{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return syncSnapshotPage{}, err
	}
	defer tx.Rollback()
	session, pinned, err := a.beginSyncSnapshotRead(ctx, tx, token)
	if err != nil {
		return syncSnapshotPage{}, err
	}
	if !pinned {
		return syncSnapshotPage{}, errSyncSnapshotUnproven
	}
	metadata, err := a.readSyncSnapshotScope(ctx, tx, session, signed)
	if err != nil {
		return syncSnapshotPage{}, err
	}
	if err = a.verifySyncSnapshotSeal(ctx, tx, metadata); err != nil {
		return syncSnapshotPage{}, err
	}
	records, more, err := a.readSyncSnapshotRows(ctx, tx, metadata, limits)
	if err != nil {
		return syncSnapshotPage{}, err
	}
	page := syncSnapshotPage{DatasetEpoch: metadata.Cursor.Scope.DatasetEpoch, ServerInstanceID: metadata.Cursor.Scope.ServerInstanceID,
		ProjectionVersion: metadata.Cursor.Scope.ProjectionVersion, HighWatermark: metadata.Cursor.HighWatermark, Records: records, Complete: !more}
	if more {
		next := metadata.Cursor
		next.NextOrdinal += int64(len(records))
		page.NextCursor, err = encodeSyncSnapshotCursor(a, next)
	} else {
		page.DeltaCursor, err = encodeSyncDeltaCursor(a, syncDeltaCursor{Version: 1, Scope: metadata.Cursor.Scope, After: metadata.Cursor.HighWatermark})
	}
	if err != nil {
		return syncSnapshotPage{}, err
	}
	if err = a.recheckSyncSnapshotRead(ctx, tx, token, session, metadata.Cursor); err != nil {
		return syncSnapshotPage{}, err
	}
	if err = tx.Commit(); err != nil {
		return syncSnapshotPage{}, err
	}
	return page, nil
}

func (a *Auth) verifySyncSnapshotSeal(ctx context.Context, tx *sql.Tx, metadata syncSnapshotMetadata) error {
	var total int64
	var proof []byte
	err := tx.QueryRowContext(ctx, `SELECT total_rows,
 CASE WHEN typeof(proof)='blob' AND length(proof)=32 THEN proof END
 FROM sync_snapshot_seals WHERE snapshot_id=?`, metadata.Cursor.SnapshotID).Scan(&total, &proof)
	if errors.Is(err, sql.ErrNoRows) {
		return errSyncSnapshotUnproven
	}
	if err != nil {
		return err
	}
	if total != metadata.TotalRows || len(proof) != 32 {
		return errSyncSnapshotUnproven
	}
	expected, err := a.syncSnapshotManifestProof(ctx, metadata.Cursor, total)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(proof, expected[:]) != 1 {
		return errSyncSnapshotUnproven
	}
	return nil
}

func (a *Auth) readSyncSnapshotRows(ctx context.Context, tx *sql.Tx, metadata syncSnapshotMetadata, limits syncPageLimits) ([]syncRecord, bool, error) {
	cursor, total := metadata.Cursor, metadata.TotalRows
	start := cursor.NextOrdinal
	if start > 0 {
		start--
	}
	// The previous row anchors ordering across page boundaries. It and the
	// lookahead are validated but are additional to the returned-page budget.
	rows, err := tx.QueryContext(ctx, `SELECT r.ordinal,
 CASE WHEN length(CAST(r.entity_type AS BLOB))<=32 THEN r.entity_type END,
 CASE WHEN length(CAST(r.entity_id AS BLOB))<=? THEN r.entity_id END,
 r.entity_revision,CASE WHEN length(CAST(r.payload_json AS BLOB))<=? THEN r.payload_json END,
 p.snapshot_id IS NOT NULL,CASE WHEN typeof(p.proof)='blob' AND length(p.proof)=32 THEN p.proof END
 FROM sync_snapshot_rows r LEFT JOIN sync_snapshot_row_proofs p ON p.snapshot_id=r.snapshot_id AND p.ordinal=r.ordinal
 WHERE r.snapshot_id=? AND r.ordinal>=? ORDER BY r.ordinal LIMIT ?`, maxSyncIdentityBytes, limits.PayloadBytes, cursor.SnapshotID, start, limits.Records+2)
	if err != nil {
		return nil, false, err
	}
	records := make([]syncRecord, 0, limits.Records)
	var used int
	more := false
	readErr := func() error {
		expectedOrdinal := start
		var previousKind, previousID string
		havePrevious := false
		for rows.Next() {
			var ordinal, revision int64
			var kind, id, payload sql.NullString
			var proofPresent bool
			var proof []byte
			if err := rows.Scan(&ordinal, &kind, &id, &revision, &payload, &proofPresent, &proof); err != nil {
				return err
			}
			if ordinal != expectedOrdinal || ordinal >= total || !kind.Valid || !id.Valid {
				return ErrDomainState
			}
			expectedOrdinal++
			if !payload.Valid {
				return errSyncPayloadTooLarge
			}
			record := syncRecord{EntityType: kind.String, EntityID: id.String, EntityRevision: revision,
				Operation: "upsert", ProjectionVersion: 1, Payload: json.RawMessage(payload.String)}
			if err := validateSyncRecord(record.EntityType, record.EntityID, record.Operation, record.EntityRevision, 1, record.Payload); err != nil {
				return err
			}
			if !proofPresent || len(proof) != 32 {
				return errSyncSnapshotUnproven
			}
			expected, err := a.syncSnapshotRowProof(ctx, cursor, total, ordinal, record.EntityType, record.EntityID, revision, record.Payload)
			if err != nil {
				return err
			}
			if subtle.ConstantTimeCompare(proof, expected[:]) != 1 {
				return errSyncSnapshotUnproven
			}
			if havePrevious && (record.EntityType < previousKind || record.EntityType == previousKind && record.EntityID <= previousID) {
				return ErrDomainState
			}
			previousKind, previousID, havePrevious = record.EntityType, record.EntityID, true
			if ordinal < cursor.NextOrdinal {
				continue
			}
			size := len(record.EntityType) + len(record.EntityID) + len(record.Operation) + len(record.Payload)
			if size > limits.PageBytes {
				return errSyncPayloadTooLarge
			}
			if len(records) == limits.Records || used+size > limits.PageBytes {
				more = true
				break
			}
			records = append(records, record)
			used += size
		}
		if !more && expectedOrdinal != total {
			return ErrDomainState
		}
		return nil
	}()
	if err = errors.Join(readErr, rows.Err(), rows.Close()); err != nil {
		return nil, false, err
	}
	return records, more, nil
}
