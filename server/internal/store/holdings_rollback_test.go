package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestHoldingPublicationRollsBackEverySourceDependencyProjectionAndReceiptFailure(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"item_units", "UPDATE"}, {"stock_balances", "UPDATE"}, {"items", "UPDATE"},
		{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
		{"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
		{"search_documents", "INSERT"}, {"search_documents", "UPDATE"},
		{"unit_search_documents", "INSERT"}, {"unit_search_documents", "UPDATE"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"},
		{"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			fixture := holdingsFixture(t)
			s := fixture.store
			execSQL(t, s.db, "INSERT INTO unit_search_documents(unit_id,title,revision,updated_at) VALUES(?,'OriginalUnitCache',55,'2026')", fixture.unit.ID)
			now := fixture.auth.now().Add(time.Minute)
			fixture.auth.now = func() time.Time { return now }
			before := domainState(t, s)
			execSQL(t, s.db, "CREATE TRIGGER fail_holding BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'holding publication failure'); END")
			if value, err := mutateHoldingProjectionFixture(t.Context(), fixture, "failed-holding-original-key", "Final", 1490, 10); err == nil || value != (Stock{}) {
				t.Fatal("failed physical publication exposed a partial success", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'FinalNotes'") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'OriginalUnitCache'") != 1 {
				t.Fatal("rolled-back holding publication left partial FTS state")
			}
		})
	}
}

func TestHoldingPublicationLateAuthorizationAndDeferredCommitWithholdIndependentStockDTO(t *testing.T) {
	for _, failure := range []string{"late revocation", "deferred commit"} {
		t.Run(failure, func(t *testing.T) {
			fixture := holdingsFixture(t)
			s := fixture.store
			if failure == "late revocation" {
				execSQL(t, s.db, "CREATE TRIGGER fail_holding_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
			} else {
				execSQL(t, s.db, "CREATE TABLE holding_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				execSQL(t, s.db, "CREATE TRIGGER fail_holding_late AFTER INSERT ON mutation_events BEGIN INSERT INTO holding_commit_guard VALUES('missing'); END")
			}
			before := domainState(t, s)
			if value, err := mutateHoldingProjectionFixture(t.Context(), fixture, "late-holding-original-key", "Final", 1490, 10); err == nil || value != (Stock{}) || failure == "late revocation" && !errors.Is(err, ErrAuthentication) {
				t.Fatal("late auth/COMMIT failure released a physical success", err)
			}
			requireDomainState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts") != 0 {
				t.Fatal("failed final authorization/COMMIT left unit search publication")
			}
		})
	}
}

func TestHoldingPublicationRejectsMissingOriginalRowAndOwnerAuthorityInsteadOfReseedingIt(t *testing.T) {
	for _, scope := range []string{"unit", "source stock", "target stock", "unit owner", "stock owner"} {
		t.Run(scope, func(t *testing.T) {
			fixture := holdingsFixture(t)
			s := fixture.store
			key := projectionKey{"unit", fixture.unit.ID}
			switch scope {
			case "source stock":
				key = projectionKey{"stock", projectionPair(fixture.stock.ItemID, fixture.stock.LocationID)}
			case "target stock":
				key = projectionKey{"stock", projectionPair(fixture.target.ItemID, fixture.target.LocationID)}
			case "unit owner":
				key = projectionKey{"item", fixture.individual.ID}
			case "stock owner":
				key = projectionKey{"item", fixture.quantity.ID}
			}
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
			execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
			before := domainState(t, s)
			if value, err := mutateHoldingProjectionFixture(t.Context(), fixture, "missing-holding-original-key", "Final", 1490, 10); !errors.Is(err, ErrDomainState) || value != (Stock{}) {
				t.Fatal("existing holding acquired fictional revision one", scope, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestHoldingPublicationCounterAndSequenceExhaustionRollBackEveryDependency(t *testing.T) {
	for _, scope := range []string{"unit", "source stock", "target stock", "unit owner", "stock owner", "root", "source", "target", "unit search", "item search", "catalog revision", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture := holdingsFixture(t)
			s := fixture.store
			key := projectionKey{"unit", fixture.unit.ID}
			switch scope {
			case "source stock":
				key = projectionKey{"stock", projectionPair(fixture.stock.ItemID, fixture.stock.LocationID)}
			case "target stock":
				key = projectionKey{"stock", projectionPair(fixture.target.ItemID, fixture.target.LocationID)}
			case "unit owner":
				key = projectionKey{"item", fixture.individual.ID}
			case "stock owner":
				key = projectionKey{"item", fixture.quantity.ID}
			case "root", "source", "target":
				key = projectionKey{"location", scope}
			case "unit search":
				key = projectionKey{"search_doc", projectionPair("unit", fixture.unit.ID)}
				seedCatalogProjection(t, s, key.kind, key.id, searchData{EntityType: "unit", EntityID: fixture.unit.ID, ApprovedText: "Original"})
			case "item search":
				key = projectionKey{"search_doc", projectionPair("item", fixture.individual.ID)}
			}
			if scope == "catalog revision" {
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			} else if scope == "sequence" {
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			} else {
				setCatalogProjectionRevision(t, s, key.kind, key.id, maxSafeInteger)
			}
			before := domainState(t, s)
			if value, err := mutateHoldingProjectionFixture(t.Context(), fixture, "exhausted-holding-original-key", "Final", 1490, 10); !errors.Is(err, ErrRevisionOverflow) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("exhausted holding/dependent authority released success", scope, err)
			}
			requireDomainState(t, s, before)
		})
	}
}
