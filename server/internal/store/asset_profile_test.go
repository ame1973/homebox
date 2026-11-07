package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func manualProfileInput(itemRevision, amount int64) AssetProfileWrite {
	return AssetProfileWrite{ItemRevision: itemRevision, CostBasis: "manual", Currency: "JPY", ManualAcquisitionMinor: &amount, UsageUnit: "times"}
}

func retainedProfileInput(itemRevision int64, asset Asset) AssetProfileWrite {
	return AssetProfileWrite{ItemRevision: itemRevision, AssetRevision: snapshotAssetNumber(&asset.Revision), CostBasis: asset.CostBasis,
		Currency: asset.Currency, ManualAcquisitionMinor: snapshotAssetNumber(asset.ManualAcquisitionMinor),
		InServiceAt: snapshotPurchaseString(asset.InServiceAt), RetiredAt: snapshotPurchaseString(asset.RetiredAt),
		EstimatedResidualMinor: snapshotAssetNumber(asset.EstimatedResidualMinor), UsageUnit: asset.UsageUnit}
}

func assetProfileState(t *testing.T, s *Store) map[string]string {
	t.Helper()
	state := purchaseCommandState(t, s)
	state["asset_profile_changes"] = scalar[string](t, s.db, `SELECT json_group_array(json_array(mutation_event_id,item_unit_id,context_json)) FROM (SELECT * FROM asset_profile_changes ORDER BY mutation_event_id)`)
	return state
}

func requireAssetProfileState(t *testing.T, s *Store, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, assetProfileState(t, s)) {
		t.Fatal("failed profile command changed financial/source/audit/projection/session state")
	}
}

func TestAssetProfileCreatesExplicitManualZeroWithIndependentAssetAndOwnerOnlyPublication(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	service := assetReader(t, fixture)
	input := manualProfileInput(fixture.individual.Revision, 0)
	before := assetProfileState(t, s)
	var reply Asset
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = service.PutProfile(t.Context(), fixture.token, "profile-explicit-zero", fixture.unit.ID, fixture.unit.Revision, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Revision != 1 || reply.ItemUnitID != fixture.unit.ID || reply.CostBasis != "manual" || reply.ManualAcquisitionMinor == nil || *reply.ManualAcquisitionMinor != 0 || reply.InServiceAt != nil || reply.RetiredAt != nil || reply.EstimatedResidualMinor != nil {
		t.Fatal("profile invented money/service/residual or used a parent-plus-one counter", reply)
	}
	if len(changed) != 2 || !changed[projectionKey{"asset", fixture.unit.ID}] || !changed[projectionKey{"item", fixture.individual.ID}] {
		t.Fatal("financial opt-in omitted its owner or invented physical/search dependencies", changed)
	}
	if scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID) != fixture.unit.Revision {
		t.Fatal("unchanged physical Unit counter advanced with Asset opt-in")
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"item_units", "stock_balances", "inventory_events", "purchases", "purchase_lines", "asset_usage_events", "asset_cost_events", "asset_disposals", "locations", "search_documents", "unit_search_documents", "media", "media_links", "ai_jobs"} {
		if after[table] != before[table] {
			t.Fatal("profile opt-in invented unrelated physical/financial/search/AI source", table)
		}
	}
	var context struct {
		Before *Asset              `json:"before"`
		After  Asset               `json:"after"`
		Input  assetProfileCommand `json:"input"`
	}
	encoded := scalar[string](t, s.db, "SELECT context_json FROM asset_profile_changes WHERE item_unit_id=?", fixture.unit.ID)
	if json.Unmarshal([]byte(encoded), &context) != nil || context.Before != nil || !reflect.DeepEqual(context.After, reply) || !reflect.DeepEqual(context.Input, assetProfileCommand{fixture.unit.ID, fixture.unit.Revision, input}) {
		t.Fatal("creation context lost reviewed absence, exact after-image, original proofs or complete nullable intent")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE c.item_unit_id=? AND e.operation='put_units_id_asset'", fixture.unit.ID) != 1 {
		t.Fatal("profile context did not commit its actual mutation event")
	}
	if scalar[string](t, s.db, "SELECT created_at FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != stamp(fixture.auth.now()) || scalar[string](t, s.db, "SELECT updated_at FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != stamp(fixture.auth.now()) {
		t.Fatal("new profile invented an old creation/update instant")
	}
}

func TestAssetProfileAdoptsExactUnitPurchaseBasisWithoutNewReceiptOrQuantityMultiplication(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	service := assetReader(t, fixture.holdingFixture)
	input := AssetProfileWrite{ItemRevision: fixture.individual.Revision, CostBasis: "purchase", Currency: "JPY", InServiceAt: testPointer("2024-11-07T12:00:00.123456789+08:00"), UsageUnit: "times"}
	before := assetProfileState(t, s)
	reply, err := service.PutProfile(t.Context(), fixture.token, "profile-purchase-basis", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil || reply.ManualAcquisitionMinor != nil || reply.InServiceAt == nil || *reply.InServiceAt != "2024-11-07T04:00:00.123456789Z" || reply.Revision != 1 {
		t.Fatal("purchase profile inferred fallback/date or lost nanoseconds", reply, err)
	}
	summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-06T04:00:00Z")
	if err != nil || summary.AcquisitionMinor != 120000 || summary.TotalUsage != 0 || summary.ElapsedDays == nil || *summary.ElapsedDays != 730 {
		t.Fatal("explicit profile adoption counted retained quantity/header instead of each allocated whole amount", summary, err)
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"purchases", "purchase_lines", "item_units", "stock_balances", "inventory_events"} {
		if after[table] != before[table] {
			t.Fatal("financial opt-in received stock, moved Units or rewrote allocation", table)
		}
	}
}

func TestAssetProfileReplacementPreservesBeforeImageRequestedSpellingAndIndependentScopes(t *testing.T) {
	fixture := assetsFixture(t, true)
	s := fixture.store
	service := assetReader(t, fixture.holdingFixture)
	input := retainedProfileInput(fixture.individual.Revision, fixture.asset)
	input.InServiceAt = testPointer("2024-11-07T04:00:00.123456789Z")
	input.EstimatedResidualMinor = testPointer(int64(25000))
	originalInput, _ := json.Marshal(input)
	before := assetProfileState(t, s)
	reply, err := service.PutProfile(t.Context(), fixture.token, "profile-exact-replacement", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil || reply.Revision != fixture.asset.Revision+1 || reply.InServiceAt == nil || *reply.InServiceAt != *fixture.asset.InServiceAt || reply.EstimatedResidualMinor == nil || *reply.EstimatedResidualMinor != 25000 {
		t.Fatal("replacement canonicalized an equal retained instant, lost proposed residual or used Unit-plus-one", reply, err)
	}
	if actual, _ := json.Marshal(input); string(actual) != string(originalInput) {
		t.Fatal("profile writer mutated the caller's original complete command")
	}
	var context struct {
		Before *Asset              `json:"before"`
		After  Asset               `json:"after"`
		Input  assetProfileCommand `json:"input"`
	}
	if json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT context_json FROM asset_profile_changes WHERE item_unit_id=?", fixture.unit.ID)), &context) != nil || context.Before == nil || !reflect.DeepEqual(*context.Before, fixture.asset) || !reflect.DeepEqual(context.After, reply) || !reflect.DeepEqual(context.Input.Input, input) {
		t.Fatal("financial correction lost the exact retained before/after/requested context")
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"item_units", "locations", "asset_usage_events", "asset_cost_events", "asset_disposals", "purchase_lines", "inventory_events", "search_documents", "unit_search_documents"} {
		if after[table] != before[table] {
			t.Fatal("financial profile correction changed unrelated immutable/physical/search source", table)
		}
	}
	if scalar[string](t, s.db, "SELECT created_at FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != "legacy-created" {
		t.Fatal("profile replacement rewrote unknown original creation metadata")
	}
}

func TestAssetProfileEqualNewKeyIsAuditedReceiptWithoutProjectionOrSourceChange(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	input := retainedProfileInput(fixture.individual.Revision, asset)
	before := assetProfileState(t, s)
	reply, err := service.PutProfile(t.Context(), fixture.token, "profile-equal-new-key", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil || !reflect.DeepEqual(reply, asset) {
		t.Fatal("equal reviewed profile did not retain its resource/proof", reply, err)
	}
	after := assetProfileState(t, s)
	for table, prior := range before {
		if table != "asset_profile_changes" && table != "mutation_events" && table != "api_idempotency" && after[table] != prior {
			t.Fatal("equal profile bumped or rewrote authoritative/derived state", table)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes") != 1 {
		t.Fatal("no-op intent did not receive its own immutable context")
	}
	before = after
	if replay, err := service.PutProfile(t.Context(), fixture.token, "profile-equal-new-key", fixture.unit.ID, fixture.unit.Revision, input); err != nil || !reflect.DeepEqual(replay, reply) {
		t.Fatal("equal profile original-key reconciliation failed", err)
	}
	requireAssetProfileState(t, s, before)
}

func TestAssetProfileFrozenOriginalReplySurvivesLaterSourceAndFreshLogin(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	service := assetReader(t, fixture)
	input := manualProfileInput(fixture.individual.Revision, 100)
	original, err := service.PutProfile(t.Context(), fixture.token, "profile-original-key", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := retainedProfileInput(item.Revision, original)
	changed.ManualAcquisitionMinor = testPointer(int64(200))
	if _, err = service.PutProfile(t.Context(), fixture.token, "profile-later-key", fixture.unit.ID, fixture.unit.Revision, changed); err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, fixture.auth)
	before := assetProfileState(t, s)
	if reply, err := service.PutProfile(t.Context(), fresh, "profile-original-key", fixture.unit.ID, fixture.unit.Revision, input); err != nil || !reflect.DeepEqual(reply, original) {
		t.Fatal("frozen original profile was rebound to later source or the old session", reply, err)
	}
	requireAssetProfileState(t, s, before)
	input.ManualAcquisitionMinor = testPointer(int64(101))
	if value, err := service.PutProfile(t.Context(), fresh, "profile-original-key", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrIdempotencyConflict) || value != (Asset{}) {
		t.Fatal("changed profile input reused an original committed key", value, err)
	}
	requireAssetProfileState(t, s, before)
}
