package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
)

// This key dependency is independent of the keyed Media receipt marker. It
// binds a key, not a search row or the reviewed origin of any stored bytes.
func syncSearchSignerProof(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("homebox-sync-search-signer-v1"))
	return mac.Sum(nil)
}

func verifySyncSearchSigner(ctx context.Context, tx *sql.Tx, expected []byte) (bool, error) {
	if _, err := verifyKeyedReceiptSecret(ctx, tx, expected); err != nil {
		return false, err
	}
	var proof []byte
	err := tx.QueryRowContext(ctx, "SELECT key_proof FROM sync_search_signer WHERE singleton=1").Scan(&proof)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare(proof, syncSearchSignerProof(expected)) != 1 {
		return false, ErrAuthState
	}
	return true, nil
}

// A first pin must not assign authority to unknown, previously stored proofs.
// The caller still owns authorization, source validation and transaction commit.
func pinSyncSearchSigner(ctx context.Context, tx *sql.Tx, expected []byte) error {
	receiptPinned, err := verifyKeyedReceiptSecret(ctx, tx, expected)
	if err != nil {
		return err
	}
	pinned, err := verifySyncSearchSigner(ctx, tx, expected)
	if err != nil || pinned {
		return err
	}
	var existing bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sync_search_proofs)").Scan(&existing); err != nil {
		return err
	}
	if existing {
		return ErrDomainState
	}
	if err = requireDomainRow(tx.ExecContext(ctx, "INSERT INTO sync_search_signer(singleton,key_proof) VALUES(1,?)", syncSearchSignerProof(expected))); err != nil {
		return err
	}
	pinned, err = verifySyncSearchSigner(ctx, tx, expected)
	if err != nil {
		return err
	}
	if !pinned {
		return ErrAuthState
	}
	currentReceiptPinned, err := verifyKeyedReceiptSecret(ctx, tx, expected)
	if err != nil {
		return err
	}
	if currentReceiptPinned != receiptPinned {
		return ErrAuthState
	}
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sync_search_proofs)").Scan(&existing); err != nil {
		return err
	}
	if existing {
		return ErrDomainState
	}
	return nil
}
