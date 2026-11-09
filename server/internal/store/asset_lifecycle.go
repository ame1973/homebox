package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"homebox/internal/currency"
)

var ErrAssetHistory = errors.New("recorded asset lifecycle requires an audited correction or void")

type AssetRetireWrite struct {
	UnitRevision int64  `json:"unit_revision"`
	RetiredAt    string `json:"retired_at"`
	Note         string `json:"note"`
}

// Explicit zero proceeds are a decision; HTTP must require the JSON field.
type AssetDisposalWrite struct {
	UnitRevision          int64  `json:"unit_revision"`
	Kind                  string `json:"kind"`
	DisposedAt            string `json:"disposed_at"`
	RealizedProceedsMinor int64  `json:"realized_proceeds_minor"`
	Currency              string `json:"currency"`
	Note                  string `json:"note"`
}

type assetLifecycleContext struct {
	Before         Asset     `json:"before"`
	After          Asset     `json:"after"`
	UnitBefore     Unit      `json:"unit_before"`
	UnitAfter      Unit      `json:"unit_after"`
	DisposalBefore *Disposal `json:"disposal_before"`
	DisposalAfter  *Disposal `json:"disposal_after"`
	Input          any       `json:"input"`
}

func (m *mutationTx) recordAssetLifecycleChange(value assetLifecycleContext) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return m.execAssetRow("INSERT INTO asset_profile_changes(mutation_event_id,item_unit_id,context_json) VALUES(?,?,?)", m.eventID, value.After.ItemUnitID, string(encoded))
}

func (a *Assets) Retire(ctx context.Context, token, key, id string, revision int64, input AssetRetireWrite) (Unit, error) {
	if !validDomainID(id) || input.UnitRevision < 1 || input.UnitRevision > maxSafeInteger ||
		!validPurchaseInstant(input.RetiredAt) || !authString(input.Note, 0, 10000) {
		return Unit{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Unit{}, err
	}
	request := struct {
		ID       string           `json:"unit_id"`
		Revision int64            `json:"asset_revision"`
		Input    AssetRetireWrite `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, a.auth, token, key, "post_units_id_retire", request, func(m *mutationTx) (Unit, error) {
		before, err := assetAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Unit{}, err
		}
		unit, err := unitAtRevision(ctx, m.tx, id, input.UnitRevision)
		if err != nil {
			return Unit{}, err
		}
		_, err = loadDisposalRecord(ctx, m.tx, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Unit{}, err
		}
		if unit.Status == "disposed" || err == nil {
			return Unit{}, ErrUnitDisposed
		}
		paths, err := m.unitLocationPaths(unit.LocationID, nil)
		if err != nil {
			return Unit{}, err
		}
		after := before
		after.RetiredAt = canonicalAssetInstant(&input.RetiredAt, before.RetiredAt)
		if before.RetiredAt != nil && !sameParent(before.RetiredAt, after.RetiredAt) {
			return Unit{}, ErrAssetHistory
		}
		if after.validateDomainResult() != nil {
			return Unit{}, ErrDomainInput
		}
		result := unit
		dateChanged, physicalChanged := !sameParent(before.RetiredAt, after.RetiredAt), unit.Status != "retired"
		if dateChanged {
			if err = m.execAssetRow("UPDATE asset_profiles SET retired_at=? WHERE item_unit_id=?", after.RetiredAt, id); err != nil {
				return Unit{}, err
			}
		}
		if physicalChanged {
			if err = m.execAssetRow("UPDATE item_units SET status='retired',updated_at=? WHERE id=?", stamp(m.now), id); err != nil {
				return Unit{}, err
			}
			result.Status = "retired"
			result.Revision, err = m.touchUnitState(result.UnitData, paths, false)
			if err != nil {
				return Unit{}, err
			}
			if result.Revision != unit.Revision+1 {
				return Unit{}, ErrDomainState
			}
			if err = m.appendInventoryMovement(unit.ItemID, &id, "unit_status_retired", nil, nil, 0, input.Note, ""); err != nil {
				return Unit{}, err
			}
		}
		if dateChanged || physicalChanged {
			if err = m.finishAssetEvent(after); err != nil {
				return Unit{}, err
			}
			stored, err := loadAsset(ctx, m.tx, id)
			if err != nil {
				return Unit{}, err
			}
			if stored.Revision != before.Revision+1 || !reflect.DeepEqual(stored.AssetData, after.AssetData) {
				return Unit{}, ErrDomainState
			}
			after = stored
		} else if _, err = readAssetTotals(ctx, m.tx, after); err != nil {
			return Unit{}, err
		}
		storedUnit, err := loadUnit(ctx, m.tx, id)
		if err != nil {
			return Unit{}, err
		}
		if !reflect.DeepEqual(storedUnit, result) {
			return Unit{}, ErrDomainState
		}
		m.note = input.Note
		if err = m.recordAssetLifecycleChange(assetLifecycleContext{Before: before, After: after, UnitBefore: unit, UnitAfter: storedUnit, Input: request}); err != nil {
			return Unit{}, err
		}
		return storedUnit, nil
	})
}

func (a *Assets) Dispose(ctx context.Context, token, key, id string, revision int64, input AssetDisposalWrite) (Disposal, error) {
	data := DisposalData{ItemUnitID: id, Kind: input.Kind, DisposedAt: input.DisposedAt, RealizedProceedsMinor: input.RealizedProceedsMinor, Currency: input.Currency, Note: input.Note}
	if (Disposal{data, 1}).validateDomainResult() != nil || input.UnitRevision < 1 || input.UnitRevision > maxSafeInteger ||
		input.Kind != "sold" && input.RealizedProceedsMinor != 0 {
		return Disposal{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Disposal{}, err
	}
	request := struct {
		ID       string             `json:"unit_id"`
		Revision int64              `json:"asset_revision"`
		Input    AssetDisposalWrite `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, a.auth, token, key, "post_units_id_disposal", request, func(m *mutationTx) (Disposal, error) {
		before, err := assetAtRevision(ctx, m.tx, id, revision)
		if err != nil {
			return Disposal{}, err
		}
		unit, err := unitAtRevision(ctx, m.tx, id, input.UnitRevision)
		if err != nil {
			return Disposal{}, err
		}
		record, err := loadDisposalRecord(ctx, m.tx, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Disposal{}, err
		}
		var prior *Disposal
		if err == nil {
			if record.Current == nil {
				return Disposal{}, ErrAssetHistory
			}
			prior = &Disposal{*record.Current, record.Revision}
		}
		paths, err := m.unitLocationPaths(unit.LocationID, nil)
		if err != nil {
			return Disposal{}, err
		}
		var retainedInstant *string
		if prior != nil {
			retainedInstant = &prior.DisposedAt
		}
		data.DisposedAt = *canonicalAssetInstant(&input.DisposedAt, retainedInstant)
		if data.Currency != before.Currency {
			return Disposal{}, ErrDomainInput
		}
		m.note, m.reason = input.Note, input.Kind
		if prior != nil {
			if unit.Status != "disposed" {
				return Disposal{}, ErrDomainState
			}
			if !reflect.DeepEqual(prior.DisposalData, data) {
				return Disposal{}, ErrAssetHistory
			}
			if _, err = readAssetTotals(ctx, m.tx, before); err != nil {
				return Disposal{}, err
			}
			if err = m.recordAssetLifecycleChange(assetLifecycleContext{Before: before, After: before, UnitBefore: unit, UnitAfter: unit, DisposalBefore: prior, DisposalAfter: prior, Input: request}); err != nil {
				return Disposal{}, err
			}
			return *prior, nil
		}
		if _, ok := currency.MinorUnits(data.Currency); !ok {
			return Disposal{}, ErrAssetCurrency
		}
		if err = m.execAssetRow(`INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,note,created_at,mutation_event_id)
 VALUES(?,?,?,?,?,?,?,?)`, id, data.DisposedAt, data.Kind, data.RealizedProceedsMinor, data.Currency, data.Note, stamp(m.now), m.eventID); err != nil {
			return Disposal{}, err
		}
		counter, err := m.touch("disposal", id, false)
		if err != nil {
			return Disposal{}, err
		}
		if counter != 1 {
			return Disposal{}, ErrDomainState
		}
		result := unit
		if unit.Status != "disposed" {
			if err = m.execAssetRow("UPDATE item_units SET status='disposed',updated_at=? WHERE id=?", stamp(m.now), id); err != nil {
				return Disposal{}, err
			}
			// Preserve the inspectable last location; disposed status removes current
			// inventory membership. Do not invent another physical placement choice.
			result.Status = "disposed"
			result.Revision, err = m.touchUnitState(result.UnitData, paths, false)
			if err != nil {
				return Disposal{}, err
			}
			if result.Revision != unit.Revision+1 {
				return Disposal{}, ErrDomainState
			}
			if err = m.appendInventoryMovement(unit.ItemID, &id, "unit_dispose_"+input.Kind, unit.LocationID, nil, -1, input.Note, input.Kind); err != nil {
				return Disposal{}, err
			}
		}
		if err = m.finishAssetEvent(before); err != nil {
			return Disposal{}, err
		}
		after, err := loadAsset(ctx, m.tx, id)
		if err != nil {
			return Disposal{}, err
		}
		if after.Revision != before.Revision+1 || !reflect.DeepEqual(after.AssetData, before.AssetData) {
			return Disposal{}, ErrDomainState
		}
		storedUnit, err := loadUnit(ctx, m.tx, id)
		if err != nil {
			return Disposal{}, err
		}
		if !reflect.DeepEqual(storedUnit, result) {
			return Disposal{}, ErrDomainState
		}
		stored, err := loadDisposal(ctx, m.tx, id)
		if err != nil {
			return Disposal{}, err
		}
		if stored.Revision != 1 || !reflect.DeepEqual(stored.DisposalData, data) {
			return Disposal{}, ErrDomainState
		}
		if err = m.recordAssetLifecycleChange(assetLifecycleContext{Before: before, After: after, UnitBefore: unit, UnitAfter: storedUnit, DisposalAfter: &stored, Input: request}); err != nil {
			return Disposal{}, err
		}
		return stored, nil
	})
}
