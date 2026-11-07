package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func purchaseAssetFixture(t *testing.T) (purchaseFixture, *Purchases, *Assets, Asset) {
	t.Helper()
	fixture, purchases := purchaseCommands(t)
	assets := assetReader(t, fixture.holdingFixture)
	profile, err := assets.PutProfile(t.Context(), fixture.token, "purchase-asset-opt-in", fixture.unit.ID, fixture.unit.Revision,
		AssetProfileWrite{ItemRevision: fixture.individual.Revision, CostBasis: "purchase", Currency: "JPY", UsageUnit: "times"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.individual, err = fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, purchases, assets, profile
}

func TestAssetPurchaseAllocationPublishesIndependentFinancialAuthorityWithActualSearchAndOriginalReply(t *testing.T) {
	fixture, purchases, assets, profile := purchaseAssetFixture(t)
	s := fixture.store
	setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 13)
	profile.Revision = 13
	before := assetProfileState(t, s)
	now := fixture.auth.now().Add(time.Minute)
	fixture.auth.now = func() time.Time { return now }
	input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID,
		QuantityBase: 1, PaidMinor: 345, Note: "Exact new acquisition é\n\x00"}
	var reply PurchaseLine
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-allocation", fixture.purchase.ID, fixture.purchase.Revision, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Revision != 1 || reply.ItemUnitID == nil || *reply.ItemUnitID != fixture.unit.ID || reply.PaidMinor != input.PaidMinor || reply.QuantityBase != 1 || reply.Note != input.Note {
		t.Fatal("asset allocation changed the whole amount, exact note or independent new Line proof", reply)
	}
	if len(changed) != 6 {
		t.Fatal("asset allocation omitted a financial/search identity or changed physical scope", changed)
	}
	for _, key := range []projectionKey{{"purchase", fixture.purchase.ID}, {"purchase_line", reply.ID}, {"asset", fixture.unit.ID}, {"item", fixture.individual.ID},
		{"search_doc", projectionPair("item", fixture.individual.ID)}, {"search_doc", projectionPair("unit", fixture.unit.ID)}} {
		if !changed[key] {
			t.Fatal("missing actual allocation dependency", key)
		}
	}
	current, err := assets.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || current.Revision != 14 || !reflect.DeepEqual(current.AssetData, profile.AssetData) {
		t.Fatal("allocation changed financial interpretation or failed to advance actual Asset authority", current, err)
	}
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil || item.Revision != fixture.individual.Revision+1 {
		t.Fatal("coalesced Item financial/search aggregate advanced more or less than once", item, err)
	}
	if scalar[string](t, s.db, "SELECT updated_at FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != stamp(now) ||
		scalar[string](t, s.db, "SELECT updated_at FROM items WHERE id=?", fixture.individual.ID) != stamp(now) {
		t.Fatal("actual financial aggregates retained obsolete timestamps")
	}
	if scalar[int](t, s.db, `SELECT COUNT(*) FROM purchase_lines l JOIN mutation_events e ON e.id=l.mutation_event_id
 WHERE l.id=? AND e.operation='post_purchases_id_lines' AND l.created_at=? AND l.note=?`, reply.ID, stamp(now), input.Note) != 1 {
		t.Fatal("allocation did not commit its actual immutable source/audit association")
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"purchases", "item_units", "locations", "stock_balances", "inventory_events", "asset_usage_events", "asset_cost_events", "asset_disposals", "asset_profile_changes", "media", "media_links", "ai_jobs"} {
		if before[table] != after[table] {
			t.Fatal("purchase allocation implicitly changed unrelated financial/physical/history source", table)
		}
	}
	summary, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z")
	if err != nil || summary.AcquisitionMinor != 120345 || summary.NetCostMinor != 120345 {
		t.Fatal("actual financial calculation multiplied retained quantity or missed the committed allocation", summary, err)
	}
}

func TestAssetPurchaseZeroAllocationsRemainDistinctHistoryAndOnlyOriginalKeysDeduplicate(t *testing.T) {
	fixture, purchases, assets, profile := purchaseAssetFixture(t)
	input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
	first, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-zero-first", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	original := input
	input.ItemRevision++
	second, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-zero-second", fixture.purchase.ID, fixture.purchase.Revision+1, input)
	if err != nil || second.ID == first.ID || second.Revision != 1 || first.Revision != 1 {
		t.Fatal("fresh reviewed zero allocation was deduplicated or reused a parent proof", second, err)
	}
	current, err := assets.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || current.Revision != profile.Revision+2 || !reflect.DeepEqual(current.AssetData, profile.AssetData) {
		t.Fatal("zero allocation failed to invalidate financial authority without changing interpretation", current, err)
	}
	before := assetProfileState(t, fixture.store)
	if frozen, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-zero-first", fixture.purchase.ID, fixture.purchase.Revision, original); err != nil || !reflect.DeepEqual(frozen, first) {
		t.Fatal("original zero allocation was rebased or appended again", frozen, err)
	}
	requireAssetProfileState(t, fixture.store, before)
	summary, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z")
	if err != nil || summary.AcquisitionMinor != 120000 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM purchase_lines WHERE item_unit_id=?", fixture.unit.ID) != 3 {
		t.Fatal("zero financial history was lost or counted as a per-unit amount", summary, err)
	}
}

func TestAssetPurchaseOriginalItemProofAlsoFreezesTheCurrentFinancialInterpretation(t *testing.T) {
	fixture, purchases, assets, profile := purchaseAssetFixture(t)
	if _, err := assets.RecordUsage(t.Context(), fixture.token, "purchase-asset-concurrent-usage", fixture.unit.ID, profile.Revision, usageInput(1)); err != nil {
		t.Fatal(err)
	}
	input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
	before := assetProfileState(t, fixture.store)
	if value, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-original-item", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrRevisionConflict) || value != (PurchaseLine{}) {
		t.Fatal("financial change bypassed original Item interpretation proof", value, err)
	}
	requireAssetProfileState(t, fixture.store, before)
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.ItemRevision = item.Revision
	if _, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-original-item", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal("explicitly reviewed current Item could not reuse the uncommitted key", err)
	}
}
