package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestAssetPurchaseValidatesCompleteActualFinancialSourceInsteadOfOnlyProfileSpelling(t *testing.T) {
	for _, failure := range []string{"missing Asset counter", "missing old Line counter", "foreign cost currency", "negative usage prefix", "invalid service interval", "unsafe residual", "invalid usage unit"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			switch failure {
			case "missing Asset counter", "missing old Line counter":
				kind, id := "asset", fixture.unit.ID
				if failure == "missing old Line counter" {
					kind, id = "purchase_line", fixture.unitLine.ID
				}
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, id)
			case "foreign cost currency":
				insertAssetCost(t, s, "foreign-cost", fixture.unit.ID, 1, "expense")
				execSQL(t, s.db, "DROP TRIGGER asset_cost_events_no_update")
				execSQL(t, s.db, "UPDATE asset_cost_events SET currency='USD'")
			case "negative usage prefix":
				insertAssetUsage(t, s, "later-use", fixture.unit.ID, "2026-11-07T00:00:01Z", 1, nil)
				insertAssetUsage(t, s, "earlier-correction", fixture.unit.ID, "2026-11-07T00:00:00.999999999Z", -1, testPointer("later-use"))
			case "invalid service interval":
				execSQL(t, s.db, "UPDATE asset_profiles SET in_service_at='2026-11-07T12:00:00+08:00',retired_at='2026-11-07T12:00:00+09:00'")
			case "unsafe residual":
				execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=?", maxSafeInteger+1)
			case "invalid usage unit":
				execSQL(t, s.db, "PRAGMA ignore_check_constraints=ON")
				execSQL(t, s.db, "UPDATE asset_profiles SET usage_unit='invalid'")
				execSQL(t, s.db, "PRAGMA ignore_check_constraints=OFF")
			}
			before := assetProfileState(t, s)
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
			if value, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-invalid-source", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
				t.Fatal("invalid actual financial source accepted a new allocation", value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetPurchaseProposedUnsafeAcquisitionAndNetRollbackWithoutLosingTheKey(t *testing.T) {
	for _, failure := range []string{"acquisition", "net", "projected net"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			amount := maxSafeInteger
			if failure != "acquisition" {
				insertAssetCost(t, s, "large-expense", fixture.unit.ID, maxSafeInteger-120000, "expense")
				amount = 1
				if failure == "projected net" {
					execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=0")
					execSQL(t, s.db, "INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,created_at) VALUES(?,'2026-11-07T00:00:00Z','sold',1,'JPY','legacy')", fixture.unit.ID)
					execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,1)", fixture.unit.ID)
				}
			}
			before := assetProfileState(t, s)
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: amount}
			if value, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-unsafe-total", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrAssetOverflow) || value != (PurchaseLine{}) {
				t.Fatal("unsafe proposed total returned a partial financial allocation", value, err)
			}
			requireAssetProfileState(t, s, before)
			input.PaidMinor = 0
			if _, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-unsafe-total", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
				t.Fatal("explicitly reviewed safe decision could not reuse its rolled-back key", err)
			}
		})
	}
}

func TestAssetPurchaseValidatesTheProposedCompleteTotalsNotAnUnsafeOldDerivedNet(t *testing.T) {
	fixture, purchases, assets, _ := purchaseAssetFixture(t)
	s := fixture.store
	insertAssetCost(t, s, "retained-large-credit", fixture.unit.ID, maxSafeInteger, "credit")
	execSQL(t, s.db, "UPDATE item_units SET status='disposed' WHERE id=?", fixture.unit.ID)
	execSQL(t, s.db, "INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,created_at) VALUES(?,'2026-11-07T00:00:00Z','sold',?,'JPY','legacy')", fixture.unit.ID, maxSafeInteger)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,1)", fixture.unit.ID)
	if value, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z"); !errors.Is(err, ErrAssetOverflow) || value != (AssetCostSummary{}) {
		t.Fatal("fixture failed to expose its actual unsafe old net", value, err)
	}
	input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: maxSafeInteger - 120000}
	if _, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-proposed-safe", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal("safe complete proposed totals were rejected using an old cached/derived summary", err)
	}
	summary, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z")
	if err != nil || summary.AcquisitionMinor != maxSafeInteger || summary.NetCostMinor != -maxSafeInteger {
		t.Fatal("proposed exact source lost signed precision or clamped negative net", summary, err)
	}
}

func TestAssetPurchaseDoesNotUseCachesTimezonePlacementOrPhysicalStatusAsFinancialAuthority(t *testing.T) {
	fixture, purchases, assets, profile := purchaseAssetFixture(t)
	s := fixture.store
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{}' WHERE entity_type='asset'")
	execSQL(t, s.db, `INSERT INTO app_settings VALUES('preferences.timezone','"Invalid/Zone"','legacy')`)
	execSQL(t, s.db, "UPDATE item_units SET status='disposed' WHERE id=?", fixture.unit.ID)
	setCatalogProjectionRevision(t, s, "unit", fixture.unit.ID, maxSafeInteger)
	before := assetProfileState(t, s)
	input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
	if _, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-no-physical-authority", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal("nonphysical attribution was gated by an unchanged physical counter, timezone or cache", err)
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"item_units", "locations", "stock_balances", "inventory_events", "asset_usage_events", "asset_cost_events", "asset_disposals", "asset_profile_changes"} {
		if after[table] != before[table] {
			t.Fatal("allocation revived, moved, retired or otherwise reinterpreted actual source", table)
		}
	}
	current, err := assets.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || current.Revision != profile.Revision+1 || !reflect.DeepEqual(current.AssetData, profile.AssetData) {
		t.Fatal("actual profile authority was taken from cache or silently repaired", current, err)
	}
}

func TestAssetPurchaseNullOrOtherUnitAllocationDoesNotBorrowSiblingAssetFinance(t *testing.T) {
	fixture, purchases, _, profile := purchaseAssetFixture(t)
	s := fixture.store
	for _, scope := range []string{"null", "sibling"} {
		item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := purchases.Get(t.Context(), fixture.token, fixture.purchase.ID)
		if err != nil {
			t.Fatal(err)
		}
		input := PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1, PaidMinor: 500}
		if scope == "sibling" {
			inventory, err := s.NewInventory(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			unit, err := inventory.CreateUnit(t.Context(), fixture.token, "purchase-asset-unbacked-sibling", item.ID, item.Revision, UnitCreate{})
			if err != nil {
				t.Fatal(err)
			}
			input.ItemUnitID, input.ItemRevision = &unit.ID, item.Revision+1
		}
		before := scalar[string](t, s.db, "SELECT json_array(cost_basis,currency,manual_acquisition_minor,in_service_at,retired_at,estimated_residual_minor,usage_unit,updated_at) FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID)
		if _, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-unbacked-"+scope, parent.ID, parent.Revision, input); err != nil {
			t.Fatal(err)
		}
		if scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID) != profile.Revision ||
			scalar[string](t, s.db, "SELECT json_array(cost_basis,currency,manual_acquisition_minor,in_service_at,retired_at,estimated_residual_minor,usage_unit,updated_at) FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != before {
			t.Fatal("unallocated or sibling spend changed an unrelated Asset")
		}
	}
}
