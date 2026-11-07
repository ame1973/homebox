package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func usageInput(delta int64) UsageWrite {
	return UsageWrite{Delta: delta, UsageUnit: "times", EventType: "use", OccurredAt: "2026-11-07T12:00:00.123456789+08:00"}
}

func costInput(amount int64) CostWrite {
	return CostWrite{AmountMinor: amount, Currency: "JPY", Kind: "repair", Direction: "expense", OccurredAt: "2026-11-07T12:00:00.123456789+08:00"}
}

func requireAssetEventState(t *testing.T, s *Store, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, assetProfileState(t, s)) {
		t.Fatal("failed event command changed financial/source/context/audit/projection/session state")
	}
}

func TestAssetEventsUsagePublishesIndependentEventAndOnlyActualFinancialOwnerScopes(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	service := assetReader(t, fixture)
	profile, err := service.PutProfile(t.Context(), fixture.token, "event-profile-opt-in", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(fixture.individual.Revision, 0))
	if err != nil {
		t.Fatal(err)
	}
	before := assetProfileState(t, s)
	now := fixture.auth.now().Add(time.Minute)
	fixture.auth.now = func() time.Time { return now }
	input := usageInput(2)
	input.Note = "  Exact é\n\x00 usage note  "
	var reply Usage
	changed := requireProjectionReplay(t, s, func() {
		reply, err = service.RecordUsage(t.Context(), fixture.token, "event-usage-published", fixture.unit.ID, profile.Revision, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Revision != 1 || reply.ID == "" || reply.ItemUnitID != fixture.unit.ID || reply.Delta != 2 || reply.EventType != "use" || reply.UsageUnit != "times" || reply.OccurredAt != "2026-11-07T04:00:00.123456789Z" || reply.Note != input.Note || reply.CorrectionOf != nil {
		t.Fatal("usage reply changed exact input, invented correction, or used the Asset next revision", reply)
	}
	if len(changed) != 3 || !changed[projectionKey{"usage", reply.ID}] || !changed[projectionKey{"asset", fixture.unit.ID}] || !changed[projectionKey{"item", fixture.individual.ID}] {
		t.Fatal("usage omitted financial authority or touched unchanged physical/search scope", changed)
	}
	if scalar[int](t, s.db, `SELECT COUNT(*) FROM asset_usage_events u JOIN mutation_events e ON e.id=u.mutation_event_id WHERE u.id=? AND e.operation='post_units_id_usage' AND u.idempotency_key IS NULL AND u.created_at=? AND e.note=?`, reply.ID, stamp(now), input.Note) != 1 {
		t.Fatal("new usage lost actual deferred audit, note, creation time or scoped-receipt ownership")
	}
	if scalar[string](t, s.db, "SELECT updated_at FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != stamp(now) || scalar[string](t, s.db, "SELECT updated_at FROM items WHERE id=?", fixture.individual.ID) != stamp(now) {
		t.Fatal("financial aggregates retained an obsolete update instant")
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"item_units", "locations", "stock_balances", "inventory_events", "purchases", "purchase_lines", "asset_cost_events", "asset_disposals", "asset_profile_changes", "search_documents", "unit_search_documents", "media", "media_links", "ai_jobs"} {
		if after[table] != before[table] {
			t.Fatal("usage invented unrelated physical/financial/search source", table)
		}
	}
	summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, input.OccurredAt)
	if err != nil || summary.TotalUsage != 2 || summary.NetCostMinor != 0 || summary.PerUseMinor == nil || *summary.PerUseMinor != 0 {
		t.Fatal("actual summary did not consume newly committed usage", summary, err)
	}
}

func TestAssetEventsCostPublishesExplicitCreditAndKeepsResidualSeparateFromProceeds(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 100)
	s := fixture.store
	service := assetReader(t, fixture)
	execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=50 WHERE item_unit_id=?", fixture.unit.ID)
	before := assetProfileState(t, s)
	input := costInput(200)
	input.Kind, input.Direction, input.Note = "refund", "credit", "Exact credit\n\x00"
	var reply Cost
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = service.RecordCost(t.Context(), fixture.token, "event-cost-published", fixture.unit.ID, asset.Revision, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Revision != 1 || reply.ItemUnitID != fixture.unit.ID || reply.AmountMinor != 200 || reply.Direction != "credit" || reply.Kind != "refund" || reply.Currency != "JPY" || reply.OccurredAt != "2026-11-07T04:00:00.123456789Z" || reply.Note != input.Note || reply.CorrectionOf != nil {
		t.Fatal("cost invented sign/conversion/reference or an aggregate-based event revision", reply)
	}
	if len(changed) != 3 || !changed[projectionKey{"cost", reply.ID}] || !changed[projectionKey{"asset", fixture.unit.ID}] || !changed[projectionKey{"item", fixture.individual.ID}] {
		t.Fatal("cost omitted actual financial owner or invented physical/search dependencies", changed)
	}
	if scalar[int](t, s.db, `SELECT COUNT(*) FROM asset_cost_events c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE c.id=? AND c.idempotency_key IS NULL AND e.operation='post_units_id_costs' AND e.note=?`, reply.ID, input.Note) != 1 {
		t.Fatal("new cost did not commit its actual immutable audit with a null legacy raw key")
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"item_units", "locations", "stock_balances", "inventory_events", "purchases", "purchase_lines", "asset_usage_events", "asset_disposals", "asset_profile_changes", "search_documents", "unit_search_documents"} {
		if after[table] != before[table] {
			t.Fatal("cost changed unrelated source", table)
		}
	}
	summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, input.OccurredAt)
	if err != nil || summary.ExtraMinor != -200 || summary.NetCostMinor != -100 || summary.RealizedProceedsMinor != 0 || summary.ProjectedNetCostMinor == nil || *summary.ProjectedNetCostMinor != -150 {
		t.Fatal("cost clamped negative net, counted a refund as proceeds or mixed residual/proceeds", summary, err)
	}
}

func TestAssetEventsReferencedUsageCorrectionPreservesExactOriginalAndNanosecondSteps(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	use, err := service.RecordUsage(t.Context(), fixture.token, "event-positive-usage", fixture.unit.ID, asset.Revision, usageInput(3))
	if err != nil {
		t.Fatal(err)
	}
	original := scalar[string](t, s.db, "SELECT json_array(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,created_at,correction_of,mutation_event_id) FROM asset_usage_events WHERE id=?", use.ID)
	input := usageInput(-3)
	input.EventType, input.CorrectionOf = "correction", &use.ID
	input.OccurredAt = "2026-11-07T04:00:00.123456788Z"
	before := assetProfileState(t, s)
	if value, err := service.RecordUsage(t.Context(), fixture.token, "event-backdated-step", fixture.unit.ID, asset.Revision+1, input); !errors.Is(err, ErrDomainState) || value != (Usage{}) {
		t.Fatal("backdated correction created a negative earlier nanosecond prefix", value, err)
	}
	requireAssetEventState(t, s, before)
	input.OccurredAt = "2026-11-07T05:00:00.123456789+01:00"
	correction, err := service.RecordUsage(t.Context(), fixture.token, "event-equal-instant", fixture.unit.ID, asset.Revision+1, input)
	if err != nil || correction.ID == use.ID || correction.Revision != 1 || correction.CorrectionOf == nil || *correction.CorrectionOf != use.ID || correction.Delta != -3 || correction.OccurredAt != use.OccurredAt {
		t.Fatal("equal instants did not form one exact step or correction replaced original", correction, err)
	}
	if scalar[string](t, s.db, "SELECT json_array(id,item_unit_id,occurred_at,delta,usage_unit,event_type,note,created_at,correction_of,mutation_event_id) FROM asset_usage_events WHERE id=?", use.ID) != original {
		t.Fatal("referenced correction rewrote original usage bytes")
	}
	summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, input.OccurredAt)
	if err != nil || summary.TotalUsage != 0 || summary.PerUseMinor != nil {
		t.Fatal("complete current usage ignored explicit correction or invented a divisor", summary, err)
	}
}

func TestAssetEventsReferencedCostAdjustmentAppendsWithoutEditingPostedSource(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	cost, err := service.RecordCost(t.Context(), fixture.token, "event-original-cost", fixture.unit.ID, asset.Revision, costInput(40))
	if err != nil {
		t.Fatal(err)
	}
	original := scalar[string](t, s.db, "SELECT json_array(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,created_at,correction_of,mutation_event_id) FROM asset_cost_events WHERE id=?", cost.ID)
	input := costInput(60)
	input.Kind, input.Direction, input.CorrectionOf = "adjustment", "credit", &cost.ID
	value, err := service.RecordCost(t.Context(), fixture.token, "event-adjusted-cost", fixture.unit.ID, asset.Revision+1, input)
	if err != nil || value.ID == cost.ID || value.Revision != 1 || value.CorrectionOf == nil || *value.CorrectionOf != cost.ID {
		t.Fatal("adjustment omitted its actual reference or replaced an event", value, err)
	}
	if scalar[string](t, s.db, "SELECT json_array(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,note,created_at,correction_of,mutation_event_id) FROM asset_cost_events WHERE id=?", cost.ID) != original {
		t.Fatal("adjustment rewrote exact posted cost")
	}
	summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, input.OccurredAt)
	if err != nil || summary.ExtraMinor != -20 || summary.NetCostMinor != -20 {
		t.Fatal("adjustment replaced rather than applied its signed magnitude", summary, err)
	}
}

func TestAssetEventsFreshKeysAppendButOriginalKeysAloneReconcile(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			var first, replay, second domainResult
			var err error
			if kind == "usage" {
				first, err = service.RecordUsage(t.Context(), fixture.token, "event-original-key", fixture.unit.ID, asset.Revision, usageInput(2))
			} else {
				first, err = service.RecordCost(t.Context(), fixture.token, "event-original-key", fixture.unit.ID, asset.Revision, costInput(2))
			}
			if err != nil {
				t.Fatal(err)
			}
			before := assetProfileState(t, s)
			if kind == "usage" {
				replay, err = service.RecordUsage(t.Context(), fixture.token, "event-original-key", fixture.unit.ID, asset.Revision, usageInput(2))
			} else {
				replay, err = service.RecordCost(t.Context(), fixture.token, "event-original-key", fixture.unit.ID, asset.Revision, costInput(2))
			}
			if err != nil || !reflect.DeepEqual(first, replay) {
				t.Fatal("original key did not return its exact independent event", err)
			}
			requireAssetEventState(t, s, before)
			if kind == "usage" {
				second, err = service.RecordUsage(t.Context(), fixture.token, "event-another-key", fixture.unit.ID, asset.Revision+1, usageInput(2))
			} else {
				second, err = service.RecordCost(t.Context(), fixture.token, "event-another-key", fixture.unit.ID, asset.Revision+1, costInput(2))
			}
			if err != nil || reflect.DeepEqual(first, second) || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_"+map[string]string{"usage": "usage_events", "cost": "cost_events"}[kind]) != 2 {
				t.Fatal("fresh reviewed key was incorrectly treated as an equal-state no-op", err)
			}
		})
	}
}

func TestAssetEventsUseIndependentAssetProofWithoutAnotherUnitOrItemPrecondition(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 7)
	setCatalogProjectionRevision(t, s, "unit", fixture.unit.ID, maxSafeInteger)
	setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, 51)
	for _, proof := range []int64{maxSafeInteger, 51, 6} {
		before := assetProfileState(t, s)
		if value, err := service.RecordUsage(t.Context(), fixture.token, "event-wrong-resource-proof", fixture.unit.ID, proof, usageInput(1)); !errors.Is(err, ErrRevisionConflict) || value != (Usage{}) {
			t.Fatal("event accepted a physical/owner/stale proof", proof, value, err)
		}
		requireAssetEventState(t, s, before)
	}
	value, err := service.RecordUsage(t.Context(), fixture.token, "event-actual-asset-proof", fixture.unit.ID, 7, usageInput(1))
	if err != nil || value.Revision != 1 || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID) != 8 || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='item' AND entity_id=?", fixture.individual.ID) != 52 || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID) != maxSafeInteger {
		t.Fatal("financial append invented an extra Unit proof, advanced Unit or coupled event revision", value, err)
	}
}
