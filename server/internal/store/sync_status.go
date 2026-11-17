package store

import (
	"context"
	"database/sql"
	"errors"
)

// SyncStatus describes the current materialization, not a complete offline replica.
// Bootstrap remains unavailable until all source domains are backfilled and gated.
// RetentionDays is the minimum policy; no automatic pruning is currently enabled.
type SyncStatus struct {
	DatasetEpoch      string           `json:"dataset_epoch"`
	ServerInstanceID  string           `json:"server_instance_id"`
	ProjectionVersion int64            `json:"projection_version"`
	MinRetainedSeq    int64            `json:"min_retained_seq"`
	HighWatermark     int64            `json:"high_watermark"`
	Counts            map[string]int64 `json:"counts"`
	CountsScope       string           `json:"counts_scope"`
	LastChangeAt      *string          `json:"last_change_at"`
	RetentionDays     int64            `json:"retention_days"`
	BootstrapReady    bool             `json:"bootstrap_ready"`
	RetentionEnabled  bool             `json:"retention_enabled"`
}

// ReadSyncStatus is a protected diagnostic read, not permission to bootstrap or
// proof that the partial projections match every authoritative domain source.
func (a *Auth) ReadSyncStatus(ctx context.Context, token string) (SyncStatus, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil {
		return SyncStatus{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return SyncStatus{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncStatus{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return SyncStatus{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return SyncStatus{}, err
	}
	// Read the diagnostic after session activity so all fields describe the same
	// post-write SQLite snapshot. The final session check performs no writes.
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return SyncStatus{}, err
	}
	value, err := readSyncStatus(ctx, tx)
	if err != nil {
		return SyncStatus{}, err
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return SyncStatus{}, err
	}
	if err = tx.Commit(); err != nil {
		return SyncStatus{}, err
	}
	return value, nil
}

func readSyncStatus(ctx context.Context, tx *sql.Tx) (SyncStatus, error) {
	value := SyncStatus{Counts: make(map[string]int64), CountsScope: "materialized_projections", RetentionDays: 30}
	err := tx.QueryRowContext(ctx, `SELECT dataset_epoch,server_instance_id,projection_version,min_retained_seq
 FROM sync_state WHERE singleton=1`).Scan(&value.DatasetEpoch, &value.ServerInstanceID, &value.ProjectionVersion, &value.MinRetainedSeq)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!authString(value.DatasetEpoch, 1, 128) || !validMediaInstance(value.ServerInstanceID) ||
		value.ProjectionVersion != 1 || value.MinRetainedSeq < 0 || value.MinRetainedSeq > maxSafeInteger) {
		return SyncStatus{}, ErrDomainState
	}
	if err != nil {
		return SyncStatus{}, err
	}
	value.HighWatermark, value.LastChangeAt, err = readSyncWatermark(ctx, tx, value.MinRetainedSeq)
	if err != nil {
		return SyncStatus{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT entity_type,COUNT(*) FROM current_projections GROUP BY entity_type")
	if err != nil {
		return SyncStatus{}, err
	}
	for rows.Next() {
		var kind string
		var count int64
		if err = rows.Scan(&kind, &count); err != nil {
			return SyncStatus{}, errors.Join(err, rows.Close())
		}
		if !validSyncStatusKind(kind) || count < 1 || count > maxSafeInteger {
			return SyncStatus{}, errors.Join(ErrDomainState, rows.Close())
		}
		value.Counts[kind] = count
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return SyncStatus{}, err
	}
	return value, nil
}

func validSyncStatusKind(kind string) bool {
	switch kind {
	case "location", "item", "unit", "stock", "barcode", "media", "media_link", "category", "tag", "item_tag", "search_doc", "purchase", "purchase_line", "asset", "usage", "cost", "disposal", "alias", "attribute", "location_event", "inventory_event", "ai_review":
		return true
	default:
		return false
	}
}

// AUTOINCREMENT retains the last allocated sequence even after every frozen row
// is pruned. MAX(sync_changes.seq) alone would make the watermark go backwards.
func readSyncWatermark(ctx context.Context, tx *sql.Tx, floor int64) (int64, *string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT seq,typeof(seq) FROM sqlite_sequence WHERE name='sync_changes'")
	if err != nil {
		return 0, nil, err
	}
	var watermark int64
	seen := false
	for rows.Next() {
		var raw any
		var kind string
		if err = rows.Scan(&raw, &kind); err != nil {
			return 0, nil, errors.Join(err, rows.Close())
		}
		sequence, integer := raw.(int64)
		if seen || kind != "integer" || !integer || sequence < 0 || sequence > maxSafeInteger {
			return 0, nil, errors.Join(ErrDomainState, rows.Close())
		}
		seen, watermark = true, sequence
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, nil, err
	}
	var first, last sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT MIN(seq),MAX(seq) FROM sync_changes").Scan(&first, &last); err != nil {
		return 0, nil, err
	}
	if floor < 0 || floor > watermark || first.Valid != last.Valid ||
		first.Valid && (!seen || first.Int64 <= floor || last.Int64 > watermark) {
		return 0, nil, ErrDomainState
	}
	if !last.Valid {
		return watermark, nil, nil
	}
	var created string
	if err = tx.QueryRowContext(ctx, "SELECT created_at FROM sync_changes WHERE seq=?", last.Int64).Scan(&created); err != nil {
		return 0, nil, err
	}
	if !validPurchaseInstant(created) {
		return 0, nil, ErrDomainState
	}
	return watermark, &created, nil
}
