package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAssetPurchaseEveryActualStatementFailureRollsBackFinancialAndSearchPublication(t *testing.T) {
	for _, failure := range []struct{ table, verb string }{
		{"purchase_lines", "INSERT"}, {"asset_profiles", "UPDATE"}, {"items", "UPDATE"},
		{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
		{"current_projections", "INSERT"}, {"current_projections", "UPDATE"}, {"sync_changes", "INSERT"},
		{"search_documents", "INSERT"}, {"unit_search_documents", "INSERT"},
		{"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+"/"+failure.verb, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase BEFORE "+failure.verb+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'financial allocation statement failure'); END")
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 123}
			before := assetProfileState(t, s)
			if value, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-failed-statement", fixture.purchase.ID, fixture.purchase.Revision, input); err == nil || value != (PurchaseLine{}) {
				t.Fatal("failed financial/source/search/publication statement returned a partial allocation", value, err)
			}
			requireAssetProfileState(t, s, before)
			execSQL(t, s.db, "DROP TRIGGER fail_asset_purchase")
			if _, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-failed-statement", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
				t.Fatal("failed original key or pooled transaction could not be explicitly retried", err)
			}
		})
	}
}

func TestAssetPurchaseIgnoredRewrittenOrReseededSourceCannotFreezeSuccess(t *testing.T) {
	for _, failure := range []string{"ignored Line", "ignored Asset", "ignored Item", "rewritten Line", "rewritten Asset", "reseeded Asset", "missing final Asset", "missing final Unit", "occupied Line identity"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 7)
			switch failure {
			case "ignored Line", "ignored Asset", "ignored Item":
				table, verb := "purchase_lines", "INSERT"
				if failure == "ignored Asset" {
					table, verb = "asset_profiles", "UPDATE"
				} else if failure == "ignored Item" {
					table, verb = "items", "UPDATE"
				}
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase BEFORE "+verb+" ON "+table+" BEGIN SELECT RAISE(IGNORE); END")
			case "rewritten Line":
				execSQL(t, s.db, "DROP TRIGGER purchase_lines_no_update")
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase AFTER INSERT ON purchase_lines BEGIN UPDATE purchase_lines SET note='rewritten' WHERE id=NEW.id; END")
			case "rewritten Asset":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase AFTER UPDATE ON asset_profiles WHEN NEW.usage_unit='times' BEGIN UPDATE asset_profiles SET usage_unit='minutes' WHERE item_unit_id=NEW.item_unit_id; END")
			case "reseeded Asset":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase AFTER INSERT ON purchase_lines BEGIN DELETE FROM current_projections WHERE entity_type='asset' AND entity_id=NEW.item_unit_id; DELETE FROM projection_revisions WHERE entity_type='asset' AND entity_id=NEW.item_unit_id; INSERT INTO projection_revisions VALUES('asset',NEW.item_unit_id,1); END")
			case "missing final Asset":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase AFTER UPDATE ON items BEGIN DELETE FROM current_projections WHERE entity_type='asset'; DELETE FROM projection_revisions WHERE entity_type='asset'; END")
			case "missing final Unit":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase AFTER UPDATE ON asset_profiles BEGIN DELETE FROM current_projections WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; DELETE FROM projection_revisions WHERE entity_type='unit' AND entity_id=NEW.item_unit_id; END")
			case "occupied Line identity":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase BEFORE INSERT ON purchase_lines BEGIN INSERT INTO projection_revisions VALUES('purchase_line',NEW.id,2); END")
			}
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 123}
			before := assetProfileState(t, s)
			if value, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-skipped-source", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
				t.Fatal("ignored/rewritten/missing authority became a successful financial receipt", value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetPurchaseFinancialLimitsNeverReseedAuthorityOrPartiallyPublish(t *testing.T) {
	for _, scope := range []string{"asset", "item", "purchase", "catalog", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			switch scope {
			case "asset":
				setCatalogProjectionRevision(t, s, scope, fixture.unit.ID, maxSafeInteger)
			case "item":
				setCatalogProjectionRevision(t, s, scope, fixture.individual.ID, maxSafeInteger)
				fixture.individual.Revision = maxSafeInteger
			case "purchase":
				setCatalogProjectionRevision(t, s, scope, fixture.purchase.ID, maxSafeInteger)
				fixture.purchase.Revision = maxSafeInteger
			case "catalog":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			}
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
			before := assetProfileState(t, s)
			if value, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-safe-counter-limit", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrRevisionOverflow) || value != (PurchaseLine{}) {
				t.Fatal("unsafe financial/dependent authority returned a partial zero-paid allocation", value, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
}

func TestAssetPurchaseFinalAuthorizationCancellationAndDeferredCommitWithholdEverything(t *testing.T) {
	for _, failure := range []string{"mandatory", "revoked", "epoch", "canceled", "late expiry", "deferred commit"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			ctx := t.Context()
			want := ErrAuthentication
			switch failure {
			case "mandatory":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase_final AFTER INSERT ON mutation_events BEGIN UPDATE admin_accounts SET must_change_password=1; END")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase_final AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			case "epoch":
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase_final AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='changed-during-allocation'; END")
			case "deferred commit":
				execSQL(t, s.db, "CREATE TABLE allocation_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_asset_purchase_final AFTER UPDATE ON sessions BEGIN INSERT INTO allocation_commit_guard VALUES('missing'); END")
				want = nil
			case "canceled", "late expiry":
				base := fixture.auth.now()
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				calls := 0
				fixture.auth.now = func() time.Time {
					calls++
					if calls > 1 {
						if failure == "canceled" {
							cancel()
						} else {
							return base.Add(31 * 24 * time.Hour)
						}
					}
					return base
				}
				if failure == "canceled" {
					want = context.Canceled
				}
			}
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 123}
			before := assetProfileState(t, s)
			if value, err := purchases.CreateLine(ctx, fixture.token, "purchase-asset-final-failure", fixture.purchase.ID, fixture.purchase.Revision, input); err == nil || want != nil && !errors.Is(err, want) || value != (PurchaseLine{}) {
				t.Fatal("late authorization or financial COMMIT failure returned a partial allocation", value, err)
			}
			requireAssetProfileState(t, s, before)
			if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM allocation_commit_guard") != 0 {
				t.Fatal("failed financial COMMIT retained private deferred rows")
			}
		})
	}
}
