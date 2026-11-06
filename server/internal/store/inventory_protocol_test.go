package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInventoryFrozenRepliesBindEveryOriginalFieldProofAndOperationAcrossFreshLogins(t *testing.T) {
	for _, action := range []string{"receive", "consume", "transfer", "adjust"} {
		t.Run(action, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			var original stockCommand
			for _, command := range stockCommandsTest(fixture.quantity.ID) {
				if command.action == action {
					original = command
				}
			}
			proof := fixture.quantity.Revision
			first, err := callStockTest(t.Context(), inventory, fixture.token, "stock-original-request", proof, original)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = inventory.Adjust(t.Context(), fixture.token, "stock-later-edit", proof+1, StockAdjust{ItemID: first.ItemID, TargetLocationID: first.LocationID, QuantityBase: first.QuantityBase + 1, Reason: "Later real stock mutation"}); err != nil {
				t.Fatal(err)
			}
			_, fresh := login(t, fixture.auth)
			before := domainState(t, fixture.store)
			for _, token := range []string{fixture.token, fresh} {
				if frozen, err := callStockTest(t.Context(), inventory, token, "stock-original-request", proof, original); err != nil || frozen != first {
					t.Fatal("same-principal reconciliation silently refreshed body/proof/reply", err)
				}
				requireDomainState(t, fixture.store, before)
			}
			for _, field := range []string{"item", "source", "target", "quantity", "note", "reason", "proof", "operation"} {
				if field == "source" && original.sourceID == "" || field == "target" && original.targetID == "" || field == "reason" && action != "adjust" {
					continue
				}
				command, changedProof := original, proof
				switch field {
				case "item":
					command.itemID += "-other"
				case "source":
					command.sourceID += "-other"
				case "target":
					command.targetID += "-other"
				case "quantity":
					command.quantity++
				case "note":
					command.note += "changed"
				case "reason":
					command.reason += "changed"
				case "proof":
					changedProof++
				case "operation":
					command.action, command.sourceID, command.targetID = "receive", "", "root"
					if action == "receive" {
						command.action, command.sourceID, command.targetID = "consume", "source", ""
					}
				}
				if reply, err := callStockTest(t.Context(), inventory, fresh, "stock-original-request", changedProof, command); !errors.Is(err, ErrIdempotencyConflict) || reply != (Stock{}) {
					t.Fatal("changed original reused an immutable domain key", field, err)
				}
				requireDomainState(t, fixture.store, before)
			}
			// Deliberate test-only source removal proves that replay does not fetch
			// a new balance/proof. Ordinary consume/transfer never deletes zero rows.
			execSQL(t, fixture.store.db, "DELETE FROM stock_balances WHERE item_id=? AND location_id=?", first.ItemID, first.LocationID)
			execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='stock' AND entity_id=?", projectionPair(first.ItemID, first.LocationID))
			before = domainState(t, fixture.store)
			if frozen, err := callStockTest(t.Context(), inventory, fresh, "stock-original-request", proof, original); err != nil || frozen != first {
				t.Fatal("original acknowledgement depended on still-current source", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestInventoryDomainKeysRemainEpochScopedWithoutGlobalRawMovementKeyCollisions(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	s := fixture.store
	input := StockReceive{ItemID: fixture.quantity.ID, TargetLocationID: "target", QuantityBase: 10}
	first, err := inventory.Receive(t.Context(), fixture.token, "same-caller-key-two-epochs", fixture.quantity.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-epoch'")
	before := domainState(t, s)
	if reply, err := inventory.Receive(t.Context(), fixture.token, "same-caller-key-two-epochs", fixture.quantity.Revision, input); !errors.Is(err, ErrAuthentication) || reply != (Stock{}) {
		t.Fatal("restore resurrected a session or frozen reply", err)
	}
	requireDomainState(t, s, before)
	_, fixture.token = login(t, fixture.auth)
	before = domainState(t, s)
	if reply, err := inventory.Receive(t.Context(), fixture.token, "same-caller-key-two-epochs", fixture.quantity.Revision, input); !errors.Is(err, ErrRevisionConflict) || reply != (Stock{}) {
		t.Fatal("a new epoch reused an old epoch acknowledgement/proof", err)
	}
	requireDomainState(t, s, before)
	second, err := inventory.Receive(t.Context(), fixture.token, "same-caller-key-two-epochs", fixture.quantity.Revision+1, input)
	if err != nil || second.QuantityBase != first.QuantityBase+10 || second.Revision != first.Revision+1 {
		t.Fatal("an unscoped inventory-event key blocked a valid newly reviewed epoch command", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM inventory_events WHERE idempotency_key IS NULL") != 2 || scalar[int](t, s.db, "SELECT COUNT(DISTINCT dataset_epoch) FROM mutation_events WHERE operation='post_stock_receive'") != 2 {
		t.Fatal("movements bypassed scoped immutable receipts")
	}
}

func TestInventoryAllCommandsAndFrozenRepliesRequireCompletedCurrentEpochSessions(t *testing.T) {
	for _, state := range []string{"mandatory", "revoked", "expired", "idle", "epoch", "invalid"} {
		t.Run(state, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			commands := stockCommandsTest(fixture.quantity.ID)
			for index, command := range commands {
				if _, err := callStockTest(t.Context(), inventory, fixture.token, "authorized-original-"+command.action, fixture.quantity.Revision+int64(index), command); err != nil {
					t.Fatal(err)
				}
			}
			want := ErrAuthentication
			switch state {
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "expired":
				execSQL(t, fixture.store.db, "UPDATE sessions SET expires_at=?", stamp(fixture.auth.now()))
			case "idle":
				execSQL(t, fixture.store.db, "UPDATE sessions SET last_seen_at=?", stamp(fixture.auth.now().Add(-31*24*time.Hour)))
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='another-epoch'")
			case "invalid":
				fixture.token = "invalid"
			}
			before := domainState(t, fixture.store)
			for index, command := range commands {
				for _, replay := range []bool{false, true} {
					key, proof := "unauthorized-new-"+command.action, fixture.quantity.Revision+4
					if replay {
						key, proof = "authorized-original-"+command.action, fixture.quantity.Revision+int64(index)
					}
					if reply, err := callStockTest(t.Context(), inventory, fixture.token, key, proof, command); !errors.Is(err, want) || reply != (Stock{}) {
						t.Fatal("physical command/replay escaped transactional authorization", command.action, replay, err)
					}
					requireDomainState(t, fixture.store, before)
				}
			}
		})
	}
}

func TestInventoryExpiredClaimsAndCanceledCallsCannotBecomeAnotherPhysicalWrite(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	commands := stockCommandsTest(fixture.quantity.ID)
	for index, command := range commands {
		if _, err := callStockTest(t.Context(), inventory, fixture.token, "stock-expiry-"+command.action, fixture.quantity.Revision+int64(index), command); err != nil {
			t.Fatal(err)
		}
	}
	now := fixture.auth.now().Add(domainReplyLifetime)
	fixture.auth.now = func() time.Time { return now }
	_, fixture.token = login(t, fixture.auth)
	before := domainState(t, fixture.store)
	for index, command := range commands {
		if reply, err := callStockTest(t.Context(), inventory, fixture.token, "stock-expiry-"+command.action, fixture.quantity.Revision+int64(index), command); !errors.Is(err, ErrIdempotencyExpired) || reply != (Stock{}) {
			t.Fatal("expired physical claim became a fresh mutation", command.action, err)
		}
		requireDomainState(t, fixture.store, before)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		for _, key := range []string{"stock-expiry-" + command.action, "canceled-new-" + command.action} {
			if reply, err := callStockTest(ctx, inventory, fixture.token, key, fixture.quantity.Revision+int64(index), command); !errors.Is(err, context.Canceled) || reply != (Stock{}) {
				t.Fatal("canceled command/replay released a physical reply", command.action, err)
			}
			requireDomainState(t, fixture.store, before)
		}
	}
}

func TestInventoryConcurrentOriginalsAndCompetingItemProofsHaveExactlyOnePhysicalCommit(t *testing.T) {
	for _, action := range []string{"receive", "consume", "transfer", "adjust"} {
		for _, duplicate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duplicate=%t", action, duplicate), func(t *testing.T) {
				fixture, inventory := inventoryFixture(t)
				var command stockCommand
				for _, candidate := range stockCommandsTest(fixture.quantity.ID) {
					if candidate.action == action {
						command = candidate
					}
				}
				type outcome struct {
					value Stock
					err   error
				}
				results, start := make(chan outcome, 8), make(chan struct{})
				for index := range 8 {
					go func(index int) {
						<-start
						key := "concurrent-physical-original"
						if !duplicate {
							key = fmt.Sprintf("competing-physical-%d", index)
						}
						value, err := callStockTest(t.Context(), inventory, fixture.token, key, fixture.quantity.Revision, command)
						results <- outcome{value, err}
					}(index)
				}
				close(start)
				var first Stock
				successes, conflicts := 0, 0
				for range 8 {
					result := <-results
					if result.err == nil {
						if successes > 0 && result.value != first {
							t.Fatal("duplicate original returned different frozen balances")
						}
						first = result.value
						successes++
					} else if !duplicate && errors.Is(result.err, ErrRevisionConflict) && result.value == (Stock{}) {
						conflicts++
					} else {
						t.Fatal("concurrent physical request returned unexpected outcome", result.err)
					}
				}
				want := 1
				if duplicate {
					want = 8
				}
				if successes != want || conflicts != 8-want || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM mutation_events WHERE operation=?", "post_stock_"+action) != 1 || getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID).Revision != fixture.quantity.Revision+1 {
					t.Fatal("concurrent physical requests duplicated source/history or accepted a stale Item scope")
				}
			})
		}
	}
}

func TestInventoryOppositeTransfersConserveBaseQuantityAndCannotShareAnOriginalItemProof(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for index, location := range []string{"source", "target"} {
		if _, err := inventory.Adjust(t.Context(), fixture.token, "prepare-opposite-"+location, fixture.quantity.Revision+int64(index), StockAdjust{ItemID: fixture.quantity.ID, TargetLocationID: location, QuantityBase: 750, Reason: "Reviewed opening balance"}); err != nil {
			t.Fatal(err)
		}
	}
	start, results := make(chan struct{}), make(chan error, 2)
	for _, locations := range [][2]string{{"source", "target"}, {"target", "source"}} {
		go func(locations [2]string) {
			<-start
			_, err := inventory.Transfer(t.Context(), fixture.token, "opposite-transfer-"+locations[0], fixture.quantity.Revision+2, StockTransfer{ItemID: fixture.quantity.ID, SourceLocationID: locations[0], TargetLocationID: locations[1], QuantityBase: 500})
			results <- err
		}(locations)
	}
	close(start)
	first, second := <-results, <-results
	if !(first == nil && errors.Is(second, ErrRevisionConflict) || second == nil && errors.Is(first, ErrRevisionConflict)) || scalar[int](t, fixture.store.db, "SELECT SUM(quantity_base) FROM stock_balances WHERE item_id=?", fixture.quantity.ID) != 1500 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events WHERE action='transfer'") != 1 {
		t.Fatal("opposite transfers duplicated a reviewed scope or destroyed conservation", first, second)
	}
}
