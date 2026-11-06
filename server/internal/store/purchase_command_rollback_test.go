package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func callPurchaseCommand(ctx context.Context, fixture purchaseFixture, service *Purchases, kind, key string) (domainResult, error) {
	switch kind {
	case "create":
		return service.Create(ctx, fixture.token, key, PurchaseCreate{MerchantName: "UncommittedPurchaseEvidence", PurchasedAt: "2026-11-06T00:00:00Z", Currency: "JPY"})
	case "patch":
		return service.Patch(ctx, fixture.token, key, fixture.purchase.ID, fixture.purchase.Revision, PurchasePatch{MerchantName: testPointer("UncommittedPurchaseEvidence")})
	case "line":
		return service.CreateLine(ctx, fixture.token, key, fixture.purchase.ID, fixture.purchase.Revision, PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 1, PaidMinor: 120000, Note: "UncommittedPurchaseEvidence"})
	default:
		panic("unknown purchase test command")
	}
}

func TestPurchaseActualCommandsRollbackEverySourceCorrectionProjectionSearchReceiptAndSessionStatement(t *testing.T) {
	for _, kind := range []string{"create", "patch", "line"} {
		t.Run(kind, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			execSQL(t, s.db, "INSERT INTO unit_search_documents(unit_id,title,revision,updated_at) VALUES(?,'OriginalPurchaseUnitCache',55,'2026')", fixture.unit.ID)
			now := fixture.auth.now().Add(time.Minute)
			fixture.auth.now = func() time.Time { return now }
			failures := []struct{ table, event string }{
				{"projection_revisions", "INSERT"}, {"current_projections", "INSERT"}, {"sync_changes", "INSERT"},
				{"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
			}
			if kind == "create" {
				failures = append(failures, struct{ table, event string }{"purchases", "INSERT"})
			} else {
				failures = append(failures, []struct{ table, event string }{
					{"items", "UPDATE"}, {"projection_revisions", "UPDATE"}, {"current_projections", "UPDATE"},
					{"search_documents", "INSERT"}, {"search_documents", "UPDATE"}, {"unit_search_documents", "INSERT"}, {"unit_search_documents", "UPDATE"},
				}...)
				if kind == "patch" {
					failures = append(failures, struct{ table, event string }{"purchases", "UPDATE"}, struct{ table, event string }{"purchase_header_corrections", "INSERT"})
				} else {
					failures = append(failures, struct{ table, event string }{"purchase_lines", "INSERT"})
				}
			}
			for _, failure := range failures {
				t.Run(failure.table+failure.event, func(t *testing.T) {
					before := purchaseCommandState(t, s)
					execSQL(t, s.db, "CREATE TRIGGER fail_purchase_command BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'purchase command failure'); END")
					defer execSQL(t, s.db, "DROP TRIGGER fail_purchase_command")
					if value, err := callPurchaseCommand(t.Context(), fixture, p, kind, "failed-actual-purchase-command"); err == nil || !reflect.ValueOf(value).IsZero() {
						t.Fatal("failed actual command exposed financial source/result", err)
					}
					requirePurchaseCommandState(t, s, before)
					if scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'UncommittedPurchaseEvidence'") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'UncommittedPurchaseEvidence'") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'OriginalPurchaseUnitCache'") != 1 {
						t.Fatal("failed command leaked or removed approved FTS")
					}
				})
			}
			if _, err := callPurchaseCommand(t.Context(), fixture, p, kind, "failed-actual-purchase-command"); err != nil {
				t.Fatal("rolled-back claim/connection could not execute an explicit original-key retry", err)
			}
		})
	}
}

func TestPurchaseActualCommandsWithholdLateRevocationMandatoryChangeAndDeferredCommit(t *testing.T) {
	for _, kind := range []string{"create", "patch", "line"} {
		for _, failure := range []string{"revoked", "mandatory", "deferred commit"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, p := purchaseCommands(t)
				s := fixture.store
				want := ErrAuthentication
				body := "UPDATE sessions SET revoked_at='2026-11-06T00:00:00Z';"
				if failure == "mandatory" {
					body = "UPDATE admin_accounts SET must_change_password=1;"
					want = ErrPasswordChangeRequired
				}
				if failure == "deferred commit" {
					execSQL(t, s.db, "CREATE TABLE purchase_command_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
					body = "INSERT INTO purchase_command_commit_guard VALUES('missing');"
				}
				execSQL(t, s.db, "CREATE TRIGGER fail_purchase_command_late AFTER INSERT ON mutation_events BEGIN "+body+" END")
				before := purchaseCommandState(t, s)
				if value, err := callPurchaseCommand(t.Context(), fixture, p, kind, "failed-late-purchase-command"); err == nil || !reflect.ValueOf(value).IsZero() || failure != "deferred commit" && !errors.Is(err, want) {
					t.Fatal("late session/COMMIT failure released success", err)
				}
				requirePurchaseCommandState(t, s, before)
				if failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_command_commit_guard") != 0 {
					t.Fatal("failed COMMIT retained its deferred guard row")
				}
				execSQL(t, s.db, "DROP TRIGGER fail_purchase_command_late")
				if _, err := callPurchaseCommand(t.Context(), fixture, p, kind, "failed-late-purchase-command"); err != nil {
					t.Fatal("failed final transaction retained the key or lost the private connection", err)
				}
			})
		}
	}
}

func TestPurchaseActualCommandsRejectCounterExhaustionAndFreshIdentityReuseAtomically(t *testing.T) {
	for _, kind := range []string{"create", "patch", "line"} {
		for _, scope := range []string{"purchase", "Item", "item search", "unit search", "catalog revision", "sequence", "new identity"} {
			if kind == "create" && scope != "catalog revision" && scope != "sequence" && scope != "new identity" || kind == "patch" && scope == "new identity" {
				continue
			}
			t.Run(kind+"/"+scope, func(t *testing.T) {
				fixture, p := purchaseCommands(t)
				s := fixture.store
				key := projectionKey{"purchase", fixture.purchase.ID}
				want := ErrRevisionOverflow
				switch scope {
				case "purchase":
					fixture.purchase.Revision = maxSafeInteger
				case "Item":
					key = projectionKey{"item", fixture.individual.ID}
					fixture.individual.Revision = maxSafeInteger
				case "item search":
					key = projectionKey{"search_doc", projectionPair("item", fixture.individual.ID)}
				case "unit search":
					key = projectionKey{"search_doc", projectionPair("unit", fixture.unit.ID)}
					seedCatalogProjection(t, s, key.kind, key.id, searchData{EntityType: "unit", EntityID: fixture.unit.ID, ApprovedText: "Original"})
				}
				switch scope {
				case "catalog revision":
					execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
				case "sequence":
					execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
				case "new identity":
					table, entity := "purchases", "purchase"
					if kind == "line" {
						table, entity = "purchase_lines", "purchase_line"
					}
					execSQL(t, s.db, "CREATE TRIGGER reserve_purchase_identity BEFORE INSERT ON "+table+" BEGIN INSERT INTO projection_revisions VALUES('"+entity+"',NEW.id,7); END")
					want = ErrDomainState
				default:
					setCatalogProjectionRevision(t, s, key.kind, key.id, maxSafeInteger)
				}
				before := purchaseCommandState(t, s)
				if value, err := callPurchaseCommand(t.Context(), fixture, p, kind, "exhausted-purchase-command"); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("unsafe/reused authority released financial success", err, want)
				}
				requirePurchaseCommandState(t, s, before)
			})
		}
	}
}

func TestPurchaseActualCorrectionChecksAllOwnedLineAuthorityBeforePublishingAnyPrefix(t *testing.T) {
	for _, scope := range []string{"purchase", "first Line", "last Line", "individual Item", "quantity Item", "Unit", "invalid late Line"} {
		t.Run(scope, func(t *testing.T) {
			fixture, p := purchaseCommands(t)
			s := fixture.store
			key := projectionKey{"purchase", fixture.purchase.ID}
			switch scope {
			case "first Line":
				key = projectionKey{"purchase_line", fixture.stockLine.ID}
			case "last Line":
				key = projectionKey{"purchase_line", fixture.unitLine.ID}
			case "individual Item":
				key = projectionKey{"item", fixture.individual.ID}
			case "quantity Item":
				key = projectionKey{"item", fixture.quantity.ID}
			case "Unit":
				key = projectionKey{"unit", fixture.unit.ID}
			}
			if scope == "invalid late Line" {
				execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,quantity_base,paid_minor,note) VALUES('zzz-invalid-line',?,?,1,0,CAST(x'ff' AS TEXT))", fixture.purchase.ID, fixture.quantity.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase_line','zzz-invalid-line',1)")
			} else {
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
			}
			before := purchaseCommandState(t, s)
			if value, err := callPurchaseCommand(t.Context(), fixture, p, "patch", "invalid-actual-correction-source"); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("invalid late owner source/counter exposed a corrected prefix", err)
			}
			requirePurchaseCommandState(t, s, before)
		})
	}
}

func TestPurchaseActualCommandsAndGetHonorCancellationAndIndependentUnchangedCounters(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := purchaseCommandState(t, s)
	for _, kind := range []string{"create", "patch", "line"} {
		if value, err := callPurchaseCommand(ctx, fixture, p, kind, "canceled-purchase-command"); !errors.Is(err, context.Canceled) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("canceled command returned a financial result", err)
		}
	}
	if value, err := p.Get(ctx, fixture.token, fixture.purchase.ID); !errors.Is(err, context.Canceled) || value != (Purchase{}) {
		t.Fatal("canceled Get returned private source", err)
	}
	requirePurchaseCommandState(t, s, before)
	for _, key := range []projectionKey{{"unit", fixture.unit.ID}, {"purchase_line", fixture.unitLine.ID}, {"purchase_line", fixture.stockLine.ID}} {
		setCatalogProjectionRevision(t, s, key.kind, key.id, maxSafeInteger)
	}
	if _, err := callPurchaseCommand(t.Context(), fixture, p, "line", "independent-actual-allocation"); err != nil {
		t.Fatal("unchanged Unit/immutable Line maximum counter became a false write precondition", err)
	}
	for _, key := range []projectionKey{{"unit", fixture.unit.ID}, {"purchase_line", fixture.unitLine.ID}, {"purchase_line", fixture.stockLine.ID}} {
		if scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id) != maxSafeInteger {
			t.Fatal("new allocation advanced an unchanged independent resource")
		}
	}
}
