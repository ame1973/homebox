package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var (
	errSyncSearchUnproven  = errors.New("historical search projection provenance is not established")
	errSyncPayloadTooLarge = errors.New("sync record exceeds the selected byte budget")
)

const (
	maxSyncPageBytes     = 64 << 20
	maxSyncIdentityBytes = 2*128*6 + 7
)

// Limits are chosen by the trusted caller, not by a remote request. The byte
// budget bounds buffered source fields, not a future serialized HTTP envelope.
type syncPageLimits struct {
	Records      int
	PayloadBytes int
	PageBytes    int
}

func (limits syncPageLimits) valid() bool {
	return limits.Records >= 1 && limits.Records <= 500 && limits.PayloadBytes >= 1 &&
		limits.PayloadBytes <= limits.PageBytes && limits.PageBytes <= maxSyncPageBytes
}

type syncRecord struct {
	EntityType        string          `json:"entity_type"`
	EntityID          string          `json:"entity_id"`
	EntityRevision    int64           `json:"entity_revision"`
	Operation         string          `json:"operation"`
	ProjectionVersion int64           `json:"projection_version"`
	Payload           json.RawMessage `json:"payload"`
}

type syncChange struct {
	Seq    int64      `json:"seq"`
	Record syncRecord `json:"record"`
}

type syncDeltaPage struct {
	DatasetEpoch      string       `json:"dataset_epoch"`
	ServerInstanceID  string       `json:"server_instance_id"`
	ProjectionVersion int64        `json:"projection_version"`
	HighWatermark     int64        `json:"high_watermark"`
	Changes           []syncChange `json:"changes"`
	NextCursor        string       `json:"next_cursor"`
	HasMore           bool         `json:"has_more"`
}

// readSyncDeltaPage is private groundwork, not an HTTP or offline-replica
// boundary. Backfill, historical provenance and retention remain incomplete.
// In particular, search upserts are withheld until their historical approved
// source can be established; current source cannot prove a frozen old value.
func (a *Auth) readSyncDeltaPage(ctx context.Context, token, cursor string, limits syncPageLimits) (syncDeltaPage, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil || !limits.valid() {
		return syncDeltaPage{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return syncDeltaPage{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return syncDeltaPage{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return syncDeltaPage{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return syncDeltaPage{}, err
	}
	// Session activity can run SQL triggers. Read both dataset state and frozen
	// rows only after that write, then revalidate the session before commit.
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return syncDeltaPage{}, err
	}
	if _, err = verifySyncSearchSigner(ctx, tx, a.serverSecret); err != nil {
		return syncDeltaPage{}, err
	}
	dataset, err := readSyncDataset(ctx, tx)
	if err != nil {
		return syncDeltaPage{}, err
	}
	watermark, _, err := readSyncWatermark(ctx, tx, dataset.Floor)
	if err != nil {
		return syncDeltaPage{}, err
	}
	scope := syncCursorScope{AdminID: session.Admin.ID, ServerInstanceID: dataset.Instance, DatasetEpoch: dataset.Epoch, ProjectionVersion: dataset.Version}
	position, err := decodeSyncDeltaCursor(a, cursor, scope)
	if err != nil {
		return syncDeltaPage{}, err
	}
	if err = checkSyncDeltaPosition(position.After, dataset.Floor, watermark); err != nil {
		return syncDeltaPage{}, err
	}
	changes, more, err := readSyncChanges(ctx, tx, position.After, watermark, limits)
	if err != nil {
		return syncDeltaPage{}, err
	}
	if len(changes) != 0 {
		position.After = changes[len(changes)-1].Seq
	}
	next, err := encodeSyncDeltaCursor(a, position)
	if err != nil {
		return syncDeltaPage{}, err
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return syncDeltaPage{}, err
	}
	if err = tx.Commit(); err != nil {
		return syncDeltaPage{}, err
	}
	return syncDeltaPage{DatasetEpoch: dataset.Epoch, ServerInstanceID: dataset.Instance, ProjectionVersion: dataset.Version,
		HighWatermark: watermark, Changes: changes, NextCursor: next, HasMore: more}, nil
}

func readSyncChanges(ctx context.Context, tx *sql.Tx, after, watermark int64, limits syncPageLimits) ([]syncChange, bool, error) {
	// SQL bounds individual fields before returning them to Go. The NULL flag
	// distinguishes a real tombstone from a non-NULL oversized payload.
	rows, err := tx.QueryContext(ctx, `SELECT seq,
 CASE WHEN length(CAST(entity_type AS BLOB))<=32 THEN entity_type END,
 CASE WHEN length(CAST(entity_id AS BLOB))<=? THEN entity_id END,
 CASE WHEN length(CAST(operation AS BLOB))<=16 THEN operation END,
 entity_revision,
 CASE WHEN length(CAST(projection_json AS BLOB))<=? THEN projection_json END,
 projection_json IS NULL,
 CASE WHEN length(CAST(created_at AS BLOB))<=35 THEN created_at END
 FROM sync_changes WHERE seq>? AND seq<=? ORDER BY seq LIMIT ?`,
		maxSyncIdentityBytes, limits.PayloadBytes, after, watermark, limits.Records+1)
	if err != nil {
		return nil, false, err
	}
	changes := make([]syncChange, 0, limits.Records)
	var used int
	more := false
	readErr := func() error {
		previous := after
		for rows.Next() {
			var change syncChange
			var kind, id, operation, payload, created sql.NullString
			var nullPayload bool
			if err := rows.Scan(&change.Seq, &kind, &id, &operation, &change.Record.EntityRevision, &payload, &nullPayload, &created); err != nil {
				return err
			}
			if change.Seq <= previous || change.Seq > watermark || !kind.Valid || !id.Valid || !operation.Valid || !created.Valid || !validPurchaseInstant(created.String) {
				return ErrDomainState
			}
			if !nullPayload && !payload.Valid {
				return errSyncPayloadTooLarge
			}
			change.Record.EntityType, change.Record.EntityID, change.Record.Operation = kind.String, id.String, operation.String
			change.Record.ProjectionVersion = 1
			if payload.Valid {
				change.Record.Payload = json.RawMessage(payload.String)
			}
			if err := validateSyncRecord(kind.String, id.String, operation.String, change.Record.EntityRevision, 1, change.Record.Payload); err != nil {
				return err
			}
			if kind.String == "search_doc" && operation.String == "upsert" {
				return errSyncSearchUnproven
			}
			size := len(kind.String) + len(id.String) + len(operation.String) + len(change.Record.Payload)
			if size > limits.PageBytes {
				return errSyncPayloadTooLarge
			}
			if len(changes) == limits.Records || used+size > limits.PageBytes {
				more = true
				break
			}
			changes = append(changes, change)
			used += size
			previous = change.Seq
		}
		return nil
	}()
	if err = errors.Join(readErr, rows.Err(), rows.Close()); err != nil {
		return nil, false, err
	}
	return changes, more, nil
}
