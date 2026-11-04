package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"homebox/internal/password"
)

var (
	ErrAuthentication = errors.New("invalid or expired credentials")
	ErrSetupClosed    = errors.New("administrator is already provisioned")
	ErrAuthInput      = errors.New("authentication input is outside contract bounds")
	ErrAuthConfig     = errors.New("invalid authentication lifetime configuration")
	ErrAuthState      = errors.New("persisted authentication state is invalid")
)

const bootstrapLifetime = 30 * time.Minute
const authTimestamp = "2006-01-02T15:04:05.000000000Z"

// Admin is the public account projection; password hashes never leave the store boundary.
type Admin struct {
	ID                 string `json:"id"`
	Revision           int64  `json:"revision"`
	Username           string `json:"username"`
	MustChangePassword bool   `json:"must_change_password"`
}

// AuthSession contains metadata only. Methods return a new raw token separately for cookie delivery.
type AuthSession struct {
	ID        string    `json:"-"`
	Admin     Admin     `json:"admin"`
	ExpiresAt time.Time `json:"expires_at"`
	revoked   bool
}

// ActionAck identifies the immutable audit event associated with an acknowledged action.
type ActionAck struct {
	OK          bool   `json:"ok"`
	OperationID string `json:"operation_id"`
}

// AuthConfig configures trusted server policy, never request-supplied hash or expiry parameters.
type AuthConfig struct {
	PasswordParams  password.Params
	IdleTimeout     time.Duration
	SessionLifetime time.Duration
}

// Auth owns security transactions without exposing the underlying SQL connection.
// HTTP callers must additionally enforce Origin/CSRF, client versions, and rate limits.
type Auth struct {
	store    *Store
	hasher   *password.Hasher
	dummyPHC string
	idle     time.Duration
	lifetime time.Duration
	now      func() time.Time
}

func (s *Store) NewAuth(cfg AuthConfig) (*Auth, error) {
	if cfg.PasswordParams == (password.Params{}) {
		cfg.PasswordParams = password.DefaultParams()
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	if cfg.SessionLifetime == 0 {
		cfg.SessionLifetime = 24 * time.Hour
	}
	if cfg.IdleTimeout < time.Minute || cfg.IdleTimeout > cfg.SessionLifetime || cfg.SessionLifetime > 7*24*time.Hour {
		return nil, ErrAuthConfig
	}
	hasher, err := password.New(cfg.PasswordParams)
	if err != nil {
		return nil, err
	}
	p := cfg.PasswordParams
	// The dummy envelope consumes the configured work when no account exists. It
	// is never an account credential, and its comparison result cannot authorize.
	dummy := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", p.MemoryKiB, p.Iterations, p.Parallelism)
	return &Auth{store: s, hasher: hasher, dummyPHC: dummy, idle: cfg.IdleTimeout, lifetime: cfg.SessionLifetime, now: time.Now}, nil
}

// IssueBootstrap rotates a short-lived setup secret only before provisioning.
// The caller must write the returned token to a private file, never stdout or logs.
func (a *Auth) IssueBootstrap(ctx context.Context) (token string, expires time.Time, err error) {
	token, err = newAuthToken()
	if err != nil {
		return "", expires, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", expires, err
	}
	defer tx.Rollback()
	if err = ensureUnconfigured(ctx, tx); err != nil {
		return "", expires, err
	}
	now := a.now().UTC()
	expires = now.Add(bootstrapLifetime)
	_, err = tx.ExecContext(ctx, `INSERT INTO auth_bootstrap(singleton,token_hash,created_at,expires_at,dataset_epoch)
 VALUES(1,?,?,?,(SELECT dataset_epoch FROM sync_state WHERE singleton=1))
 ON CONFLICT(singleton) DO UPDATE SET token_hash=excluded.token_hash,
 created_at=excluded.created_at,expires_at=excluded.expires_at,dataset_epoch=excluded.dataset_epoch`, authDigest(token), stamp(now), stamp(expires))
	if err != nil {
		return "", time.Time{}, err
	}
	if _, err = appendAuthAudit(ctx, tx, "bootstrap_issued", nil, now); err != nil {
		return "", time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Setup consumes the one-time secret and creates exactly one administrator atomically.
// The initial credential must be changed before domain access is enabled by HTTP middleware.
func (a *Auth) Setup(ctx context.Context, token, username, secret string) (Admin, error) {
	if !authString(username, 1, 64) {
		return Admin{}, ErrAuthInput
	}
	if err := ensureUnconfigured(ctx, a.store.db); err != nil {
		return Admin{}, err
	}
	if err := checkBootstrap(ctx, a.store.db, token, a.now().UTC()); err != nil {
		return Admin{}, err
	}
	encoded, err := a.hasher.Hash(ctx, secret)
	if err != nil {
		return Admin{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Admin{}, err
	}
	defer tx.Rollback()
	now := a.now().UTC()
	if err = ensureUnconfigured(ctx, tx); err != nil {
		return Admin{}, err
	}
	// Recheck after hashing: another setup, rotation, or expiry may have won.
	if err = checkBootstrap(ctx, tx, token, now); err != nil {
		return Admin{}, err
	}
	admin := Admin{ID: rand.Text(), Revision: 1, Username: username, MustChangePassword: true}
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_accounts(id,username,password_hash,created_at,updated_at,revision,must_change_password)
 VALUES(?,?,?,?,?,1,1)`, admin.ID, username, encoded, stamp(now), stamp(now))
	if err != nil {
		return Admin{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM auth_bootstrap"); err != nil {
		return Admin{}, err
	}
	if _, err = appendAuthAudit(ctx, tx, "admin_setup", admin.ID, now); err != nil {
		return Admin{}, err
	}
	if err = tx.Commit(); err != nil {
		return Admin{}, err
	}
	return admin, nil
}

// Login always verifies the sole account's hash even when the username is wrong.
// Each successful login receives an independent 256-bit opaque session secret.
func (a *Auth) Login(ctx context.Context, username, secret string) (AuthSession, string, error) {
	if !authString(username, 1, 64) || !authString(secret, 1, 256) {
		return AuthSession{}, "", ErrAuthInput
	}
	admin, encoded, err := loadAdmin(ctx, a.store.db)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return AuthSession{}, "", err
	}
	if missing {
		encoded = a.dummyPHC
	}
	matched, err := a.hasher.Verify(ctx, secret, encoded)
	if err != nil {
		return AuthSession{}, "", err
	}
	if missing || !matched || subtle.ConstantTimeCompare([]byte(username), []byte(admin.Username)) != 1 {
		var adminID any
		if !missing {
			adminID = admin.ID
		}
		_, err = a.store.db.ExecContext(ctx, `INSERT INTO security_audit(id,event_type,admin_id,occurred_at,details_json)
 VALUES(?,'login_failed',?,?,'{}')`, rand.Text(), adminID, stamp(a.now()))
		if err != nil {
			return AuthSession{}, "", err
		}
		return AuthSession{}, "", ErrAuthentication
	}
	return a.commitLogin(ctx, admin, encoded)
}

// Revalidate a password proof inside the same transaction that creates its session.
func (a *Auth) commitLogin(ctx context.Context, admin Admin, encoded string) (AuthSession, string, error) {
	token, err := newAuthToken()
	if err != nil {
		return AuthSession{}, "", err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthSession{}, "", err
	}
	defer tx.Rollback()
	current, currentHash, err := loadAdmin(ctx, tx)
	if err != nil {
		return AuthSession{}, "", err
	}
	if current != admin || currentHash != encoded {
		return AuthSession{}, "", ErrAuthentication
	}
	now := a.now().UTC()
	session := AuthSession{ID: rand.Text(), Admin: current, ExpiresAt: now.Add(a.lifetime)}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(id,admin_id,token_hash,expires_at,created_at,last_seen_at,dataset_epoch)
 VALUES(?,?,?,?,?,?,(SELECT dataset_epoch FROM sync_state WHERE singleton=1))`, session.ID, admin.ID, authDigest(token), stamp(session.ExpiresAt), stamp(now), stamp(now))
	if err != nil {
		return AuthSession{}, "", err
	}
	if _, err = appendAuthAudit(ctx, tx, "login_succeeded", admin.ID, now); err != nil {
		return AuthSession{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return AuthSession{}, "", err
	}
	return session, token, nil
}

// Authenticate checks revocation and absolute/idle expiry, then records activity atomically.
func (a *Auth) Authenticate(ctx context.Context, token string) (AuthSession, error) {
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthSession{}, err
	}
	defer tx.Rollback()
	now := a.now().UTC()
	session, err := a.loadSession(ctx, tx, token, now)
	if err != nil {
		return AuthSession{}, err
	}
	if session.revoked {
		return AuthSession{}, ErrAuthentication
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at=? WHERE id=?", stamp(now), session.ID); err != nil {
		return AuthSession{}, err
	}
	if err = tx.Commit(); err != nil {
		return AuthSession{}, err
	}
	return session, nil
}

// Logout commits revocation, audit, and its frozen acknowledgement together.
// A matching retry may acknowledge a revoked token; it never authorizes any other action.
func (a *Auth) Logout(ctx context.Context, token, key string) (ActionAck, error) {
	if !authString(key, 8, 128) {
		return ActionAck{}, ErrAuthInput
	}
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
	identity := authDigest("post_auth_logout\x00" + session.ID + "\x00" + key)
	fingerprint := authDigest("post_auth_logout\x00" + session.ID)
	var operation, storedFingerprint, body string
	var status int
	err = tx.QueryRowContext(ctx, `SELECT operation,request_sha256,response_status,response_json FROM api_idempotency WHERE key=?`, identity).
		Scan(&operation, &storedFingerprint, &status, &body)
	if err == nil {
		var ack ActionAck
		if operation != "post_auth_logout" || storedFingerprint != fingerprint || status != 200 || json.Unmarshal([]byte(body), &ack) != nil || !ack.OK || ack.OperationID == "" {
			return ActionAck{}, ErrAuthState
		}
		if err = tx.Commit(); err != nil {
			return ActionAck{}, err
		}
		return ack, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ActionAck{}, err
	}
	if session.revoked {
		return ActionAck{}, ErrAuthentication
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET revoked_at=? WHERE id=?", stamp(now), session.ID); err != nil {
		return ActionAck{}, err
	}
	eventID, err := appendAuthAudit(ctx, tx, "logout", session.Admin.ID, now)
	if err != nil {
		return ActionAck{}, err
	}
	ack := ActionAck{OK: true, OperationID: eventID}
	response, err := json.Marshal(ack)
	if err != nil {
		return ActionAck{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_idempotency(key,operation,request_sha256,response_status,response_json,created_at,expires_at)
 VALUES(?,'post_auth_logout',?,200,?,?,?)`, identity, fingerprint, string(response), stamp(now), stamp(session.ExpiresAt))
	if err != nil {
		return ActionAck{}, err
	}
	if err = tx.Commit(); err != nil {
		return ActionAck{}, err
	}
	return ack, nil
}

func ensureUnconfigured(ctx context.Context, q queryer) error {
	var count int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_accounts").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrSetupClosed
	}
	return nil
}

func checkBootstrap(ctx context.Context, q queryer, token string, now time.Time) error {
	if !validAuthToken(token) {
		return ErrAuthentication
	}
	var expected, created, expires string
	err := q.QueryRowContext(ctx, `SELECT b.token_hash,b.created_at,b.expires_at FROM auth_bootstrap b
 JOIN sync_state d ON d.singleton=1 AND b.dataset_epoch=d.dataset_epoch WHERE b.singleton=1`).Scan(&expected, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAuthentication
	}
	if err != nil {
		return err
	}
	start, err1 := time.Parse(time.RFC3339Nano, created)
	end, err2 := time.Parse(time.RFC3339Nano, expires)
	if err1 != nil || err2 != nil {
		return ErrAuthState
	}
	if now.Before(start) || !now.Before(end) || subtle.ConstantTimeCompare([]byte(authDigest(token)), []byte(expected)) != 1 {
		return ErrAuthentication
	}
	return nil
}

func loadAdmin(ctx context.Context, q queryer) (Admin, string, error) {
	var admin Admin
	var hash string
	err := q.QueryRowContext(ctx, "SELECT id,revision,username,must_change_password,password_hash FROM admin_accounts LIMIT 1").
		Scan(&admin.ID, &admin.Revision, &admin.Username, &admin.MustChangePassword, &hash)
	return admin, hash, err
}

func (a *Auth) loadSession(ctx context.Context, q queryer, token string, now time.Time) (AuthSession, error) {
	if !validAuthToken(token) {
		return AuthSession{}, ErrAuthentication
	}
	var session AuthSession
	var created, expires, seen string
	var revoked sql.NullString
	err := q.QueryRowContext(ctx, `SELECT s.id,s.created_at,s.expires_at,s.last_seen_at,s.revoked_at,
 a.id,a.revision,a.username,a.must_change_password FROM sessions s JOIN admin_accounts a ON a.id=s.admin_id
 JOIN sync_state d ON d.singleton=1 AND s.dataset_epoch=d.dataset_epoch WHERE s.token_hash=?`, authDigest(token)).Scan(&session.ID, &created, &expires, &seen, &revoked,
		&session.Admin.ID, &session.Admin.Revision, &session.Admin.Username, &session.Admin.MustChangePassword)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthSession{}, ErrAuthentication
	}
	if err != nil {
		return AuthSession{}, err
	}
	start, err1 := time.Parse(time.RFC3339Nano, created)
	lastSeen, err2 := time.Parse(time.RFC3339Nano, seen)
	end, err3 := time.Parse(time.RFC3339Nano, expires)
	if err1 != nil || err2 != nil || err3 != nil {
		return AuthSession{}, ErrAuthState
	}
	if now.Before(start) || now.Before(lastSeen) || !now.Before(end) || now.Sub(lastSeen) >= a.idle {
		return AuthSession{}, ErrAuthentication
	}
	session.ExpiresAt, session.revoked = end, revoked.Valid
	return session, nil
}

func appendAuthAudit(ctx context.Context, tx *sql.Tx, kind string, adminID any, now time.Time) (string, error) {
	id := rand.Text()
	_, err := tx.ExecContext(ctx, "INSERT INTO security_audit(id,event_type,admin_id,occurred_at,details_json) VALUES(?,?,?,?,'{}')", id, kind, adminID, stamp(now))
	return id, err
}

func newAuthToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func validAuthToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	bytes, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(bytes) == 32 && base64.RawURLEncoding.EncodeToString(bytes) == token
}

func authDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func authString(value string, minimum, maximum int) bool {
	if len(value) > 4*maximum || !utf8.ValidString(value) {
		return false
	}
	n := utf8.RuneCountInString(value)
	return n >= minimum && n <= maximum
}

func stamp(value time.Time) string { return value.UTC().Format(authTimestamp) }
