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
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return syncSnapshotMetadata{}, err
	}
	snapshotPinned, err := verifySyncSnapshotSigner(ctx, tx, a.serverSecret)
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	// Activity can trigger SQL changes. Observe the dataset and manifest only
	// afterward, then revalidate the session and expiry before committing.
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return syncSnapshotMetadata{}, err
	}
	currentPinned, err := verifySyncSnapshotSigner(ctx, tx, a.serverSecret)
	if err != nil {
		return syncSnapshotMetadata{}, err
	}
	if currentPinned != snapshotPinned {
		return syncSnapshotMetadata{}, ErrAuthState
	}
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
	now := a.now().UTC()
	if _, err = checkDomainSession(ctx, a, tx, token, session, now); err != nil {
		return syncSnapshotMetadata{}, err
	}
	start, end, _ := syncSnapshotTimes(cursor)
	if now.Before(start) {
		return syncSnapshotMetadata{}, ErrInvalidCursor
	}
	if !now.Before(end) {
		return syncSnapshotMetadata{}, ErrSyncSnapshotExpired
	}
	if err = tx.Commit(); err != nil {
		return syncSnapshotMetadata{}, err
	}
	return syncSnapshotMetadata{Cursor: cursor, TotalRows: total}, nil
}
