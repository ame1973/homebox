package store

import (
	"context"

	"homebox/migrations"
)

// VersionState is the authorized database portion of a future version handshake.
// It does not establish frontend/API compatibility or offline replica readiness.
type VersionState struct {
	DatasetEpoch      string
	ServerInstanceID  string
	ProjectionVersion int64
	SchemaVersion     int
}

// ReadVersionState reads the current dataset and verified migration history in
// one completed-session transaction after activity, then revalidates the session.
func (a *Auth) ReadVersionState(ctx context.Context, token string) (VersionState, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil {
		return VersionState{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return VersionState{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return VersionState{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return VersionState{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return VersionState{}, err
	}
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return VersionState{}, err
	}
	all, err := migrations.Load()
	if err != nil {
		return VersionState{}, err
	}
	version, hasLedger, err := history(ctx, tx, all)
	if err != nil {
		return VersionState{}, err
	}
	if !hasLedger || version != len(all) {
		return VersionState{}, ErrSchemaChanged
	}
	dataset, err := readSyncDataset(ctx, tx)
	if err != nil {
		return VersionState{}, err
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return VersionState{}, err
	}
	if err = tx.Commit(); err != nil {
		return VersionState{}, err
	}
	return VersionState{
		DatasetEpoch:      dataset.Epoch,
		ServerInstanceID:  dataset.Instance,
		ProjectionVersion: dataset.Version,
		SchemaVersion:     version,
	}, nil
}
