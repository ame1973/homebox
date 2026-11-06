package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// InventoryEventData is the unchanged offline movement payload. Exact producer
// notes/reasons and receipt association stay in immutable authoritative history,
// not in the general offline projection or a raw public idempotency key.
type InventoryEventData struct {
	ID             string  `json:"id"`
	ItemID         string  `json:"item_id"`
	Action         string  `json:"action"`
	OccurredAt     string  `json:"occurred_at"`
	ItemUnitID     *string `json:"item_unit_id"`
	FromLocationID *string `json:"from_location_id"`
	ToLocationID   *string `json:"to_location_id"`
	QuantityBase   int64   `json:"quantity_base"`
}

func loadInventoryEvent(ctx context.Context, query queryer, id string) (InventoryEventData, error) {
	var value InventoryEventData
	var unit, from, to sql.NullString
	var revision int64
	var note, reason string
	err := query.QueryRowContext(ctx, `SELECT e.id,e.item_id,e.action,e.occurred_at,e.item_unit_id,e.from_location_id,e.to_location_id,
 e.quantity_base,COALESCE(r.revision,0),e.note,e.reason FROM inventory_events e
 LEFT JOIN projection_revisions r ON r.entity_type='inventory_event' AND r.entity_id=e.id WHERE e.id=?`, id).
		Scan(&value.ID, &value.ItemID, &value.Action, &value.OccurredAt, &unit, &from, &to, &value.QuantityBase, &revision, &note, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return InventoryEventData{}, ErrNotFound
	}
	if err != nil {
		return InventoryEventData{}, err
	}
	if unit.Valid {
		value.ItemUnitID = &unit.String
	}
	if from.Valid {
		value.FromLocationID = &from.String
	}
	if to.Valid {
		value.ToLocationID = &to.String
	}
	instant, err := time.Parse(time.RFC3339Nano, value.OccurredAt)
	if !validDomainID(value.ID) || !validDomainID(value.ItemID) || !authString(value.Action, 1, 10000) ||
		!domainInstantSyntax.MatchString(value.OccurredAt) || err != nil || instant.Year() < 1 ||
		(value.ItemUnitID != nil && !validDomainID(*value.ItemUnitID)) || (value.FromLocationID != nil && !validDomainID(*value.FromLocationID)) ||
		(value.ToLocationID != nil && !validDomainID(*value.ToLocationID)) || value.QuantityBase < -maxSafeInteger || value.QuantityBase > maxSafeInteger ||
		revision < 1 || revision > maxSafeInteger || !authString(note, 0, 10000) || !authString(reason, 0, 10000) {
		return InventoryEventData{}, ErrDomainState
	}
	return value, nil
}

func (m *mutationTx) appendStockMovement(itemID, action string, from, to *string, quantity int64, note, reason string) error {
	id, err := newDomainID()
	if err != nil {
		return err
	}
	_, err = m.tx.ExecContext(m.ctx, `INSERT INTO inventory_events(id,item_id,from_location_id,to_location_id,action,quantity_base,occurred_at,note,reason,mutation_event_id)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, id, itemID, from, to, action, quantity, stamp(m.now), note, reason, m.eventID)
	if err != nil {
		return err
	}
	revision, err := m.touch("inventory_event", id, false)
	if err != nil {
		return err
	}
	if revision != 1 {
		return ErrDomainState
	}
	return nil
}
