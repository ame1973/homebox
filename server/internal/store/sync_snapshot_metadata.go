package store

import (
	"context"
	"database/sql"
	"errors"
)

// This is a bound metadata observation only. It does not authenticate frozen
// payloads, prove source completeness, construct a snapshot or enable bootstrap.
type syncSnapshotMetadata struct {
	Cursor    syncSnapshotCursor
	TotalRows int64
}

func (a *Auth) readSyncSnapshotMetadata(ctx context.Context, token, signed string) (syncSnapshotMetadata, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil {
		return syncSnapshotMetadata{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return syncSnapshotMetadata{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	defer tx.Rollback()
	session, _, err := a.beginSyncSnapshotRead(ctx, tx, token)
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	metadata, err := a.readSyncSnapshotScope(ctx, tx, session, signed)
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	if err = a.recheckSyncSnapshotRead(ctx, tx, token, session, metadata.Cursor); err != nil {
		return syncSnapshotMetadata{}, err
	}
	if err = tx.Commit(); err != nil {
		return syncSnapshotMetadata{}, err
	}
	return metadata, nil
}

// Complete session activity before observing snapshot state. Capture and verify
// the signer on both sides because activity can run SQL triggers.
func (a *Auth) beginSyncSnapshotRead(ctx context.Context, tx *sql.Tx, token string) (AuthSession, bool, error) {
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return AuthSession{}, false, err
	}
	if err = requireCompleteSession(session); err != nil {
		return AuthSession{}, false, err
	}
	initialPinned, err := verifySyncSnapshotSigner(ctx, tx, a.serverSecret)
	if err != nil {
		return AuthSession{}, false, err
	}
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return AuthSession{}, false, err
	}
	pinned, err := verifySyncSnapshotSigner(ctx, tx, a.serverSecret)
	if err != nil {
		return AuthSession{}, false, err
	}
	if pinned != initialPinned {
		return AuthSession{}, false, ErrAuthState
	}
	return session, pinned, nil
}

// Recheck the completed session and snapshot lifetime after the final read.
func (a *Auth) recheckSyncSnapshotRead(ctx context.Context, tx *sql.Tx, token string, session AuthSession, cursor syncSnapshotCursor) error {
	now := a.now().UTC()
	if _, err := checkDomainSession(ctx, a, tx, token, session, now); err != nil {
		return err
	}
	start, end, _ := syncSnapshotTimes(cursor)
	if now.Before(start) {
		return ErrInvalidCursor
	}
	if !now.Before(end) {
		return ErrSyncSnapshotExpired
	}
	return nil
}

// The caller owns the authorized transaction and must recheck the session and
// snapshot lifetime before committing or returning any contents.
func (a *Auth) readSyncSnapshotScope(ctx context.Context, tx *sql.Tx, session AuthSession, signed string) (syncSnapshotMetadata, error) {
	dataset, err := readSyncDataset(ctx, tx)
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	watermark, _, err := readSyncWatermark(ctx, tx, dataset.Floor)
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	scope := syncCursorScope{AdminID: session.Admin.ID, ServerInstanceID: dataset.Instance, DatasetEpoch: dataset.Epoch, ProjectionVersion: dataset.Version}
	cursor, err := decodeSyncSnapshotCursor(a, signed, scope, a.now().UTC())
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	var instance, epoch, created, expires, status sql.NullString
	var version, highWatermark int64
	err = tx.QueryRowContext(ctx, `SELECT
 CASE WHEN length(CAST(server_instance_id AS BLOB))<=32 THEN server_instance_id END,
 CASE WHEN length(CAST(dataset_epoch AS BLOB))<=512 THEN dataset_epoch END,
 projection_version,high_watermark,
 CASE WHEN length(CAST(created_at AS BLOB))<=35 THEN created_at END,
 CASE WHEN length(CAST(expires_at AS BLOB))<=35 THEN expires_at END,
 CASE WHEN length(CAST(status AS BLOB))<=16 THEN status END
 FROM sync_snapshots WHERE id=?`, cursor.SnapshotID).
		Scan(&instance, &epoch, &version, &highWatermark, &created, &expires, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return syncSnapshotMetadata{}, ErrDomainState
	}
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	if !instance.Valid || instance.String != cursor.Scope.ServerInstanceID || !epoch.Valid || epoch.String != cursor.Scope.DatasetEpoch ||
		version != cursor.Scope.ProjectionVersion || highWatermark != cursor.HighWatermark ||
		!created.Valid || created.String != cursor.CreatedAt || !expires.Valid || expires.String != cursor.ExpiresAt ||
		!status.Valid || status.String != "ready" {
		return syncSnapshotMetadata{}, ErrDomainState
	}
	if err = checkSyncDeltaPosition(highWatermark, dataset.Floor, watermark); err != nil {
		return syncSnapshotMetadata{}, err
	}
	var total int64
	var first, last sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*),MIN(ordinal),MAX(ordinal) FROM sync_snapshot_rows WHERE snapshot_id=?", cursor.SnapshotID).
		Scan(&total, &first, &last); err != nil {
		return syncSnapshotMetadata{}, err
	}
	if total < 0 || total > maxSafeInteger || total == 0 && (first.Valid || last.Valid) ||
		total > 0 && (!first.Valid || !last.Valid || first.Int64 != 0 || last.Int64 != total-1) {
		return syncSnapshotMetadata{}, ErrDomainState
	}
	if cursor.NextOrdinal > total {
		return syncSnapshotMetadata{}, ErrInvalidCursor
	}
	return syncSnapshotMetadata{Cursor: cursor, TotalRows: total}, nil
}
