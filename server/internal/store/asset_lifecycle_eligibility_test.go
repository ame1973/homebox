package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestAssetLifecycleCanCompleteUnknownRetirementWithoutAnotherPhysicalTransition(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	inventory, err := s.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := inventory.SetUnitStatus(t.Context(), fixture.token, "lifecycle-old-retired-status", fixture.unit.ID, fixture.unit.Revision, UnitStatus{Status: "retired"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	service := assetReader(t, fixture)
	asset, err := service.PutProfile(t.Context(), fixture.token, "lifecycle-unknown-end-profile", unit.ID, unit.Revision, manualProfileInput(item.Revision, 0))
	if err != nil {
		t.Fatal(err)
	}
	setCatalogProjectionRevision(t, s, "unit", unit.ID, maxSafeInteger)
	before := assetProfileState(t, s)
	changed := requireProjectionReplay(t, s, func() {
		if value, err := service.Retire(t.Context(), fixture.token, "lifecycle-explicit-missing-end", unit.ID, asset.Revision, retirementInput(maxSafeInteger)); err != nil || value.Revision != maxSafeInteger || value.Status != "retired" {
			t.Fatal("date-only retirement required a new physical counter/transition", value, err)
		}
	})
	if len(changed) != 2 || !changed[projectionKey{"asset", unit.ID}] || !changed[projectionKey{"item", unit.ItemID}] {
		t.Fatal("explicit missing retirement date changed physical/path/search scopes", changed)
	}
	after := assetProfileState(t, s)
	for _, table := range []string{"item_units", "inventory_events", "locations", "stock_balances", "search_documents", "unit_search_documents", "asset_disposals"} {
		if before[table] != after[table] {
			t.Fatal("date-only retirement invented physical history or disposal", table)
		}
	}
}

func TestAssetLifecycleRetainsEquivalentHistoricalRetirementSpellingWhenAligningPhysicalStatus(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	original := "2026-11-07T12:00:00.123456789+08:00"
	execSQL(t, s.db, "UPDATE asset_profiles SET retired_at=? WHERE item_unit_id=?", original, fixture.unit.ID)
	input := retirementInput(fixture.unit.Revision)
	input.RetiredAt = "2026-11-07T04:00:00.123456789Z"
	value, err := assetReader(t, fixture).Retire(t.Context(), fixture.token, "lifecycle-historical-retirement-alignment", fixture.unit.ID, asset.Revision, input)
	if err != nil || value.Status != "retired" || scalar[string](t, s.db, "SELECT retired_at FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != original {
		t.Fatal("physical retirement rewrote an equivalent retained financial date", value, err)
	}
}

func TestAssetLifecycleRemovalsAcceptArchivedOrUnknownPlacementButRejectInvalidPaths(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		for _, state := range []string{"archived path", "unknown placement", "source counter", "ancestor counter", "cycle"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				switch state {
				case "archived path":
					execSQL(t, s.db, "UPDATE locations SET status='archived' WHERE id IN ('source','root')")
				case "unknown placement":
					execSQL(t, s.db, "UPDATE item_units SET location_id=NULL WHERE id=?", fixture.unit.ID)
				case "source counter", "ancestor counter":
					id := "source"
					if state == "ancestor counter" {
						id = "root"
					}
					execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id=?", id)
					execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id=?", id)
				case "cycle":
					execSQL(t, s.db, "UPDATE locations SET parent_id='source' WHERE id='root'")
				}
				before := assetProfileState(t, s)
				value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-path-reviewed", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind)
				if state == "archived path" || state == "unknown placement" {
					if err != nil || zeroAssetEvent(value) {
						t.Fatal("terminal/status command required a new active or guessed placement", value, err)
					}
					location, err := loadUnit(t.Context(), s.db, fixture.unit.ID)
					if err != nil || state == "unknown placement" && location.LocationID != nil || state == "archived path" && !sameParent(location.LocationID, fixture.unit.LocationID) {
						t.Fatal("terminal command silently cleared or invented placement", location, err)
					}
				} else {
					if !errors.Is(err, ErrDomainState) || !zeroAssetEvent(value) {
						t.Fatal("broken current ancestry became a successful financial/physical transition", value, err)
					}
					requireAssetEventState(t, s, before)
				}
			})
		}
	}
}

func TestAssetLifecycleNewProceedsValidateProposedTotalsRatherThanAnOldUnsafeSummary(t *testing.T) {
	fixture, asset := manualAssetFixture(t, maxSafeInteger)
	s := fixture.store
	service := assetReader(t, fixture)
	insertAssetCost(t, s, "existing-safe-expense", fixture.unit.ID, maxSafeInteger, "expense")
	if value, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T00:00:00Z"); !errors.Is(err, ErrAssetOverflow) || !reflect.DeepEqual(value, AssetCostSummary{}) {
		t.Fatal("old net outside safe range was silently rounded or clamped", value, err)
	}
	before := assetProfileState(t, s)
	if value, err := service.Retire(t.Context(), fixture.token, "lifecycle-unsafe-old-money", fixture.unit.ID, asset.Revision, retirementInput(fixture.unit.Revision)); !errors.Is(err, ErrAssetOverflow) || value != (Unit{}) {
		t.Fatal("retirement ignored complete unsafe source", value, err)
	}
	requireAssetEventState(t, s, before)
	input := disposalInput(fixture.unit.Revision, maxSafeInteger)
	input.Note = "\x00" + string(make([]byte, 9999))
	value, err := service.Dispose(t.Context(), fixture.token, "lifecycle-safe-proposed-proceeds", fixture.unit.ID, asset.Revision, input)
	if err != nil || value.RealizedProceedsMinor != maxSafeInteger || value.Note != input.Note || value.Revision != 1 {
		t.Fatal("safe proposed source was blocked by an old summary or lost max-safe/actual note values", value, err)
	}
	summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, input.DisposedAt)
	if err != nil || summary.NetCostMinor != maxSafeInteger || summary.ExtraMinor != maxSafeInteger {
		t.Fatal("actual proceeds did not produce exact safe source totals", summary, err)
	}
}

func TestAssetLifecycleRejectsUnsafeNegativeProceedsResultWithoutPartialPhysicalChange(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	insertAssetCost(t, s, "existing-safe-credit", fixture.unit.ID, maxSafeInteger, "credit")
	before := assetProfileState(t, s)
	if value, err := assetReader(t, fixture).Dispose(t.Context(), fixture.token, "lifecycle-overflow-net", fixture.unit.ID, asset.Revision, disposalInput(fixture.unit.Revision, 1)); !errors.Is(err, ErrAssetOverflow) || value != (Disposal{}) {
		t.Fatal("disposal rounded/clamped unsafe negative net or returned partial physical state", value, err)
	}
	requireAssetEventState(t, s, before)
}

func TestAssetLifecycleEqualRetainedDecisionsAtCounterLimitsAreNotNewMoneyOrProjections(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, _ := manualAssetFixture(t, 0)
			s := fixture.store
			instant := retirementInput(1).RetiredAt
			execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
			if kind == "retire" {
				execSQL(t, s.db, "UPDATE item_units SET status='retired' WHERE id=?", fixture.unit.ID)
				execSQL(t, s.db, "UPDATE asset_profiles SET retired_at=? WHERE item_unit_id=?", instant, fixture.unit.ID)
			} else {
				execSQL(t, s.db, "UPDATE item_units SET status='disposed' WHERE id=?", fixture.unit.ID)
				execSQL(t, s.db, `INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,note,created_at) VALUES(?,?,'sold',0,'ZZZ',?,'legacy')`, fixture.unit.ID, instant, disposalInput(1, 0).Note)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,?)", fixture.unit.ID, maxSafeInteger)
			}
			setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, maxSafeInteger)
			setCatalogProjectionRevision(t, s, "unit", fixture.unit.ID, maxSafeInteger)
			setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, maxSafeInteger)
			execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			before := assetProfileState(t, s)
			service := assetReader(t, fixture)
			changed := requireProjectionReplay(t, s, func() {
				if kind == "retire" {
					value, err := service.Retire(t.Context(), fixture.token, "lifecycle-retained-equal-limits", fixture.unit.ID, maxSafeInteger, retirementInput(maxSafeInteger))
					if err != nil || value.Revision != maxSafeInteger {
						t.Fatal("equal retained retirement was repriced or advanced a saturated counter", value, err)
					}
				} else {
					input := disposalInput(maxSafeInteger, 0)
					input.Currency = "ZZZ"
					value, err := service.Dispose(t.Context(), fixture.token, "lifecycle-retained-equal-limits", fixture.unit.ID, maxSafeInteger, input)
					if err != nil || value.Revision != maxSafeInteger || value.DisposedAt != instant || value.Currency != "ZZZ" {
						t.Fatal("equal retained disposal was repriced, respelled or assigned revision one", value, err)
					}
				}
			})
			if len(changed) != 0 {
				t.Fatal("equal eligible lifecycle produced new projections at safe limits", changed)
			}
			after := assetProfileState(t, s)
			for _, table := range []string{"items", "item_units", "asset_profiles", "asset_disposals", "inventory_events", "projection_revisions", "current_projections", "sync_changes", "data_revision"} {
				if before[table] != after[table] {
					t.Fatal("retained no-op changed source/counters/history rather than only context/audit/receipt", table)
				}
			}
		})
	}
}

func TestAssetLifecycleCommandsDoNotDependOnUnrelatedHomeDaySettings(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			execSQL(t, fixture.store.db, `INSERT INTO app_settings VALUES('preferences.timezone','"not/a-zone"','legacy')`)
			if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-independent-timezone", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err != nil || zeroAssetEvent(value) {
				t.Fatal("lifecycle command used a day-summary setting as financial authority", value, err)
			}
		})
	}
}

func TestAssetLifecycleExistingDisposalNeedsRealAuthorityAndMatchingTerminalState(t *testing.T) {
	for _, missingCounter := range []bool{false, true} {
		for _, kind := range []string{"retire", "dispose"} {
			t.Run(kind+"/"+map[bool]string{false: "nonterminal Unit", true: "missing Disposal counter"}[missingCounter], func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				input := disposalInput(fixture.unit.Revision, 0)
				execSQL(t, s.db, `INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,realized_proceeds_minor,currency,note,created_at) VALUES(?,?,'sold',0,'JPY',?,'legacy')`, fixture.unit.ID, input.DisposedAt, input.Note)
				want := ErrDomainState
				if missingCounter {
					execSQL(t, s.db, "UPDATE item_units SET status='disposed' WHERE id=?", fixture.unit.ID)
				} else {
					execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('disposal',?,1)", fixture.unit.ID)
					if kind == "retire" {
						want = ErrUnitDisposed
					}
				}
				before := assetProfileState(t, s)
				if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-invalid-existing-disposal", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); !errors.Is(err, want) || !zeroAssetEvent(value) {
					t.Fatal("existing disposal was repaired, revived or read with invented revision one", value, err)
				}
				requireAssetEventState(t, s, before)
			})
		}
	}
}

func TestAssetLifecycleCompleteFinancialSourceMustMatchBeforeAnyAcknowledgement(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			execSQL(t, s.db, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,created_at) VALUES('bad-prior-cost',?,'2026-11-07T00:00:00Z','repair',1,'expense','USD','legacy')`, fixture.unit.ID)
			execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('cost','bad-prior-cost',1)")
			before := assetProfileState(t, s)
			if value, err := recordAssetLifecycle(t.Context(), assetReader(t, fixture), fixture.token, "lifecycle-incompatible-prior-finance", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); !errors.Is(err, ErrDomainState) || !zeroAssetEvent(value) {
				t.Fatal("terminal command ignored complete currency authority or returned a partial result", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetLifecycleEqualFreshKeysStillValidateSourceButOriginalRepliesDoNotRebase(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			original, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-original-finance-valid", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind)
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
			execSQL(t, s.db, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,kind,amount_minor,direction,currency,created_at) VALUES('later-bad-cost',?,'2026-11-07T00:00:00Z','repair',1,'expense','USD','legacy')`, fixture.unit.ID)
			execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('cost','later-bad-cost',1)")
			before := assetProfileState(t, s)
			if value, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-new-key-ineligible-noop", unit.ID, current.Revision, unit.Revision, kind); !errors.Is(err, ErrDomainState) || !zeroAssetEvent(value) {
				t.Fatal("equal fields bypassed complete current eligibility under a fresh key", value, err)
			}
			requireAssetEventState(t, s, before)
			if value, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-original-finance-valid", unit.ID, asset.Revision, fixture.unit.Revision, kind); err != nil || !reflect.DeepEqual(value, original) {
				t.Fatal("original reconciliation reapplied later source policy or repeated a terminal action", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}
