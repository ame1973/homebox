package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
)

var (
	ErrOfflineOperator = errors.New("credential recovery requires an offline operator store")
	ErrNotProvisioned  = errors.New("administrator has not been provisioned; use initial setup")
	ErrRecoveryChanged = errors.New("administrator changed while preparing recovery; retry explicitly")
)

// RecoveryFile contains only safe local delivery metadata, never the temporary password.
type RecoveryFile struct {
	Path               string `json:"recovery_file"`
	Username           string `json:"username"`
	Revision           int64  `json:"revision"`
	MustChangePassword bool   `json:"must_change_password"`
	OperationID        string `json:"operation_id"`
}

// RecoverPasswordFile is available only on an explicitly offline-owner store.
// Never wire this operation to an HTTP route or to the normally opened serving store.
func (a *Auth) RecoverPasswordFile(ctx context.Context, path string) (RecoveryFile, error) {
	if !a.store.offlineAuth {
		return RecoveryFile{}, ErrOfflineOperator
	}
	var result RecoveryFile
	path, err := deliverCredentialFile(ctx, path, func(deliver func(string) error) error {
		var err error
		result, err = a.recoverPassword(ctx, deliver)
		return err
	})
	if err != nil {
		return RecoveryFile{}, err
	}
	result.Path = path
	return result, nil
}

func (a *Auth) recoverPassword(ctx context.Context, deliver func(string) error) (RecoveryFile, error) {
	if !a.store.offlineAuth {
		return RecoveryFile{}, ErrOfflineOperator
	}
	target, _, err := loadAdmin(ctx, a.store.db)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryFile{}, ErrNotProvisioned
	}
	if err != nil {
		return RecoveryFile{}, err
	}
	// Random ASCII avoids transport normalization and is never supplied on the command line.
	temporary := rand.Text()
	encoded, err := a.hasher.Hash(ctx, temporary)
	if err != nil {
		return RecoveryFile{}, err
	}
	return a.commitRecovery(ctx, target, encoded, temporary, deliver)
}

func (a *Auth) commitRecovery(ctx context.Context, target Admin, encoded, temporary string, deliver func(string) error) (RecoveryFile, error) {
	if !a.store.offlineAuth {
		return RecoveryFile{}, ErrOfflineOperator
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return RecoveryFile{}, err
	}
	defer tx.Rollback()
	current, _, err := loadAdmin(ctx, tx)
	if err != nil {
		return RecoveryFile{}, err
	}
	if current != target {
		return RecoveryFile{}, ErrRecoveryChanged
	}
	now := a.now().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE admin_accounts SET password_hash=?,revision=revision+1,
 must_change_password=1,updated_at=? WHERE id=?`, encoded, stamp(now), target.ID)
	if err != nil {
		return RecoveryFile{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE admin_id=?", stamp(now), target.ID); err != nil {
		return RecoveryFile{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM auth_bootstrap"); err != nil {
		return RecoveryFile{}, err
	}
	eventID, err := appendAuthAudit(ctx, tx, "password_recovered", target.ID, now)
	if err != nil {
		return RecoveryFile{}, err
	}
	if err = deliver(temporary); err != nil {
		return RecoveryFile{}, err
	}
	if err = tx.Commit(); err != nil {
		return RecoveryFile{}, err
	}
	return RecoveryFile{Username: target.Username, OperationID: eventID, Revision: target.Revision + 1, MustChangePassword: true}, nil
}
