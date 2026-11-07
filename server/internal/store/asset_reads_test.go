package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestAssetPagesReadExactRetainedSourceIndependentCountersAndPlainCompleteEnvelopes(t *testing.T) {
	fixture := assetsFixture(t, true)
	s := fixture.store
	service := assetReader(t, fixture.holdingFixture)
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{}' WHERE entity_type IN('asset','usage','cost')")
	before := assetProfileState(t, s)
	assets, err := service.Page(t.Context(), fixture.token, "", 50, AssetFilters{})
	if err != nil || !reflect.DeepEqual(assets.Data, []Asset{fixture.asset}) || assets.NextCursor != nil {
		t.Fatal("Asset page copied cache, mixed counters or invented a continuation", assets, err)
	}
	usage, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || !reflect.DeepEqual(usage.Data, []Usage{fixture.usage}) || usage.NextCursor != nil {
		t.Fatal("usage page changed exact retained instants/provenance/notes or independent proof", usage, err)
	}
	costs, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || !reflect.DeepEqual(costs.Data, []Cost{fixture.cost}) || costs.NextCursor != nil {
		t.Fatal("cost page repriced retained source or mixed event/parent proof", costs, err)
	}
	for _, value := range []any{assets, usage, costs} {
		encoded, err := json.Marshal(value)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(encoded, &fields) != nil || len(fields) != 2 || string(fields["next_cursor"]) != "null" || fields["data"] == nil {
			t.Fatal("collection omitted its required nullable continuation or invented a snapshot/ETag field", err)
		}
	}
	requireAssetProfileState(t, s, before)
}

func TestAssetPagesDistinguishCompleteEmptyCollectionsFromMissingActualOwner(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	page, err := service.Page(t.Context(), fixture.token, "", 50, AssetFilters{})
	if err != nil || page.Data == nil || len(page.Data) != 0 || page.NextCursor != nil {
		t.Fatal("empty actual profile collection was invented or encoded as missing", page, err)
	}
	if value, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 50); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("Unit without an actual Asset was treated as an owned usage collection", value, err)
	}
	if value, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 50); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("Unit without an actual Asset was treated as an owned cost collection", value, err)
	}
	if _, err := service.PutProfile(t.Context(), fixture.token, "pages-real-profile", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(fixture.individual.Revision, 0)); err != nil {
		t.Fatal(err)
	}
	usage, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || usage.Data == nil || len(usage.Data) != 0 || usage.NextCursor != nil {
		t.Fatal("fully read empty usage history was not an explicit empty collection", usage, err)
	}
	costs, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || costs.Data == nil || len(costs.Data) != 0 || costs.NextCursor != nil {
		t.Fatal("fully read empty cost history was not an explicit empty collection", costs, err)
	}
}

func TestAssetPagesRetainReadableHistoryWithoutNewMoneyOrAggregateEligibilityPolicy(t *testing.T) {
	fixture := assetsFixture(t, false)
	s := fixture.store
	service := assetReader(t, fixture.holdingFixture)
	execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
	execSQL(t, s.db, "DROP TRIGGER asset_usage_events_no_update")
	execSQL(t, s.db, "UPDATE asset_usage_events SET delta=-1,usage_unit='minutes' WHERE id=?", fixture.usage.ID)
	execSQL(t, s.db, "DROP TRIGGER asset_cost_events_no_update")
	execSQL(t, s.db, "UPDATE asset_cost_events SET currency='XAU',kind='adjustment',direction='credit' WHERE id=?", fixture.cost.ID)
	fixture.asset.Currency = "ZZZ"
	fixture.usage.Delta, fixture.usage.UsageUnit = -1, "minutes"
	fixture.cost.Currency, fixture.cost.Kind, fixture.cost.Direction = "XAU", "adjustment", "credit"
	before := assetProfileState(t, s)
	page, err := service.Page(t.Context(), fixture.token, "", 50, AssetFilters{Currency: "ZZZ", Merchant: fixture.purchase.MerchantName})
	if err != nil || !reflect.DeepEqual(page.Data, []Asset{fixture.asset}) {
		t.Fatal("profile currency was treated as purchase currency/new-money policy or financial totals became list authority", page, err)
	}
	usage, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || !reflect.DeepEqual(usage.Data, []Usage{fixture.usage}) {
		t.Fatal("retained usage was rewritten to new positive-use/current-interpretation rules", usage, err)
	}
	costs, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || !reflect.DeepEqual(costs.Data, []Cost{fixture.cost}) {
		t.Fatal("retained currency or unknown historical adjustment provenance was repriced/invented", costs, err)
	}
	requireAssetProfileState(t, s, before)
}

func TestAssetPagesReadActualNewImmutableEventsAndReferencesWithoutPublishingAgain(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	profile, err := service.PutProfile(t.Context(), fixture.token, "pages-event-profile", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(fixture.individual.Revision, 0))
	if err != nil {
		t.Fatal(err)
	}
	use, err := service.RecordUsage(t.Context(), fixture.token, "pages-event-use", fixture.unit.ID, profile.Revision, usageInput(3))
	if err != nil {
		t.Fatal(err)
	}
	correction := usageInput(-1)
	correction.EventType, correction.CorrectionOf = "correction", &use.ID
	fixed, err := service.RecordUsage(t.Context(), fixture.token, "pages-event-correction", fixture.unit.ID, profile.Revision+1, correction)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := service.RecordCost(t.Context(), fixture.token, "pages-event-cost", fixture.unit.ID, profile.Revision+2, costInput(5))
	if err != nil {
		t.Fatal(err)
	}
	before := assetProfileState(t, fixture.store)
	usage, err := service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || len(usage.Data) != 2 || usage.NextCursor != nil {
		t.Fatal("actual immutable history was omitted", usage, err)
	}
	byID := make(map[string]Usage)
	for _, value := range usage.Data {
		byID[value.ID] = value
	}
	if !reflect.DeepEqual(byID[use.ID], use) || !reflect.DeepEqual(byID[fixed.ID], fixed) || usage.Data[0].ID >= usage.Data[1].ID {
		t.Fatal("owned page lost original references or used date/counter ordering")
	}
	costs, err := service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 50)
	if err != nil || !reflect.DeepEqual(costs.Data, []Cost{cost}) {
		t.Fatal("actual posted cost was replaced by a projection or total", costs, err)
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestAssetPagesPreserveImportedAndMaximumUnicodeControlIdentitiesThroughSignedCursors(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	ids := []string{"new", "tree", strings.Repeat("😀", 128), strings.Repeat("\x00", 128)}
	for _, id := range ids {
		seedAssetPageOwner(t, fixture, id)
		insertAssetUsage(t, fixture.store, id, "new", "2026-11-07T00:00:00Z", 1, nil)
		insertAssetCost(t, fixture.store, id, "new", 1, "expense")
	}
	slices.Sort(ids)
	if got := allAssetPageIDs(t, service, fixture.token, AssetFilters{}, 1); !reflect.DeepEqual(got, ids) {
		t.Fatal("imported/wide/control Asset identity was reserved, normalized or lost in a cursor", got)
	}
	var usageIDs, costIDs []string
	cursor := ""
	for {
		page, err := service.PageUsage(t.Context(), fixture.token, "new", cursor, 1)
		if err != nil || len(page.Data) != 1 {
			t.Fatal("wide/control usage continuation lost actual source", page, err)
		}
		usageIDs = append(usageIDs, page.Data[0].ID)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	cursor = ""
	for {
		page, err := service.PageCosts(t.Context(), fixture.token, "new", cursor, 1)
		if err != nil || len(page.Data) != 1 {
			t.Fatal("wide/control cost continuation lost actual source", page, err)
		}
		costIDs = append(costIDs, page.Data[0].ID)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if !reflect.DeepEqual(usageIDs, ids) || !reflect.DeepEqual(costIDs, ids) {
		t.Fatal("owned event identity or signed byte spelling changed", usageIDs, costIDs)
	}
}
