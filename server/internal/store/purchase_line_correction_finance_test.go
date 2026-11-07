package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestPurchaseLineCorrectionReattributesTwoActualAssetsAtomicallyWithoutChangingPhysicalUnits(t *testing.T) {
	fixture, purchases, assets, originalAsset := purchaseAssetFixture(t)
	inventory, err := NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	targetItem := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Actual replacement owner"})
	targetUnit, err := inventory.CreateUnit(t.Context(), fixture.token, "new-correction-target-unit", targetItem.ID, targetItem.Revision, UnitCreate{})
	if err != nil {
		t.Fatal(err)
	}
	targetItem = getCatalogItem(t, fixture.catalog, fixture.token, targetItem.ID)
	targetAsset, err := assets.PutProfile(t.Context(), fixture.token, "new-correction-target-profile", targetUnit.ID, targetUnit.Revision, AssetProfileWrite{ItemRevision: targetItem.Revision, CostBasis: "purchase", Currency: "JPY", UsageUnit: "times"})
	if err != nil {
		t.Fatal(err)
	}
	targetItem = getCatalogItem(t, fixture.catalog, fixture.token, targetItem.ID)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: targetItem.ID, ItemRevision: targetItem.Revision, ItemUnitID: &targetUnit.ID, QuantityBase: 1, PaidMinor: 321}
	before := assetProfileState(t, fixture.store)
	changed := requireProjectionReplay(t, fixture.store, func() {
		if _, err := purchases.CorrectLine(t.Context(), fixture.token, "correct-two-actual-assets", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
			t.Fatal(err)
		}
	})
	if len(changed) != 11 || !changed[projectionKey{"asset", fixture.unit.ID}] || !changed[projectionKey{"asset", targetUnit.ID}] || changed[projectionKey{"unit", fixture.unit.ID}] || changed[projectionKey{"unit", targetUnit.ID}] {
		t.Fatal("correction missed an actual financial aggregate or touched physical authority", changed)
	}
	for _, original := range []Asset{originalAsset, targetAsset} {
		current, err := assets.Get(t.Context(), fixture.token, original.ItemUnitID)
		if err != nil || current.Revision != original.Revision+1 || !reflect.DeepEqual(current.AssetData, original.AssetData) {
			t.Fatal("old/new Asset interpretation changed or financial counters were not coalesced", err)
		}
		want := int64(0)
		if original.ItemUnitID == targetUnit.ID {
			want = 321
		}
		summary, err := assets.CostSummary(t.Context(), fixture.token, original.ItemUnitID, "2026-11-07T04:00:00Z")
		if err != nil || summary.AcquisitionMinor != want || summary.NetCostMinor != want {
			t.Fatal("financial attribution borrowed, multiplied or retained the wrong Unit's spend", summary, err)
		}
	}
	after := assetProfileState(t, fixture.store)
	for _, table := range []string{"item_units", "locations", "stock_balances", "inventory_events", "asset_usage_events", "asset_cost_events", "asset_disposals", "asset_profile_changes"} {
		if after[table] != before[table] {
			t.Fatal("financial reattribution changed physical or unrelated immutable history", table)
		}
	}
}

func TestPurchaseLineCorrectionValidatesWholeProposedTotalsNotTemporaryOrUnsafeOldTotals(t *testing.T) {
	for _, scenario := range []string{"same Asset maximum replacement", "unsafe old positive net", "unsafe proposed negative net", "retained manual incompatibility", "retained foreign currency"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, purchases, assets, original := purchaseAssetFixture(t)
			s := fixture.store
			input := reviewedLineVoid(t, fixture, fixture.unitLine)
			want := int64(0)
			switch scenario {
			case "same Asset maximum replacement":
				input.Action = "replace"
				input.Replacement = &PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: input.ItemRevision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: maxSafeInteger}
				want = maxSafeInteger
			case "unsafe old positive net":
				insertAssetCost(t, s, "retained-max-expense", fixture.unit.ID, maxSafeInteger, "expense")
				if value, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z"); !errors.Is(err, ErrAssetOverflow) || value != (AssetCostSummary{}) {
					t.Fatal("fixture did not retain an actually unsafe old derived net", err)
				}
				want = maxSafeInteger
			case "unsafe proposed negative net":
				insertAssetCost(t, s, "retained-max-credit", fixture.unit.ID, maxSafeInteger, "credit")
				execSQL(t, s.db, "UPDATE item_units SET status='disposed' WHERE id=?", fixture.unit.ID)
				execSQL(t, s.db, "INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,created_at) VALUES(?,'2026-11-07T00:00:00Z','sold',1,'JPY','retained')", fixture.unit.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,1)", fixture.unit.ID)
				if value, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z"); err != nil || value.NetCostMinor != -maxSafeInteger+119999 {
					t.Fatal("fixture did not retain a safe original negative net", value, err)
				}
				before := assetProfileState(t, s)
				if value, err := purchases.CorrectLine(t.Context(), fixture.token, "complete-proposed-correction", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrAssetOverflow) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("unsafe proposed negative total partially voided original acquisition", err)
				}
				requireAssetProfileState(t, s, before)
				input.Action = "replace"
				input.Replacement = &PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: input.ItemRevision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 1}
				want = -maxSafeInteger
			case "retained manual incompatibility":
				execSQL(t, s.db, "UPDATE asset_profiles SET cost_basis='manual',manual_acquisition_minor=0 WHERE item_unit_id=?", fixture.unit.ID)
			case "retained foreign currency":
				execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
			}
			if _, err := purchases.CorrectLine(t.Context(), fixture.token, "complete-proposed-correction", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
				t.Fatal("valid whole proposed history was rejected using old/temporary totals or new-money policy", err)
			}
			summary, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z")
			if err != nil || summary.NetCostMinor != want {
				t.Fatal("complete exact proposed source lost signed precision or was clamped", summary, err)
			}
			if current, err := assets.Get(t.Context(), fixture.token, fixture.unit.ID); err != nil || current.Revision != original.Revision+1 {
				t.Fatal("same old/new Asset advanced more than once", err)
			}
		})
	}
}

func TestPurchaseLineCorrectionZeroRemainsActiveFinancialReferenceUntilExplicitVoid(t *testing.T) {
	fixture, purchases, assets, _ := purchaseAssetFixture(t)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: input.ItemRevision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
	entry, err := purchases.CorrectLine(t.Context(), fixture.token, "replace-with-deliberate-zero", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	current, err := assets.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	manual := manualProfileInput(item.Revision, 0)
	manual.AssetRevision = &current.Revision
	before := assetProfileState(t, fixture.store)
	if value, err := assets.PutProfile(t.Context(), fixture.token, "manual-with-active-zero", fixture.unit.ID, fixture.unit.Revision, manual); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("zero-paid active history was interpreted as financial absence", err)
	}
	requireAssetProfileState(t, fixture.store, before)
	if page, err := assets.Page(t.Context(), fixture.token, "", 50, AssetFilters{Merchant: fixture.purchase.MerchantName}); err != nil || len(page.Data) != 1 {
		t.Fatal("active zero no longer qualified exact-Unit purchase evidence", err)
	}
	void := reviewedLineVoid(t, fixture, *entry.After)
	if _, err := purchases.CorrectLine(t.Context(), fixture.token, "explicitly-void-zero-reference", fixture.purchase.ID, fixture.purchase.Revision+1, void); err != nil {
		t.Fatal(err)
	}
	if page, err := assets.Page(t.Context(), fixture.token, "", 50, AssetFilters{Merchant: fixture.purchase.MerchantName}); err != nil || len(page.Data) != 0 {
		t.Fatal("voided zero qualified current exact-Unit purchase evidence", err)
	}
	current, err = assets.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	manual.ItemRevision, manual.AssetRevision = item.Revision, &current.Revision
	if value, err := assets.PutProfile(t.Context(), fixture.token, "manual-after-zero-void", fixture.unit.ID, fixture.unit.Revision, manual); err != nil || value.CostBasis != "manual" {
		t.Fatal("explicit financial void did not remove current acquisition eligibility", err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM purchase_lines WHERE item_unit_id=?", fixture.unit.ID) != 2 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM purchase_line_corrections") != 2 {
		t.Fatal("void erased zero or original paid source history")
	}
}

func TestPurchaseLineCorrectionReplacementStillRequiresItsOwnActualInterpretationAndNewMoneyEligibility(t *testing.T) {
	for _, failure := range []string{"missing target", "stale target", "quantity Unit", "foreign Unit", "missing Unit", "manual target", "foreign currency", "missing Asset counter", "negative usage prefix"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			input := reviewedLineVoid(t, fixture, fixture.unitLine)
			input.Action = "replace"
			input.Replacement = &PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: input.ItemRevision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
			want := ErrDomainState
			switch failure {
			case "missing target":
				input.Replacement.ItemID, want = "missing", ErrNotFound
			case "stale target":
				input.Replacement.ItemRevision++
				want = ErrRevisionConflict
			case "quantity Unit":
				input.Replacement.ItemID, input.Replacement.ItemRevision, want = fixture.quantity.ID, fixture.quantity.Revision, ErrUnitTracking
			case "foreign Unit":
				other := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Another actual Unit owner"})
				input.Replacement.ItemID, input.Replacement.ItemRevision, want = other.ID, other.Revision, ErrDomainInput
			case "missing Unit":
				input.Replacement.ItemUnitID, want = testPointer("missing"), ErrNotFound
			case "manual target":
				execSQL(t, fixture.store.db, "UPDATE asset_profiles SET cost_basis='manual',manual_acquisition_minor=0")
				want = ErrPurchaseAssetBasis
			case "foreign currency":
				execSQL(t, fixture.store.db, "UPDATE asset_profiles SET currency='USD'")
				want = ErrPurchaseCurrency
			case "missing Asset counter":
				execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='asset'")
				execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type='asset'")
			case "negative usage prefix":
				insertAssetUsage(t, fixture.store, "later-retained-use", fixture.unit.ID, "2026-11-07T00:00:01Z", 1, nil)
				insertAssetUsage(t, fixture.store, "earlier-retained-correction", fixture.unit.ID, "2026-11-07T00:00:00.999999999Z", -1, testPointer("later-retained-use"))
			}
			before := assetProfileState(t, fixture.store)
			if value, err := purchases.CorrectLine(t.Context(), fixture.token, "guarded-replacement-finance", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("replacement borrowed old proofs, sibling finance or ignored invalid proposed history", failure, err)
			}
			requireAssetProfileState(t, fixture.store, before)
		})
	}
}
