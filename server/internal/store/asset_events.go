package store

import (
	"context"
	"reflect"
	"time"

	"homebox/internal/currency"
)

// UsageWrite appends a delta, never a replacement count or an event edit.
// The HTTP reader separately enforces required fields and optional defaults.
type UsageWrite struct {
	Delta        int64   `json:"delta"`
	UsageUnit    string  `json:"usage_unit"`
	EventType    string  `json:"event_type"`
	OccurredAt   string  `json:"occurred_at"`
	Note         string  `json:"note"`
	CorrectionOf *string `json:"correction_of"`
}

// CostWrite uses a positive magnitude and an explicit financial direction.
type CostWrite struct {
	AmountMinor  int64   `json:"amount_minor"`
	Currency     string  `json:"currency"`
	Kind         string  `json:"kind"`
	Direction    string  `json:"direction"`
	OccurredAt   string  `json:"occurred_at"`
	Note         string  `json:"note"`
	CorrectionOf *string `json:"correction_of"`
}

func assetAtRevision(ctx context.Context, query queryer, id string, revision int64) (Asset, error) {
	value, err := loadAsset(ctx, query, id)
	if err != nil {
		return Asset{}, err
	}
	if value.Revision != revision {
		return Asset{}, ErrRevisionConflict
	}
	return value, nil
}

func (a *Assets) RecordUsage(ctx context.Context, token, key, id string, revision int64, input UsageWrite) (Usage, error) {
	input.CorrectionOf = snapshotPurchaseString(input.CorrectionOf)
	if !validDomainID(id) || input.Delta == 0 || input.Delta < -maxSafeInteger || input.Delta > maxSafeInteger ||
		!validUsageUnit(input.UsageUnit) || !validPurchaseInstant(input.OccurredAt) || !authString(input.Note, 0, 10000) ||
		(input.CorrectionOf != nil && !validDomainID(*input.CorrectionOf)) {
		return Usage{}, ErrDomainInput
	}
	switch input.EventType {
	case "use":
		if input.Delta < 1 || input.CorrectionOf != nil {
			return Usage{}, ErrDomainInput
		}
	case "correction":
		if input.CorrectionOf == nil {
			return Usage{}, ErrDomainInput
		}
	default:
		return Usage{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Usage{}, err
	}
	request := struct {
		ID       string     `json:"unit_id"`
		Revision int64      `json:"asset_revision"`
		Input    UsageWrite `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, a.auth, token, key, "post_units_id_usage", request, func(m *mutationTx) (Usage, error) {
		asset, err := assetAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Usage{}, err
		}
		if input.UsageUnit != asset.UsageUnit {
			return Usage{}, ErrDomainInput
		}
		if input.CorrectionOf != nil {
			target, err := loadUsage(ctx, m.tx, *input.CorrectionOf)
			if err != nil {
				return Usage{}, err
			}
			if target.ItemUnitID != id || target.UsageUnit != input.UsageUnit {
				return Usage{}, ErrDomainInput
			}
		}
		eventID, err := newDomainID()
		if err != nil {
			return Usage{}, err
		}
		instant, _ := time.Parse(time.RFC3339Nano, input.OccurredAt)
		data := UsageData{ID: eventID, ItemUnitID: id, Delta: input.Delta, UsageUnit: input.UsageUnit, EventType: input.EventType,
			OccurredAt: stamp(instant), Note: input.Note, CorrectionOf: input.CorrectionOf}
		if err = m.execAssetRow(`INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,created_at,correction_of,mutation_event_id)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, eventID, id, data.OccurredAt, data.Delta, data.UsageUnit, data.EventType, data.Note, stamp(m.now), data.CorrectionOf, m.eventID); err != nil {
			return Usage{}, err
		}
		counter, err := m.touch("usage", eventID, false)
		if err != nil {
			return Usage{}, err
		}
		if counter != 1 {
			return Usage{}, ErrDomainState
		}
		if err = m.finishAssetEvent(asset); err != nil {
			return Usage{}, err
		}
		stored, err := loadUsage(ctx, m.tx, eventID)
		if err != nil {
			return Usage{}, err
		}
		if stored.Revision != 1 || !reflect.DeepEqual(stored.UsageData, data) {
			return Usage{}, ErrDomainState
		}
		m.note = input.Note
		return stored, nil
	})
}

func (a *Assets) RecordCost(ctx context.Context, token, key, id string, revision int64, input CostWrite) (Cost, error) {
	input.CorrectionOf = snapshotPurchaseString(input.CorrectionOf)
	if !validDomainID(id) || input.AmountMinor < 1 || input.AmountMinor > maxSafeInteger || !validCurrencyCode(input.Currency) ||
		!validCostKind(input.Kind) || (input.Direction != "expense" && input.Direction != "credit") ||
		!validPurchaseInstant(input.OccurredAt) || !authString(input.Note, 0, 10000) ||
		(input.CorrectionOf != nil && !validDomainID(*input.CorrectionOf)) || (input.Kind == "adjustment") != (input.CorrectionOf != nil) {
		return Cost{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Cost{}, err
	}
	request := struct {
		ID       string    `json:"unit_id"`
		Revision int64     `json:"asset_revision"`
		Input    CostWrite `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, a.auth, token, key, "post_units_id_costs", request, func(m *mutationTx) (Cost, error) {
		asset, err := assetAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Cost{}, err
		}
		if _, ok := currency.MinorUnits(input.Currency); !ok {
			return Cost{}, ErrAssetCurrency
		}
		if input.Currency != asset.Currency {
			return Cost{}, ErrDomainInput
		}
		if input.CorrectionOf != nil {
			target, err := loadCost(ctx, m.tx, *input.CorrectionOf)
			if err != nil {
				return Cost{}, err
			}
			if target.ItemUnitID != id || target.Currency != input.Currency {
				return Cost{}, ErrDomainInput
			}
		}
		eventID, err := newDomainID()
		if err != nil {
			return Cost{}, err
		}
		instant, _ := time.Parse(time.RFC3339Nano, input.OccurredAt)
		data := CostData{ID: eventID, ItemUnitID: id, AmountMinor: input.AmountMinor, Currency: input.Currency,
			Kind: input.Kind, Direction: input.Direction, OccurredAt: stamp(instant), Note: input.Note, CorrectionOf: input.CorrectionOf}
		if err = m.execAssetRow(`INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,created_at,correction_of,mutation_event_id)
 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, eventID, id, data.OccurredAt, data.Kind, data.AmountMinor, data.Direction, data.Currency, data.Note, stamp(m.now), data.CorrectionOf, m.eventID); err != nil {
			return Cost{}, err
		}
		counter, err := m.touch("cost", eventID, false)
		if err != nil {
			return Cost{}, err
		}
		if counter != 1 {
			return Cost{}, ErrDomainState
		}
		if err = m.finishAssetEvent(asset); err != nil {
			return Cost{}, err
		}
		stored, err := loadCost(ctx, m.tx, eventID)
		if err != nil {
			return Cost{}, err
		}
		if stored.Revision != 1 || !reflect.DeepEqual(stored.CostData, data) {
			return Cost{}, ErrDomainState
		}
		m.note = input.Note
		return stored, nil
	})
}

// Check complete proposed history, not a cached total or a civil-day summary.
// Only financial aggregates change; unchanged Unit/location/search stay untouched.
func (m *mutationTx) finishAssetEvent(asset Asset) error {
	if _, err := readAssetTotals(m.ctx, m.tx, asset); err != nil {
		return err
	}
	if _, err := m.touch("asset", asset.ItemUnitID, false); err != nil {
		return err
	}
	if err := m.execAssetRow("UPDATE asset_profiles SET updated_at=? WHERE item_unit_id=?", stamp(m.now), asset.ItemUnitID); err != nil {
		return err
	}
	unit, err := loadUnit(m.ctx, m.tx, asset.ItemUnitID)
	if err != nil {
		return err
	}
	if _, err = m.touch("item", unit.ItemID, false); err != nil {
		return err
	}
	return m.execAssetRow("UPDATE items SET updated_at=? WHERE id=?", stamp(m.now), unit.ItemID)
}
