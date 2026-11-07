package store

import (
	"fmt"
	"reflect"
	"testing"
)

func allAssetPageIDs(t *testing.T, service *Assets, token string, filters AssetFilters, size int) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := service.Page(t.Context(), token, cursor, size, filters)
		if err != nil {
			t.Fatal("actual filtered Asset page failed", err)
		}
		for _, value := range page.Data {
			ids = append(ids, value.ItemUnitID)
		}
		if page.NextCursor == nil {
			return ids
		}
		cursor = *page.NextCursor
	}
}

func TestAssetPagesFilterActualOwnerCategoryStatusProfileCurrencyAndExactNondisposedUnitPlacement(t *testing.T) {
	fixture := holdingsFixture(t)
	s, service := fixture.store, assetReader(t, fixture)
	taxonomy, err := NewTaxonomy(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, fixture.token, "Asset root", nil)
	child := createCategory(t, taxonomy, fixture.token, "Asset child", &category.ID)
	otherCategory := createCategory(t, taxonomy, fixture.token, "Asset other", nil)
	fixture.individual, err = fixture.catalog.Patch(t.Context(), fixture.token, "asset-page-category", fixture.individual.ID, fixture.individual.Revision, ItemPatch{CategoryID: NullableID{Set: true, Value: &category.ID}})
	if err != nil {
		t.Fatal(err)
	}
	seedAssetPageOwner(t, fixture, "a-root")
	childOwner := fixture
	childOwner.individual = createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Child Asset", CategoryID: &child.ID})
	seedAssetPageOwner(t, childOwner, "b-child")
	otherOwner := fixture
	otherOwner.individual = createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Archived Asset", CategoryID: &otherCategory.ID, Status: testPointer("archived")})
	seedAssetPageOwner(t, otherOwner, "c-archived")
	execSQL(t, s.db, "UPDATE item_units SET location_id='source' WHERE id IN('a-root','c-archived')")
	execSQL(t, s.db, "UPDATE item_units SET location_id='target',status='retired' WHERE id='b-child'")
	execSQL(t, s.db, "UPDATE item_units SET status='disposed' WHERE id='c-archived'")
	// This unbacked sibling is not the a-root Asset's placement or acquisition.
	execSQL(t, s.db, "UPDATE item_units SET location_id='target' WHERE id=?", fixture.unit.ID)
	execSQL(t, s.db, "UPDATE asset_profiles SET cost_basis='purchase',manual_acquisition_minor=NULL")
	execSQL(t, s.db, "UPDATE asset_profiles SET currency='USD',in_service_at='2050-01-01T00:00:00Z' WHERE item_unit_id='b-child'")
	for _, row := range []struct{ id, merchant, instant string }{
		{"first-merchant", "Case Sensitive\n ", "2026-11-05T00:00:00Z"},
		{"first-date", "Other", "2026-11-06T00:00:00Z"},
		{"matching", "Case Sensitive\n ", "2026-11-06T00:00:00Z"},
	} {
		seedReadPurchase(t, s, PurchaseData{ID: row.id, MerchantName: row.merchant, PurchasedAt: row.instant, Currency: "JPY"})
	}
	for _, row := range []struct {
		id, parent, item string
		unit             *string
	}{
		{"first-merchant-line", "first-merchant", fixture.individual.ID, testPointer("a-root")},
		{"first-date-line", "first-date", fixture.individual.ID, testPointer("a-root")},
		{"matching-child", "matching", childOwner.individual.ID, testPointer("b-child")},
		{"unallocated-first", "matching", fixture.individual.ID, nil},
		{"sibling-first", "matching", fixture.individual.ID, &fixture.unit.ID},
	} {
		seedReadPurchaseLine(t, s, PurchaseLineData{ID: row.id, PurchaseID: row.parent, ItemID: row.item, ItemUnitID: row.unit, QuantityBase: 1, PaidMinor: 0})
	}
	before := assetProfileState(t, s)
	for _, test := range []struct {
		name    string
		filters AssetFilters
		want    []string
	}{
		{"all actual profiles", AssetFilters{}, []string{"a-root", "b-child", "c-archived"}},
		{"direct category", AssetFilters{CategoryID: category.ID, Scope: "direct"}, []string{"a-root"}},
		{"subtree category", AssetFilters{CategoryID: category.ID}, []string{"a-root", "b-child"}},
		{"child category", AssetFilters{CategoryID: child.ID}, []string{"b-child"}},
		{"metadata archived", AssetFilters{Status: "archived"}, []string{"c-archived"}},
		{"metadata active", AssetFilters{Status: "active"}, []string{"a-root", "b-child"}},
		{"profile currency", AssetFilters{Currency: "USD"}, []string{"b-child"}},
		{"direct source excludes disposed", AssetFilters{LocationID: "source", Scope: "direct"}, []string{"a-root"}},
		{"exact Unit not sibling", AssetFilters{LocationID: "target", Scope: "direct"}, []string{"b-child"}},
		{"subtree location", AssetFilters{LocationID: "root"}, []string{"a-root", "b-child"}},
		{"no implicit root placement", AssetFilters{LocationID: "root", Scope: "direct"}, nil},
		{"combined exact owner", AssetFilters{CategoryID: category.ID, LocationID: "target", Scope: "direct"}, nil},
		{"combined subtree", AssetFilters{CategoryID: category.ID, LocationID: "target"}, []string{"b-child"}},
		{"same purchase not sibling or unallocated", AssetFilters{From: "2026-11-06", To: "2026-11-06", Merchant: "Case Sensitive\n "}, []string{"b-child"}},
		{"profile currency not header currency", AssetFilters{Currency: "USD", From: "2026-11-06", Merchant: "Case Sensitive\n "}, []string{"b-child"}},
		{"exact merchant", AssetFilters{Merchant: "Case Sensitive\n "}, []string{"a-root", "b-child"}},
		{"no merchant trim", AssetFilters{Merchant: "Case Sensitive"}, nil},
		{"no merchant case fold", AssetFilters{Merchant: "case sensitive\n "}, nil},
		{"missing category", AssetFilters{CategoryID: "absent"}, nil},
		{"missing location", AssetFilters{LocationID: "absent"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allAssetPageIDs(t, service, fixture.token, test.filters, 1); !reflect.DeepEqual(got, test.want) {
				t.Fatal("Asset filters borrowed another Unit/Item/purchase or filtered after slicing", got, test.want)
			}
		})
	}
	requireAssetProfileState(t, s, before)
}

func TestAssetPagesClassifyPurchaseInstantsInInclusiveHomeCivilDatesNotServiceDatesOrDurations(t *testing.T) {
	for _, test := range []struct {
		zone, date string
		instants   []string
		want       []int
	}{
		{"Asia/Taipei", "2026-11-05", []string{"2026-11-04T15:59:59.999999999Z", "2026-11-04T16:00:00Z", "2026-11-05T23:59:59.999999999+08:00", "2026-11-05T16:00:00Z"}, []int{1, 2}},
		{"America/New_York", "2026-03-08", []string{"2026-03-08T04:59:59.999999999Z", "2026-03-08T05:00:00Z", "2026-03-09T03:59:59.999999999Z", "2026-03-09T04:00:00Z"}, []int{1, 2}},
		{"America/New_York", "2026-11-01", []string{"2026-11-01T03:59:59.999999999Z", "2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00", "2026-11-02T05:00:00Z"}, []int{1, 2}},
		{"Pacific/Apia", "2011-12-30", []string{"2011-12-30T09:59:59.999999999Z", "2011-12-30T10:00:00Z"}, nil},
		{"Pacific/Apia", "2011-12-31", []string{"2011-12-30T09:59:59.999999999Z", "2011-12-30T10:00:00Z", "2011-12-31T23:59:59.999999999+14:00"}, []int{1, 2}},
	} {
		t.Run(test.zone+test.date, func(t *testing.T) {
			fixture := holdingsFixture(t)
			service := assetReader(t, fixture)
			setCatalogTimezone(t, fixture.store, test.zone)
			for index, instant := range test.instants {
				id := fmt.Sprintf("civil-%02d", index)
				seedAssetPageOwner(t, fixture, id)
				seedReadPurchase(t, fixture.store, PurchaseData{ID: id, MerchantName: "Civil", PurchasedAt: instant, Currency: "JPY"})
				seedReadPurchaseLine(t, fixture.store, PurchaseLineData{ID: id, PurchaseID: id, ItemID: fixture.individual.ID, ItemUnitID: &id, QuantityBase: 1, PaidMinor: 0})
			}
			var want []string
			for _, index := range test.want {
				want = append(want, fmt.Sprintf("civil-%02d", index))
			}
			before := assetProfileState(t, fixture.store)
			if got := allAssetPageIDs(t, service, fixture.token, AssetFilters{From: test.date, To: test.date, Merchant: "Civil"}, 1); !reflect.DeepEqual(got, want) {
				t.Fatal("Asset purchase date used UTC/duration windows or missed repeated/skipped labels", got, want)
			}
			requireAssetProfileState(t, fixture.store, before)
		})
	}
}

func TestAssetPagesStreamAllDateCandidatesBeforeSelectingAValidatedMatchingLookahead(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	for index := range 123 {
		id, instant := fmt.Sprintf("old-%03d", index), "2025-11-06T00:00:00Z"
		if index >= 120 {
			id, instant = fmt.Sprintf("zz-match-%03d", index-120), "2026-11-06T00:00:00Z"
		}
		seedAssetPageOwner(t, fixture, id)
		seedReadPurchase(t, fixture.store, PurchaseData{ID: id, MerchantName: "Stream", PurchasedAt: instant, Currency: "JPY"})
		seedReadPurchaseLine(t, fixture.store, PurchaseLineData{ID: id, PurchaseID: id, ItemID: fixture.individual.ID, ItemUnitID: &id, QuantityBase: 1, PaidMinor: 0})
	}
	before := assetProfileState(t, fixture.store)
	want := []string{"zz-match-000", "zz-match-001", "zz-match-002"}
	if got := allAssetPageIDs(t, service, fixture.token, AssetFilters{From: "2026-11-06", To: "2026-11-06", Merchant: "Stream"}, 1); !reflect.DeepEqual(got, want) {
		t.Fatal("date filtering silently sliced raw candidates first", got, want)
	}
	requireAssetProfileState(t, fixture.store, before)
}
