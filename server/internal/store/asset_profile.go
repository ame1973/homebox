package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"homebox/internal/currency"
)

var ErrAssetCurrency = errors.New("currency is unsupported for new asset money")

// AssetProfileWrite is a complete decision, including explicit nullable intent.
// The HTTP reader must separately require every JSON field, including nulls.
type AssetProfileWrite struct {
	ItemRevision           int64   `json:"item_revision"`
	AssetRevision          *int64  `json:"asset_revision"`
	CostBasis              string  `json:"cost_basis"`
	Currency               string  `json:"currency"`
	ManualAcquisitionMinor *int64  `json:"manual_acquisition_minor"`
	InServiceAt            *string `json:"in_service_at"`
	RetiredAt              *string `json:"retired_at"`
	EstimatedResidualMinor *int64  `json:"estimated_residual_minor"`
	UsageUnit              string  `json:"usage_unit"`
}

type assetProfileCommand struct {
	UnitID       string            `json:"unit_id"`
	UnitRevision int64             `json:"unit_revision"`
	Input        AssetProfileWrite `json:"input"`
}

func snapshotAssetNumber(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (a *Assets) PutProfile(ctx context.Context, token, key, id string, unitRevision int64, input AssetProfileWrite) (Asset, error) {
	input.AssetRevision = snapshotAssetNumber(input.AssetRevision)
	input.ManualAcquisitionMinor = snapshotAssetNumber(input.ManualAcquisitionMinor)
	input.EstimatedResidualMinor = snapshotAssetNumber(input.EstimatedResidualMinor)
	input.InServiceAt, input.RetiredAt = snapshotPurchaseString(input.InServiceAt), snapshotPurchaseString(input.RetiredAt)
	data := AssetData{ItemUnitID: id, CostBasis: input.CostBasis, Currency: input.Currency,
		ManualAcquisitionMinor: input.ManualAcquisitionMinor, InServiceAt: input.InServiceAt,
		RetiredAt: input.RetiredAt, EstimatedResidualMinor: input.EstimatedResidualMinor, UsageUnit: input.UsageUnit}
	if (Asset{data, 1}).validateDomainResult() != nil || input.ItemRevision < 1 || input.ItemRevision > maxSafeInteger ||
		(input.AssetRevision != nil && (*input.AssetRevision < 1 || *input.AssetRevision > maxSafeInteger)) {
		return Asset{}, ErrDomainInput
	}
	if err := validExpectedRevision(unitRevision); err != nil {
		return Asset{}, err
	}
	request := assetProfileCommand{id, unitRevision, input}
	return runDomainMutation(ctx, a.auth, token, key, "put_units_id_asset", request, func(m *mutationTx) (Asset, error) {
		unit, err := unitAtRevision(ctx, m.tx, id, unitRevision)
		if err != nil {
			return Asset{}, err
		}
		if _, err = itemAtRevision(ctx, m.tx, unit.ItemID, input.ItemRevision); err != nil {
			return Asset{}, err
		}
		var before *Asset
		retained, err := loadAsset(ctx, m.tx, id)
		if err == nil {
			before = &retained
		} else if !errors.Is(err, ErrNotFound) {
			return Asset{}, err
		}
		if input.AssetRevision == nil && before != nil || input.AssetRevision != nil && (before == nil || *input.AssetRevision != before.Revision) {
			return Asset{}, ErrRevisionConflict
		}
		after := Asset{data, 1}
		if before != nil {
			after.Revision = before.Revision
			after.InServiceAt = canonicalAssetInstant(input.InServiceAt, before.InServiceAt)
			after.RetiredAt = canonicalAssetInstant(input.RetiredAt, before.RetiredAt)
			if !sameParent(after.RetiredAt, before.RetiredAt) {
				return Asset{}, ErrUnitAssetLifecycle
			}
		} else {
			after.InServiceAt = canonicalAssetInstant(input.InServiceAt, nil)
			after.RetiredAt = canonicalAssetInstant(input.RetiredAt, nil)
			// Initial historical adoption is explicit and never changes Unit state.
			// Ordinary active Units must retire through the lifecycle command.
			if after.RetiredAt != nil && unit.Status != "retired" && unit.Status != "disposed" {
				return Asset{}, ErrUnitAssetLifecycle
			}
			var orphaned bool
			err = m.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM asset_usage_events WHERE item_unit_id=?)
 OR EXISTS(SELECT 1 FROM asset_cost_events WHERE item_unit_id=?) OR EXISTS(SELECT 1 FROM asset_disposals WHERE item_unit_id=?)`, id, id, id).Scan(&orphaned)
			if err != nil {
				return Asset{}, err
			}
			if orphaned {
				return Asset{}, ErrDomainState
			}
		}
		newMoney := before == nil || before.CostBasis != after.CostBasis || before.Currency != after.Currency ||
			!reflect.DeepEqual(before.ManualAcquisitionMinor, after.ManualAcquisitionMinor) || !reflect.DeepEqual(before.EstimatedResidualMinor, after.EstimatedResidualMinor)
		if newMoney {
			if _, ok := currency.MinorUnits(after.Currency); !ok {
				return Asset{}, ErrAssetCurrency
			}
		}
		// Validate the entire actual source against the proposed interpretation,
		// including zero references and usage prefixes, before changing any row.
		if _, err = readAssetTotals(ctx, m.tx, after); err != nil {
			return Asset{}, err
		}
		if before == nil || !reflect.DeepEqual(before.AssetData, after.AssetData) {
			if before == nil {
				err = m.execAssetRow(`INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,manual_acquisition_minor,in_service_at,retired_at,estimated_residual_minor,usage_unit,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, id, after.CostBasis, after.Currency, after.ManualAcquisitionMinor, after.InServiceAt, after.RetiredAt, after.EstimatedResidualMinor, after.UsageUnit, stamp(m.now), stamp(m.now))
			} else {
				err = m.execAssetRow(`UPDATE asset_profiles SET cost_basis=?,currency=?,manual_acquisition_minor=?,in_service_at=?,retired_at=?,estimated_residual_minor=?,usage_unit=?,updated_at=? WHERE item_unit_id=?`,
					after.CostBasis, after.Currency, after.ManualAcquisitionMinor, after.InServiceAt, after.RetiredAt, after.EstimatedResidualMinor, after.UsageUnit, stamp(m.now), id)
			}
			if err != nil {
				return Asset{}, err
			}
			revision, err := m.touch("asset", id, false)
			if err != nil {
				return Asset{}, err
			}
			if before == nil && revision != 1 {
				return Asset{}, ErrDomainState
			}
			if _, err = m.touch("item", unit.ItemID, false); err != nil {
				return Asset{}, err
			}
			if err = m.execAssetRow("UPDATE items SET updated_at=? WHERE id=?", stamp(m.now), unit.ItemID); err != nil {
				return Asset{}, err
			}
			stored, err := loadAsset(ctx, m.tx, id)
			if err != nil {
				return Asset{}, err
			}
			if stored.Revision != revision || !reflect.DeepEqual(stored.AssetData, after.AssetData) {
				return Asset{}, ErrDomainState
			}
			after = stored
		}
		if err = m.recordAssetProfileChange(before, after, request); err != nil {
			return Asset{}, err
		}
		return after, nil
	})
}

func canonicalAssetInstant(input, retained *string) *string {
	if input == nil {
		return nil
	}
	instant, _ := time.Parse(time.RFC3339Nano, *input)
	if retained != nil {
		original, _ := time.Parse(time.RFC3339Nano, *retained)
		if instant.Equal(original) {
			return snapshotPurchaseString(retained)
		}
	}
	value := stamp(instant)
	return &value
}

// Every financial source/context statement must affect exactly one real row;
// RAISE(IGNORE) must not turn a missing write or before-image into success.
func (m *mutationTx) execAssetRow(statement string, args ...any) error {
	result, err := m.tx.ExecContext(m.ctx, statement, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrDomainState
	}
	return nil
}

func (m *mutationTx) recordAssetProfileChange(before *Asset, after Asset, input assetProfileCommand) error {
	encoded, err := json.Marshal(struct {
		Before *Asset              `json:"before"`
		After  Asset               `json:"after"`
		Input  assetProfileCommand `json:"input"`
	}{before, after, input})
	if err != nil {
		return err
	}
	return m.execAssetRow("INSERT INTO asset_profile_changes(mutation_event_id,item_unit_id,context_json) VALUES(?,?,?)", m.eventID, after.ItemUnitID, string(encoded))
}
