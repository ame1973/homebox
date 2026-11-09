package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestUnitEveryStatementAndOwnedCascadeFailureRollsBackSourceSearchHistoryAndOriginalKey(t *testing.T) {
	for _, failure := range []struct{ kind, table, event string }{
		{"create", "item_units", "INSERT"}, {"patch", "item_units", "UPDATE"}, {"move", "item_units", "UPDATE"}, {"status", "item_units", "UPDATE"}, {"delete", "item_units", "DELETE"},
		{"patch", "items", "UPDATE"}, {"patch", "projection_revisions", "INSERT"}, {"patch", "projection_revisions", "UPDATE"},
		{"patch", "current_projections", "INSERT"}, {"patch", "current_projections", "UPDATE"},
		{"patch", "unit_search_documents", "INSERT"}, {"patch", "unit_search_documents", "UPDATE"}, {"delete", "unit_search_documents", "DELETE"},
		{"patch", "inventory_events", "INSERT"}, {"patch", "sync_changes", "INSERT"}, {"patch", "data_revision", "UPDATE"},
		{"patch", "api_idempotency", "INSERT"}, {"patch", "mutation_events", "INSERT"}, {"patch", "sessions", "UPDATE"}, {"delete", "media_links", "DELETE"},
	} {
		t.Run(failure.kind+failure.table+failure.event, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			var command unitCallTest
			for _, candidate := range unitCallsTest(fixture) {
				if candidate.kind == failure.kind {
					command = candidate
				}
			}
			execSQL(t, fixture.store.db, "INSERT INTO unit_search_documents(unit_id,title,revision,updated_at) VALUES(?,'OriginalCache',55,'2026')", fixture.unit.ID)
			if failure.table == "media_links" {
				execSQL(t, fixture.store.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','private',?,'image/png',1,1,1,'2026')", strings.Repeat("a", 64))
				seedMediaProjection(t, fixture.store, "photo")
				execSQL(t, fixture.store.db, "INSERT INTO media_links(id,media_id,item_unit_id) VALUES('unit-link','photo',?)", fixture.unit.ID)
				seedMediaLinkProjection(t, fixture.store, "unit-link")
			}
			before := domainState(t, fixture.store)
			execSQL(t, fixture.store.db, "CREATE TRIGGER fail_real_unit BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'Unit transaction failure'); END")
			if value, err := callUnitTest(t.Context(), inventory, fixture.token, "failed-unit-original-key", command); err == nil || !reflect.ValueOf(value).IsZero() || !strings.Contains(err.Error(), "Unit transaction failure") {
				t.Fatal("targeted Unit statement failure released a partial result or was not reached", err)
			}
			requireDomainState(t, fixture.store, before)
			if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'OriginalCache'") != 1 {
				t.Fatal("failed Unit operation did not restore source FTS cache")
			}
			execSQL(t, fixture.store.db, "DROP TRIGGER fail_real_unit")
			if _, err := callUnitTest(t.Context(), inventory, fixture.token, "failed-unit-original-key", command); err != nil {
				t.Fatal("failed Unit operation retained a claim or poisoned its connection", err)
			}
		})
	}
}

func TestUnitLateAuthorizationAndDeferredCommitFailuresWithholdEveryOperation(t *testing.T) {
	for _, kind := range []string{"create", "patch", "move", "status", "delete"} {
		for _, failure := range []string{"revocation", "epoch", "deferred audit"} {
			t.Run(kind+failure, func(t *testing.T) {
				fixture, inventory := inventoryFixture(t)
				var command unitCallTest
				for _, candidate := range unitCallsTest(fixture) {
					if candidate.kind == kind {
						command = candidate
					}
				}
				statement := "CREATE TRIGGER fail_unit_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END"
				if failure == "epoch" {
					statement = "CREATE TRIGGER fail_unit_late AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='uncommitted-unit-epoch'; END"
				} else if failure == "deferred audit" {
					statement = "CREATE TRIGGER fail_unit_late BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END"
				}
				execSQL(t, fixture.store.db, statement)
				before := domainState(t, fixture.store)
				if value, err := callUnitTest(t.Context(), inventory, fixture.token, "late-unit-original", command); err == nil || !reflect.ValueOf(value).IsZero() || failure != "deferred audit" && !errors.Is(err, ErrAuthentication) {
					t.Fatal("late authorization/COMMIT failure released a Unit result", err)
				}
				requireDomainState(t, fixture.store, before)
				execSQL(t, fixture.store.db, "DROP TRIGGER fail_unit_late")
				if _, err := callUnitTest(t.Context(), inventory, fixture.token, "late-unit-original", command); err != nil {
					t.Fatal("rolled-back late Unit operation retained effects/key", err)
				}
			})
		}
	}
}

func TestUnitCountersSequencesAndFreshIdentityCollisionsNeverReseedOrPartiallyCommit(t *testing.T) {
	for _, scope := range []string{"unit", "item", "source", "target", "root", "search", "media link", "catalog", "sequence", "new Unit reuse", "new event reuse", "event exhaustion", "missing event"} {
		t.Run(scope, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			command := unitCallTest{kind: "move", subject: fixture.unit.ID, proof: fixture.unit.Revision, target: "target"}
			want := ErrRevisionOverflow
			switch scope {
			case "unit":
				setCatalogProjectionRevision(t, fixture.store, "unit", fixture.unit.ID, maxSafeInteger)
				command.proof = maxSafeInteger
			case "item":
				setCatalogProjectionRevision(t, fixture.store, "item", fixture.individual.ID, maxSafeInteger)
			case "source", "target", "root":
				setCatalogProjectionRevision(t, fixture.store, "location", scope, maxSafeInteger)
			case "search":
				execSQL(t, fixture.store.db, "INSERT INTO projection_revisions VALUES('search_doc',?,?)", projectionPair("unit", fixture.unit.ID), maxSafeInteger)
			case "media link":
				command.kind = "delete"
				execSQL(t, fixture.store.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('photo','private',?,'image/png',1,1,1,'2026')", strings.Repeat("a", 64))
				seedMediaProjection(t, fixture.store, "photo")
				execSQL(t, fixture.store.db, "INSERT INTO media_links(id,media_id,item_unit_id) VALUES('unit-link','photo',?)", fixture.unit.ID)
				seedMediaLinkProjection(t, fixture.store, "unit-link")
				setCatalogProjectionRevision(t, fixture.store, "media_link", "unit-link", maxSafeInteger)
			case "catalog":
				execSQL(t, fixture.store.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, fixture.store.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			case "new Unit reuse":
				command = unitCallTest{kind: "create", subject: fixture.individual.ID, proof: fixture.individual.Revision}
				execSQL(t, fixture.store.db, "CREATE TRIGGER collide_new_unit AFTER INSERT ON item_units BEGIN INSERT INTO projection_revisions VALUES('unit',NEW.id,7); END")
				want = ErrDomainState
			case "new event reuse":
				execSQL(t, fixture.store.db, "CREATE TRIGGER collide_new_unit_event AFTER INSERT ON inventory_events BEGIN INSERT INTO projection_revisions VALUES('inventory_event',NEW.id,7); END")
				want = ErrDomainState
			case "event exhaustion":
				execSQL(t, fixture.store.db, fmt.Sprintf("CREATE TRIGGER collide_new_unit_event AFTER INSERT ON inventory_events BEGIN INSERT INTO projection_revisions VALUES('inventory_event',NEW.id,%d); END", maxSafeInteger))
			case "missing event":
				execSQL(t, fixture.store.db, "CREATE TRIGGER collide_new_unit_event BEFORE INSERT ON inventory_events BEGIN SELECT RAISE(IGNORE); END")
				want = ErrNotFound
			}
			before := domainState(t, fixture.store)
			if value, err := callUnitTest(t.Context(), inventory, fixture.token, "exhausted-unit-original", command); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("exhausted/reused/missing identity released physical success", scope, err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestUnitMissingOriginalAuthorityAndInvalidAncestryCannotBecomeRevisionOneOrANewRoot(t *testing.T) {
	for _, scope := range []string{"unit", "item", "source", "target", "root", "cycle", "deleted ancestor", "unsafe ancestor"} {
		t.Run(scope, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			if scope == "cycle" {
				execSQL(t, fixture.store.db, "UPDATE locations SET parent_id='target' WHERE id='root'")
			} else if scope == "deleted ancestor" {
				execSQL(t, fixture.store.db, "UPDATE locations SET deleted_at='2026' WHERE id='root'")
			} else {
				key := projectionKey{"location", scope}
				if scope == "unit" {
					key = projectionKey{"unit", fixture.unit.ID}
				} else if scope == "item" {
					key = projectionKey{"item", fixture.individual.ID}
				} else if scope == "unsafe ancestor" {
					key.id = "root"
				}
				execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
				if scope == "unsafe ancestor" {
					execSQL(t, fixture.store.db, "UPDATE projection_revisions SET revision=? WHERE entity_type=? AND entity_id=?", maxSafeInteger+1, key.kind, key.id)
				} else {
					execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
				}
			}
			before := domainState(t, fixture.store)
			if unit, err := inventory.MoveUnit(t.Context(), fixture.token, "missing-unit-authority", fixture.unit.ID, fixture.unit.Revision, "target"); !errors.Is(err, ErrDomainState) || unit != (Unit{}) {
				t.Fatal("missing source/path authority was reseeded or silently shortened", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestUnitPurchaseReferencesAndAllExplicitNonAssetDisposalKindsRemainDistinct(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	execSQL(t, fixture.store.db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,created_at) VALUES('unit-purchase','Merchant','2026-11-04T12:00:00Z','TWD','2026')")
	execSQL(t, fixture.store.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('unit-line','unit-purchase',?,?,1,0)", fixture.individual.ID, fixture.unit.ID)
	before := domainState(t, fixture.store)
	if ack, err := inventory.DeleteUnit(t.Context(), fixture.token, "zero-paid-unit-reference", fixture.unit.ID, fixture.unit.Revision); !errors.Is(err, ErrUnitHistory) || ack.OK {
		t.Fatal("zero-valued purchase history was treated as no reference", err)
	}
	requireDomainState(t, fixture.store, before)
	for _, kind := range []string{"sold", "donated", "recycled", "discarded"} {
		unit := createUnitTest(t, fixture, inventory, "physical-kind-create-"+kind, nil)
		value, err := inventory.SetUnitStatus(t.Context(), fixture.token, "physical-kind-"+kind, unit.ID, unit.Revision, UnitStatus{Status: "disposed", DisposalKind: &kind})
		if err != nil || value.Status != "disposed" {
			t.Fatal("explicit non-asset disposition was rejected", kind, err)
		}
		requireUnitEventTest(t, fixture, "physical-kind-"+kind, unit.ID, "unit_dispose_"+kind, -1, nil, nil)
		count := scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events WHERE item_unit_id=?", unit.ID)
		if _, err := inventory.SetUnitStatus(t.Context(), fixture.token, "equal-kind-"+kind, unit.ID, value.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("discarded"), Note: "Receipt, not historical reclassification"}); err != nil || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events WHERE item_unit_id=?", unit.ID) != count {
			t.Fatal("equal state rewrote/reclassified immutable disposal history", err)
		}
	}
}
