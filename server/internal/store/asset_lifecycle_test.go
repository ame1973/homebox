package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func retirementInput(unitRevision int64) AssetRetireWrite {
	return AssetRetireWrite{UnitRevision: unitRevision, RetiredAt: "2026-11-07T12:00:00.123456789+08:00", Note: "Exact retirement\n\x00"}
}

func disposalInput(unitRevision, amount int64) AssetDisposalWrite {
	return AssetDisposalWrite{UnitRevision: unitRevision, Kind: "sold", DisposedAt: "2026-11-07T12:00:00.123456789+08:00", RealizedProceedsMinor: amount, Currency: "JPY", Note: "Exact disposal\n\x00"}
}

func TestAssetLifecycleRetiresWithIndependentProofsRetainedPlacementAndExactContext(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 100)
	s := fixture.store
	service := assetReader(t, fixture)
	setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 7)
	setCatalogProjectionRevision(t, s, "unit", fixture.unit.ID, 3)
	setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, 5)
	input := retirementInput(3)
	before := assetProfileState(t, s)
	var reply Unit
	var err error
	changed := requireProjectionReplay(t, s, func() {
		reply, err = service.Retire(t.Context(), fixture.token, "lifecycle-reviewed-retirement", fixture.unit.ID, 7, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Status != "retired" || reply.Revision != 4 || !sameParent(reply.LocationID, fixture.unit.LocationID) || reply.AssetCode != fixture.unit.AssetCode || reply.Notes != fixture.unit.Notes || !sameParent(reply.SerialNumber, fixture.unit.SerialNumber) {
		t.Fatal("retirement used Asset-plus-one, removed placement or changed physical metadata", reply)
	}
	asset, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || asset.Revision != 8 || asset.RetiredAt == nil || *asset.RetiredAt != "2026-11-07T04:00:00.123456789Z" || asset.InServiceAt != nil {
		t.Fatal("retirement omitted its financial aggregate or guessed/rounded service dates", asset, err)
	}
	for _, key := range []projectionKey{{"asset", fixture.unit.ID}, {"unit", fixture.unit.ID}, {"item", fixture.individual.ID}, {"location", "source"}, {"location", "root"}, {"search_doc", projectionPair("unit", fixture.unit.ID)}} {
		if !changed[key] {
			t.Fatal("retirement missed a real physical/financial dependency", key)
		}
	}
	if len(changed) != 7 {
		t.Fatal("retirement omitted its movement or touched unrelated scopes", changed)
	}
	if scalar[int](t, s.db, `SELECT COUNT(*) FROM inventory_events e JOIN mutation_events m ON m.id=e.mutation_event_id WHERE e.item_unit_id=? AND e.action='unit_status_retired' AND e.quantity_base=0 AND e.from_location_id IS NULL AND e.to_location_id IS NULL AND e.occurred_at=? AND e.note=? AND m.operation='post_units_id_retire'`, fixture.unit.ID, stamp(fixture.auth.now()), input.Note) != 1 {
		t.Fatal("retirement invented physical outflow, guessed movement time or lost exact audit association")
	}
	var context struct {
		Before         Asset     `json:"before"`
		After          Asset     `json:"after"`
		UnitBefore     Unit      `json:"unit_before"`
		UnitAfter      Unit      `json:"unit_after"`
		DisposalBefore *Disposal `json:"disposal_before"`
		DisposalAfter  *Disposal `json:"disposal_after"`
		Input          struct {
			ID       string           `json:"unit_id"`
			Revision int64            `json:"asset_revision"`
			Input    AssetRetireWrite `json:"input"`
		} `json:"input"`
	}
	encoded := scalar[string](t, s.db, `SELECT c.context_json FROM asset_profile_changes c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE e.operation='post_units_id_retire'`)
	if json.Unmarshal([]byte(encoded), &context) != nil || context.Before.Revision != 7 || context.Before.RetiredAt != nil || !reflect.DeepEqual(context.After, asset) || context.UnitBefore.Revision != 3 || context.UnitBefore.Status != "stored" || !reflect.DeepEqual(context.UnitAfter, reply) || context.DisposalBefore != nil || context.DisposalAfter != nil || context.Input.ID != fixture.unit.ID || context.Input.Revision != 7 || context.Input.Input != input {
		t.Fatal("retirement context lost original proof/date intent or exact physical/financial before/after images")
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"stock_balances", "purchases", "purchase_lines", "asset_usage_events", "asset_cost_events", "asset_disposals", "media", "media_links", "ai_jobs"} {
		if before[table] != after[table] {
			t.Fatal("retirement fabricated unrelated quantity, spend, proceeds or media source", table)
		}
	}
}

func TestAssetLifecycleDisposalPublishesIndependentProceedsWithoutGuessingRetirement(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 100)
	s := fixture.store
	service := assetReader(t, fixture)
	setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 13)
	setCatalogProjectionRevision(t, s, "unit", fixture.unit.ID, 4)
	execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=50 WHERE item_unit_id=?", fixture.unit.ID)
	input := disposalInput(4, 200)
	before := assetProfileState(t, s)
	var reply Disposal
	var err error
	changed := requireProjectionReplay(t, s, func() {
		reply, err = service.Dispose(t.Context(), fixture.token, "lifecycle-reviewed-disposal", fixture.unit.ID, 13, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.Revision != 1 || reply.ItemUnitID != fixture.unit.ID || reply.Kind != "sold" || reply.RealizedProceedsMinor != 200 || reply.Currency != "JPY" || reply.Note != input.Note || reply.DisposedAt != "2026-11-07T04:00:00.123456789Z" {
		t.Fatal("disposal changed explicit money/kind/date/note or used an aggregate revision", reply)
	}
	unit, err := loadUnit(t.Context(), s.db, fixture.unit.ID)
	if err != nil || unit.Status != "disposed" || unit.Revision != 5 || !sameParent(unit.LocationID, fixture.unit.LocationID) || unit.Notes != fixture.unit.Notes || !sameParent(unit.SerialNumber, fixture.unit.SerialNumber) {
		t.Fatal("disposal revived/moved a Unit or lost independently versioned exact metadata", unit, err)
	}
	asset, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || asset.Revision != 14 || asset.RetiredAt != nil || asset.InServiceAt != nil || asset.EstimatedResidualMinor == nil || *asset.EstimatedResidualMinor != 50 {
		t.Fatal("disposal guessed service/retirement dates or changed an estimated residual", asset, err)
	}
	if len(changed) != 8 || !changed[projectionKey{"disposal", fixture.unit.ID}] || !changed[projectionKey{"asset", fixture.unit.ID}] || !changed[projectionKey{"unit", fixture.unit.ID}] || !changed[projectionKey{"location", "source"}] || !changed[projectionKey{"location", "root"}] || !changed[projectionKey{"search_doc", projectionPair("unit", fixture.unit.ID)}] {
		t.Fatal("terminal physical/financial publication omitted dependencies or touched siblings", changed)
	}
	if scalar[int](t, s.db, `SELECT COUNT(*) FROM asset_disposals d JOIN inventory_events i ON i.mutation_event_id=d.mutation_event_id JOIN mutation_events e ON e.id=d.mutation_event_id JOIN asset_profile_changes c ON c.mutation_event_id=e.id WHERE d.item_unit_id=? AND i.item_unit_id=d.item_unit_id AND i.action='unit_dispose_sold' AND i.quantity_base=-1 AND i.from_location_id='source' AND i.to_location_id IS NULL AND i.note=? AND i.reason='sold' AND e.operation='post_units_id_disposal'`, fixture.unit.ID, input.Note) != 1 {
		t.Fatal("disposal lost its actual common audit/context or inferred the wrong physical leg")
	}
	var context assetLifecycleContext
	encoded := scalar[string](t, s.db, `SELECT c.context_json FROM asset_profile_changes c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE e.operation='post_units_id_disposal'`)
	if json.Unmarshal([]byte(encoded), &context) != nil || context.Before.Revision != 13 || !reflect.DeepEqual(context.After, asset) || context.UnitBefore.Status != "stored" || context.UnitBefore.Revision != 4 || !reflect.DeepEqual(context.UnitAfter, unit) || context.DisposalBefore != nil || context.DisposalAfter == nil || !reflect.DeepEqual(*context.DisposalAfter, reply) {
		t.Fatal("disposal omitted exact old/new physical/financial context or invented old history")
	}
	summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, input.DisposedAt)
	if err != nil || summary.RealizedProceedsMinor != 200 || summary.NetCostMinor != -100 || summary.ProjectedNetCostMinor == nil || *summary.ProjectedNetCostMinor != 50 || summary.ElapsedDays != nil {
		t.Fatal("disposal clamped net, mixed residual/proceeds or inferred retirement/service", summary, err)
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"stock_balances", "purchases", "purchase_lines", "asset_usage_events", "asset_cost_events", "media", "media_links", "ai_jobs"} {
		if before[table] != after[table] {
			t.Fatal("disposal changed unrelated source", table)
		}
	}
}

func TestAssetLifecycleHistoricalDisposedAdoptionDoesNotInventAnotherPhysicalRemoval(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	inventory, err := s.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := inventory.SetUnitStatus(t.Context(), fixture.token, "lifecycle-prior-physical-disposal", fixture.unit.ID, fixture.unit.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("discarded")})
	if err != nil {
		t.Fatal(err)
	}
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := assetReader(t, fixture)
	asset, err := service.PutProfile(t.Context(), fixture.token, "lifecycle-historical-profile", unit.ID, unit.Revision, manualProfileInput(item.Revision, 10))
	if err != nil {
		t.Fatal(err)
	}
	before := assetProfileState(t, s)
	input := disposalInput(unit.Revision, 0)
	input.Kind = "discarded"
	changed := requireProjectionReplay(t, s, func() {
		if value, err := service.Dispose(t.Context(), fixture.token, "lifecycle-historical-finance", unit.ID, asset.Revision, input); err != nil || value.Revision != 1 || value.Kind != "discarded" {
			t.Fatal("explicit historical disposal could not adopt its real financial source", value, err)
		}
	})
	if len(changed) != 3 || !changed[projectionKey{"disposal", unit.ID}] || !changed[projectionKey{"asset", unit.ID}] || !changed[projectionKey{"item", item.ID}] {
		t.Fatal("historical financial adoption invented another physical/search/path change", changed)
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"item_units", "locations", "inventory_events", "stock_balances", "search_documents", "unit_search_documents"} {
		if before[table] != after[table] {
			t.Fatal("historical financial adoption invented an actual physical transition", table)
		}
	}
}

func TestAssetLifecycleEqualRetirementAndDisposalAreAuditedReceiptsOnly(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			var original domainResult
			var err error
			if kind == "retire" {
				original, err = service.Retire(t.Context(), fixture.token, "lifecycle-first-original", fixture.unit.ID, asset.Revision, retirementInput(fixture.unit.Revision))
			} else {
				original, err = service.Dispose(t.Context(), fixture.token, "lifecycle-first-original", fixture.unit.ID, asset.Revision, disposalInput(fixture.unit.Revision, 0))
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil {
				t.Fatal(err)
			}
			unit, err := loadUnit(t.Context(), s.db, fixture.unit.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := assetProfileState(t, s)
			changed := requireProjectionReplay(t, s, func() {
				var value domainResult
				if kind == "retire" {
					input := retirementInput(unit.Revision)
					input.RetiredAt = "2026-11-07T04:00:00.123456789Z"
					value, err = service.Retire(t.Context(), fixture.token, "lifecycle-equal-reviewed", unit.ID, current.Revision, input)
				} else {
					input := disposalInput(unit.Revision, 0)
					input.DisposedAt = "2026-11-07T04:00:00.123456789Z"
					value, err = service.Dispose(t.Context(), fixture.token, "lifecycle-equal-reviewed", unit.ID, current.Revision, input)
				}
				if err != nil || !reflect.DeepEqual(value, original) {
					t.Fatal("equal reviewed lifecycle did not preserve independent original resource", value, err)
				}
			})
			if len(changed) != 0 {
				t.Fatal("equal eligible lifecycle bumped projections", changed)
			}
			after := assetProfileState(t, s)
			for _, table := range []string{"items", "item_units", "asset_profiles", "asset_disposals", "inventory_events", "projection_revisions", "current_projections", "sync_changes", "data_revision"} {
				if before[table] != after[table] {
					t.Fatal("equal lifecycle changed immutable source/timestamps instead of only audit/receipt", table)
				}
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes") != 2 {
				t.Fatal("equal lifecycle lost exact fresh-key audit context")
			}
		})
	}
}

func TestAssetLifecycleNeverOverwritesRetirementOrDisposalOrRevivesATerminalUnit(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	retired, err := service.Retire(t.Context(), fixture.token, "lifecycle-before-history-guard", fixture.unit.ID, asset.Revision, retirementInput(fixture.unit.Revision))
	if err != nil {
		t.Fatal(err)
	}
	input := retirementInput(retired.Revision)
	input.RetiredAt = "2026-11-08T04:00:00Z"
	before := assetProfileState(t, s)
	if value, err := service.Retire(t.Context(), fixture.token, "lifecycle-no-retirement-edit", fixture.unit.ID, asset.Revision+1, input); !errors.Is(err, ErrAssetHistory) || value != (Unit{}) {
		t.Fatal("ordinary retirement rewrote historical end of service", value, err)
	}
	requireAssetEventState(t, s, before)
	disposal := disposalInput(retired.Revision, 10)
	if _, err = service.Dispose(t.Context(), fixture.token, "lifecycle-terminal-record", fixture.unit.ID, asset.Revision+1, disposal); err != nil {
		t.Fatal(err)
	}
	unit, err := loadUnit(t.Context(), s.db, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	before = assetProfileState(t, s)
	if value, err := service.Retire(t.Context(), fixture.token, "lifecycle-no-terminal-revival", fixture.unit.ID, asset.Revision+2, retirementInput(unit.Revision)); !errors.Is(err, ErrUnitDisposed) || value != (Unit{}) {
		t.Fatal("retirement revived a terminal Unit", value, err)
	}
	requireAssetEventState(t, s, before)
	disposal.UnitRevision, disposal.RealizedProceedsMinor = unit.Revision, 11
	if value, err := service.Dispose(t.Context(), fixture.token, "lifecycle-no-disposal-edit", fixture.unit.ID, asset.Revision+2, disposal); !errors.Is(err, ErrAssetHistory) || value != (Disposal{}) {
		t.Fatal("ordinary disposal overwrote immutable proceeds", value, err)
	}
	requireAssetEventState(t, s, before)
}
