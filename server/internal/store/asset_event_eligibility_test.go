package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func anotherAssetOwner(t *testing.T, fixture holdingFixture) string {
	t.Helper()
	unit := fixture.unit
	unit.ID, unit.AssetCode = "other-asset-unit", "OTHER-ASSET"
	execSQL(t, fixture.store.db, `INSERT INTO item_units(id,item_id,location_id,asset_code,status,serial_number,notes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'legacy','legacy')`, unit.ID, unit.ItemID, unit.LocationID, unit.AssetCode, unit.Status, unit.SerialNumber, unit.Notes)
	seedCatalogProjection(t, fixture.store, "unit", unit.ID, unit.UnitData)
	amount := int64(0)
	asset := AssetData{ItemUnitID: unit.ID, CostBasis: "manual", Currency: "JPY", ManualAcquisitionMinor: &amount, UsageUnit: "times"}
	execSQL(t, fixture.store.db, `INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,manual_acquisition_minor,usage_unit,created_at,updated_at) VALUES(?,'manual','JPY',0,'times','legacy','legacy')`, unit.ID)
	seedCatalogProjection(t, fixture.store, "asset", unit.ID, asset)
	return unit.ID
}

func TestAssetEventsRequireActualEarlierSameOwnerAndInterpretationReferences(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		for _, failure := range []string{"missing", "other owner", "different interpretation", "missing counter", "broken chain"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				s := fixture.store
				service := assetReader(t, fixture)
				owner := fixture.unit.ID
				if failure == "other owner" {
					owner = anotherAssetOwner(t, fixture)
				}
				if failure != "missing" {
					if kind == "usage" {
						unit := "times"
						if failure == "different interpretation" {
							unit = "minutes"
						}
						execSQL(t, s.db, `INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,created_at) VALUES('reference',?,?,2,?,'use','legacy')`, owner, usageInput(1).OccurredAt, unit)
					} else {
						code := "JPY"
						if failure == "different interpretation" {
							code = "USD"
						}
						execSQL(t, s.db, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,amount_minor,currency,kind,direction,created_at) VALUES('reference',?,?,2,?,'repair','expense','legacy')`, owner, costInput(1).OccurredAt, code)
					}
					if failure != "missing counter" {
						execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,'reference',1)", kind)
					}
				}
				if failure == "broken chain" {
					execSQL(t, s.db, "DROP TRIGGER asset_"+kind+"_events_no_update")
					if kind == "usage" {
						execSQL(t, s.db, `INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,created_at) VALUES('missing-tail',?,? ,1,'times','use','legacy')`, owner, usageInput(1).OccurredAt)
						execSQL(t, s.db, "UPDATE asset_usage_events SET event_type='correction',correction_of='missing-tail' WHERE id='reference'")
					} else {
						execSQL(t, s.db, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,amount_minor,currency,kind,direction,created_at) VALUES('missing-tail',?,?,1,'JPY','repair','expense','legacy')`, owner, costInput(1).OccurredAt)
						execSQL(t, s.db, "UPDATE asset_cost_events SET kind='adjustment',correction_of='missing-tail' WHERE id='reference'")
					}
				}
				before := assetProfileState(t, s)
				want := ErrDomainInput
				if failure == "missing" {
					want = ErrNotFound
				} else if failure == "missing counter" || failure == "broken chain" {
					want = ErrDomainState
				}
				if kind == "usage" {
					input := usageInput(-1)
					input.EventType, input.CorrectionOf = "correction", testPointer("reference")
					if value, err := service.RecordUsage(t.Context(), fixture.token, "event-real-reference", fixture.unit.ID, asset.Revision, input); !errors.Is(err, want) || value != (Usage{}) {
						t.Fatal("usage accepted missing/foreign/broken source reference", value, err)
					}
				} else {
					input := costInput(1)
					input.Kind, input.Direction, input.CorrectionOf = "adjustment", "credit", testPointer("reference")
					if value, err := service.RecordCost(t.Context(), fixture.token, "event-real-reference", fixture.unit.ID, asset.Revision, input); !errors.Is(err, want) || value != (Cost{}) {
						t.Fatal("cost accepted missing/foreign/broken source reference", value, err)
					}
				}
				requireAssetEventState(t, s, before)
			})
		}
	}
}

func TestAssetEventsValidateCompleteSourceAndEverySafePublishedTotal(t *testing.T) {
	for _, failure := range []string{"usage lifetime overflow", "cost realized overflow", "cost projected overflow", "unrelated prior cost mismatch", "unrelated prior usage mismatch", "manual purchase conflict"} {
		t.Run(failure, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			proof := asset.Revision
			if failure == "usage lifetime overflow" {
				if _, err := service.RecordUsage(t.Context(), fixture.token, "event-usage-safe-maximum", fixture.unit.ID, proof, usageInput(maxSafeInteger)); err != nil {
					t.Fatal("safe maximum usage was rejected", err)
				}
				proof++
			} else if failure == "cost realized overflow" {
				execSQL(t, s.db, "UPDATE asset_profiles SET manual_acquisition_minor=? WHERE item_unit_id=?", maxSafeInteger, fixture.unit.ID)
			} else if failure == "cost projected overflow" {
				execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=? WHERE item_unit_id=?", maxSafeInteger, fixture.unit.ID)
			} else if failure == "unrelated prior cost mismatch" {
				execSQL(t, s.db, `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,amount_minor,currency,kind,direction,created_at) VALUES('late-bad-cost',?,'2026-11-07T00:00:00Z',1,'USD','repair','expense','legacy')`, fixture.unit.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('cost','late-bad-cost',1)")
			} else if failure == "unrelated prior usage mismatch" {
				execSQL(t, s.db, `INSERT INTO asset_usage_events(id,item_unit_id,occurred_at,delta,usage_unit,event_type,created_at) VALUES('late-bad-use',?,'2026-11-07T00:00:00Z',1,'minutes','use','legacy')`, fixture.unit.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('usage','late-bad-use',1)")
			} else {
				purchases, err := s.NewPurchases(fixture.auth)
				if err != nil {
					t.Fatal(err)
				}
				// Retained contradictory source is not silently repaired by an event.
				execSQL(t, s.db, "DROP TRIGGER asset_profiles_no_delete")
				execSQL(t, s.db, "DELETE FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID)
				purchase := createPurchaseCommand(t, purchases, fixture.token, "event-zero-header", "JPY")
				_, err = purchases.CreateLine(t.Context(), fixture.token, "event-zero-allocation", purchase.ID, purchase.Revision, PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0})
				if err != nil {
					t.Fatal(err)
				}
				execSQL(t, s.db, `INSERT INTO asset_profiles(item_unit_id,cost_basis,currency,manual_acquisition_minor,usage_unit,created_at,updated_at) VALUES(?,'manual','JPY',0,'times','legacy','legacy')`, fixture.unit.ID)
			}
			before := assetProfileState(t, s)
			want := ErrDomainState
			if failure == "usage lifetime overflow" || failure == "cost realized overflow" || failure == "cost projected overflow" {
				want = ErrAssetOverflow
			}
			if failure == "usage lifetime overflow" || failure == "unrelated prior cost mismatch" || failure == "manual purchase conflict" {
				if value, err := service.RecordUsage(t.Context(), fixture.token, "event-ineligible-history", fixture.unit.ID, proof, usageInput(1)); !errors.Is(err, want) || value != (Usage{}) {
					t.Fatal("usage ignored complete financial source or safe totals", value, err)
				}
			} else {
				input := costInput(1)
				if failure == "cost projected overflow" {
					input.Direction = "credit"
				}
				if value, err := service.RecordCost(t.Context(), fixture.token, "event-ineligible-history", fixture.unit.ID, proof, input); !errors.Is(err, want) || value != (Cost{}) {
					t.Fatal("cost ignored complete financial source or safe totals", value, err)
				}
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetEventsCompleteLargeSignedHistoryWithoutInt64OrCachedAuthority(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, direction := range []string{"expense", "credit"} {
		for i := range 1025 {
			id := fmt.Sprintf("%s-%04d", direction, i)
			if _, err = tx.ExecContext(t.Context(), `INSERT INTO asset_cost_events(id,item_unit_id,occurred_at,amount_minor,currency,kind,direction,created_at) VALUES(?,?,'2026-11-07T00:00:00Z',?,'JPY','repair',?,'legacy')`, id, fixture.unit.ID, maxSafeInteger, direction); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO projection_revisions VALUES('cost',?,1)", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"NotFinancialAuthority\":true}' WHERE entity_type IN('asset','cost','usage')")
	execSQL(t, s.db, `INSERT INTO app_settings VALUES('preferences.timezone','"not/a-zone"','legacy')`)
	value, err := assetReader(t, fixture).RecordCost(t.Context(), fixture.token, "event-exact-large-source", fixture.unit.ID, asset.Revision, costInput(5))
	if err != nil || value.AmountMinor != 5 || value.Revision != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_cost_events") != 2051 {
		t.Fatal("event truncated history, overflowed an int64 intermediate, used a cache or depended on civil-day settings", value, err)
	}
}

func TestAssetEventsOrdinaryCostKindsNeverInferDirectionAndPostedRowsStayImmutable(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	proof := asset.Revision
	for _, kind := range []string{"maintenance", "repair", "accessory", "fee", "refund"} {
		for _, direction := range []string{"expense", "credit"} {
			input := costInput(1)
			input.Kind, input.Direction = kind, direction
			value, err := service.RecordCost(t.Context(), fixture.token, "event-kind-"+kind+"-"+direction, fixture.unit.ID, proof, input)
			if err != nil || value.Kind != kind || value.Direction != direction || value.Note != "" || value.CorrectionOf != nil {
				t.Fatal("cost inferred a direction, correction or note from its descriptive kind", value, err)
			}
			proof++
			before := assetProfileState(t, s)
			for _, query := range []string{"UPDATE asset_cost_events SET amount_minor=amount_minor WHERE id=?", "DELETE FROM asset_cost_events WHERE id=?"} {
				if _, err = s.db.ExecContext(t.Context(), query, value.ID); err == nil {
					t.Fatal("new posted cost could be rewritten or deleted")
				}
				requireAssetEventState(t, s, before)
			}
		}
	}
	usage, err := service.RecordUsage(t.Context(), fixture.token, "event-immutable-new-usage", fixture.unit.ID, proof, usageInput(1))
	if err != nil {
		t.Fatal(err)
	}
	before := assetProfileState(t, s)
	for _, query := range []string{"UPDATE asset_usage_events SET delta=delta WHERE id=?", "DELETE FROM asset_usage_events WHERE id=?"} {
		if _, err = s.db.ExecContext(t.Context(), query, usage.ID); err == nil {
			t.Fatal("new posted usage could be rewritten or deleted")
		}
		requireAssetEventState(t, s, before)
	}
}

func TestAssetEventsUsageMustRetainTheReviewedProfileInterpretation(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	input := usageInput(1)
	input.UsageUnit = "minutes"
	before := assetProfileState(t, fixture.store)
	if value, err := assetReader(t, fixture).RecordUsage(t.Context(), fixture.token, "event-wrong-usage-interpretation", fixture.unit.ID, asset.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Usage{}) {
		t.Fatal("usage silently converted a valid but different measurement interpretation", value, err)
	}
	requireAssetEventState(t, fixture.store, before)
}

func TestAssetEventsMaximumCostAndRetainedPostedCounterStayIndependent(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	reference := strings.Repeat("界", 128)
	insertAssetCost(t, s, reference, fixture.unit.ID, 1, "expense")
	setCatalogProjectionRevision(t, s, "cost", reference, maxSafeInteger)
	input := costInput(maxSafeInteger)
	input.Kind, input.Direction, input.CorrectionOf = "adjustment", "credit", &reference
	input.Note = "\x00" + strings.Repeat("界", 9999)
	value, err := assetReader(t, fixture).RecordCost(t.Context(), fixture.token, "event-maximum-safe-cost", fixture.unit.ID, asset.Revision, input)
	if err != nil || value.Revision != 1 || value.AmountMinor != maxSafeInteger || value.Note != input.Note || value.CorrectionOf == nil || *value.CorrectionOf != reference || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='cost' AND entity_id=?", reference) != maxSafeInteger {
		t.Fatal("safe cost/reference/text boundary was rejected, truncated or coupled to immutable posted counter", value, err)
	}
}
