package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// LocationMovement preserves the independent event and its exact historical
// parent IDs. It is not an assertion about the current location hierarchy.
type LocationMovement = locationMovement

// LocationMovements is one live page ordered by event ID, not by time. Deleted
// reports logical owner deletion; Revision is the retained aggregate counter.
type LocationMovements struct {
	LocationID string             `json:"location_id"`
	Deleted    bool               `json:"deleted"`
	Data       []LocationMovement `json:"data"`
	NextCursor *string            `json:"next_cursor"`
	Revision   int64              `json:"-"`
}

const locationMovementSelect = `SELECT e.id,e.location_id,e.occurred_at,e.from_parent_id,e.to_parent_id,
 COALESCE(r.revision,0),fp.id,tp.id FROM location_events e
 LEFT JOIN projection_revisions r ON r.entity_type='location_event' AND r.entity_id=e.id
 LEFT JOIN locations fp ON fp.id=e.from_parent_id LEFT JOIN locations tp ON tp.id=e.to_parent_id`

func scanLocationMovement(row rowScanner, owner string) (LocationMovement, error) {
	var value LocationMovement
	var from, to, actualFrom, actualTo sql.NullString
	var revision int64
	if err := row.Scan(&value.ID, &value.LocationID, &value.OccurredAt, &from, &to, &revision, &actualFrom, &actualTo); err != nil {
		return LocationMovement{}, err
	}
	if !validDomainID(value.ID) || value.LocationID != owner || !validDomainID(value.LocationID) ||
		!authString(value.OccurredAt, 1, 10000) || revision < 1 || revision > maxSafeInteger ||
		from.Valid && (!validDomainID(from.String) || !actualFrom.Valid || actualFrom.String != from.String) ||
		to.Valid && (!validDomainID(to.String) || !actualTo.Valid || actualTo.String != to.String) {
		return LocationMovement{}, ErrDomainState
	}
	if from.Valid {
		value.FromParentID = &from.String
	}
	if to.Valid {
		value.ToParentID = &to.String
	}
	return value, nil
}

// PageMovements reads the selected retained Location and actual immutable event
// rows after session activity, then rechecks authorization before commit. A live
// continuation binds its current aggregate revision and never authorizes a read.
func (l *Locations) PageMovements(ctx context.Context, token, id, cursor string, limit int) (LocationMovements, error) {
	if ctx == nil || l == nil || l.auth == nil || l.auth.store == nil || l.auth.store.db == nil ||
		!validDomainID(id) || limit < 1 || limit > 100 || len(cursor) > 2048 {
		return LocationMovements{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return LocationMovements{}, err
	}
	a := l.auth
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return LocationMovements{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return LocationMovements{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return LocationMovements{}, err
	}
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return LocationMovements{}, err
	}
	// A logically deleted source and its tombstone counter remain authoritative
	// for historical movement reads; a missing source is not an empty history.
	owner, err := scanLocation(tx.QueryRowContext(ctx, locationSelect+" WHERE l.id=?", id))
	if err != nil {
		return LocationMovements{}, err
	}
	var deletedAt sql.NullString
	if err = tx.QueryRowContext(ctx, "SELECT deleted_at FROM locations WHERE id=?", id).Scan(&deletedAt); err != nil {
		return LocationMovements{}, err
	}
	if deletedAt.Valid && !authString(deletedAt.String, 1, 10000) {
		return LocationMovements{}, ErrDomainState
	}
	purpose := "location-movements-cursor-v1\x00" + projectionPair("location", id) + ":" + strconv.FormatInt(owner.Revision, 10)
	page, err := readMetadataPageTx(ctx, tx, a, token, cursor, limit, purpose,
		func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[LocationMovement], error) {
			return listLocationMovements(ctx, tx, id, after, size)
		})
	if err != nil {
		return LocationMovements{}, err
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return LocationMovements{}, err
	}
	if err = tx.Commit(); err != nil {
		return LocationMovements{}, err
	}
	return LocationMovements{LocationID: id, Deleted: deletedAt.Valid, Data: page.Data, NextCursor: page.NextCursor, Revision: owner.Revision}, nil
}

func listLocationMovements(ctx context.Context, tx *sql.Tx, id, after string, limit int) (metadataPage[LocationMovement], error) {
	rows, err := tx.QueryContext(ctx, locationMovementSelect+" WHERE e.location_id=? AND e.id>? ORDER BY e.id LIMIT ?", id, after, limit+1)
	if err != nil {
		return metadataPage[LocationMovement]{}, err
	}
	page := metadataPage[LocationMovement]{Data: make([]LocationMovement, 0, limit)}
	for rows.Next() {
		value, err := scanLocationMovement(rows, id)
		if err != nil {
			return metadataPage[LocationMovement]{}, errors.Join(err, rows.Close())
		}
		if len(page.Data) == limit {
			page.NextID = page.Data[len(page.Data)-1].ID
			return page, rows.Close()
		}
		page.Data = append(page.Data, value)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return metadataPage[LocationMovement]{}, err
	}
	return page, nil
}
