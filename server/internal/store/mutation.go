package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const maxSafeInteger int64 = 9007199254740991
const domainReplyLifetime = 30 * 24 * time.Hour

var (
	ErrDomainInput          = errors.New("domain input is outside contract bounds")
	ErrDomainState          = errors.New("persisted domain state is invalid")
	ErrNotFound             = errors.New("resource does not exist")
	ErrPreconditionRequired = errors.New("a current resource revision is required")
	ErrRevisionConflict     = errors.New("resource revision changed")
	ErrRevisionOverflow     = errors.New("safe revision or sequence limit reached")
	ErrIdempotencyExpired   = errors.New("idempotency acknowledgement expired; review and use a new key")
)

type projectionKey struct{ kind, id string }

type pendingProjection struct {
	revision int64
	deleted  bool
}

// mutationTx is private to authoritative domain services. One identity receives
// one revision/change per transaction; payloads are built from the final SQL state.
// Authentication must use its independently keyed credential fingerprint instead.
type mutationTx struct {
	ctx     context.Context
	tx      *sql.Tx
	now     time.Time
	eventID string
	reason  string
	pending map[projectionKey]pendingProjection
}

type domainResult interface{ validateDomainResult() error }

// runDomainMutation never returns a successful result before COMMIT. Domain keys
// bind the administrator and dataset epoch, not a particular login session. An
// active, completed session is still required before reading any frozen reply.
func runDomainMutation[T domainResult](ctx context.Context, a *Auth, token, key, operation string, request any, apply func(*mutationTx) (T, error)) (T, error) {
	var zero T
	if !authString(key, 8, 128) {
		return zero, ErrDomainInput
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	fingerprint := authDigest(operation + "\x00" + string(requestJSON))
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	now := a.now().UTC()
	session, err := a.loadSession(ctx, tx, token, now)
	if err != nil {
		return zero, err
	}
	if err = requireCompleteSession(session); err != nil {
		return zero, err
	}
	var epoch string
	if err = tx.QueryRowContext(ctx, "SELECT dataset_epoch FROM sync_state WHERE singleton=1").Scan(&epoch); err != nil {
		return zero, err
	}
	scopedKey := domainReplyKey(session.Admin.ID, epoch, key)
	body, err := readDomainReply(ctx, tx, scopedKey, operation, fingerprint, now)
	if err != nil {
		return zero, err
	}
	var result T
	if body != "" {
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&result); err != nil {
			return zero, ErrDomainState
		}
		if err = result.validateDomainResult(); err != nil {
			return zero, err
		}
	} else {
		eventID, err := newDomainID()
		if err != nil {
			return zero, err
		}
		m := &mutationTx{ctx: ctx, tx: tx, now: now, eventID: eventID, pending: make(map[projectionKey]pendingProjection)}
		result, err = apply(m)
		if err != nil {
			return zero, err
		}
		if err = result.validateDomainResult(); err != nil {
			return zero, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return zero, err
		}
		if err = m.flush(); err != nil {
			return zero, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO api_idempotency(key,operation,request_sha256,response_status,response_json,created_at,expires_at)
 VALUES(?,?,?,200,?,?,?)`, scopedKey, operation, fingerprint, string(encoded), stamp(now), stamp(now.Add(domainReplyLifetime)))
		if err != nil {
			return zero, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mutation_events(id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json)
 VALUES(?,?,?,?,?,?,?,?)`, eventID, session.Admin.ID, epoch, operation, scopedKey, stamp(now), m.reason, string(encoded))
		if err != nil {
			return zero, err
		}
	}
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func domainReplyKey(adminID, epoch, key string) string {
	// Length-delimited JSON avoids ambiguous concatenation of imported identifiers.
	encoded, _ := json.Marshal([]string{adminID, epoch, key})
	return authDigest("domain\x00" + string(encoded))
}

func readDomainReply(ctx context.Context, tx *sql.Tx, key, operation, fingerprint string, now time.Time) (string, error) {
	var storedOperation, storedFingerprint, created, expires string
	var body, eventBody sql.NullString
	var status sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT i.operation,i.request_sha256,i.response_status,i.response_json,i.created_at,i.expires_at,e.result_json
 FROM api_idempotency i LEFT JOIN mutation_events e ON e.idempotency_key=i.key WHERE i.key=?`, key).
		Scan(&storedOperation, &storedFingerprint, &status, &body, &created, &expires, &eventBody)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if storedOperation != operation || subtle.ConstantTimeCompare([]byte(storedFingerprint), []byte(fingerprint)) != 1 {
		return "", ErrIdempotencyConflict
	}
	start, err1 := time.Parse(time.RFC3339Nano, created)
	end, err2 := time.Parse(time.RFC3339Nano, expires)
	if err1 != nil || err2 != nil || !end.After(start) || now.Before(start) {
		return "", ErrDomainState
	}
	// Keep the claim even after expiry. Never turn a long-delayed retry into a
	// second inventory/financial write by deleting or reusing an expired key.
	if !now.Before(end) {
		return "", ErrIdempotencyExpired
	}
	if !status.Valid || status.Int64 != 200 || !body.Valid || !json.Valid([]byte(body.String)) || body.String == "null" || !eventBody.Valid || body.String != eventBody.String {
		return "", ErrDomainState
	}
	return body.String, nil
}

func finishDomainSession(ctx context.Context, a *Auth, tx *sql.Tx, token string, initial AuthSession) error {
	now := a.now().UTC()
	current, err := a.loadSession(ctx, tx, token, now)
	if err != nil {
		return err
	}
	if err = requireCompleteSession(current); err != nil {
		return err
	}
	if current.ID != initial.ID || current.Admin != initial.Admin {
		return ErrAuthentication
	}
	_, err = tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at=? WHERE id=?", stamp(now), current.ID)
	return err
}

func readDomain[T any](ctx context.Context, a *Auth, token string, read func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return zero, err
	}
	if err = requireCompleteSession(session); err != nil {
		return zero, err
	}
	result, err := read(tx)
	if err != nil {
		return zero, err
	}
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func (m *mutationTx) touch(kind, id string, deleted bool) (int64, error) {
	if !validProjectionIdentity(kind, id) {
		return 0, ErrDomainInput
	}
	key := projectionKey{kind, id}
	if pending, ok := m.pending[key]; ok {
		pending.deleted = deleted
		m.pending[key] = pending
		return pending.revision, nil
	}
	var revision int64
	err := m.tx.QueryRowContext(m.ctx, `INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)
 ON CONFLICT(entity_type,entity_id) DO UPDATE SET revision=revision+1 WHERE revision BETWEEN 1 AND ? RETURNING revision`,
		kind, id, maxSafeInteger-1).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRevisionOverflow
	}
	if err != nil {
		return 0, err
	}
	m.pending[key] = pendingProjection{revision: revision, deleted: deleted}
	return revision, nil
}

func (m *mutationTx) flush() error {
	keys := make([]projectionKey, 0, len(m.pending))
	for key := range m.pending {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].id < keys[j].id
	})
	for _, key := range keys {
		pending := m.pending[key]
		operation := "upsert"
		var payload any
		if pending.deleted {
			operation = "delete"
			if _, err := m.tx.ExecContext(m.ctx, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id); err != nil {
				return err
			}
		} else {
			encoded, err := m.projectionPayload(key)
			if err != nil {
				return err
			}
			payload = string(encoded)
			_, err = m.tx.ExecContext(m.ctx, `INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at)
 VALUES(?,?,?,?,?) ON CONFLICT(entity_type,entity_id) DO UPDATE SET entity_revision=excluded.entity_revision,
 payload_json=excluded.payload_json,updated_at=excluded.updated_at`, key.kind, key.id, pending.revision, payload, stamp(m.now))
			if err != nil {
				return err
			}
		}
		inserted, err := m.tx.ExecContext(m.ctx, `INSERT INTO sync_changes(entity_type,entity_id,operation,entity_revision,projection_json,created_at)
 VALUES(?,?,?,?,?,?)`, key.kind, key.id, operation, pending.revision, payload, stamp(m.now))
		if err != nil {
			return err
		}
		seq, err := inserted.LastInsertId()
		if err != nil {
			return err
		}
		if seq < 1 || seq > maxSafeInteger {
			return ErrRevisionOverflow
		}
	}
	if len(keys) > 0 {
		var revision int64
		err := m.tx.QueryRowContext(m.ctx, `UPDATE data_revision SET revision=revision+1
 WHERE scope='catalog' AND revision BETWEEN 0 AND ? RETURNING revision`, maxSafeInteger-1).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRevisionOverflow
		}
		return err
	}
	return nil
}

func newDomainID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}
