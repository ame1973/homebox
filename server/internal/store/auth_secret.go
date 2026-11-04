package store

import (
	"context"
	"crypto/rand"
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
 (SELECT COUNT(*) FROM api_idempotency WHERE operation IN ('post_auth_logout','post_auth_password','post_auth_reauth'))`).Scan(&outstanding); err != nil {
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
	if len(secret) != 32 {
		return nil, ErrAuthState
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return secret, nil
}
