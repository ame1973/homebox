package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Financial payloads keep the unchanged version-one offline shapes. Independent
// revisions and private producer/audit metadata belong outside those payloads.
type AssetData struct {
	ItemUnitID             string  `json:"item_unit_id"`
	CostBasis              string  `json:"cost_basis"`
	Currency               string  `json:"currency"`
	ManualAcquisitionMinor *int64  `json:"manual_acquisition_minor"`
	InServiceAt            *string `json:"in_service_at"`
	RetiredAt              *string `json:"retired_at"`
	EstimatedResidualMinor *int64  `json:"estimated_residual_minor"`
	UsageUnit              string  `json:"usage_unit"`
}

type Asset struct {
	AssetData
	Revision int64 `json:"revision"`
}

type UsageData struct {
	ID           string  `json:"id"`
	ItemUnitID   string  `json:"item_unit_id"`
	Delta        int64   `json:"delta"`
	UsageUnit    string  `json:"usage_unit"`
	EventType    string  `json:"event_type"`
	OccurredAt   string  `json:"occurred_at"`
	Note         string  `json:"note"`
	CorrectionOf *string `json:"correction_of"`
}

type Usage struct {
	UsageData
	Revision int64 `json:"revision"`
}

type CostData struct {
	ID           string  `json:"id"`
	ItemUnitID   string  `json:"item_unit_id"`
	AmountMinor  int64   `json:"amount_minor"`
	Direction    string  `json:"direction"`
	Currency     string  `json:"currency"`
	Kind         string  `json:"kind"`
	OccurredAt   string  `json:"occurred_at"`
	Note         string  `json:"note"`
	CorrectionOf *string `json:"correction_of"`
}

type Cost struct {
	CostData
	Revision int64 `json:"revision"`
}

type DisposalData struct {
	ItemUnitID            string `json:"item_unit_id"`
	Kind                  string `json:"kind"`
	DisposedAt            string `json:"disposed_at"`
	RealizedProceedsMinor int64  `json:"realized_proceeds_minor"`
	Currency              string `json:"currency"`
	Note                  string `json:"note"`
}

type Disposal struct {
	DisposalData
	Revision int64 `json:"revision"`
}

const assetSelect = `SELECT a.item_unit_id,a.cost_basis,a.currency,a.manual_acquisition_minor,a.in_service_at,
 a.retired_at,a.estimated_residual_minor,a.usage_unit,COALESCE(r.revision,0) FROM asset_profiles a
 LEFT JOIN projection_revisions r ON r.entity_type='asset' AND r.entity_id=a.item_unit_id`
const usageSelect = `SELECT u.id,u.item_unit_id,u.delta,u.usage_unit,u.event_type,u.occurred_at,u.note,
 u.correction_of,COALESCE(r.revision,0),u.created_at,u.mutation_event_id FROM asset_usage_events u
 LEFT JOIN projection_revisions r ON r.entity_type='usage' AND r.entity_id=u.id`
const costSelect = `SELECT c.id,c.item_unit_id,c.amount_minor,c.direction,c.currency,c.kind,c.occurred_at,c.note,
 c.correction_of,COALESCE(r.revision,0),c.created_at,c.mutation_event_id FROM asset_cost_events c
 LEFT JOIN projection_revisions r ON r.entity_type='cost' AND r.entity_id=c.id`
const disposalSelect = `SELECT d.item_unit_id,d.kind,d.disposed_at,d.realized_proceeds_minor,d.currency,d.note,
 COALESCE(r.revision,0),d.created_at,d.mutation_event_id FROM asset_disposals d
 LEFT JOIN projection_revisions r ON r.entity_type='disposal' AND r.entity_id=d.item_unit_id`

func scanAsset(row rowScanner) (Asset, error) {
	var value Asset
	var manual, residual sql.NullInt64
	var service, retired sql.NullString
	if err := row.Scan(&value.ItemUnitID, &value.CostBasis, &value.Currency, &manual, &service, &retired,
		&residual, &value.UsageUnit, &value.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, err
	}
	if manual.Valid {
		value.ManualAcquisitionMinor = &manual.Int64
	}
	if residual.Valid {
		value.EstimatedResidualMinor = &residual.Int64
	}
	if service.Valid {
		value.InServiceAt = &service.String
	}
	if retired.Valid {
		value.RetiredAt = &retired.String
	}
	if err := value.validateDomainResult(); err != nil {
		return Asset{}, err
	}
	return value, nil
}

func scanUsage(row rowScanner) (Usage, error) {
	var value Usage
	var correction, event sql.NullString
	var created string
	if err := row.Scan(&value.ID, &value.ItemUnitID, &value.Delta, &value.UsageUnit, &value.EventType,
		&value.OccurredAt, &value.Note, &correction, &value.Revision, &created, &event); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Usage{}, ErrNotFound
		}
		return Usage{}, err
	}
	if correction.Valid {
		value.CorrectionOf = &correction.String
	}
	if !validAssetProvenance(created, event) {
		return Usage{}, ErrDomainState
	}
	if err := value.validateDomainResult(); err != nil {
		return Usage{}, err
	}
	return value, nil
}

func scanCost(row rowScanner) (Cost, error) {
	var value Cost
	var correction, event sql.NullString
	var created string
	if err := row.Scan(&value.ID, &value.ItemUnitID, &value.AmountMinor, &value.Direction, &value.Currency,
		&value.Kind, &value.OccurredAt, &value.Note, &correction, &value.Revision, &created, &event); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Cost{}, ErrNotFound
		}
		return Cost{}, err
	}
	if correction.Valid {
		value.CorrectionOf = &correction.String
	}
	if !validAssetProvenance(created, event) {
		return Cost{}, ErrDomainState
	}
	if err := value.validateDomainResult(); err != nil {
		return Cost{}, err
	}
	return value, nil
}

func scanDisposal(row rowScanner) (Disposal, error) {
	var value Disposal
	var event sql.NullString
	var created string
	if err := row.Scan(&value.ItemUnitID, &value.Kind, &value.DisposedAt, &value.RealizedProceedsMinor,
		&value.Currency, &value.Note, &value.Revision, &created, &event); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Disposal{}, ErrNotFound
		}
		return Disposal{}, err
	}
	if !validAssetProvenance(created, event) {
		return Disposal{}, ErrDomainState
	}
	if err := value.validateDomainResult(); err != nil {
		return Disposal{}, err
	}
	return value, nil
}

func validAssetProvenance(created string, event sql.NullString) bool {
	// Legacy created_at stays exact, even when its old spelling is not an instant.
	// Only an explicitly associated new producer asserts actual UTC-capable time.
	return !event.Valid || validDomainID(event.String) && validPurchaseInstant(created)
}

func loadAsset(ctx context.Context, query queryer, id string) (Asset, error) {
	value, err := scanAsset(query.QueryRowContext(ctx, assetSelect+" WHERE a.item_unit_id=?", id))
	if err != nil {
		return Asset{}, err
	}
	if err = validateAssetUnit(ctx, query, value.ItemUnitID); err != nil {
		return Asset{}, err
	}
	return value, nil
}

func loadUsage(ctx context.Context, query queryer, id string) (Usage, error) {
	value, err := scanUsage(query.QueryRowContext(ctx, usageSelect+" WHERE u.id=?", id))
	if err != nil {
		return Usage{}, err
	}
	if err = validateAssetEventOwner(ctx, query, value.ItemUnitID); err != nil {
		return Usage{}, err
	}
	err = validateAssetCorrection(value.correctionRef(), func(id string) (assetCorrectionRef, error) {
		target, err := scanUsage(query.QueryRowContext(ctx, usageSelect+" WHERE u.id=?", id))
		return target.correctionRef(), err
	}, make(map[string]bool))
	if err != nil {
		return Usage{}, err
	}
	return value, nil
}

func loadCost(ctx context.Context, query queryer, id string) (Cost, error) {
	value, err := scanCost(query.QueryRowContext(ctx, costSelect+" WHERE c.id=?", id))
	if err != nil {
		return Cost{}, err
	}
	if err = validateAssetEventOwner(ctx, query, value.ItemUnitID); err != nil {
		return Cost{}, err
	}
	err = validateAssetCorrection(value.correctionRef(), func(id string) (assetCorrectionRef, error) {
		target, err := scanCost(query.QueryRowContext(ctx, costSelect+" WHERE c.id=?", id))
		return target.correctionRef(), err
	}, make(map[string]bool))
	if err != nil {
		return Cost{}, err
	}
	return value, nil
}

func loadDisposal(ctx context.Context, query queryer, id string) (Disposal, error) {
	value, err := scanDisposal(query.QueryRowContext(ctx, disposalSelect+" WHERE d.item_unit_id=?", id))
	if err != nil {
		return Disposal{}, err
	}
	if err = validateAssetEventOwner(ctx, query, value.ItemUnitID); err != nil {
		return Disposal{}, err
	}
	return value, nil
}

func validateAssetUnit(ctx context.Context, query queryer, id string) error {
	unit, err := loadUnit(ctx, query, id)
	if err == nil {
		_, err = loadItem(ctx, query, unit.ItemID)
	}
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	return err
}

func validateAssetEventOwner(ctx context.Context, query queryer, id string) error {
	_, err := loadAsset(ctx, query, id)
	if errors.Is(err, ErrNotFound) {
		return ErrDomainState
	}
	return err
}

func validUsageUnit(value string) bool {
	return value == "times" || value == "minutes" || value == "cycles"
}

func (value Asset) validateDomainResult() error {
	if !validDomainID(value.ItemUnitID) || !validCurrencyCode(value.Currency) || !validUsageUnit(value.UsageUnit) ||
		(value.CostBasis != "manual" && value.CostBasis != "purchase") ||
		(value.CostBasis == "manual") != (value.ManualAcquisitionMinor != nil) ||
		(value.ManualAcquisitionMinor != nil && (*value.ManualAcquisitionMinor < 0 || *value.ManualAcquisitionMinor > maxSafeInteger)) ||
		(value.EstimatedResidualMinor != nil && (*value.EstimatedResidualMinor < 0 || *value.EstimatedResidualMinor > maxSafeInteger)) ||
		(value.InServiceAt != nil && !validPurchaseInstant(*value.InServiceAt)) ||
		(value.RetiredAt != nil && !validPurchaseInstant(*value.RetiredAt)) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	if value.InServiceAt != nil && value.RetiredAt != nil {
		start, _ := time.Parse(time.RFC3339Nano, *value.InServiceAt)
		end, _ := time.Parse(time.RFC3339Nano, *value.RetiredAt)
		if end.Before(start) {
			return ErrDomainState
		}
	}
	return nil
}

func (value Usage) validateDomainResult() error {
	if !validDomainID(value.ID) || !validDomainID(value.ItemUnitID) || value.Delta == 0 || value.Delta < -maxSafeInteger || value.Delta > maxSafeInteger ||
		!validUsageUnit(value.UsageUnit) || (value.EventType != "use" && value.EventType != "correction") || !validPurchaseInstant(value.OccurredAt) ||
		!authString(value.Note, 0, 10000) || (value.CorrectionOf != nil && (!validDomainID(*value.CorrectionOf) || *value.CorrectionOf == value.ID || value.EventType != "correction")) ||
		value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	return nil
}

func (value Cost) validateDomainResult() error {
	if !validDomainID(value.ID) || !validDomainID(value.ItemUnitID) || value.AmountMinor < 1 || value.AmountMinor > maxSafeInteger ||
		!validCurrencyCode(value.Currency) || (value.Direction != "expense" && value.Direction != "credit") ||
		!validPurchaseInstant(value.OccurredAt) || !authString(value.Note, 0, 10000) ||
		(value.CorrectionOf != nil && (!validDomainID(*value.CorrectionOf) || *value.CorrectionOf == value.ID || value.Kind != "adjustment")) ||
		value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	if !validCostKind(value.Kind) {
		return ErrDomainState
	}
	return nil
}

func validCostKind(value string) bool {
	switch value {
	case "maintenance", "repair", "accessory", "fee", "refund", "adjustment":
		return true
	default:
		return false
	}
}

func (value Disposal) validateDomainResult() error {
	if !validDomainID(value.ItemUnitID) || !validPurchaseInstant(value.DisposedAt) || value.RealizedProceedsMinor < 0 || value.RealizedProceedsMinor > maxSafeInteger ||
		!validCurrencyCode(value.Currency) || !authString(value.Note, 0, 10000) || value.Revision < 1 || value.Revision > maxSafeInteger {
		return ErrDomainState
	}
	switch value.Kind {
	case "sold", "donated", "recycled", "discarded", "other":
		return nil
	default:
		return ErrDomainState
	}
}

// Shared by Usage/Cost only: validate actual same-owner, same-interpretation
// correction chains without recursion. Backfill reuses verified tails so long
// retained chains are checked in linear rather than quadratic work.
type assetCorrectionRef struct {
	id, owner, interpretation string
	target                    *string
}

func (value Usage) correctionRef() assetCorrectionRef {
	return assetCorrectionRef{value.ID, value.ItemUnitID, value.UsageUnit, value.CorrectionOf}
}

func (value Cost) correctionRef() assetCorrectionRef {
	return assetCorrectionRef{value.ID, value.ItemUnitID, value.Currency, value.CorrectionOf}
}

func validateAssetCorrection(value assetCorrectionRef, load func(string) (assetCorrectionRef, error), verified map[string]bool) error {
	seen := make(map[string]bool)
	for current := value; ; {
		if seen[current.id] || current.owner != value.owner || current.interpretation != value.interpretation {
			return ErrDomainState
		}
		seen[current.id] = true
		if verified[current.id] || current.target == nil {
			for id := range seen {
				verified[id] = true
			}
			return nil
		}
		var err error
		current, err = load(*current.target)
		if errors.Is(err, ErrNotFound) {
			return ErrDomainState
		}
		if err != nil {
			return err
		}
	}
}

// These loaders certify retained representation and real ownership, not new-money
// eligibility, acquisition-basis changes or calculation totals. Never repair
// legacy currency/provenance or enable financial commands from a backfill alone.
func validateAssetsBackfill(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT entity_type,entity_id,revision FROM projection_revisions WHERE entity_type IN('asset','usage','cost','disposal')")
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind, identity string
		var revision int64
		if err = rows.Scan(&kind, &identity, &revision); err != nil {
			_ = rows.Close()
			return err
		}
		if !validProjectionIdentity(kind, identity) || revision < 1 || revision > maxSafeInteger {
			_ = rows.Close()
			return ErrDomainState
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	usageVerified, costVerified := make(map[string]bool), make(map[string]bool)
	for _, check := range []struct {
		query string
		scan  func(rowScanner) error
	}{
		{assetSelect, func(row rowScanner) error {
			value, err := scanAsset(row)
			if err == nil {
				err = validateAssetUnit(ctx, tx, value.ItemUnitID)
			}
			return err
		}},
		{usageSelect, func(row rowScanner) error {
			value, err := scanUsage(row)
			if err != nil {
				return err
			}
			if err = validateAssetEventOwner(ctx, tx, value.ItemUnitID); err != nil {
				return err
			}
			return validateAssetCorrection(value.correctionRef(), func(id string) (assetCorrectionRef, error) {
				target, err := scanUsage(tx.QueryRowContext(ctx, usageSelect+" WHERE u.id=?", id))
				return target.correctionRef(), err
			}, usageVerified)
		}},
		{costSelect, func(row rowScanner) error {
			value, err := scanCost(row)
			if err != nil {
				return err
			}
			if err = validateAssetEventOwner(ctx, tx, value.ItemUnitID); err != nil {
				return err
			}
			return validateAssetCorrection(value.correctionRef(), func(id string) (assetCorrectionRef, error) {
				target, err := scanCost(tx.QueryRowContext(ctx, costSelect+" WHERE c.id=?", id))
				return target.correctionRef(), err
			}, costVerified)
		}},
		{disposalSelect, func(row rowScanner) error {
			value, err := scanDisposal(row)
			if err == nil {
				err = validateAssetEventOwner(ctx, tx, value.ItemUnitID)
			}
			return err
		}},
	} {
		rows, err := tx.QueryContext(ctx, check.query)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err = check.scan(rows); err != nil {
				_ = rows.Close()
				return err
			}
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	return nil
}
