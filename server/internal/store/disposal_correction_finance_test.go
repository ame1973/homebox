package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDisposalCorrectionValidatesCompleteProposedFinancialHistoryRatherThanUnsafeOldNet(t *testing.T) {
	for _, scenario := range []string{"repair old unsafe by void", "repair old unsafe by replacement", "unsafe proposed net", "exact negative safe edge", "invalid usage prefix", "unsafe residual projection", "foreign cost currency"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			s := fixture.store
			input := disposalCorrectionInput(asset.Revision, 0)
			want, failure := int64(100-maxSafeInteger), error(nil)
			switch scenario {
			case "repair old unsafe by void", "repair old unsafe by replacement":
				insertAssetCost(t, s, "retained-credit", fixture.unit.ID, maxSafeInteger, "credit")
				if value, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, ""); !errors.Is(err, ErrAssetOverflow) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("fixture did not establish an actually unsafe old derived net", err)
				}
				if scenario == "repair old unsafe by void" {
					input.Action, input.Replacement = "void", nil
				}
			case "unsafe proposed net", "exact negative safe edge":
				credit := int64(101)
				if scenario == "exact negative safe edge" {
					credit = 100
				}
				insertAssetCost(t, s, "retained-credit", fixture.unit.ID, credit, "credit")
				input.Replacement.RealizedProceedsMinor = maxSafeInteger
				want = -maxSafeInteger
				if credit == 101 {
					failure = ErrAssetOverflow
				}
			case "invalid usage prefix":
				insertAssetUsage(t, s, "late-positive", fixture.unit.ID, "2026-11-07T00:00:00.000000002Z", 2, nil)
				insertAssetUsage(t, s, "earlier-negative", fixture.unit.ID, "2026-11-07T00:00:00.000000001Z", -1, nil)
				failure = ErrDomainState
			case "unsafe residual projection":
				insertAssetCost(t, s, "retained-credit", fixture.unit.ID, maxSafeInteger, "credit")
				execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=? WHERE item_unit_id=?", maxSafeInteger, fixture.unit.ID)
				failure = ErrAssetOverflow
			case "foreign cost currency":
				insertAssetCost(t, s, "foreign-cost", fixture.unit.ID, 1, "expense")
				execSQL(t, s.db, "DROP TRIGGER asset_cost_events_no_update")
				execSQL(t, s.db, "UPDATE asset_cost_events SET currency='USD'")
				failure = ErrDomainState
			}
			before := disposalCorrectionState(t, s)
			entry, err := service.CorrectDisposal(t.Context(), fixture.token, "complete-proposed-disposal-history", fixture.unit.ID, record.Revision, input)
			if failure != nil {
				if !errors.Is(err, failure) || !reflect.ValueOf(entry).IsZero() {
					t.Fatal("unsafe/incompatible complete proposed history returned partial correction", err)
				}
				requireDisposalCorrectionState(t, s, before)
				return
			}
			if err != nil {
				t.Fatal("valid proposed history was blocked by unsafe old derived total", err)
			}
			summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2020-01-01T00:00:00Z")
			if err != nil || summary.NetCostMinor != want {
				t.Fatal("complete signed safe result rounded, overflowed, clamped or depended on as_of history", summary, err)
			}
		})
	}
}

func TestDisposalCorrectionRetainedUnknownCurrencyAndNonSaleProceedsCanBeVoidedWithoutRepricing(t *testing.T) {
	for _, code := range []string{"ZZZ", "XXX", "XAU"} {
		t.Run(code, func(t *testing.T) {
			fixture := assetsFixture(t, true)
			s := fixture.store
			service := assetReader(t, fixture.holdingFixture)
			execSQL(t, s.db, "UPDATE asset_profiles SET currency=? WHERE item_unit_id=?", code, fixture.unit.ID)
			execSQL(t, s.db, "DROP TRIGGER asset_disposals_no_update")
			execSQL(t, s.db, "UPDATE asset_disposals SET kind='donated',currency=?,note=?,disposed_at='2026-11-07T12:00:00.123456789+08:00' WHERE item_unit_id=?", code, "\x00"+strings.Repeat("😀", 9999), fixture.unit.ID)
			// Current acquisition/cost currency must still match this retained profile;
			// a void does not waive unrelated source validation or reprice it.
			execSQL(t, s.db, "UPDATE purchases SET currency=? WHERE id=?", code, fixture.purchase.ID)
			execSQL(t, s.db, "DROP TRIGGER asset_cost_events_no_update")
			execSQL(t, s.db, "UPDATE asset_cost_events SET currency=? WHERE item_unit_id=?", code, fixture.unit.ID)
			asset, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil {
				t.Fatal(err)
			}
			record, err := service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil || record.Current == nil || record.Current.Kind != "donated" || record.Current.RealizedProceedsMinor != 20000 || record.Current.DisposedAt != "2026-11-07T12:00:00.123456789+08:00" {
				t.Fatal("retained reads applied new-money/non-sale/canonicalization policy", err)
			}
			input := disposalCorrectionInput(asset.Revision, 0)
			input.Replacement.Currency = code
			before := disposalCorrectionState(t, s)
			if value, err := service.CorrectDisposal(t.Context(), fixture.token, "unsupported-new-disposal-money", fixture.unit.ID, record.Revision, input); !errors.Is(err, ErrAssetCurrency) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("new zero-money decision accepted unknown or nonfinite currency", err)
			}
			requireDisposalCorrectionState(t, s, before)
			input.Action, input.Replacement = "void", nil
			input.Reason = "\x00" + strings.Repeat("😀", 9999)
			entry, err := service.CorrectDisposal(t.Context(), fixture.token, "void-retained-unpriced-history", fixture.unit.ID, record.Revision, input)
			if err != nil || entry.After.Current != nil || entry.After.Original != record.Original {
				t.Fatal("financial void repriced/repaired retained non-sale money or rejected native leading NUL", err)
			}
			retained, err := service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil || retained.Original != record.Original || retained.Current != nil {
				t.Fatal("void lost exact original representation", err)
			}
		})
	}
}

func TestDisposalCorrectionDeliberateZeroRemainsAnActiveCurrencyReferenceButVoidCanRemoveCurrentEffect(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 0)
	entry, err := service.CorrectDisposal(t.Context(), fixture.token, "deliberate-zero-proceeds", fixture.unit.ID, record.Revision, input)
	if err != nil || entry.After.Current == nil {
		t.Fatal(err)
	}
	asset, err = service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	profile := retainedProfileInput(item.Revision, asset)
	profile.Currency = "USD"
	before := disposalCorrectionState(t, fixture.store)
	if value, err := service.PutProfile(t.Context(), fixture.token, "cannot-reprice-active-zero-disposal", fixture.unit.ID, fixture.unit.Revision, profile); !errors.Is(err, ErrDomainState) || value != (Asset{}) {
		t.Fatal("nonnull zero proceeds became absence of current financial authority", err)
	}
	requireDisposalCorrectionState(t, fixture.store, before)
	input.AssetRevision, input.Action, input.Replacement = asset.Revision, "void", nil
	voided, err := service.CorrectDisposal(t.Context(), fixture.token, "explicit-zero-disposal-void", fixture.unit.ID, entry.After.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	asset, err = service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	item = getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	profile.AssetRevision, profile.ItemRevision = &asset.Revision, item.Revision
	changed, err := service.PutProfile(t.Context(), fixture.token, "reviewed-currency-after-financial-void", fixture.unit.ID, fixture.unit.Revision, profile)
	if err != nil || changed.Currency != "USD" {
		t.Fatal("void failed to remove only current disposal interpretation", err)
	}
	input = disposalCorrectionInput(changed.Revision, 13)
	before = disposalCorrectionState(t, fixture.store)
	if value, err := service.CorrectDisposal(t.Context(), fixture.token, "foreign-reinstatement", fixture.unit.ID, voided.After.Revision, input); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("reinstatement borrowed original currency instead of the actual Asset interpretation", err)
	}
	requireDisposalCorrectionState(t, fixture.store, before)
	input.Replacement.Currency = "USD"
	if value, err := service.CorrectDisposal(t.Context(), fixture.token, "matching-reviewed-reinstatement", fixture.unit.ID, voided.After.Revision, input); err != nil || value.After.Current == nil || value.After.Current.Currency != "USD" || value.After.Original.Currency != "JPY" {
		t.Fatal("explicit reinstatement converted immutable original history or rejected a changed reviewed profile", err)
	}
	if page, err := service.PageDisposalCorrections(t.Context(), fixture.token, fixture.unit.ID, "", 100); err != nil || len(page.Data) != 3 {
		t.Fatal("legitimate later profile interpretation made complete old correction history unreadable", err)
	}
}

func TestDisposalCorrectionDoesNotAddPhysicalUnitOrOwnerItemProofs(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	inventory, err := NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.PatchUnit(t.Context(), fixture.token, "later-physical-metadata-review", fixture.unit.ID, fixture.unit.Revision, UnitPatch{Notes: testPointer("Later independently reviewed Unit metadata")}); err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	if _, err := fixture.catalog.Patch(t.Context(), fixture.token, "later-owner-label-review", item.ID, item.Revision, ItemPatch{Name: testPointer("Later independently reviewed Item name")}); err != nil {
		t.Fatal(err)
	}
	setCatalogProjectionRevision(t, fixture.store, "unit", fixture.unit.ID, maxSafeInteger)
	before := disposalCorrectionState(t, fixture.store)
	if _, err := service.CorrectDisposal(t.Context(), fixture.token, "financial-original-scopes-only", fixture.unit.ID, record.Revision, disposalCorrectionInput(asset.Revision, 0)); err != nil {
		t.Fatal("unrelated later Unit/Item metadata or unchanged physical max counter became another write proof", err)
	}
	after := disposalCorrectionState(t, fixture.store)
	if before["item_units"] != after["item_units"] || before["inventory_events"] != after["inventory_events"] || before["locations"] != after["locations"] || scalar[int64](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID) != maxSafeInteger {
		t.Fatal("financial decision changed independently reviewed physical metadata/authority")
	}
}
