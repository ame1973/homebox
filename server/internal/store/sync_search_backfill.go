package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
)

// This private single-source operation is not a public backfill job or proof of
// complete coverage. A new command consumes a session-bound grant atomically;
// an original-key replay returns its frozen receipt without consuming another.
type syncSearchBackfillRequest struct {
	EntityType   string `json:"entity_type"`
	EntityID     string `json:"entity_id"`
	ItemID       string `json:"item_id"`
	ItemRevision int64  `json:"item_revision"`
	UnitRevision int64  `json:"unit_revision"`
}

func (value syncSearchBackfillRequest) validate() error {
	if !validDomainID(value.EntityID) || !validDomainID(value.ItemID) {
		return ErrDomainInput
	}
	if err := validExpectedRevision(value.ItemRevision); err != nil {
		return err
	}
	switch value.EntityType {
	case "item":
		if value.EntityID != value.ItemID || value.UnitRevision != 0 {
			return ErrDomainInput
		}
	case "unit":
		if err := validExpectedRevision(value.UnitRevision); err != nil {
			return err
		}
	default:
		return ErrDomainInput
	}
	return nil
}

type syncSearchCurrent struct {
	revision int64
	payload  string
	updated  string
}

func readSyncSearchCurrent(ctx context.Context, tx *sql.Tx, identity string) (syncSearchCurrent, bool, error) {
	var value syncSearchCurrent
	var version int64
	var counter sql.NullInt64
	var payload, updated sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT c.entity_revision,c.projection_version,
 CASE WHEN length(CAST(c.payload_json AS BLOB))<=? THEN c.payload_json END,
 CASE WHEN length(CAST(c.updated_at AS BLOB))<=40000 THEN c.updated_at END,r.revision
 FROM current_projections c LEFT JOIN projection_revisions r ON r.entity_type=c.entity_type AND r.entity_id=c.entity_id
 WHERE c.entity_type='search_doc' AND c.entity_id=?`, maxSyncPageBytes, identity).
		Scan(&value.revision, &version, &payload, &updated, &counter)
	if errors.Is(err, sql.ErrNoRows) {
		return syncSearchCurrent{}, false, nil
	}
	if err != nil {
		return syncSearchCurrent{}, false, err
	}
	if !payload.Valid {
		return syncSearchCurrent{}, false, errSyncPayloadTooLarge
	}
	if value.revision < 1 || value.revision > maxSafeInteger || version != 1 || !counter.Valid || counter.Int64 != value.revision ||
		!updated.Valid || !authString(updated.String, 1, 10000) {
		return syncSearchCurrent{}, false, ErrDomainState
	}
	value.payload, value.updated = payload.String, updated.String
	return value, true, nil
}

// A missing counter is safe to initialize only before retention and only when
// no retained change or same-dataset snapshot names this identity. Existing
// counters must not lag either history; never silently reuse a revision.
func readSyncSearchCounter(ctx context.Context, tx *sql.Tx, dataset syncDataset, identity string) (sql.NullInt64, error) {
	if _, _, err := readSyncWatermark(ctx, tx, dataset.Floor); err != nil {
		return sql.NullInt64{}, err
	}
	var counter, historical, frozen sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT revision FROM projection_revisions WHERE entity_type='search_doc' AND entity_id=?),
 (SELECT MAX(entity_revision) FROM sync_changes WHERE entity_type='search_doc' AND entity_id=?),
 (SELECT MAX(r.entity_revision) FROM sync_snapshot_rows r JOIN sync_snapshots s ON s.id=r.snapshot_id
  WHERE r.entity_type='search_doc' AND r.entity_id=? AND s.server_instance_id=? AND s.dataset_epoch=? AND s.projection_version=?)`,
		identity, identity, identity, dataset.Instance, dataset.Epoch, dataset.Version).
		Scan(&counter, &historical, &frozen)
	if err != nil {
		return sql.NullInt64{}, err
	}
	if counter.Valid && (counter.Int64 < 1 || counter.Int64 > maxSafeInteger) ||
		historical.Valid && (historical.Int64 < 1 || historical.Int64 > maxSafeInteger) ||
		frozen.Valid && (frozen.Int64 < 1 || frozen.Int64 > maxSafeInteger) ||
		!counter.Valid && (historical.Valid || frozen.Valid || dataset.Floor != 0) ||
		counter.Valid && historical.Valid && historical.Int64 > counter.Int64 ||
		counter.Valid && frozen.Valid && frozen.Int64 > counter.Int64 {
		return sql.NullInt64{}, ErrDomainState
	}
	return counter, nil
}

type syncBackfillGrant struct {
	sessionID, created, expires string
	adminRevision               int64
	consumed                    sql.NullString
}

func readSyncBackfillGrant(ctx context.Context, tx *sql.Tx, hash string) (syncBackfillGrant, error) {
	var value syncBackfillGrant
	var sessionID, created, expires sql.NullString
	var consumedNull bool
	err := tx.QueryRowContext(ctx, `SELECT
 CASE WHEN length(CAST(session_id AS BLOB))<=512 THEN session_id END,admin_revision,
 CASE WHEN length(CAST(created_at AS BLOB))<=35 THEN created_at END,
 CASE WHEN length(CAST(expires_at AS BLOB))<=35 THEN expires_at END,
 CASE WHEN length(CAST(consumed_at AS BLOB))<=35 THEN consumed_at END,consumed_at IS NULL
 FROM auth_reauth_grants WHERE token_hash=?`, hash).
		Scan(&sessionID, &value.adminRevision, &created, &expires, &value.consumed, &consumedNull)
	if err != nil {
		return syncBackfillGrant{}, err
	}
	if !sessionID.Valid || !validDomainID(sessionID.String) || !created.Valid || !expires.Valid ||
		(!consumedNull && !value.consumed.Valid) {
		return syncBackfillGrant{}, ErrAuthState
	}
	value.sessionID, value.created, value.expires = sessionID.String, created.String, expires.String
	return value, nil
}

func (a *Auth) consumeSyncBackfillGrant(m *mutationTx, token, grant string) error {
	if !validAuthToken(grant) {
		return ErrReauthRequired
	}
	hash := authDigest(grant)
	before, err := readSyncBackfillGrant(m.ctx, m.tx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrReauthRequired
	}
	if err != nil {
		return err
	}
	session, err := a.consumeReauth(m.ctx, m.tx, token, grant, m.now)
	if err != nil {
		return err
	}
	if before.sessionID != session.ID || before.adminRevision != session.Admin.Revision || before.consumed.Valid {
		return ErrAuthState
	}
	expected := before
	expected.consumed = sql.NullString{String: stamp(m.now), Valid: true}
	check := func() error {
		actual, err := readSyncBackfillGrant(m.ctx, m.tx, hash)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAuthState
		}
		if err != nil {
			return err
		}
		if actual != expected {
			return ErrAuthState
		}
		return nil
	}
	if err = check(); err != nil {
		return err
	}
	m.finalChecks = append(m.finalChecks, check)
	return nil
}

func (a *Auth) backfillSyncSearch(ctx context.Context, token, grant, key string, request syncSearchBackfillRequest) (ActionAck, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil {
		return ActionAck{}, ErrDomainInput
	}
	if err := request.validate(); err != nil {
		return ActionAck{}, err
	}
	return runDomainMutation(ctx, a, token, key, "internal_sync_search_backfill", request, func(m *mutationTx) (ActionAck, error) {
		item, err := itemAtRevision(ctx, m.tx, request.ItemID, request.ItemRevision)
		if err != nil {
			return ActionAck{}, err
		}
		var unit *Unit
		if request.EntityType == "unit" {
			value, err := unitAtRevision(ctx, m.tx, request.EntityID, request.UnitRevision)
			if err != nil {
				return ActionAck{}, err
			}
			if value.ItemID != item.ID {
				return ActionAck{}, ErrDomainInput
			}
			unit = &value
		}
		identity := projectionPair(request.EntityType, request.EntityID)
		if _, err = readSyncSearchCounter(ctx, m.tx, m.dataset, identity); err != nil {
			return ActionAck{}, err
		}
		current, present, err := readSyncSearchCurrent(ctx, m.tx, identity)
		if err != nil {
			return ActionAck{}, err
		}
		if present {
			if err = validateSearchProjectionSource(ctx, m.tx, identity, current.revision, []byte(current.payload)); err != nil {
				return ActionAck{}, err
			}
		}
		if err = a.consumeSyncBackfillGrant(m, token, grant); err != nil {
			return ActionAck{}, err
		}
		revision := current.revision
		m.finalChecks = append(m.finalChecks, func() error {
			dataset, err := readSyncDataset(ctx, m.tx)
			if err != nil {
				return err
			}
			if dataset != m.dataset {
				return ErrDomainState
			}
			counter, err := readSyncSearchCounter(ctx, m.tx, dataset, identity)
			if err != nil {
				return err
			}
			if !counter.Valid || counter.Int64 != revision {
				return ErrDomainState
			}
			signer, err := verifySyncSearchSigner(ctx, m.tx, a.serverSecret)
			if err != nil {
				return err
			}
			if signer != m.searchSignerPinned {
				return ErrAuthState
			}
			receipt, err := verifyKeyedReceiptSecret(ctx, m.tx, a.serverSecret)
			if err != nil {
				return err
			}
			if receipt != m.receiptPinned {
				return ErrAuthState
			}
			actualItem, err := loadItem(ctx, m.tx, item.ID)
			if err != nil || !reflect.DeepEqual(actualItem, item) {
				if err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
				return ErrDomainState
			}
			if unit != nil {
				actualUnit, err := loadUnit(ctx, m.tx, unit.ID)
				if err != nil || !reflect.DeepEqual(actualUnit, *unit) {
					if err != nil && !errors.Is(err, ErrNotFound) {
						return err
					}
					return ErrDomainState
				}
			}
			if present {
				actual, exists, err := readSyncSearchCurrent(ctx, m.tx, identity)
				if err != nil {
					return err
				}
				if !exists || actual != current {
					return ErrDomainState
				}
				return validateSearchProjectionSource(ctx, m.tx, identity, current.revision, []byte(current.payload))
			}
			return nil
		})
		if !present {
			revision, err = m.touch("search_doc", identity, false)
			if err != nil {
				return ActionAck{}, err
			}
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: revision}, nil
	})
}
