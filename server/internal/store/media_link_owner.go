package store

import (
	"errors"
	"reflect"
	"slices"
)

// mediaOwnerState captures actual aggregate data and every affected location
// ancestor. Link changes do not create physical movements or search text.
type mediaOwnerState struct {
	kind, id          string
	item              *Item
	unit              *Unit
	locations         []Location
	itemUpdatedAt     string
	unitUpdatedAt     string
	locationUpdatedAt string
}

func (state mediaOwnerState) revision() int64 {
	switch state.kind {
	case "item":
		return state.item.Revision
	case "unit":
		return state.unit.Revision
	case "location":
		return state.locations[0].Revision
	default:
		return 0
	}
}

func (m *mutationTx) readMediaOwner(kind, id string) (mediaOwnerState, error) {
	state := mediaOwnerState{kind: kind, id: id}
	var pathID *string
	switch kind {
	case "item":
		item, err := loadItem(m.ctx, m.tx, id)
		if err != nil {
			return state, err
		}
		state.item = &item
	case "unit":
		unit, err := loadUnit(m.ctx, m.tx, id)
		if err != nil {
			return state, err
		}
		item, err := loadItem(m.ctx, m.tx, unit.ItemID)
		if err != nil {
			return state, mediaAuthorityError(err)
		}
		state.item, state.unit, pathID = &item, &unit, unit.LocationID
		if err = m.tx.QueryRowContext(m.ctx, "SELECT updated_at FROM item_units WHERE id=?", id).Scan(&state.unitUpdatedAt); err != nil {
			return state, err
		}
	case "location":
		pathID = &id
		if _, err := loadLocation(m.ctx, m.tx, id); err != nil {
			return state, err
		}
		if err := m.tx.QueryRowContext(m.ctx, "SELECT updated_at FROM locations WHERE id=? AND deleted_at IS NULL", id).Scan(&state.locationUpdatedAt); err != nil {
			return state, err
		}
	default:
		return state, ErrDomainInput
	}
	if state.item != nil {
		if err := m.tx.QueryRowContext(m.ctx, "SELECT updated_at FROM items WHERE id=?", state.item.ID).Scan(&state.itemUpdatedAt); err != nil {
			return state, err
		}
	}
	if pathID != nil {
		ids, err := holdingLocationPath(m.ctx, m.tx, *pathID, false)
		if err != nil {
			return state, mediaAuthorityError(err)
		}
		for _, locationID := range ids {
			value, err := loadLocation(m.ctx, m.tx, locationID)
			if err != nil {
				return state, mediaAuthorityError(err)
			}
			state.locations = append(state.locations, value)
		}
	}
	return state, nil
}

func (m *mutationTx) touchMediaOwner(before mediaOwnerState) error {
	if before.item != nil {
		if err := requireDomainRow(m.tx.ExecContext(m.ctx, "UPDATE items SET updated_at=? WHERE id=?", stamp(m.now), before.item.ID)); err != nil {
			return err
		}
		if next, err := m.touch("item", before.item.ID, false); err != nil {
			return err
		} else if next != before.item.Revision+1 {
			return ErrDomainState
		}
	}
	if before.unit != nil {
		if err := requireDomainRow(m.tx.ExecContext(m.ctx, "UPDATE item_units SET updated_at=? WHERE id=?", stamp(m.now), before.unit.ID)); err != nil {
			return err
		}
		if next, err := m.touch("unit", before.unit.ID, false); err != nil {
			return err
		} else if next != before.unit.Revision+1 {
			return ErrDomainState
		}
	}
	if before.kind == "location" {
		if err := requireDomainRow(m.tx.ExecContext(m.ctx, "UPDATE locations SET updated_at=? WHERE id=? AND deleted_at IS NULL", stamp(m.now), before.id)); err != nil {
			return err
		}
	}
	for _, value := range before.locations {
		if next, err := m.touch("location", value.ID, false); err != nil {
			return err
		} else if next != value.Revision+1 {
			return ErrDomainState
		}
	}
	return nil
}

func (m *mutationTx) verifyMediaOwner(before mediaOwnerState) error {
	expected := before
	if before.item != nil {
		item := *before.item
		item.Revision++
		expected.item, expected.itemUpdatedAt = &item, stamp(m.now)
	}
	if before.unit != nil {
		unit := *before.unit
		unit.Revision++
		expected.unit, expected.unitUpdatedAt = &unit, stamp(m.now)
	}
	expected.locations = slices.Clone(before.locations)
	for i := range expected.locations {
		expected.locations[i].Revision++
	}
	if before.kind == "location" {
		expected.locationUpdatedAt = stamp(m.now)
	}
	actual, err := m.readMediaOwner(before.kind, before.id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		return err
	}
	if !reflect.DeepEqual(expected, actual) {
		return ErrDomainState
	}
	if expected.item != nil {
		if err = m.requirePublishedMedia("item", expected.item.ID, expected.item.Revision, expected.item.ItemData); err != nil {
			return err
		}
	}
	if expected.unit != nil {
		if err = m.requirePublishedMedia("unit", expected.unit.ID, expected.unit.Revision, expected.unit.UnitData); err != nil {
			return err
		}
	}
	for _, location := range expected.locations {
		if err = m.requirePublishedMedia("location", location.ID, location.Revision, location.LocationData); err != nil {
			return err
		}
	}
	return nil
}
