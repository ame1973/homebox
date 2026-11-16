package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"
)

// Authentication's server secret is private SQLite state, never a client/session-derived key.
// Combining it with the raw session credential protects either database-only or cookie-only exposure.
func (s *Store) loadAuthServerSecret(ctx context.Context) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var secret []byte
	err = tx.QueryRowContext(ctx, "SELECT key_bytes FROM auth_server_secret WHERE singleton=1").Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		var outstanding int
		if err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL) +
 (SELECT COUNT(*) FROM auth_reauth_grants) +
 (SELECT COUNT(*) FROM api_idempotency WHERE operation IN ('post_auth_logout','post_auth_password','post_auth_reauth')) +
 (SELECT COUNT(*) FROM auth_keyed_receipt_secret)`).Scan(&outstanding); err != nil {
			return nil, err
		}
		if outstanding != 0 {
			return nil, ErrAuthState
		}
		secret = make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		if _, err = tx.ExecContext(ctx, "INSERT INTO auth_server_secret(singleton,key_bytes,created_at) VALUES(1,?,?)", secret, stamp(now)); err != nil {
			return nil, err
		}
		if _, err = appendAuthAudit(ctx, tx, "auth_key_initialized", nil, now); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(secret) != sha256.Size {
		return nil, ErrAuthState
	}
	if _, err = verifyKeyedReceiptSecret(ctx, tx, secret); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return secret, nil
}

func keyedReceiptSecretProof(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("homebox-keyed-receipt-secret-v1"))
	return mac.Sum(nil)
}

// verifyKeyedReceiptSecret checks the actual database key and, when present,
// its immutable receipt dependency inside the caller's current transaction.
func verifyKeyedReceiptSecret(ctx context.Context, tx *sql.Tx, expected []byte) (bool, error) {
	if len(expected) != sha256.Size {
		return false, ErrAuthState
	}
	var actual []byte
	if err := tx.QueryRowContext(ctx, "SELECT key_bytes FROM auth_server_secret WHERE singleton=1").Scan(&actual); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrAuthState
		}
		return false, err
	}
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return false, ErrAuthState
	}
	var proof []byte
	err := tx.QueryRowContext(ctx, "SELECT key_proof FROM auth_keyed_receipt_secret WHERE singleton=1").Scan(&proof)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare(proof, keyedReceiptSecretProof(expected)) != 1 {
		return false, ErrAuthState
	}
	return true, nil
}
