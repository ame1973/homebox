package store

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"sort"
	"time"
)

var ErrAssetOverflow = errors.New("asset calculation exceeds the safe financial range")

// Assets owns authorized financial source services. HTTP still owns transport,
// Origin/CSRF/admission; event/lifecycle producers remain separate boundaries.
type Assets struct{ auth *Auth }

func NewAssets(a *Auth) (*Assets, error) {
	if a == nil || a.store == nil {
		return nil, ErrDomainInput
	}
	return a.store.NewAssets(a)
}

func (s *Store) NewAssets(a *Auth) (*Assets, error) {
	if s == nil || a == nil || a.store != s {
		return nil, ErrDomainInput
	}
	return &Assets{auth: a}, nil
}

func (a *Assets) Get(ctx context.Context, token, id string) (Asset, error) {
	if !validDomainID(id) {
		return Asset{}, ErrDomainInput
	}
	return readDomain(ctx, a.auth, token, func(tx *sql.Tx) (Asset, error) { return loadAsset(ctx, tx, id) })
}

func (a *Assets) GetDisposal(ctx context.Context, token, id string) (Disposal, error) {
	if !validDomainID(id) {
		return Disposal{}, ErrDomainInput
	}
	return readDomain(ctx, a.auth, token, func(tx *sql.Tx) (Disposal, error) { return loadDisposal(ctx, tx, id) })
}

type AssetCostSummary struct {
	UnitID                 string   `json:"unit_id"`
	Currency               string   `json:"currency"`
	CostBasis              string   `json:"cost_basis"`
	AcquisitionMinor       int64    `json:"acquisition_minor"`
	ExtraMinor             int64    `json:"extra_minor"`
	RealizedProceedsMinor  int64    `json:"realized_proceeds_minor"`
	EstimatedResidualMinor *int64   `json:"estimated_residual_minor"`
	NetCostMinor           int64    `json:"net_cost_minor"`
	ProjectedNetCostMinor  *int64   `json:"projected_net_cost_minor"`
	ElapsedDays            *int64   `json:"elapsed_days"`
	TotalUsage             int64    `json:"total_usage"`
	UsageUnit              string   `json:"usage_unit"`
	PerDayMinor            *float64 `json:"per_day_minor"`
	PerUseMinor            *float64 `json:"per_use_minor"`
	ProjectedPerDayMinor   *float64 `json:"projected_per_day_minor"`
	ProjectedPerUseMinor   *float64 `json:"projected_per_use_minor"`
	AsOf                   string   `json:"as_of"`
	Timezone               string   `json:"timezone"`
	DayPolicy              string   `json:"day_policy"`
	SourceRevision         int64    `json:"source_revision"`
}

func (a *Assets) CostSummary(ctx context.Context, token, id, asOf string) (AssetCostSummary, error) {
	if !validDomainID(id) || asOf != "" && !validPurchaseInstant(asOf) {
		return AssetCostSummary{}, ErrDomainInput
	}
	return readDomain(ctx, a.auth, token, func(tx *sql.Tx) (AssetCostSummary, error) {
		instant, _ := time.Parse(time.RFC3339Nano, asOf)
		if asOf == "" {
			instant = a.auth.now().UTC()
		}
		if !validPurchaseInstant(stamp(instant)) {
			return AssetCostSummary{}, ErrDomainState
		}
		return loadAssetCostSummary(ctx, tx, id, instant)
	})
}

type assetTotals struct {
	acquisition, extra, proceeds, usage, net int64
	projected                                *int64
}

// Shared by current summaries and proposed profile validation. Calendar settings
// are not financial source and must not gate an otherwise valid profile command.
func readAssetTotals(ctx context.Context, query queryer, asset Asset) (assetTotals, error) {
	var value assetTotals
	var err error
	if value.acquisition, err = readAssetAcquisition(ctx, query, asset); err != nil {
		return assetTotals{}, err
	}
	if value.extra, err = readAssetExtra(ctx, query, asset); err != nil {
		return assetTotals{}, err
	}
	if value.usage, err = readAssetUsage(ctx, query, asset); err != nil {
		return assetTotals{}, err
	}
	disposal, err := loadDisposal(ctx, query, asset.ItemUnitID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return assetTotals{}, err
	}
	if err == nil {
		if disposal.Currency != asset.Currency {
			return assetTotals{}, ErrDomainState
		}
		value.proceeds = disposal.RealizedProceedsMinor
	}
	// Each operand is already a safe integer; three such magnitudes fit int64.
	// Validate published results, not an arbitrary intermediate/row ordering.
	base := value.acquisition + value.extra
	if value.net, err = assetSafeTotal(big.NewInt(base - value.proceeds)); err != nil {
		return assetTotals{}, err
	}
	if asset.EstimatedResidualMinor != nil {
		projected, err := assetSafeTotal(big.NewInt(base - *asset.EstimatedResidualMinor))
		if err != nil {
			return assetTotals{}, err
		}
		value.projected = &projected
	}
	return value, nil
}

func loadAssetCostSummary(ctx context.Context, query queryer, id string, asOf time.Time) (AssetCostSummary, error) {
	asset, err := loadAsset(ctx, query, id)
	if err != nil {
		return AssetCostSummary{}, err
	}
	zoneName, zone, err := readHomeTimezone(ctx, query)
	if err != nil {
		return AssetCostSummary{}, err
	}
	totals, err := readAssetTotals(ctx, query, asset)
	if err != nil {
		return AssetCostSummary{}, err
	}
	value := AssetCostSummary{UnitID: id, Currency: asset.Currency, CostBasis: asset.CostBasis,
		AcquisitionMinor: totals.acquisition, ExtraMinor: totals.extra, RealizedProceedsMinor: totals.proceeds,
		EstimatedResidualMinor: asset.EstimatedResidualMinor, NetCostMinor: totals.net, ProjectedNetCostMinor: totals.projected,
		TotalUsage: totals.usage, UsageUnit: asset.UsageUnit, AsOf: stamp(asOf), Timezone: zoneName, DayPolicy: "inclusive_civil_days"}
	if value.ElapsedDays, err = assetElapsedDays(asset, asOf, zone); err != nil {
		return AssetCostSummary{}, err
	}
	if value.ElapsedDays != nil {
		ratio := float64(value.NetCostMinor) / float64(*value.ElapsedDays)
		value.PerDayMinor = &ratio
		if value.ProjectedNetCostMinor != nil {
			projected := float64(*value.ProjectedNetCostMinor) / float64(*value.ElapsedDays)
			value.ProjectedPerDayMinor = &projected
		}
	}
	if value.TotalUsage > 0 {
		ratio := float64(value.NetCostMinor) / float64(value.TotalUsage)
		value.PerUseMinor = &ratio
		if value.ProjectedNetCostMinor != nil {
			projected := float64(*value.ProjectedNetCostMinor) / float64(value.TotalUsage)
			value.ProjectedPerUseMinor = &projected
		}
	}
	err = query.QueryRowContext(ctx, "SELECT revision FROM data_revision WHERE scope='catalog'").Scan(&value.SourceRevision)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (value.SourceRevision < 0 || value.SourceRevision > maxSafeInteger) {
		return AssetCostSummary{}, ErrDomainState
	}
	if err != nil {
		return AssetCostSummary{}, err
	}
	return value, nil
}

func readAssetAcquisition(ctx context.Context, query queryer, asset Asset) (int64, error) {
	rows, err := query.QueryContext(ctx, purchaseLineSelect+" WHERE l.item_unit_id=?", asset.ItemUnitID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total big.Int
	found := false
	for rows.Next() {
		line, err := scanPurchaseLine(rows)
		if err != nil {
			return 0, err
		}
		purchase, err := loadPurchaseLineAuthority(ctx, query, line)
		if err != nil {
			return 0, err
		}
		if line.ItemUnitID == nil || *line.ItemUnitID != asset.ItemUnitID {
			return 0, ErrDomainState
		}
		active, err := purchaseLineIsActive(ctx, query, line)
		if err != nil {
			return 0, err
		}
		if !active {
			continue
		}
		if purchase.Currency != asset.Currency {
			return 0, ErrDomainState
		}
		found = true
		total.Add(&total, big.NewInt(line.PaidMinor))
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	if asset.CostBasis == "manual" {
		if found {
			return 0, ErrDomainState
		}
		return *asset.ManualAcquisitionMinor, nil
	}
	return assetSafeTotal(&total)
}

func readAssetExtra(ctx context.Context, query queryer, asset Asset) (int64, error) {
	rows, err := query.QueryContext(ctx, costSelect+" WHERE c.item_unit_id=?", asset.ItemUnitID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var total big.Int
	verified := make(map[string]bool)
	for rows.Next() {
		value, err := scanCost(rows)
		if err != nil {
			return 0, err
		}
		if value.ItemUnitID != asset.ItemUnitID || value.Currency != asset.Currency {
			return 0, ErrDomainState
		}
		if err = validateAssetCorrection(value.correctionRef(), func(id string) (assetCorrectionRef, error) {
			target, err := scanCost(query.QueryRowContext(ctx, costSelect+" WHERE c.id=?", id))
			return target.correctionRef(), err
		}, verified); err != nil {
			return 0, err
		}
		amount := value.AmountMinor
		if value.Direction == "credit" {
			amount = -amount
		}
		total.Add(&total, big.NewInt(amount))
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	return assetSafeTotal(&total)
}

func readAssetUsage(ctx context.Context, query queryer, asset Asset) (int64, error) {
	rows, err := query.QueryContext(ctx, usageSelect+" WHERE u.item_unit_id=?", asset.ItemUnitID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	steps, verified := make(map[time.Time]*big.Int), make(map[string]bool)
	for rows.Next() {
		value, err := scanUsage(rows)
		if err != nil {
			return 0, err
		}
		if value.ItemUnitID != asset.ItemUnitID || value.UsageUnit != asset.UsageUnit {
			return 0, ErrDomainState
		}
		if err = validateAssetCorrection(value.correctionRef(), func(id string) (assetCorrectionRef, error) {
			target, err := scanUsage(query.QueryRowContext(ctx, usageSelect+" WHERE u.id=?", id))
			return target.correctionRef(), err
		}, verified); err != nil {
			return 0, err
		}
		instant, _ := time.Parse(time.RFC3339Nano, value.OccurredAt)
		instant = instant.UTC()
		if steps[instant] == nil {
			steps[instant] = new(big.Int)
		}
		steps[instant].Add(steps[instant], big.NewInt(value.Delta))
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	instants := make([]time.Time, 0, len(steps))
	for instant := range steps {
		instants = append(instants, instant)
	}
	sort.Slice(instants, func(i, j int) bool { return instants[i].Before(instants[j]) })
	var total big.Int
	for _, instant := range instants {
		total.Add(&total, steps[instant])
		if total.Sign() < 0 {
			return 0, ErrDomainState
		}
		if _, err = assetSafeTotal(&total); err != nil {
			return 0, err
		}
	}
	return total.Int64(), nil
}

func assetSafeTotal(value *big.Int) (int64, error) {
	if !value.IsInt64() {
		return 0, ErrAssetOverflow
	}
	number := value.Int64()
	if number < -maxSafeInteger || number > maxSafeInteger {
		return 0, ErrAssetOverflow
	}
	return number, nil
}

func assetElapsedDays(asset Asset, asOf time.Time, zone *time.Location) (*int64, error) {
	if asset.InServiceAt == nil {
		return nil, nil
	}
	start, _ := time.Parse(time.RFC3339Nano, *asset.InServiceAt)
	end := asOf
	if asset.RetiredAt != nil {
		retired, _ := time.Parse(time.RFC3339Nano, *asset.RetiredAt)
		if retired.Before(end) {
			end = retired
		}
	}
	if end.Before(start) {
		return nil, nil
	}
	// Count proleptic Gregorian date labels, not elapsed 24-hour periods. Skipped
	// labels remain in the range and repeated labels never count another day.
	ordinal := func(instant time.Time) int64 {
		year, month, day := instant.In(zone).Date()
		return time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Unix() / 86400
	}
	days := ordinal(end) - ordinal(start) + 1
	if days < 1 || days > maxSafeInteger {
		return nil, ErrDomainState
	}
	return &days, nil
}
