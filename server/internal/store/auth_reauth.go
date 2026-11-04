package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

const reauthLifetime = 5 * time.Minute

var (
	ErrPasswordChangeRequired = errors.New("the initial password must be changed first")
	ErrReauthRequired         = errors.New("a current unused reauthentication grant is required")
)

// ReauthGrant contains safe response metadata; the raw capability is returned separately.
type ReauthGrant struct {
	Admin     Admin     `json:"admin"`
	ExpiresAt time.Time `json:"expires_at"`
}

type reauthRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Reauthenticate issues a short-lived single-use capability after a fresh password proof.
// Idempotent retries reconstruct its token from the still-active session, not from storage.
func (a *Auth) Reauthenticate(ctx context.Context, token, key, username, secret string) (ReauthGrant, string, error) {
	if !authString(key, 8, 128) || !authString(username, 1, 64) || !authString(secret, 1, 256) {
		return ReauthGrant{}, "", ErrAuthInput
	}
	fingerprint, err := a.authFingerprint(token, key, reauthOperation, reauthRequest{username, secret})
	if err != nil {
		return ReauthGrant{}, "", err
	}
	proof, replay, err := a.prepareReauth(ctx, token, key, fingerprint)
	if err != nil {
		return ReauthGrant{}, "", err
	}
	if replay != nil {
		return *replay, a.reauthToken(token, key), nil
	}
	matched, err := a.hasher.Verify(ctx, secret, proof.hash)
	if err != nil {
		return ReauthGrant{}, "", err
	}
	if !matched || subtle.ConstantTimeCompare([]byte(username), []byte(proof.session.Admin.Username)) != 1 {
		if err = a.recordCredentialFailure(ctx, "reauth_failed", proof.session.Admin.ID); err != nil {
			return ReauthGrant{}, "", err
		}
		return ReauthGrant{}, "", ErrPasswordMismatch
	}
	return a.commitReauth(ctx, token, key, fingerprint, proof)
}

func (a *Auth) prepareReauth(ctx context.Context, token, key, fingerprint string) (credentialProof, *ReauthGrant, error) {
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return credentialProof{}, nil, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return credentialProof{}, nil, err
	}
	if err = requireCompleteSession(session); err != nil {
		return credentialProof{}, nil, err
	}
	body, err := readAuthReply(ctx, tx, session, key, reauthOperation, fingerprint)
	if err != nil {
		return credentialProof{}, nil, err
	}
	if body != "" {
		grant, err := decodeReauthGrant(body, session)
		if err != nil {
			return credentialProof{}, nil, err
		}
		if err = tx.Commit(); err != nil {
			return credentialProof{}, nil, err
		}
		return credentialProof{}, &grant, nil
	}
	_, hash, err := loadAdmin(ctx, tx)
	if err != nil {
		return credentialProof{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		return credentialProof{}, nil, err
	}
	return credentialProof{session: session, hash: hash}, nil, nil
}

func (a *Auth) commitReauth(ctx context.Context, token, key, fingerprint string, proof credentialProof) (ReauthGrant, string, error) {
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return ReauthGrant{}, "", err
	}
	defer tx.Rollback()
	now := a.now().UTC()
	session, err := a.loadSession(ctx, tx, token, now)
	if err != nil {
		return ReauthGrant{}, "", err
	}
	if err = requireCompleteSession(session); err != nil {
		return ReauthGrant{}, "", err
	}
	body, err := readAuthReply(ctx, tx, session, key, reauthOperation, fingerprint)
	if err != nil {
		return ReauthGrant{}, "", err
	}
	if body != "" {
		grant, err := decodeReauthGrant(body, session)
		if err != nil {
			return ReauthGrant{}, "", err
		}
		if err = tx.Commit(); err != nil {
			return ReauthGrant{}, "", err
		}
		return grant, a.reauthToken(token, key), nil
	}
	if err = a.revalidateCredentialProof(ctx, tx, session, proof); err != nil {
		return ReauthGrant{}, "", err
	}
	expires := now.Add(reauthLifetime)
	if idle := now.Add(a.idle); idle.Before(expires) {
		expires = idle
	}
	if session.ExpiresAt.Before(expires) {
		expires = session.ExpiresAt
	}
	grant := ReauthGrant{Admin: session.Admin, ExpiresAt: expires}
	raw := a.reauthToken(token, key)
	_, err = tx.ExecContext(ctx, `INSERT INTO auth_reauth_grants(token_hash,session_id,admin_revision,created_at,expires_at)
 VALUES(?,?,?,?,?)`, authDigest(raw), session.ID, session.Admin.Revision, stamp(now), stamp(expires))
	if err != nil {
		return ReauthGrant{}, "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at=? WHERE id=?", stamp(now), session.ID); err != nil {
		return ReauthGrant{}, "", err
	}
	if _, err = appendAuthAudit(ctx, tx, "reauth_succeeded", session.Admin.ID, now); err != nil {
		return ReauthGrant{}, "", err
	}
	if err = saveAuthReply(ctx, tx, session, key, reauthOperation, fingerprint, grant, now); err != nil {
		return ReauthGrant{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return ReauthGrant{}, "", err
	}
	return grant, raw, nil
}

// consumeReauth must run inside the privileged mutation/job-authorization transaction.
// Never expose a separate check/consume endpoint that races the actual operation.
func (a *Auth) consumeReauth(ctx context.Context, tx *sql.Tx, sessionToken, grantToken string, now time.Time) (AuthSession, error) {
	session, err := a.loadSession(ctx, tx, sessionToken, now)
	if err != nil {
		return AuthSession{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return AuthSession{}, err
	}
	if !validAuthToken(grantToken) {
		return AuthSession{}, ErrReauthRequired
	}
	var revision int64
	var created, expires string
	var consumed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT admin_revision,created_at,expires_at,consumed_at FROM auth_reauth_grants
 WHERE token_hash=? AND session_id=?`, authDigest(grantToken), session.ID).Scan(&revision, &created, &expires, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthSession{}, ErrReauthRequired
	}
	if err != nil {
		return AuthSession{}, err
	}
	start, err1 := time.Parse(time.RFC3339Nano, created)
	end, err2 := time.Parse(time.RFC3339Nano, expires)
	if err1 != nil || err2 != nil {
		return AuthSession{}, ErrAuthState
	}
	if consumed.Valid || revision != session.Admin.Revision || now.Before(start) || !now.Before(end) {
		return AuthSession{}, ErrReauthRequired
	}
	result, err := tx.ExecContext(ctx, "UPDATE auth_reauth_grants SET consumed_at=? WHERE token_hash=? AND consumed_at IS NULL", stamp(now), authDigest(grantToken))
	if err != nil {
		return AuthSession{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return AuthSession{}, err
	}
	if changed != 1 {
		return AuthSession{}, ErrReauthRequired
	}
	return session, nil
}

func requireCompleteSession(session AuthSession) error {
	if session.revoked {
		return ErrAuthentication
	}
	if session.Admin.MustChangePassword {
		return ErrPasswordChangeRequired
	}
	return nil
}

func (a *Auth) reauthToken(sessionToken, key string) string {
	return base64.RawURLEncoding.EncodeToString(a.authMAC(sessionToken, "reauth-token-v2", []byte(key)))
}

func decodeReauthGrant(body string, session AuthSession) (ReauthGrant, error) {
	var grant ReauthGrant
	if json.Unmarshal([]byte(body), &grant) != nil || grant.Admin.ID != session.Admin.ID ||
		grant.Admin.Revision < 1 || grant.Admin.Revision > 9007199254740991 ||
		grant.Admin.MustChangePassword || !authString(grant.Admin.Username, 1, 64) || grant.ExpiresAt.IsZero() {
		return ReauthGrant{}, ErrAuthState
	}
	return grant, nil
}
