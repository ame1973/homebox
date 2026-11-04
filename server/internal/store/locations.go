package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var (
	ErrLocationCode     = errors.New("location code is already reserved")
	ErrLocationCycle    = errors.New("location cannot contain itself or an ancestor")
	ErrLocationArchived = errors.New("destination location or an ancestor is archived")
	ErrLocationChildren = errors.New("location has children that require explicit review")
	ErrLocationInUse    = errors.New("location contains inventory references")
)

// LocationData is exactly the version-one offline location payload. Paths are
// derived from stable parent IDs; moving a container never rewrites its contents.
type LocationData struct {
	ID          string  `json:"id"`
	ParentID    *string `json:"parent_id"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Kind        string  `json:"kind"`
	SortOrder   int64   `json:"sort_order"`
	Description string  `json:"description"`
}

// Location adds an aggregate revision, including changes to descendants.
type Location struct {
	LocationData
	Revision int64 `json:"revision"`
}

type LocationCreate struct {
	ParentID    *string `json:"parent_id"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Kind        *string `json:"kind,omitempty"`
	Status      *string `json:"status,omitempty"`
	SortOrder   int64   `json:"sort_order"`
	Description string  `json:"description"`
}

// NullableID is a command field, not a wire object. Set distinguishes an omitted
// parent from an explicit move to the root. HTTP adapters must preserve presence.
type NullableID struct {
	Set   bool    `json:"set"`
	Value *string `json:"value"`
}

type LocationPatch struct {
	ParentID    NullableID `json:"parent_id"`
	Code        *string    `json:"code,omitempty"`
	Name        *string    `json:"name,omitempty"`
	Kind        *string    `json:"kind,omitempty"`
	Status      *string    `json:"status,omitempty"`
	SortOrder   *int64     `json:"sort_order,omitempty"`
	Description *string    `json:"description,omitempty"`
}

type LocationPage struct {
	Data   []Location `json:"data"`
	NextID string     `json:"-"`
}

// Locations keeps authorization, source writes, and projection publication behind
// the store boundary. HTTP still owns Origin/CSRF/version/admission checks.
type Locations struct{ auth *Auth }

func (s *Store) NewLocations(a *Auth) (*Locations, error) {
	if s == nil || a == nil || a.store != s {
		return nil, ErrDomainInput
	}
	return &Locations{auth: a}, nil
}

func (l *Locations) Get(ctx context.Context, token, id string) (Location, error) {
	if !validDomainID(id) {
		return Location{}, ErrDomainInput
	}
	return readDomain(ctx, l.auth, token, func(tx *sql.Tx) (Location, error) {
		return loadLocation(ctx, tx, id)
	})
}

// List uses stable ID ordering. NextID is internal continuation data, not an
// unsigned HTTP cursor and not a frozen offline bootstrap snapshot.
func (l *Locations) List(ctx context.Context, token, afterID string, limit int) (LocationPage, error) {
	if (afterID != "" && !validDomainID(afterID)) || limit < 1 || limit > 100 {
		return LocationPage{}, ErrDomainInput
	}
	return readDomain(ctx, l.auth, token, func(tx *sql.Tx) (LocationPage, error) {
		rows, err := tx.QueryContext(ctx, locationSelect+" WHERE l.deleted_at IS NULL AND l.id>? ORDER BY l.id LIMIT ?", afterID, limit+1)
		if err != nil {
			return LocationPage{}, err
		}
		defer rows.Close()
		page := LocationPage{Data: make([]Location, 0)}
		for rows.Next() {
			location, err := scanLocation(rows)
			if err != nil {
				return LocationPage{}, err
			}
			page.Data = append(page.Data, location)
		}
		if err = rows.Err(); err != nil {
			return LocationPage{}, err
		}
		if len(page.Data) > limit {
			page.Data = page.Data[:limit]
			page.NextID = page.Data[limit-1].ID
		}
		return page, nil
	})
}

// Create checks a non-root parent's aggregate revision inside the write
// transaction. A root creation has no parent precondition (parentRevision=0).
func (l *Locations) Create(ctx context.Context, token, key string, parentRevision int64, input LocationCreate) (Location, error) {
	data := LocationData{ParentID: input.ParentID, Code: input.Code, Name: input.Name, Kind: "other", Status: "active", SortOrder: input.SortOrder, Description: input.Description}
	if input.Kind != nil {
		data.Kind = *input.Kind
	}
	if input.Status != nil {
		data.Status = *input.Status
	}
	if !validLocationData(data, false) || (data.ParentID == nil && parentRevision != 0) {
		return Location{}, ErrDomainInput
	}
	if data.ParentID != nil {
		if err := validExpectedRevision(parentRevision); err != nil {
			return Location{}, err
		}
	}
	request := struct {
		ParentRevision int64          `json:"parent_revision"`
		Input          LocationCreate `json:"input"`
	}{parentRevision, input}
	return runDomainMutation(ctx, l.auth, token, key, "post_locations", request, func(m *mutationTx) (Location, error) {
		if data.ParentID != nil {
			parent, err := loadLocation(ctx, m.tx, *data.ParentID)
			if err != nil {
				return Location{}, err
			}
			if parent.Revision != parentRevision {
				return Location{}, ErrRevisionConflict
			}
			if err = requireActiveLocationPath(ctx, m.tx, data.ParentID); err != nil {
				return Location{}, err
			}
		}
		if err := requireLocationCode(ctx, m.tx, data.Code, ""); err != nil {
			return Location{}, err
		}
		id, err := newDomainID()
		if err != nil {
			return Location{}, err
		}
		_, err = m.tx.ExecContext(ctx, `INSERT INTO locations(id,parent_id,code,name,kind,status,sort_order,description,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, id, data.ParentID, data.Code, data.Name, data.Kind, data.Status, data.SortOrder, data.Description, stamp(m.now), stamp(m.now))
		if err != nil {
			return Location{}, err
		}
		if err = m.touchLocationPath(id); err != nil {
			return Location{}, err
		}
		return loadLocation(ctx, m.tx, id)
	})
}

func (l *Locations) Patch(ctx context.Context, token, key, id string, revision int64, input LocationPatch) (Location, error) {
	if !validDomainID(id) || !input.hasFields() || (!input.ParentID.Set && input.ParentID.Value != nil) {
		return Location{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Location{}, err
	}
	request := struct {
		ID       string        `json:"id"`
		Revision int64         `json:"revision"`
		Input    LocationPatch `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, l.auth, token, key, "patch_locations_id", request, func(m *mutationTx) (Location, error) {
		return m.patchLocation(id, revision, input)
	})
}

func (l *Locations) Move(ctx context.Context, token, key, id string, revision int64, parentID *string) (Location, error) {
	if !validDomainID(id) || (parentID != nil && !validDomainID(*parentID)) {
		return Location{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Location{}, err
	}
	request := struct {
		ID       string  `json:"id"`
		Revision int64   `json:"revision"`
		ParentID *string `json:"target_parent_id"`
	}{id, revision, parentID}
	return runDomainMutation(ctx, l.auth, token, key, "post_locations_id_move", request, func(m *mutationTx) (Location, error) {
		return m.patchLocation(id, revision, LocationPatch{ParentID: NullableID{Set: true, Value: parentID}})
	})
}

func (l *Locations) Archive(ctx context.Context, token, key, id string, revision int64, archiveChildren bool, reason string) (Location, error) {
	if !validDomainID(id) || !authString(reason, 0, 10000) {
		return Location{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Location{}, err
	}
	request := struct {
		ID              string `json:"id"`
		Revision        int64  `json:"revision"`
		ArchiveChildren bool   `json:"archive_children"`
		Reason          string `json:"reason"`
	}{id, revision, archiveChildren, reason}
	return runDomainMutation(ctx, l.auth, token, key, "post_locations_id_archive", request, func(m *mutationTx) (Location, error) {
		if _, err := locationAtRevision(ctx, m.tx, id, revision); err != nil {
			return Location{}, err
		}
		ids, err := locationSubtree(ctx, m.tx, id)
		if err != nil {
			return Location{}, err
		}
		if err = requireArchivableSubtree(ctx, m.tx, id, archiveChildren); err != nil {
			return Location{}, err
		}
		m.reason = reason
		for _, childID := range ids {
			current, err := loadLocation(ctx, m.tx, childID)
			if err != nil {
				return Location{}, err
			}
			if current.Status == "archived" {
				continue
			}
			if _, err = m.tx.ExecContext(ctx, "UPDATE locations SET status='archived',updated_at=? WHERE id=?", stamp(m.now), childID); err != nil {
				return Location{}, err
			}
			if err = m.touchLocationPath(childID); err != nil {
				return Location{}, err
			}
		}
		return loadLocation(ctx, m.tx, id)
	})
}

// Delete removes a leaf from public/current projections while retaining its row
// for immutable historical references. Codes and IDs are never reassigned.
func (l *Locations) Delete(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
	if !validDomainID(id) {
		return ActionAck{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return ActionAck{}, err
	}
	request := struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}{id, revision}
	return runDomainMutation(ctx, l.auth, token, key, "delete_locations_id", request, func(m *mutationTx) (ActionAck, error) {
		before, err := locationAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return ActionAck{}, err
		}
		var children, references bool
		if err = m.tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM locations WHERE parent_id=? AND deleted_at IS NULL)", id).Scan(&children); err != nil {
			return ActionAck{}, err
		}
		if children {
			return ActionAck{}, ErrLocationChildren
		}
		// Even a disposed unit or zero balance still has a published location
		// reference. Archive instead of creating dangling replica metadata.
		if err = m.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM item_units WHERE location_id=?)
 OR EXISTS(SELECT 1 FROM stock_balances WHERE location_id=?)`, id, id).Scan(&references); err != nil {
			return ActionAck{}, err
		}
		if references {
			return ActionAck{}, ErrLocationInUse
		}
		links, err := queryIDs(ctx, m.tx, "SELECT id FROM media_links WHERE location_id=? ORDER BY id", id)
		if err != nil {
			return ActionAck{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "DELETE FROM media_links WHERE location_id=?", id); err != nil {
			return ActionAck{}, err
		}
		for _, linkID := range links {
			if _, err = m.touch("media_link", linkID, true); err != nil {
				return ActionAck{}, err
			}
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE locations SET deleted_at=?,updated_at=? WHERE id=?", stamp(m.now), stamp(m.now), id); err != nil {
			return ActionAck{}, err
		}
		if before.ParentID != nil {
			if err = m.touchLocationPath(*before.ParentID); err != nil {
				return ActionAck{}, err
			}
		}
		nextRevision, err := m.touch("location", id, true)
		if err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: nextRevision}, nil
	})
}

const locationSelect = `SELECT l.id,l.parent_id,l.code,l.name,l.status,l.kind,l.sort_order,l.description,COALESCE(r.revision,0)
 FROM locations l LEFT JOIN projection_revisions r ON r.entity_type='location' AND r.entity_id=l.id`

type rowScanner interface{ Scan(...any) error }

func scanLocation(row rowScanner) (Location, error) {
	var location Location
	var parent sql.NullString
	err := row.Scan(&location.ID, &parent, &location.Code, &location.Name, &location.Status, &location.Kind, &location.SortOrder, &location.Description, &location.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Location{}, ErrNotFound
	}
	if err != nil {
		return Location{}, err
	}
	if parent.Valid {
		location.ParentID = &parent.String
	}
	if !validLocationData(location.LocationData, true) || location.Revision < 1 || location.Revision > maxSafeInteger {
		return Location{}, ErrDomainState
	}
	return location, nil
}

func loadLocation(ctx context.Context, q queryer, id string) (Location, error) {
	return scanLocation(q.QueryRowContext(ctx, locationSelect+" WHERE l.id=? AND l.deleted_at IS NULL", id))
}

func locationAtRevision(ctx context.Context, q queryer, id string, revision int64) (Location, error) {
	location, err := loadLocation(ctx, q, id)
	if err != nil {
		return Location{}, err
	}
	if location.Revision != revision {
		return Location{}, ErrRevisionConflict
	}
	return location, nil
}

func (location Location) validateDomainResult() error {
	if !validLocationData(location.LocationData, true) || location.Revision < 1 || location.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (ack ActionAck) validateDomainResult() error {
	if !ack.OK || !validDomainID(ack.OperationID) || ack.Revision < 1 || ack.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func validDomainID(id string) bool { return authString(id, 1, 128) }

func validExpectedRevision(revision int64) error {
	if revision == 0 {
		return ErrPreconditionRequired
	}
	if revision < 1 || revision > maxSafeInteger {
		return ErrDomainInput
	}
	return nil
}

func validLocationData(location LocationData, hasID bool) bool {
	return (!hasID || validDomainID(location.ID)) &&
		(location.ParentID == nil || (validDomainID(*location.ParentID) && (!hasID || *location.ParentID != location.ID))) &&
		authString(location.Code, 1, 100) && authString(location.Name, 1, 200) && authString(location.Kind, 1, 32) &&
		authString(location.Description, 0, 10000) && (location.Status == "active" || location.Status == "archived") &&
		location.SortOrder >= -maxSafeInteger && location.SortOrder <= maxSafeInteger
}

func requireLocationCode(ctx context.Context, tx *sql.Tx, code, exceptID string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM locations WHERE code=? AND id<>?)", code, exceptID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrLocationCode
	}
	return nil
}

const ancestorCTE = `WITH RECURSIVE ancestors(id,parent_id,status) AS (
 SELECT id,parent_id,status FROM locations WHERE id=? AND deleted_at IS NULL
 UNION SELECT p.id,p.parent_id,p.status FROM locations p JOIN ancestors a ON p.id=a.parent_id WHERE p.deleted_at IS NULL
) `

func requireActiveLocationPath(ctx context.Context, tx *sql.Tx, id *string) error {
	if id == nil {
		return nil
	}
	if _, err := loadLocation(ctx, tx, *id); err != nil {
		return err
	}
	var archived bool
	if err := tx.QueryRowContext(ctx, ancestorCTE+"SELECT EXISTS(SELECT 1 FROM ancestors WHERE status='archived')", *id).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return ErrLocationArchived
	}
	return nil
}

func (m *mutationTx) touchLocationPath(id string) error {
	ids, err := queryIDs(m.ctx, m.tx, ancestorCTE+"SELECT id FROM ancestors ORDER BY id", id)
	if err != nil {
		return err
	}
	for _, ancestor := range ids {
		if _, err = m.touch("location", ancestor, false); err != nil {
			return err
		}
	}
	return nil
}

const subtreeCTE = `WITH RECURSIVE subtree(id) AS (
 SELECT id FROM locations WHERE id=? AND deleted_at IS NULL
 UNION SELECT c.id FROM locations c JOIN subtree p ON c.parent_id=p.id WHERE c.deleted_at IS NULL
) `

func locationSubtree(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	return queryIDs(ctx, tx, subtreeCTE+"SELECT id FROM subtree ORDER BY id", id)
}

func requireArchivableSubtree(ctx context.Context, tx *sql.Tx, id string, includeChildren bool) error {
	if !includeChildren {
		var activeChildren bool
		if err := tx.QueryRowContext(ctx, subtreeCTE+`SELECT EXISTS(
 SELECT 1 FROM locations WHERE id IN(SELECT id FROM subtree) AND id<>? AND status='active')`, id, id).Scan(&activeChildren); err != nil {
			return err
		}
		if activeChildren {
			return ErrLocationChildren
		}
	}
	var occupied bool
	if err := tx.QueryRowContext(ctx, subtreeCTE+`SELECT EXISTS(
 SELECT 1 FROM item_units WHERE location_id IN(SELECT id FROM subtree) AND status<>'disposed') OR EXISTS(
 SELECT 1 FROM stock_balances WHERE location_id IN(SELECT id FROM subtree) AND quantity_base>0)`, id).Scan(&occupied); err != nil {
		return err
	}
	if occupied {
		return ErrLocationInUse
	}
	return nil
}

func queryIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (input LocationPatch) hasFields() bool {
	return input.ParentID.Set || input.Code != nil || input.Name != nil || input.Kind != nil || input.Status != nil || input.SortOrder != nil || input.Description != nil
}

func sameParent(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (m *mutationTx) patchLocation(id string, revision int64, input LocationPatch) (Location, error) {
	before, err := locationAtRevision(m.ctx, m.tx, id, revision)
	if err != nil {
		return Location{}, err
	}
	after := before
	if input.ParentID.Set {
		after.ParentID = input.ParentID.Value
	}
	for _, field := range []struct{ supplied, destination *string }{
		{input.Code, &after.Code}, {input.Name, &after.Name}, {input.Kind, &after.Kind},
		{input.Status, &after.Status}, {input.Description, &after.Description},
	} {
		if field.supplied != nil {
			*field.destination = *field.supplied
		}
	}
	if input.SortOrder != nil {
		after.SortOrder = *input.SortOrder
	}
	if !validLocationData(after.LocationData, true) {
		if after.ParentID != nil && *after.ParentID == id {
			return Location{}, ErrLocationCycle
		}
		return Location{}, ErrDomainInput
	}
	moved := !sameParent(before.ParentID, after.ParentID)
	if moved && after.ParentID != nil {
		var cycle bool
		if err = m.tx.QueryRowContext(m.ctx, subtreeCTE+"SELECT EXISTS(SELECT 1 FROM subtree WHERE id=?)", id, *after.ParentID).Scan(&cycle); err != nil {
			return Location{}, err
		}
		if cycle {
			return Location{}, ErrLocationCycle
		}
	}
	if moved || after.Status == "active" {
		if err = requireActiveLocationPath(m.ctx, m.tx, after.ParentID); err != nil {
			return Location{}, err
		}
	}
	if before.Status != "archived" && after.Status == "archived" {
		if err = requireArchivableSubtree(m.ctx, m.tx, id, false); err != nil {
			return Location{}, err
		}
	}
	if err = requireLocationCode(m.ctx, m.tx, after.Code, id); err != nil {
		return Location{}, err
	}
	if !moved && before.Code == after.Code && before.Name == after.Name && before.Kind == after.Kind &&
		before.Status == after.Status && before.SortOrder == after.SortOrder && before.Description == after.Description {
		return before, nil
	}
	_, err = m.tx.ExecContext(m.ctx, `UPDATE locations SET parent_id=?,code=?,name=?,kind=?,status=?,sort_order=?,description=?,updated_at=? WHERE id=?`,
		after.ParentID, after.Code, after.Name, after.Kind, after.Status, after.SortOrder, after.Description, stamp(m.now), id)
	if err != nil {
		return Location{}, err
	}
	if moved {
		eventID, err := newDomainID()
		if err != nil {
			return Location{}, err
		}
		if _, err = m.tx.ExecContext(m.ctx, `INSERT INTO location_events(id,location_id,from_parent_id,to_parent_id,occurred_at)
 VALUES(?,?,?,?,?)`, eventID, id, before.ParentID, after.ParentID, stamp(m.now)); err != nil {
			return Location{}, err
		}
		if _, err = m.touch("location_event", eventID, false); err != nil {
			return Location{}, err
		}
		if before.ParentID != nil {
			if err = m.touchLocationPath(*before.ParentID); err != nil {
				return Location{}, err
			}
		}
	}
	if err = m.touchLocationPath(id); err != nil {
		return Location{}, err
	}
	return loadLocation(m.ctx, m.tx, id)
}

type locationMovement struct {
	ID           string  `json:"id"`
	LocationID   string  `json:"location_id"`
	OccurredAt   string  `json:"occurred_at"`
	FromParentID *string `json:"from_parent_id"`
	ToParentID   *string `json:"to_parent_id"`
}

func (m *mutationTx) projectionPayload(key projectionKey) ([]byte, error) {
	switch key.kind {
	case "location":
		location, err := loadLocation(m.ctx, m.tx, key.id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(location.LocationData)
	case "location_event":
		var movement locationMovement
		var from, to sql.NullString
		err := m.tx.QueryRowContext(m.ctx, `SELECT id,location_id,occurred_at,from_parent_id,to_parent_id FROM location_events WHERE id=?`, key.id).
			Scan(&movement.ID, &movement.LocationID, &movement.OccurredAt, &from, &to)
		if err != nil {
			return nil, err
		}
		if from.Valid {
			movement.FromParentID = &from.String
		}
		if to.Valid {
			movement.ToParentID = &to.String
		}
		return json.Marshal(movement)
	default:
		return nil, ErrDomainState
	}
}
