package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAssetPagesRejectInvalidBoundsFiltersAndMissingOwnersWithoutAResult(t *testing.T) {
	fixture := assetsFixture(t, false)
	service := assetReader(t, fixture.holdingFixture)
	before := assetProfileState(t, fixture.store)
	for _, test := range []struct {
		limit  int
		cursor string
	}{{0, ""}, {101, ""}, {1, strings.Repeat("x", 2049)}} {
		for _, read := range []func() (any, error){
			func() (any, error) {
				return service.Page(t.Context(), fixture.token, test.cursor, test.limit, AssetFilters{})
			},
			func() (any, error) {
				return service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, test.cursor, test.limit)
			},
			func() (any, error) {
				return service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, test.cursor, test.limit)
			},
		} {
			if value, err := read(); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("invalid paging bounds returned financial data", value, err)
			}
		}
	}
	for _, filters := range []AssetFilters{{From: "2026"}, {To: "2026-02-30"}, {From: "2026-11-08", To: "2026-11-07"}, {Scope: "branch"}, {Status: "disposed"},
		{Currency: "jpy"}, {CategoryID: strings.Repeat("界", 129)}, {LocationID: string([]byte{0xff})}, {Merchant: strings.Repeat("😀", 301)}} {
		if value, err := service.Page(t.Context(), fixture.token, "", 50, filters); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid civil/metadata filters returned financial data", value, err)
		}
	}
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := service.PageUsage(t.Context(), fixture.token, id, "", 50); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid owner identity returned usage data", value, err)
		}
		if value, err := service.PageCosts(t.Context(), fixture.token, id, "", 50); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid owner identity returned cost data", value, err)
		}
	}
	for _, id := range []string{"absent", "new", "tree", "\x00"} {
		if value, err := service.PageUsage(t.Context(), fixture.token, id, "", 50); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("valid absent/imported owner was reserved, normalized or invented", value, err)
		}
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestAssetPagesWithholdWholePrefixesWhenLookaheadLosesRealOwnOrRelatedCounters(t *testing.T) {
	for _, kind := range []string{"asset", "unit", "item", "usage", "cost"} {
		t.Run(kind, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s, service := fixture.store, assetReader(t, fixture.holdingFixture)
			id := "zz-invalid"
			owner := fixture.holdingFixture
			if kind == "item" {
				owner.individual = createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Lookahead owner"})
			}
			seedAssetPageOwner(t, owner, id)
			if kind == "item" {
				id = owner.individual.ID
			} else if kind == "usage" {
				insertAssetUsage(t, s, id, fixture.unit.ID, "2026-11-07T00:00:00Z", 1, nil)
			} else if kind == "cost" {
				insertAssetCost(t, s, id, fixture.unit.ID, 1, "expense")
			}
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", kind, id)
			execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", kind, id)
			before := assetProfileState(t, s)
			var value any
			var err error
			switch kind {
			case "usage":
				value, err = service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 1)
			case "cost":
				value, err = service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 1)
			default:
				value, err = service.Page(t.Context(), fixture.token, "", 1, AssetFilters{})
			}
			if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("invalid lookahead was hidden behind a valid first row or repaired", value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetPagesValidateEveryMatchingPurchaseLineNotJustTheFirstSuccessfulHeader(t *testing.T) {
	fixture := assetsFixture(t, false)
	s, service := fixture.store, assetReader(t, fixture.holdingFixture)
	seedReadPurchaseLine(t, s, PurchaseLineData{ID: "zz-invalid-match", PurchaseID: fixture.purchase.ID, ItemID: fixture.individual.ID, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0})
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='purchase_line' AND entity_id='zz-invalid-match'")
	execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id='zz-invalid-match'")
	before := assetProfileState(t, s)
	if value, err := service.Page(t.Context(), fixture.token, "", 50, AssetFilters{Merchant: fixture.purchase.MerchantName}); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("a matching first Line hid later corrupt same-Unit purchase authority", value, err)
	}
	requireAssetProfileState(t, s, before)
	// No purchase criterion means no hidden financial-history eligibility scan.
	if page, err := service.Page(t.Context(), fixture.token, "", 50, AssetFilters{}); err != nil || !reflect.DeepEqual(page.Data, []Asset{fixture.asset}) {
		t.Fatal("unrequested financial history became profile-list authority", page, err)
	}
}

func TestAssetPagesOwnedCorrectionsRequireRealSameOwnerInterpretationAndValidTails(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		for _, failure := range []string{"missing target", "foreign owner", "foreign interpretation", "cycle"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture := assetsFixture(t, false)
				s, service := fixture.store, assetReader(t, fixture.holdingFixture)
				other := seedAssetPageOwner(t, fixture.holdingFixture, "other-owner")
				ownerID := fixture.unit.ID
				if failure == "foreign owner" {
					ownerID = other.ItemUnitID
				}
				if kind == "usage" {
					insertAssetUsage(t, s, "target", ownerID, "2026-11-07T00:00:00Z", 1, nil)
					insertAssetUsage(t, s, "zz-correction", fixture.unit.ID, "2026-11-07T00:00:01Z", -1, testPointer("target"))
				} else {
					insertAssetCost(t, s, "target", ownerID, 1, "expense")
					insertAssetCost(t, s, "zz-correction", fixture.unit.ID, 1, "credit")
					execSQL(t, s.db, "DROP TRIGGER asset_cost_events_no_update")
					execSQL(t, s.db, "UPDATE asset_cost_events SET kind='adjustment',correction_of='target' WHERE id='zz-correction'")
				}
				if failure == "missing target" {
					execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id='target'", kind)
				} else if failure == "foreign interpretation" || failure == "cycle" {
					if kind == "usage" {
						execSQL(t, s.db, "DROP TRIGGER asset_usage_events_no_update")
						if failure == "cycle" {
							execSQL(t, s.db, "UPDATE asset_usage_events SET event_type='correction',correction_of='zz-correction' WHERE id='target'")
						} else {
							execSQL(t, s.db, "UPDATE asset_usage_events SET usage_unit='minutes' WHERE id='target'")
						}
					} else if failure == "cycle" {
						execSQL(t, s.db, "UPDATE asset_cost_events SET kind='adjustment',correction_of='zz-correction' WHERE id='target'")
					} else {
						execSQL(t, s.db, "UPDATE asset_cost_events SET currency='USD' WHERE id='target'")
					}
				}
				before := assetProfileState(t, s)
				var value any
				var err error
				if kind == "usage" {
					value, err = service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 100)
				} else {
					value, err = service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 100)
				}
				if !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("invalid correction graph released a partial owned page", value, err)
				}
				requireAssetProfileState(t, s, before)
			})
		}
	}
}

func TestAssetPagesSQLSignatureTimezoneAndSkippedActivityFailuresReturnNoPrefix(t *testing.T) {
	for _, failure := range []string{"query", "signature", "ignored activity", "invalid timezone"} {
		t.Run(failure, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s, service := fixture.store, assetReader(t, fixture.holdingFixture)
			seedAssetPageOwner(t, fixture.holdingFixture, "zz-lookahead")
			insertAssetUsage(t, s, "zz-lookahead", fixture.unit.ID, "2026-11-07T00:00:00Z", 1, nil)
			insertAssetCost(t, s, "zz-lookahead", fixture.unit.ID, 1, "expense")
			if failure == "signature" {
				fixture.auth.serverSecret = nil
			} else if failure == "ignored activity" {
				execSQL(t, s.db, "CREATE TRIGGER skip_asset_page_activity BEFORE UPDATE ON sessions BEGIN SELECT RAISE(IGNORE); END")
			} else if failure == "invalid timezone" {
				execSQL(t, s.db, `INSERT INTO app_settings VALUES('preferences.timezone','null','legacy')`)
			}
			before := assetProfileState(t, s)
			if failure == "query" {
				execSQL(t, s.db, "CREATE TEMP VIEW asset_profiles AS SELECT * FROM main.asset_profiles WHERE json('invalid')=1")
			}
			for index, read := range []func() (any, error){
				func() (any, error) { return service.Page(t.Context(), fixture.token, "", 1, AssetFilters{}) },
				func() (any, error) { return service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 1) },
				func() (any, error) { return service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 1) },
			} {
				value, err := read()
				if failure == "invalid timezone" && index > 0 {
					if err != nil {
						t.Fatal("owned collection acquired unrelated invalid timezone authority", err)
					}
				} else if err == nil || !reflect.ValueOf(value).IsZero() {
					t.Fatal("failed query/signing/activity/timezone released a page prefix", value, err)
				}
			}
			if failure == "query" {
				execSQL(t, s.db, "DROP VIEW temp.asset_profiles")
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetPagesActualEventAndPurchaseQueryFailuresCannotBecomeEmptyCollections(t *testing.T) {
	for _, kind := range []string{"purchase", "usage", "cost"} {
		t.Run(kind, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s, service := fixture.store, assetReader(t, fixture.holdingFixture)
			table := "purchase_lines"
			if kind == "usage" {
				table = "asset_usage_events"
			} else if kind == "cost" {
				table = "asset_cost_events"
			}
			before := assetProfileState(t, s)
			execSQL(t, s.db, "CREATE TEMP VIEW "+table+" AS SELECT * FROM main."+table+" WHERE json('invalid')=1")
			var value any
			var err error
			switch kind {
			case "purchase":
				value, err = service.Page(t.Context(), fixture.token, "", 50, AssetFilters{Merchant: fixture.purchase.MerchantName})
			case "usage":
				value, err = service.PageUsage(t.Context(), fixture.token, fixture.unit.ID, "", 50)
			case "cost":
				value, err = service.PageCosts(t.Context(), fixture.token, fixture.unit.ID, "", 50)
			}
			execSQL(t, s.db, "DROP VIEW temp."+table)
			if err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed actual scoped query returned empty/prefix financial data", value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetPagesMatchingPurchaseRequiresActualHeaderAndConsistentLineOwnership(t *testing.T) {
	for _, failure := range []string{"header counter", "foreign Item"} {
		t.Run(failure, func(t *testing.T) {
			fixture := assetsFixture(t, false)
			s, service := fixture.store, assetReader(t, fixture.holdingFixture)
			if failure == "header counter" {
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='purchase' AND entity_id=?", fixture.purchase.ID)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='purchase' AND entity_id=?", fixture.purchase.ID)
			} else {
				other := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Unrelated Line owner"})
				seedReadPurchaseLine(t, s, PurchaseLineData{ID: "zz-foreign-owner", PurchaseID: fixture.purchase.ID, ItemID: other.ID, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0})
			}
			before := assetProfileState(t, s)
			if value, err := service.Page(t.Context(), fixture.token, "", 50, AssetFilters{Merchant: fixture.purchase.MerchantName}); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("matching purchase criteria used cached/missing/inconsistent authority", value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}
