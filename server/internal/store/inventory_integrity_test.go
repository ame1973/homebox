package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInventoryActiveDestinationsRejectArchivedLeavesAndAncestorsWithoutPartialTransfer(t *testing.T) {
	for _, archived := range []string{"target", "root"} {
		t.Run(archived, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			execSQL(t, fixture.store.db, "UPDATE locations SET status='archived' WHERE id=?", archived)
			before := domainState(t, fixture.store)
			for _, command := range []stockCommand{
				{action: "receive", itemID: fixture.quantity.ID, targetID: "target", quantity: 1},
				{action: "transfer", itemID: fixture.quantity.ID, sourceID: "source", targetID: "target", quantity: 1},
				{action: "adjust", itemID: fixture.quantity.ID, targetID: "target", quantity: 1, reason: "Increase"},
				{action: "adjust", itemID: fixture.quantity.ID, targetID: "root", quantity: 0, reason: "New zero placement"},
			} {
				if command.targetID == "root" && archived != "root" {
					continue
				}
				if reply, err := callStockTest(t.Context(), inventory, fixture.token, "inactive-"+command.action+command.targetID, fixture.quantity.Revision, command); !errors.Is(err, ErrLocationArchived) || reply != (Stock{}) {
					t.Fatal("inactive destination/path admitted new holdings", err)
				}
				requireDomainState(t, fixture.store, before)
			}
		})
	}
}

func TestInventoryOutflowsAndLowerOrEqualCountsCanLeaveAValidArchivedPath(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	execSQL(t, fixture.store.db, "UPDATE locations SET status='archived' WHERE id IN('source','root')")
	execSQL(t, fixture.store.db, "UPDATE locations SET parent_id=NULL WHERE id='target'")
	for index, command := range []stockCommand{
		{action: "consume", sourceID: "source", quantity: 10},
		{action: "transfer", sourceID: "source", targetID: "target", quantity: 10},
		{action: "adjust", targetID: "source", quantity: 1400, reason: "Lower counted stock"},
		{action: "adjust", targetID: "source", quantity: 1400, reason: "Equal count"},
	} {
		command.itemID = fixture.quantity.ID
		if _, err := callStockTest(t.Context(), inventory, fixture.token, fmt.Sprintf("leave-archived-path-%d", index), fixture.quantity.Revision+int64(index), command); err != nil {
			t.Fatal("valid outflow/no-op was confused with new placement", err)
		}
	}
	before := domainState(t, fixture.store)
	for _, command := range []stockCommand{
		{action: "receive", itemID: fixture.quantity.ID, targetID: "source", quantity: 1},
		{action: "adjust", itemID: fixture.quantity.ID, targetID: "source", quantity: 1401, reason: "Increase"},
	} {
		if reply, err := callStockTest(t.Context(), inventory, fixture.token, "cannot-increase-archived-"+command.action, fixture.quantity.Revision+3, command); !errors.Is(err, ErrLocationArchived) || reply != (Stock{}) {
			t.Fatal("archived source accepted an inflow", err)
		}
		requireDomainState(t, fixture.store, before)
	}
}

func TestInventoryRejectsMissingOrUnsafeActualRowOwnerAndEveryAncestorAuthority(t *testing.T) {
	for _, key := range []projectionKey{
		{"item", "owner"}, {"stock", "source"}, {"stock", "target"}, {"location", "source"}, {"location", "target"}, {"location", "root"},
	} {
		for _, unsafe := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/unsafe=%t", key.kind, key.id, unsafe), func(t *testing.T) {
				fixture, inventory := inventoryFixture(t)
				identity := key.id
				if key.kind == "stock" {
					identity = projectionPair(fixture.quantity.ID, key.id)
				} else if key.kind == "item" {
					identity = fixture.quantity.ID
				}
				execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, identity)
				if unsafe {
					execSQL(t, fixture.store.db, "UPDATE projection_revisions SET revision=? WHERE entity_type=? AND entity_id=?", maxSafeInteger+1, key.kind, identity)
				} else {
					execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, identity)
				}
				before := domainState(t, fixture.store)
				reply, err := inventory.Transfer(t.Context(), fixture.token, "invalid-stock-authority", fixture.quantity.Revision, StockTransfer{ItemID: fixture.quantity.ID, SourceLocationID: "source", TargetLocationID: "target", QuantityBase: 10})
				if !errors.Is(err, ErrDomainState) || reply != (Stock{}) {
					t.Fatal("existing source/path acquired invented authority or exposed invalid counters", err)
				}
				requireDomainState(t, fixture.store, before)
			})
		}
	}
}

func TestInventoryMissingDeletedOrCyclicLocationPathsAreNotSilentlyShortened(t *testing.T) {
	for _, failure := range []string{"deleted leaf", "deleted ancestor", "cycle", "invalid ancestor"} {
		t.Run(failure, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			want := ErrDomainState
			switch failure {
			case "deleted leaf":
				execSQL(t, fixture.store.db, "UPDATE locations SET deleted_at='2026' WHERE id='target'")
				want = ErrNotFound
			case "deleted ancestor":
				execSQL(t, fixture.store.db, "UPDATE locations SET deleted_at='2026' WHERE id='root'")
			case "cycle":
				execSQL(t, fixture.store.db, "UPDATE locations SET parent_id='target' WHERE id='root'")
			case "invalid ancestor":
				execSQL(t, fixture.store.db, "UPDATE locations SET description=CAST(x'ff' AS TEXT) WHERE id='root'")
			}
			before := domainState(t, fixture.store)
			if reply, err := inventory.Receive(t.Context(), fixture.token, "bad-physical-path", fixture.quantity.Revision, StockReceive{ItemID: fixture.quantity.ID, TargetLocationID: "target", QuantityBase: 1}); !errors.Is(err, want) || reply != (Stock{}) {
				t.Fatal("invalid ancestry was treated as an active root", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestInventoryDeepPathPublishesOnlyValidatedAncestorsAndNotUnchangedSiblings(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	parent := "root"
	for index := range 140 {
		id := fmt.Sprintf("physical-depth-%03d", index)
		execSQL(t, fixture.store.db, "INSERT INTO locations(id,parent_id,code,name,created_at,updated_at) VALUES(?,?,?,?,'2026','2026')", id, parent, id, id)
		seedCatalogProjection(t, fixture.store, "location", id, LocationData{ID: id, ParentID: testPointer(parent), Code: id, Name: id, Kind: "other", Status: "active"})
		parent = id
	}
	changed := requireProjectionReplay(t, fixture.store, func() {
		if _, err := inventory.Receive(t.Context(), fixture.token, "deep-physical-path", fixture.quantity.Revision, StockReceive{ItemID: fixture.quantity.ID, TargetLocationID: parent, QuantityBase: 1}); err != nil {
			t.Fatal("valid deep path hit an arbitrary depth cap", err)
		}
	})
	if len(changed) != 144 || changed[projectionKey{"location", "source"}] || changed[projectionKey{"location", "target"}] || !changed[projectionKey{"location", "root"}] {
		t.Fatal("deep placement omitted ancestors or rewrote unrelated siblings", len(changed))
	}
}

func TestInventoryEveryStatementFailureRollsBackBothLegsHistoryProjectionsAndOriginalKey(t *testing.T) {
	for _, failure := range []struct{ table, event string }{
		{"stock_balances", "INSERT"}, {"stock_balances", "UPDATE"}, {"items", "UPDATE"}, {"inventory_events", "INSERT"},
		{"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"}, {"current_projections", "INSERT"}, {"current_projections", "UPDATE"},
		{"sync_changes", "INSERT"}, {"data_revision", "UPDATE"}, {"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table+failure.event, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			now := fixture.auth.now().Add(time.Minute)
			fixture.auth.now = func() time.Time { return now }
			command := StockTransfer{ItemID: fixture.quantity.ID, SourceLocationID: "source", TargetLocationID: "target", QuantityBase: 10, Note: "Never partially commit"}
			if failure.table == "stock_balances" && failure.event == "INSERT" {
				command.TargetLocationID = "root"
			}
			before := domainState(t, fixture.store)
			execSQL(t, fixture.store.db, "CREATE TRIGGER fail_real_stock BEFORE "+failure.event+" ON "+failure.table+" BEGIN SELECT RAISE(ABORT,'real stock failure'); END")
			if reply, err := inventory.Transfer(t.Context(), fixture.token, "failed-real-stock-original", fixture.quantity.Revision, command); err == nil || reply != (Stock{}) {
				t.Fatal("statement failure released a partial physical result", err)
			}
			requireDomainState(t, fixture.store, before)
			execSQL(t, fixture.store.db, "DROP TRIGGER fail_real_stock")
			if reply, err := inventory.Transfer(t.Context(), fixture.token, "failed-real-stock-original", fixture.quantity.Revision, command); err != nil || reply.QuantityBase != 10 {
				t.Fatal("rolled-back key/connection could not be explicitly retried", err)
			}
		})
	}
}

func TestInventoryLateAuthorizationAndDeferredMovementAuditAssociationWithholdSuccess(t *testing.T) {
	for _, failure := range []string{"revocation", "epoch", "audit association", "deferred guard"} {
		t.Run(failure, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			statement := "CREATE TRIGGER fail_stock_late AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END"
			switch failure {
			case "epoch":
				statement = "CREATE TRIGGER fail_stock_late AFTER INSERT ON mutation_events BEGIN UPDATE sync_state SET dataset_epoch='uncommitted-epoch'; END"
			case "audit association":
				statement = "CREATE TRIGGER fail_stock_late BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END"
			case "deferred guard":
				execSQL(t, fixture.store.db, "CREATE TABLE real_stock_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
				statement = "CREATE TRIGGER fail_stock_late AFTER INSERT ON mutation_events BEGIN INSERT INTO real_stock_commit_guard VALUES('missing'); END"
			}
			execSQL(t, fixture.store.db, statement)
			before := domainState(t, fixture.store)
			input := StockTransfer{ItemID: fixture.quantity.ID, SourceLocationID: "source", TargetLocationID: "root", QuantityBase: 10}
			if reply, err := inventory.Transfer(t.Context(), fixture.token, "late-stock-original-key", fixture.quantity.Revision, input); err == nil || reply != (Stock{}) || (failure == "revocation" || failure == "epoch") && !errors.Is(err, ErrAuthentication) {
				t.Fatal("late authorization/commit failure exposed uncommitted physical source", err)
			}
			requireDomainState(t, fixture.store, before)
			execSQL(t, fixture.store.db, "DROP TRIGGER fail_stock_late")
			if _, err := inventory.Transfer(t.Context(), fixture.token, "late-stock-original-key", fixture.quantity.Revision, input); err != nil {
				t.Fatal("late rollback retained a key, link or poisoned connection", err)
			}
		})
	}
}

func TestInventoryExhaustedStockItemAncestorCatalogAndSequenceCountersRollBackEverything(t *testing.T) {
	for _, scope := range []string{"item", "source stock", "target stock", "counter-only target", "source", "target", "root", "catalog", "sequence", "event", "event reuse", "missing event"} {
		t.Run(scope, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			s := fixture.store
			input := StockTransfer{ItemID: fixture.quantity.ID, SourceLocationID: "source", TargetLocationID: "target", QuantityBase: 10}
			want := ErrRevisionOverflow
			switch scope {
			case "item":
				setCatalogProjectionRevision(t, s, "item", fixture.quantity.ID, maxSafeInteger)
			case "source stock":
				setCatalogProjectionRevision(t, s, "stock", projectionPair(fixture.quantity.ID, "source"), maxSafeInteger)
			case "target stock":
				setCatalogProjectionRevision(t, s, "stock", projectionPair(fixture.quantity.ID, "target"), maxSafeInteger)
			case "counter-only target":
				input.TargetLocationID = "root"
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('stock',?,?)", projectionPair(fixture.quantity.ID, "root"), maxSafeInteger)
			case "source", "target", "root":
				setCatalogProjectionRevision(t, s, "location", scope, maxSafeInteger)
			case "catalog":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			case "event":
				execSQL(t, s.db, fmt.Sprintf("CREATE TRIGGER exhaust_real_event AFTER INSERT ON inventory_events BEGIN INSERT INTO projection_revisions VALUES('inventory_event',NEW.id,%d); END", maxSafeInteger))
			case "event reuse":
				execSQL(t, s.db, "CREATE TRIGGER exhaust_real_event AFTER INSERT ON inventory_events BEGIN INSERT INTO projection_revisions VALUES('inventory_event',NEW.id,7); END")
				want = ErrDomainState
			case "missing event":
				execSQL(t, s.db, "CREATE TRIGGER exhaust_real_event BEFORE INSERT ON inventory_events BEGIN SELECT RAISE(IGNORE); END")
				want = ErrNotFound
			}
			proof := fixture.quantity.Revision
			if scope == "item" {
				proof = maxSafeInteger
			}
			before := domainState(t, s)
			if reply, err := inventory.Transfer(t.Context(), fixture.token, "exhausted-real-stock", proof, input); !errors.Is(err, want) || reply != (Stock{}) {
				t.Fatal("exhausted/reused/missing publication identity released physical success", err)
			}
			requireDomainState(t, s, before)
		})
	}
}
