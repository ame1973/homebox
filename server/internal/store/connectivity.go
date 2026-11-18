package store

import "context"

// ProbeConnectivity verifies a completed session without recording activity.
// It does not authorize a subsequent operation or establish domain health.
func (a *Auth) ProbeConnectivity(ctx context.Context, token string) error {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil {
		return ErrAuthInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return err
	}
	if err = requireCompleteSession(session); err != nil {
		return err
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}
