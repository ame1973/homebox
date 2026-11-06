package store

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrUnitTracking       = errors.New("unit commands require an individually tracked item")
	ErrUnitHistory        = errors.New("unit has retained purchase or asset references")
	ErrUnitDisposed       = errors.New("disposed unit cannot move or change lifecycle")
	ErrUnitAssetLifecycle = errors.New("asset-backed retirement and disposal require asset lifecycle commands")
)

// Complete Go values use nil for an explicit unknown location/serial. The HTTP
// boundary must separately enforce required location_id JSON member presence.
type UnitCreate struct {
	LocationID   *string `json:"location_id"`
	SerialNumber *string `json:"serial_number"`
	Notes        string  `json:"notes"`
	Status       *string `json:"status,omitempty"`
}

type UnitPatch struct {
	SerialNumber NullableString `json:"serial_number"`
	Notes        *string        `json:"notes,omitempty"`
}

type UnitStatus struct {
	Status       string  `json:"status"`
	Note         string  `json:"note"`
	DisposalKind *string `json:"disposal_kind,omitempty"`
}

func runUnitMutation[T domainResult](ctx context.Context, inventory *Inventory, token, key, operation, id string, revision int64, input any, apply func(*mutationTx) (T, error)) (T, error) {
	var zero T
	if !validDomainID(id) {
		return zero, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return zero, err
	}
	request := struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
		Input    any    `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, inventory.auth, token, key, operation, request, apply)
}

func (inventory *Inventory) CreateUnit(ctx context.Context, token, key, itemID string, itemRevision int64, input UnitCreate) (Unit, error) {
	status := "stored"
	if input.Status != nil {
		status = *input.Status
	}
	if !validUnitStatus(status) || status == "retired" || status == "disposed" || !authString(input.Notes, 0, 10000) ||
		input.LocationID != nil && !validDomainID(*input.LocationID) || input.SerialNumber != nil && !authString(*input.SerialNumber, 0, 10000) {
		return Unit{}, ErrDomainInput
	}
	return runUnitMutation(ctx, inventory, token, key, "post_items_id_units", itemID, itemRevision, input, func(m *mutationTx) (Unit, error) {
		item, err := itemAtRevision(ctx, m.tx, itemID, itemRevision)
		if err != nil {
			return Unit{}, err
		}
		if item.TrackingMode != "individual" {
			return Unit{}, ErrUnitTracking
		}
		paths, err := m.unitLocationPaths(nil, input.LocationID)
		if err != nil {
			return Unit{}, err
		}
		id, err := newDomainID()
		if err != nil {
			return Unit{}, err
		}
		// The code derives from a fresh independent UUID. A retained Unit counter
		// forbids identity/code reuse even after source deletion; no short-code guess.
		value := UnitData{ID: id, ItemID: itemID, LocationID: input.LocationID, AssetCode: "AST-" + id, Status: status, SerialNumber: input.SerialNumber, Notes: input.Notes}
		_, err = m.tx.ExecContext(ctx, `INSERT INTO item_units(id,item_id,location_id,asset_code,status,serial_number,notes,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?)`, id, itemID, value.LocationID, value.AssetCode, status, value.SerialNumber, value.Notes, stamp(m.now), stamp(m.now))
		if err != nil {
			return Unit{}, err
		}
		revision, err := m.touchUnitState(value, paths, false)
		if err != nil {
			return Unit{}, err
		}
		if revision != 1 {
			return Unit{}, ErrDomainState
		}
		if err = m.appendInventoryMovement(itemID, &id, "unit_create", nil, value.LocationID, 1, "", ""); err != nil {
			return Unit{}, err
		}
		return loadUnit(ctx, m.tx, id)
	})
}

func (inventory *Inventory) PatchUnit(ctx context.Context, token, key, id string, revision int64, input UnitPatch) (Unit, error) {
	if !input.SerialNumber.Set && input.SerialNumber.Value != nil || !input.SerialNumber.Set && input.Notes == nil ||
		input.SerialNumber.Value != nil && !authString(*input.SerialNumber.Value, 0, 10000) || input.Notes != nil && !authString(*input.Notes, 0, 10000) {
		return Unit{}, ErrDomainInput
	}
	return runUnitMutation(ctx, inventory, token, key, "patch_units_id", id, revision, input, func(m *mutationTx) (Unit, error) {
		before, err := unitAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Unit{}, err
		}
		after := before
		if input.SerialNumber.Set {
			after.SerialNumber = input.SerialNumber.Value
		}
		if input.Notes != nil {
			after.Notes = *input.Notes
		}
		if sameParent(before.SerialNumber, after.SerialNumber) && before.Notes == after.Notes {
			return before, nil
		}
		paths, err := m.unitLocationPaths(before.LocationID, nil)
		if err != nil {
			return Unit{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE item_units SET serial_number=?,notes=?,updated_at=? WHERE id=?", after.SerialNumber, after.Notes, stamp(m.now), id); err != nil {
			return Unit{}, err
		}
		if _, err = m.touchUnitState(after.UnitData, paths, false); err != nil {
			return Unit{}, err
		}
		if err = m.appendInventoryMovement(after.ItemID, &id, "unit_edit", nil, nil, 0, "", ""); err != nil {
			return Unit{}, err
		}
		return loadUnit(ctx, m.tx, id)
	})
}

func (inventory *Inventory) MoveUnit(ctx context.Context, token, key, id string, revision int64, targetID string) (Unit, error) {
	if !validDomainID(targetID) {
		return Unit{}, ErrDomainInput
	}
	input := struct {
		TargetID string `json:"target_location_id"`
	}{targetID}
	return runUnitMutation(ctx, inventory, token, key, "post_units_id_move", id, revision, input, func(m *mutationTx) (Unit, error) {
		before, err := unitAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Unit{}, err
		}
		paths, err := m.unitLocationPaths(before.LocationID, nil)
		if err != nil {
			return Unit{}, err
		}
		if sameParent(before.LocationID, &targetID) {
			return before, nil
		}
		if before.Status == "disposed" {
			return Unit{}, ErrUnitDisposed
		}
		var disposed bool
		if err = m.tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM asset_disposals WHERE item_unit_id=?)", id).Scan(&disposed); err != nil {
			return Unit{}, err
		}
		if disposed {
			return Unit{}, ErrUnitDisposed
		}
		targetPaths, err := m.unitLocationPaths(nil, &targetID)
		if err != nil {
			return Unit{}, err
		}
		paths = append(paths, targetPaths...)
		if _, err = m.tx.ExecContext(ctx, "UPDATE item_units SET location_id=?,updated_at=? WHERE id=?", targetID, stamp(m.now), id); err != nil {
			return Unit{}, err
		}
		after := before
		after.LocationID = &targetID
		if _, err = m.touchUnitState(after.UnitData, paths, false); err != nil {
			return Unit{}, err
		}
		if err = m.appendInventoryMovement(after.ItemID, &id, "unit_move", before.LocationID, &targetID, 1, "", ""); err != nil {
			return Unit{}, err
		}
		return loadUnit(ctx, m.tx, id)
	})
}

func (inventory *Inventory) SetUnitStatus(ctx context.Context, token, key, id string, revision int64, input UnitStatus) (Unit, error) {
	if !validUnitStatus(input.Status) || !authString(input.Note, 0, 10000) {
		return Unit{}, ErrDomainInput
	}
	if input.Status == "disposed" {
		if input.DisposalKind == nil {
			return Unit{}, ErrDomainInput
		}
		switch *input.DisposalKind {
		case "sold", "donated", "recycled", "discarded":
		default:
			return Unit{}, ErrDomainInput
		}
	} else if input.DisposalKind != nil {
		return Unit{}, ErrDomainInput
	}
	return runUnitMutation(ctx, inventory, token, key, "post_units_id_status", id, revision, input, func(m *mutationTx) (Unit, error) {
		before, err := unitAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Unit{}, err
		}
		m.note = input.Note
		if input.DisposalKind != nil {
			m.reason = *input.DisposalKind
		}
		// Equal status is a receipt, not reclassification of an old disposal or
		// a guessed new financial date. Exact original-key replay precedes apply.
		if before.Status == input.Status {
			return before, nil
		}
		if before.Status == "disposed" {
			return Unit{}, ErrUnitDisposed
		}
		var retired sql.NullString
		var disposed bool
		err = m.tx.QueryRowContext(ctx, `SELECT p.retired_at,EXISTS(SELECT 1 FROM asset_disposals d WHERE d.item_unit_id=p.item_unit_id)
 FROM asset_profiles p WHERE p.item_unit_id=?`, id).Scan(&retired, &disposed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Unit{}, err
		}
		if err == nil {
			if disposed {
				return Unit{}, ErrUnitDisposed
			}
			if input.Status == "retired" || input.Status == "disposed" || before.Status == "retired" || retired.Valid {
				return Unit{}, ErrUnitAssetLifecycle
			}
		}
		paths, err := m.unitLocationPaths(before.LocationID, nil)
		if err != nil {
			return Unit{}, err
		}
		if _, err = m.tx.ExecContext(ctx, "UPDATE item_units SET status=?,updated_at=? WHERE id=?", input.Status, stamp(m.now), id); err != nil {
			return Unit{}, err
		}
		after := before
		after.Status = input.Status
		if _, err = m.touchUnitState(after.UnitData, paths, false); err != nil {
			return Unit{}, err
		}
		action, quantity, from := "unit_status_"+input.Status, int64(0), (*string)(nil)
		if input.Status == "disposed" {
			action, quantity, from = "unit_dispose_"+*input.DisposalKind, -1, before.LocationID
		}
		if err = m.appendInventoryMovement(after.ItemID, &id, action, from, nil, quantity, input.Note, m.reason); err != nil {
			return Unit{}, err
		}
		return loadUnit(ctx, m.tx, id)
	})
}

func (inventory *Inventory) DeleteUnit(ctx context.Context, token, key, id string, revision int64) (ActionAck, error) {
	return runUnitMutation(ctx, inventory, token, key, "delete_units_id", id, revision, struct{}{}, func(m *mutationTx) (ActionAck, error) {
		before, err := unitAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return ActionAck{}, err
		}
		var referenced bool
		err = m.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM purchase_lines WHERE item_unit_id=?)
 OR EXISTS(SELECT 1 FROM asset_profiles WHERE item_unit_id=?) OR EXISTS(SELECT 1 FROM asset_usage_events WHERE item_unit_id=?)
 OR EXISTS(SELECT 1 FROM asset_cost_events WHERE item_unit_id=?) OR EXISTS(SELECT 1 FROM asset_disposals WHERE item_unit_id=?)`, id, id, id, id, id).Scan(&referenced)
		if err != nil {
			return ActionAck{}, err
		}
		if referenced {
			return ActionAck{}, ErrUnitHistory
		}
		paths, err := m.unitLocationPaths(before.LocationID, nil)
		if err != nil {
			return ActionAck{}, err
		}
		links, err := queryIDs(ctx, m.tx, "SELECT id FROM media_links WHERE item_unit_id=? ORDER BY id", id)
		if err != nil {
			return ActionAck{}, err
		}
		for _, link := range links {
			if !validDomainID(link) {
				return ActionAck{}, ErrDomainState
			}
			if _, err = m.touch("media_link", link, true); err != nil {
				return ActionAck{}, err
			}
		}
		// Only owned source/derived FTS/link rows cascade; shared media, purchase,
		// asset and immutable movement history must never be erased here.
		if _, err = m.tx.ExecContext(ctx, "DELETE FROM item_units WHERE id=?", id); err != nil {
			return ActionAck{}, err
		}
		next, err := m.touchUnitState(before.UnitData, paths, true)
		if err != nil {
			return ActionAck{}, err
		}
		quantity := int64(-1)
		if before.Status == "disposed" {
			quantity = 0
		}
		if err = m.appendInventoryMovement(before.ItemID, &id, "unit_delete", before.LocationID, nil, quantity, "", ""); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: next}, nil
	})
}

func unitAtRevision(ctx context.Context, query queryer, id string, revision int64) (Unit, error) {
	value, err := loadUnit(ctx, query, id)
	if err == nil && value.Revision != revision {
		return Unit{}, ErrRevisionConflict
	}
	return value, err
}

func (m *mutationTx) unitLocationPaths(source, target *string) ([]string, error) {
	var paths []string
	for _, end := range []struct {
		id     *string
		active bool
	}{{source, false}, {target, true}} {
		if end.id == nil {
			continue
		}
		path, err := holdingLocationPath(m.ctx, m.tx, *end.id, end.active)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path...)
	}
	return paths, nil
}

func (m *mutationTx) touchUnitState(value UnitData, paths []string, deleted bool) (int64, error) {
	revision, err := m.touch("unit", value.ID, deleted)
	if err != nil {
		return 0, err
	}
	if _, err = m.touch("item", value.ItemID, false); err != nil {
		return 0, err
	}
	if _, err = m.tx.ExecContext(m.ctx, "UPDATE items SET updated_at=? WHERE id=?", stamp(m.now), value.ItemID); err != nil {
		return 0, err
	}
	if _, err = m.touch("search_doc", projectionPair("unit", value.ID), deleted); err != nil {
		return 0, err
	}
	for _, id := range paths {
		if _, err = m.touch("location", id, false); err != nil {
			return 0, err
		}
	}
	return revision, nil
}
