package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
)

// This independent key dependency does not authenticate snapshot rows or
// establish their source. Existing unsealed snapshots receive no signer.
func syncSnapshotSignerProof(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("homebox-sync-snapshot-signer-v1\x00"))
	return mac.Sum(nil)
}

func verifySyncSnapshotSigner(ctx context.Context, tx *sql.Tx, expected []byte) (bool, error) {
	if _, err := verifySyncSearchSigner(ctx, tx, expected); err != nil {
		return false, err
	}
	var proof []byte
	err := tx.QueryRowContext(ctx, "SELECT key_proof FROM sync_snapshot_signer WHERE singleton=1").Scan(&proof)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare(proof, syncSnapshotSignerProof(expected)) != 1 {
		return false, ErrAuthState
	}
	return true, nil
}

func syncSnapshotProofsExist(ctx context.Context, tx *sql.Tx) (bool, error) {
	var existing bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sync_snapshot_seals) OR
 EXISTS(SELECT 1 FROM sync_snapshot_row_proofs)`).Scan(&existing)
	return existing, err
}

// A first pin cannot assign authority to unknown snapshot proof rows. Callers
// must separately authorize, validate source and commit the publication.
func pinSyncSnapshotSigner(ctx context.Context, tx *sql.Tx, expected []byte) error {
	receiptPinned, err := verifyKeyedReceiptSecret(ctx, tx, expected)
	if err != nil {
		return err
	}
	searchPinned, err := verifySyncSearchSigner(ctx, tx, expected)
	if err != nil {
		return err
	}
	pinned, err := verifySyncSnapshotSigner(ctx, tx, expected)
	if err != nil || pinned {
		return err
	}
	existing, err := syncSnapshotProofsExist(ctx, tx)
	if err != nil {
		return err
	}
	if existing {
		return ErrDomainState
	}
	if err = requireDomainRow(tx.ExecContext(ctx, "INSERT INTO sync_snapshot_signer(singleton,key_proof) VALUES(1,?)", syncSnapshotSignerProof(expected))); err != nil {
		return err
	}
	pinned, err = verifySyncSnapshotSigner(ctx, tx, expected)
	if err != nil {
		return err
	}
	if !pinned {
		return ErrAuthState
	}
	currentSearch, err := verifySyncSearchSigner(ctx, tx, expected)
	if err != nil {
		return err
	}
	currentReceipt, err := verifyKeyedReceiptSecret(ctx, tx, expected)
	if err != nil {
		return err
	}
	if currentSearch != searchPinned || currentReceipt != receiptPinned {
		return ErrAuthState
	}
	existing, err = syncSnapshotProofsExist(ctx, tx)
	if err != nil {
		return err
	}
	if existing {
		return ErrDomainState
	}
	return nil
}
