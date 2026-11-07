package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func seedAssetPageOwner(t *testing.T, fixture holdingFixture, id string) Asset {
	t.Helper()
	unit := UnitData{ID: id, ItemID: fixture.individual.ID, AssetCode: "AST-" + id, Status: "stored"}
	execSQL(t, fixture.store.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES(?,?,?,'legacy','legacy')", id, unit.ItemID, unit.AssetCode)
	seedCatalogProjection(t, fixture.store, "unit", id, unit)
	asset := Asset{AssetData{ItemUnitID: id, CostBasis: "manual", Currency: "JPY", ManualAcquisitionMinor: testPointer(int64(0)), UsageUnit: "times"}, 1}
	execSQL(t, fixture.store.db, "INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,manual_acquisition_minor,created_at,updated_at) VALUES(?,'manual','JPY',0,'legacy','legacy')", id)
	seedCatalogProjection(t, fixture.store, "asset", id, asset.AssetData)
	return asset
}

func TestAssetPagesCompleteAll205ProfilesAndOwnedEventsWithIndependentSignedContinuations(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	for index := range 205 {
		id := fmt.Sprintf("asset-%03d", index)
		seedAssetPageOwner(t, fixture, id)
		eventID := fmt.Sprintf("event-%03d", index)
		insertAssetUsage(t, fixture.store, eventID, "asset-000", "2026-11-07T12:00:00.123456789+08:00", 1, nil)
		insertAssetCost(t, fixture.store, eventID, "asset-000", 1, "expense")
	}
	before := assetProfileState(t, fixture.store)
	var assetIDs, usageIDs, costIDs []string
	cursor := ""
	for {
		page, err := service.Page(t.Context(), fixture.token, cursor, 100, AssetFilters{})
		if err != nil || len(page.Data) == 0 || len(page.Data) > 100 {
			t.Fatal("complete Asset continuation failed or truncated source", page, err)
		}
		for _, value := range page.Data {
			assetIDs = append(assetIDs, value.ItemUnitID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	cursor = ""
	for {
		page, err := service.PageUsage(t.Context(), fixture.token, "asset-000", cursor, 100)
		if err != nil || len(page.Data) == 0 || len(page.Data) > 100 {
			t.Fatal("complete usage continuation failed or truncated source", page, err)
		}
		for _, value := range page.Data {
			usageIDs = append(usageIDs, value.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	cursor = ""
	for {
		page, err := service.PageCosts(t.Context(), fixture.token, "asset-000", cursor, 100)
		if err != nil || len(page.Data) == 0 || len(page.Data) > 100 {
			t.Fatal("complete cost continuation failed or truncated source", page, err)
		}
		for _, value := range page.Data {
			costIDs = append(costIDs, value.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(assetIDs) != 205 || len(usageIDs) != 205 || len(costIDs) != 205 {
		t.Fatal("source after the first/second page was lost or duplicated", len(assetIDs), len(usageIDs), len(costIDs))
	}
	for index := range 205 {
		if assetIDs[index] != fmt.Sprintf("asset-%03d", index) || usageIDs[index] != fmt.Sprintf("event-%03d", index) || costIDs[index] != usageIDs[index] {
			t.Fatal("stable source-ID order or collection independence was lost", index)
		}
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestAssetPageCursorsBindOwnerCollectionSizeFiltersAndResolvedTimezone(t *testing.T) {
	fixture := assetsFixture(t, false)
	service := assetReader(t, fixture.holdingFixture)
	seedAssetPageOwner(t, fixture.holdingFixture, "another-asset")
	insertAssetUsage(t, fixture.store, "another-use", fixture.unit.ID, "2026-11-07T00:00:00Z", 1, nil)
	insertAssetCost(t, fixture.store, "another-cost", fixture.unit.ID, 1, "expense")
	assets, err := service.Page(t.Context(), fixture.token, "", 1, AssetFilters{})
	if err != nil || assets.NextCursor == nil {
		t.Fatal("fixture did not sign actual Asset lookahead", err)
	}
	usage, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 1)
	if err != nil || usage.NextCursor == nil {
		t.Fatal("fixture did not sign actual usage lookahead", err)
	}
	costs, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 1)
	if err != nil || costs.NextCursor == nil {
		t.Fatal("fixture did not sign actual cost lookahead", err)
	}
	for _, filters := range []AssetFilters{{Currency: "JPY"}, {Merchant: "Changed"}, {From: "2026-11-07"}, {To: "2026-11-07"}, {CategoryID: "category"}, {LocationID: "location"}, {Status: "active"}, {Scope: "direct"}} {
		if value, err := service.Page(t.Context(), fixture.token, *assets.NextCursor, 1, filters); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("changed effective Asset filters reused a continuation", value, err)
		}
	}
	if value, err := service.Page(t.Context(), fixture.token, *assets.NextCursor, 2, AssetFilters{}); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("changed page size reused an Asset continuation", value, err)
	}
	for _, cursor := range []string{*assets.NextCursor, *costs.NextCursor} {
		if value, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, cursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("different collection cursor authorized usage continuation", value, err)
		}
	}
	if value, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, *usage.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("usage cursor authorized a cost continuation", value, err)
	}
	if value, err := service.PageUsage(t.Context(), fixture.token, "another-asset", *usage.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("another Asset owner reused a usage continuation", value, err)
	}
	execSQL(t, fixture.store.db, `INSERT INTO app_settings VALUES('preferences.timezone','"Asia/Taipei"','legacy')`)
	if value, err := service.Page(t.Context(), fixture.token, *assets.NextCursor, 1, AssetFilters{}); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("changed resolved timezone reused global Asset cursor", value, err)
	}
	if _, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, *usage.NextCursor, 1); err != nil {
		t.Fatal("owned usage acquired an unrelated home-timezone dependency", err)
	}
	if _, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, *costs.NextCursor, 1); err != nil {
		t.Fatal("owned costs acquired an unrelated home-timezone dependency", err)
	}
}

func TestAssetPageContinuationsRemainLiveAcrossFinancialChangesAndFreshSamePrincipalLogin(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	first := seedAssetPageOwner(t, fixture, "a-owner")
	second := seedAssetPageOwner(t, fixture, "b-owner")
	page, err := service.Page(t.Context(), fixture.token, "", 1, AssetFilters{})
	if err != nil || page.NextCursor == nil || !reflect.DeepEqual(page.Data, []Asset{first}) {
		t.Fatal("actual first Asset page was not stable", page, err)
	}
	if _, err := service.RecordUsage(t.Context(), fixture.token, "asset-page-live-usage", second.ItemUnitID, second.Revision, usageInput(1)); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	last, err := service.Page(t.Context(), fresh, *page.NextCursor, 1, AssetFilters{Scope: "subtree"})
	second.Revision++
	if err != nil || last.NextCursor != nil || !reflect.DeepEqual(last.Data, []Asset{second}) {
		t.Fatal("live page was frozen to old revisions/session or equivalent default scope differed", last, err)
	}
	now := fixture.auth.now().Add(metadataCursorLifetime)
	fixture.auth.now = func() time.Time { return now }
	if value, err := service.Page(t.Context(), fresh, *page.NextCursor, 1, AssetFilters{}); !errors.Is(err, ErrInvalidCursor) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("expired Asset continuation was extended", value, err)
	}
}
