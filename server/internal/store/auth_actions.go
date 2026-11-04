package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"homebox/internal/password"
)

const (
	logoutOperation   = "post_auth_logout"
	passwordOperation = "post_auth_password"
	reauthOperation   = "post_auth_reauth"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key was already used for a different request")
	ErrPasswordMismatch    = errors.New("current password verification failed")
	ErrPasswordUnchanged   = errors.New("new password must differ from the current password")
)

// credentialProof is private: an expensive password proof is never an HTTP DTO.
type credentialProof struct {
	session AuthSession
	hash    string
}

type passwordChangeRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ChangePassword verifies the old credential, changes it, and revokes every session.
// Only an exact frozen acknowledgement can be replayed with the now-revoked cookie.
func (a *Auth) ChangePassword(ctx context.Context, token, key, oldPassword, newPassword string) (ActionAck, error) {
	if !authString(key, 8, 128) || !authString(oldPassword, 1, 256) {
		return ActionAck{}, ErrAuthInput
	}
	if err := password.ValidateNew(newPassword); err != nil {
		return ActionAck{}, err
	}
	if oldPassword == newPassword {
		return ActionAck{}, ErrPasswordUnchanged
	}
	fingerprint, err := authFingerprint(token, key, passwordOperation, passwordChangeRequest{oldPassword, newPassword})
	if err != nil {
		return ActionAck{}, err
	}
	proof, replay, err := a.preparePasswordChange(ctx, token, key, fingerprint)
	if err != nil {
		return ActionAck{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	matched, err := a.hasher.Verify(ctx, oldPassword, proof.hash)
	if err != nil {
		return ActionAck{}, err
	}
	if !matched {
		if err = a.recordCredentialFailure(ctx, "password_change_failed", proof.session.Admin.ID); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{}, ErrPasswordMismatch
	}
	encoded, err := a.hasher.Hash(ctx, newPassword)
	if err != nil {
		return ActionAck{}, err
	}
	return a.commitPasswordChange(ctx, token, key, fingerprint, proof, encoded)
}

func (a *Auth) preparePasswordChange(ctx context.Context, token, key, fingerprint string) (credentialProof, *ActionAck, error) {
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return credentialProof{}, nil, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return credentialProof{}, nil, err
	}
	body, err := readAuthReply(ctx, tx, session, key, passwordOperation, fingerprint)
	if err != nil {
		return credentialProof{}, nil, err
	}
	if body != "" {
		ack, err := decodeAuthAck(body)
		if err != nil {
			return credentialProof{}, nil, err
		}
		if err = tx.Commit(); err != nil {
			return credentialProof{}, nil, err
		}
		return credentialProof{}, &ack, nil
	}
	if session.revoked {
		return credentialProof{}, nil, ErrAuthentication
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

func (a *Auth) commitPasswordChange(ctx context.Context, token, key, fingerprint string, proof credentialProof, encoded string) (ActionAck, error) {
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return ActionAck{}, err
	}
	defer tx.Rollback()
	now := a.now().UTC()
	session, err := a.loadSession(ctx, tx, token, now)
	if err != nil {
		return ActionAck{}, err
	}
	body, err := readAuthReply(ctx, tx, session, key, passwordOperation, fingerprint)
	if err != nil {
		return ActionAck{}, err
	}
	if body != "" {
		ack, err := decodeAuthAck(body)
		if err != nil {
			return ActionAck{}, err
		}
		if err = tx.Commit(); err != nil {
			return ActionAck{}, err
		}
		return ack, nil
	}
	if err = a.revalidateCredentialProof(ctx, tx, session, proof); err != nil {
		return ActionAck{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE admin_accounts SET password_hash=?,revision=revision+1,
 must_change_password=0,updated_at=? WHERE id=?`, encoded, stamp(now), session.Admin.ID)
	if err != nil {
		return ActionAck{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE admin_id=?", stamp(now), session.Admin.ID); err != nil {
		return ActionAck{}, err
	}
	eventID, err := appendAuthAudit(ctx, tx, "password_changed", session.Admin.ID, now)
	if err != nil {
		return ActionAck{}, err
	}
	ack := ActionAck{OK: true, OperationID: eventID, Revision: session.Admin.Revision + 1}
	if err = saveAuthReply(ctx, tx, session, key, passwordOperation, fingerprint, ack, now); err != nil {
		return ActionAck{}, err
	}
	if err = tx.Commit(); err != nil {
		return ActionAck{}, err
	}
	return ack, nil
}

func (a *Auth) revalidateCredentialProof(ctx context.Context, tx *sql.Tx, session AuthSession, proof credentialProof) error {
	if session.revoked || session.ID != proof.session.ID || session.Admin != proof.session.Admin {
		return ErrAuthentication
	}
	_, hash, err := loadAdmin(ctx, tx)
	if err != nil {
		return err
	}
	if hash != proof.hash {
		return ErrAuthentication
	}
	return nil
}

func (a *Auth) recordCredentialFailure(ctx context.Context, event string, adminID any) error {
	_, err := a.store.db.ExecContext(ctx, `INSERT INTO security_audit(id,event_type,admin_id,occurred_at,details_json)
 VALUES(?,?,?,?,'{}')`, rand.Text(), event, adminID, stamp(a.now()))
	return err
}

// Fingerprints of credential requests must not be unkeyed password digests.
// The raw session secret is never stored beside the resulting HMAC.
func authFingerprint(token, key, operation string, payload any) (string, error) {
	if !validAuthToken(token) {
		return "", ErrAuthentication
	}
	body, err := json.Marshal(struct {
		Key       string `json:"key"`
		Operation string `json:"operation"`
		Payload   any    `json:"payload"`
	}{key, operation, payload})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(authMAC(token, "auth-request-v1", body)), nil
}

func authMAC(token, purpose string, payload []byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func authReplyKey(session AuthSession, key string) string {
	return authDigest("auth\x00" + session.ID + "\x00" + key)
}

func readAuthReply(ctx context.Context, q queryer, session AuthSession, key, operation, fingerprint string) (string, error) {
	var storedOperation, storedFingerprint, body string
	var status int
	err := q.QueryRowContext(ctx, `SELECT operation,request_sha256,response_status,response_json
 FROM api_idempotency WHERE key=?`, authReplyKey(session, key)).Scan(&storedOperation, &storedFingerprint, &status, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if storedOperation != operation || subtle.ConstantTimeCompare([]byte(storedFingerprint), []byte(fingerprint)) != 1 {
		return "", ErrIdempotencyConflict
	}
	if status != 200 || !json.Valid([]byte(body)) {
		return "", ErrAuthState
	}
	return body, nil
}

func saveAuthReply(ctx context.Context, tx *sql.Tx, session AuthSession, key, operation, fingerprint string, response any, now time.Time) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_idempotency(key,operation,request_sha256,response_status,response_json,created_at,expires_at)
 VALUES(?,?,?,200,?,?,?)`, authReplyKey(session, key), operation, fingerprint, string(body), stamp(now), stamp(session.ExpiresAt))
	return err
}

func decodeAuthAck(body string) (ActionAck, error) {
	var ack ActionAck
	if json.Unmarshal([]byte(body), &ack) != nil || !ack.OK || ack.OperationID == "" {
		return ActionAck{}, ErrAuthState
	}
	return ack, nil
}
