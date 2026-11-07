package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestAssetProfileValidatesWholeActualFinancialInterpretationBeforeAnySourceChange(t *testing.T) {
	for _, conflict := range []string{"manual purchase", "currency", "usage", "residual overflow", "negative usage prefix"} {
		t.Run(conflict, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s := fixture.store
			input := retainedProfileInput(fixture.individual.Revision, fixture.asset)
			want := ErrDomainState
			switch conflict {
			case "manual purchase":
				input.CostBasis, input.ManualAcquisitionMinor = "manual", testPointer(int64(0))
			case "currency":
				input.Currency = "USD"
			case "usage":
				input.UsageUnit = "cycles"
			case "residual overflow":
				insertAssetCost(t, s, "large-credit", fixture.unit.ID, maxSafeInteger, "credit")
				input.EstimatedResidualMinor = testPointer(maxSafeInteger)
				want = ErrAssetOverflow
			case "negative usage prefix":
				insertAssetUsage(t, s, "old-negative", fixture.unit.ID, "2026-11-05T00:00:00Z", -1, &fixture.usage.ID)
				input.InServiceAt = nil
			}
			before := assetProfileState(t, s)
			if value, err := assetReader(t, fixture.holdingFixture).PutProfile(t.Context(), fixture.token, "profile-actual-eligibility", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, want) || value != (Asset{}) {
				t.Fatal("proposed profile bypassed complete actual currency/basis/usage/safe-total eligibility", conflict, value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetProfileZeroPaidHistoryLocksManualBasisAndSupportsOnlyExplicitAuditedCorrection(t *testing.T) {
	fixture, original := manualAssetFixture(t, 0)
	s := fixture.store
	execSQL(t, s.db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,created_at) VALUES('zero-header','Zero','2026-11-07T00:00:00Z','JPY','legacy')")
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('zero-line','zero-header',?,?,1,0)", fixture.individual.ID, fixture.unit.ID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase','zero-header',1),('purchase_line','zero-line',1)")
	input := retainedProfileInput(fixture.individual.Revision, original)
	before := assetProfileState(t, s)
	service := assetReader(t, fixture)
	if value, err := service.PutProfile(t.Context(), fixture.token, "profile-zero-reference", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrDomainState) || value != (Asset{}) {
		t.Fatal("zero-paid reference was mistaken for absence and accepted manual acquisition", value, err)
	}
	requireAssetProfileState(t, s, before)
	input.CostBasis, input.ManualAcquisitionMinor = "purchase", nil
	reply, err := service.PutProfile(t.Context(), fixture.token, "profile-explicit-basis-correction", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil || reply.CostBasis != "purchase" || reply.ManualAcquisitionMinor != nil || reply.Revision != original.Revision+1 {
		t.Fatal("explicit audited basis correction did not preserve zero-paid source", reply, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_lines WHERE paid_minor=0 AND id='zero-line'") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes") != 1 {
		t.Fatal("basis correction erased zero history or skipped its exact before-image")
	}
}

func TestAssetProfileNeverReseedsMissingOwnRelatedOrRetainedCounterOnlyAuthority(t *testing.T) {
	for _, scope := range []string{"asset", "unit", "item", "purchase", "purchase_line", "cost", "usage", "disposal"} {
		t.Run(scope, func(t *testing.T) {
			fixture := assetsFixture(t, true)
			s := fixture.store
			id := fixture.unit.ID
			switch scope {
			case "item":
				id = fixture.individual.ID
			case "purchase":
				id = fixture.purchase.ID
			case "purchase_line":
				id = fixture.unitLine.ID
			case "cost":
				id = fixture.cost.ID
			case "usage":
				id = fixture.usage.ID
			}
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", scope, id)
			execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", scope, id)
			input := retainedProfileInput(fixture.individual.Revision, fixture.asset)
			input.EstimatedResidualMinor = testPointer(int64(30000))
			before := assetProfileState(t, s)
			if value, err := assetReader(t, fixture.holdingFixture).PutProfile(t.Context(), fixture.token, "profile-missing-authority", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrDomainState) || value != (Asset{}) {
				t.Fatal("missing real financial/owner authority was repaired or accepted", scope, value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
	fixture := holdingsFixture(t)
	execSQL(t, fixture.store.db, "INSERT INTO projection_revisions VALUES('asset',?,7)", fixture.unit.ID)
	before := assetProfileState(t, fixture.store)
	if value, err := assetReader(t, fixture).PutProfile(t.Context(), fixture.token, "profile-counter-only", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(fixture.individual.Revision, 0)); !errors.Is(err, ErrDomainState) || value != (Asset{}) {
		t.Fatal("counter-only Asset identity became a new revision-one profile", value, err)
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestAssetProfileNewOptInRejectsOrphanHistoryAndFailedAbsenceQueries(t *testing.T) {
	for _, defect := range []string{"orphan", "query failure"} {
		t.Run(defect, func(t *testing.T) {
			fixture := holdingsFixture(t)
			s := fixture.store
			before := assetProfileState(t, s)
			execSQL(t, s.db, "ALTER TABLE asset_usage_events RENAME TO saved_profile_history_source")
			owner := "'unit'"
			if defect == "query failure" {
				owner = "json_extract('invalid JSON','$')"
			}
			execSQL(t, s.db, `CREATE VIEW asset_usage_events AS SELECT 'orphan' AS id,`+owner+` AS item_unit_id,
 '2026-11-07T00:00:00Z' AS occurred_at,1 AS delta,'times' AS usage_unit,'use' AS event_type,'' AS note,
 NULL AS idempotency_key,'legacy' AS created_at,NULL AS correction_of,NULL AS mutation_event_id`)
			restored := false
			restore := func() {
				if !restored {
					execSQL(t, s.db, "DROP VIEW asset_usage_events; ALTER TABLE saved_profile_history_source RENAME TO asset_usage_events")
					restored = true
				}
			}
			defer restore()
			value, err := assetReader(t, fixture).PutProfile(t.Context(), fixture.token, "profile-no-orphan-adoption", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(fixture.individual.Revision, 0))
			if err == nil || defect == "orphan" && !errors.Is(err, ErrDomainState) || value != (Asset{}) {
				t.Fatal("opt-in laundered unknown orphan history or treated an unsuccessful query as absence", defect, value, err)
			}
			restore()
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetProfileNoopAtSafeLimitsAndEquivalentRetirementRemainExact(t *testing.T) {
	fixture, original := manualAssetFixture(t, 0)
	s := fixture.store
	instant := "2026-11-07T12:00:00.123456789+08:00"
	execSQL(t, s.db, "UPDATE asset_profiles SET retired_at=? WHERE item_unit_id=?", instant, fixture.unit.ID)
	original.RetiredAt = &instant
	original.Revision = maxSafeInteger
	setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, maxSafeInteger)
	setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, maxSafeInteger)
	execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
	execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
	input := retainedProfileInput(maxSafeInteger, original)
	input.RetiredAt = testPointer("2026-11-07T04:00:00.123456789Z")
	before := assetProfileState(t, s)
	reply, err := assetReader(t, fixture).PutProfile(t.Context(), fixture.token, "profile-noop-safe-limits", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil || !reflect.DeepEqual(reply, original) {
		t.Fatal("eligible no-op overflowed counters or rewrote an equal retained retirement instant", reply, err)
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"asset_profiles", "items", "item_units", "projection_revisions", "current_projections", "sync_changes", "data_revision", "sqlite_sequence"} {
		if after[table] != before[table] {
			t.Fatal("safe-limit no-op changed a source/counter/projection", table)
		}
	}
}

func TestAssetProfilePresenceGuardsPhysicalLifecycleAndManualAcquisition(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	if _, err := service.PutProfile(t.Context(), fixture.token, "profile-guard-opt-in", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(fixture.individual.Revision, 0)); err != nil {
		t.Fatal(err)
	}
	inventory, err := fixture.store.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	before := assetProfileState(t, fixture.store)
	if value, err := inventory.DeleteUnit(t.Context(), fixture.token, "profile-guard-delete", fixture.unit.ID, fixture.unit.Revision); !errors.Is(err, ErrUnitHistory) || value != (ActionAck{}) {
		t.Fatal("unchanged Unit proof bypassed new actual financial history", value, err)
	}
	if value, err := inventory.SetUnitStatus(t.Context(), fixture.token, "profile-guard-retire", fixture.unit.ID, fixture.unit.Revision, UnitStatus{Status: "retired"}); !errors.Is(err, ErrUnitAssetLifecycle) || value != (Unit{}) {
		t.Fatal("ordinary status bypassed the dedicated financial lifecycle", value, err)
	}
	requireAssetProfileState(t, fixture.store, before)
	purchases, err := NewPurchases(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	purchase := createPurchaseCommand(t, purchases, fixture.token, "profile-manual-purchase", "JPY")
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	before = assetProfileState(t, fixture.store)
	if value, err := purchases.CreateLine(t.Context(), fixture.token, "profile-manual-line", purchase.ID, purchase.Revision, PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}); !errors.Is(err, ErrPurchaseAssetBasis) || value != (PurchaseLine{}) {
		t.Fatal("manual zero basis implicitly gained a second acquisition interpretation", value, err)
	}
	requireAssetProfileState(t, fixture.store, before)
}
