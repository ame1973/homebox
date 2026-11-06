package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func inventoryFixture(t *testing.T) (holdingFixture, *Inventory) {
	t.Helper()
	fixture := holdingsFixture(t)
	inventory, err := NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, inventory
}

// Drive only the public methods with their distinct typed bodies. The private
// command is reused as table data, never passed to a production apply callback.
func callStockTest(ctx context.Context, inventory *Inventory, token, key string, revision int64, command stockCommand) (Stock, error) {
	switch command.action {
	case "receive":
		return inventory.Receive(ctx, token, key, revision, StockReceive{command.itemID, command.targetID, command.quantity, command.note})
	case "consume":
		return inventory.Consume(ctx, token, key, revision, StockConsume{command.itemID, command.sourceID, command.quantity, command.note})
	case "transfer":
		return inventory.Transfer(ctx, token, key, revision, StockTransfer{command.itemID, command.sourceID, command.targetID, command.quantity, command.note})
	default:
		return inventory.Adjust(ctx, token, key, revision, StockAdjust{command.itemID, command.targetID, command.quantity, command.reason, command.note})
	}
}

func stockCommandsTest(itemID string) []stockCommand {
	return []stockCommand{
		{action: "receive", itemID: itemID, targetID: "target", quantity: 10, note: "  Receive é\n"},
		{action: "consume", itemID: itemID, sourceID: "source", quantity: 10, note: "  Consume é\n"},
		{action: "transfer", itemID: itemID, sourceID: "source", targetID: "target", quantity: 10, note: "  Transfer é\n"},
		{action: "adjust", itemID: itemID, targetID: "source", quantity: 500, reason: "  Recount é\n", note: "  Adjust é\n"},
	}
}

func stockScopedKeyTest(t *testing.T, fixture holdingFixture, key string) string {
	t.Helper()
	return domainReplyKey(scalar[string](t, fixture.store.db, "SELECT id FROM admin_accounts"), scalar[string](t, fixture.store.db, "SELECT dataset_epoch FROM sync_state"), key)
}

func stockEventTest(t *testing.T, fixture holdingFixture, key string) InventoryEventData {
	t.Helper()
	s := fixture.store
	id := scalar[string](t, s.db, `SELECT e.id FROM inventory_events e JOIN mutation_events m ON m.id=e.mutation_event_id WHERE m.idempotency_key=?`, stockScopedKeyTest(t, fixture, key))
	value, err := loadInventoryEvent(t.Context(), s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='inventory_event' AND entity_id=?", id) != 1 ||
		scalar[int](t, s.db, "SELECT idempotency_key IS NULL AND id<>mutation_event_id FROM inventory_events WHERE id=?", id) != 1 {
		t.Fatal("movement identity/authority was conflated with audit or an unscoped caller key")
	}
	return value
}

func TestInventoryConstructorsRequireTheOwningPrivateStore(t *testing.T) {
	for _, auth := range []*Auth{nil, {}} {
		if value, err := NewInventory(auth); !errors.Is(err, ErrDomainInput) || value != nil {
			t.Fatal("invalid auth acquired inventory authority", err)
		}
	}
	var absent *Store
	if value, err := absent.NewInventory(&Auth{}); !errors.Is(err, ErrDomainInput) || value != nil {
		t.Fatal("nil store acquired inventory authority", err)
	}
	s := openTestStore(t)
	for _, auth := range []*Auth{nil, {store: &Store{}}} {
		if value, err := s.NewInventory(auth); !errors.Is(err, ErrDomainInput) || value != nil {
			t.Fatal("foreign auth acquired inventory authority", err)
		}
	}
}

func TestInventoryCommandsPublishExactFinalBalancesDirectionsDependenciesAndReceipts(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	s := fixture.store
	setCatalogProjectionRevision(t, s, "item", fixture.quantity.ID, 7)
	before := domainState(t, s)
	for index, step := range []struct {
		command                 stockCommand
		location                string
		quantity, eventQuantity int64
		from, to                *string
		count                   int
	}{
		{stockCommand{action: "receive", targetID: "target", quantity: 200, note: "\x00Exact receive é\n"}, "target", 200, 200, nil, testPointer("target"), 5},
		{stockCommand{action: "consume", sourceID: "source", quantity: 500, note: "Consume"}, "source", 1000, -500, testPointer("source"), nil, 5},
		{stockCommand{action: "transfer", sourceID: "source", targetID: "target", quantity: 1000, note: "Transfer"}, "target", 1200, 1000, testPointer("source"), testPointer("target"), 7},
		{stockCommand{action: "adjust", targetID: "target", quantity: 1250, reason: "\x00Exact recount é\n", note: "Adjust up"}, "target", 1250, 50, nil, testPointer("target"), 5},
		{stockCommand{action: "adjust", targetID: "target", quantity: 0, reason: "Clear counted balance", note: "Adjust down"}, "target", 0, -1250, testPointer("target"), nil, 5},
	} {
		command := step.command
		command.itemID = fixture.quantity.ID
		key := fmt.Sprintf("physical-command-%d", index)
		var reply Stock
		changed := requireProjectionReplay(t, s, func() {
			var err error
			reply, err = callStockTest(t.Context(), inventory, fixture.token, key, int64(7+index), command)
			if err != nil {
				t.Fatal("real stock command failed", command.action, err)
			}
		})
		event := stockEventTest(t, fixture, key)
		if reply.ItemID != fixture.quantity.ID || reply.LocationID != step.location || reply.QuantityBase != step.quantity ||
			reply.UnitCode != "kg" || reply.BaseUnit != "g" || reply.ScaleFactor != 1000 || reply.Revision == int64(8+index) ||
			event.ItemID != fixture.quantity.ID || event.Action != command.action || event.QuantityBase != step.eventQuantity || event.ItemUnitID != nil ||
			!sameParent(event.FromLocationID, step.from) || !sameParent(event.ToLocationID, step.to) || event.OccurredAt != stamp(fixture.auth.now()) {
			t.Fatal("command changed units, response scope, exact direction or signed quantity", command.action, reply, event)
		}
		if len(changed) != step.count || !changed[projectionKey{"stock", projectionPair(reply.ItemID, reply.LocationID)}] ||
			!changed[projectionKey{"item", reply.ItemID}] || !changed[projectionKey{"location", "root"}] || !changed[projectionKey{"inventory_event", event.ID}] ||
			getCatalogItem(t, fixture.catalog, fixture.token, reply.ItemID).Revision != int64(8+index) {
			t.Fatal("missing/duplicate physical, Item or shared-ancestor publication", changed)
		}
		if command.action == "transfer" && (!changed[projectionKey{"stock", projectionPair(reply.ItemID, "source")}] || scalar[int](t, s.db, "SELECT SUM(quantity_base) FROM stock_balances WHERE item_id=?", reply.ItemID) != 1200) {
			t.Fatal("transfer omitted its source or changed total base quantity")
		}
		for _, row := range []struct {
			kind, id string
			payload  any
		}{
			{"stock", projectionPair(reply.ItemID, reply.LocationID), reply.StockData}, {"inventory_event", event.ID, event},
		} {
			encoded, err := json.Marshal(row.payload)
			if err != nil || scalar[string](t, s.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", row.kind, row.id) != string(encoded) {
				t.Fatal("final materialization differs from exact version-one source", err)
			}
		}
		for _, table := range []string{"inventory_events", "mutation_events"} {
			where, id := "mutation_event_id", scalar[string](t, s.db, "SELECT mutation_event_id FROM inventory_events WHERE id=?", event.ID)
			if table == "mutation_events" {
				where = "id"
			}
			if scalar[string](t, s.db, "SELECT note FROM "+table+" WHERE "+where+"=?", id) != command.note || scalar[string](t, s.db, "SELECT reason FROM "+table+" WHERE "+where+"=?", id) != command.reason {
				t.Fatal("immutable context was trimmed, repaired or dropped", table)
			}
		}
		encoded, _ := json.Marshal(reply)
		if scalar[string](t, s.db, "SELECT response_json FROM api_idempotency WHERE key=?", stockScopedKeyTest(t, fixture, key)) != string(encoded) {
			t.Fatal("original independent Stock reply was not frozen")
		}
	}
	after := domainState(t, s)
	for _, table := range []string{"categories", "tags", "item_tags", "item_units", "item_barcodes", "item_aliases", "item_attributes", "purchases", "purchase_lines", "media", "media_links", "ai_jobs", "ai_review_entries", "ocr_results", "search_documents", "unit_search_documents"} {
		if before[table] != after[table] {
			t.Fatal("quantity-only command touched unrelated metadata, finance, AI or search", table)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM stock_balances WHERE item_id=? AND quantity_base=0", fixture.quantity.ID) != 2 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='stock' AND operation='delete'") != 0 {
		t.Fatal("depletion deleted retained zero balance/history")
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE projection_json LIKE '%Exact receive%' OR projection_json LIKE '%Exact recount%'") != 0 {
		t.Fatal("private producer notes/reasons leaked into offline payloads")
	}
	for _, table := range []string{"inventory_events", "mutation_events", "api_idempotency"} {
		if strings.Contains(after[table], fixture.token) || strings.Contains(after[table], testAdminPassword) {
			t.Fatal("physical history persisted an authentication credential", table)
		}
	}
}

func TestInventoryEqualAdjustmentKeepsExactReceiptWithoutInventingMovementOrNewAuthority(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	s := fixture.store
	command := StockAdjust{ItemID: fixture.quantity.ID, TargetLocationID: "target", QuantityBase: 0, Reason: "\x00Rechecked é\n", Note: "\x00No movement é\n"}
	setCatalogProjectionRevision(t, s, "item", fixture.quantity.ID, maxSafeInteger)
	setCatalogProjectionRevision(t, s, "stock", projectionPair(fixture.quantity.ID, "target"), maxSafeInteger)
	for _, id := range []string{"target", "root"} {
		setCatalogProjectionRevision(t, s, "location", id, maxSafeInteger)
	}
	before := domainState(t, s)
	changed := requireProjectionReplay(t, s, func() {
		reply, err := inventory.Adjust(t.Context(), fixture.token, "equal-adjustment-key", maxSafeInteger, command)
		if err != nil || reply.QuantityBase != 0 || reply.Revision != maxSafeInteger {
			t.Fatal("equal absolute count invented an increment", err)
		}
	})
	if len(changed) != 0 {
		t.Fatal("no-op adjustment published physical changes", changed)
	}
	after := domainState(t, s)
	for _, table := range []string{"items", "locations", "stock_balances", "inventory_events", "projection_revisions", "current_projections", "sync_changes", "data_revision", "sqlite_sequence"} {
		if before[table] != after[table] {
			t.Fatal("no-op changed source or counters", table)
		}
	}
	if scalar[string](t, s.db, "SELECT note FROM mutation_events WHERE idempotency_key=?", stockScopedKeyTest(t, fixture, "equal-adjustment-key")) != command.Note || scalar[string](t, s.db, "SELECT reason FROM mutation_events WHERE idempotency_key=?", stockScopedKeyTest(t, fixture, "equal-adjustment-key")) != command.Reason {
		t.Fatal("a no-op lost its exact immutable context")
	}
}

func TestInventoryExplicitNewZeroAndRetainedPairRecreationAreNotFakeExistingBalances(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	s := fixture.store
	pair := projectionPair(fixture.quantity.ID, "root")
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('stock',?,7)", pair)
	reply, err := inventory.Adjust(t.Context(), fixture.token, "explicit-new-zero", fixture.quantity.Revision, StockAdjust{ItemID: fixture.quantity.ID, TargetLocationID: "root", QuantityBase: 0, Reason: "Explicit empty placement"})
	if err != nil || reply.Revision != 8 || reply.QuantityBase != 0 {
		t.Fatal("new retained zero was omitted or reset its previous pair history", err)
	}
	event := stockEventTest(t, fixture, "explicit-new-zero")
	if event.QuantityBase != 0 || event.FromLocationID != nil || event.ToLocationID == nil || *event.ToLocationID != "root" {
		t.Fatal("zero-row creation did not preserve explicit placement semantics")
	}
	reply, err = inventory.Receive(t.Context(), fixture.token, "receive-after-zero", fixture.quantity.Revision+1, StockReceive{ItemID: fixture.quantity.ID, TargetLocationID: "root", QuantityBase: 1})
	if err != nil || reply.Revision != 9 || reply.QuantityBase != 1 {
		t.Fatal("explicit receipt did not use retained source/counter", err)
	}
}

func TestInventorySafeExtremesNeverRescaleRoundOrOverflow(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	s := fixture.store
	for index, command := range []stockCommand{
		{action: "receive", targetID: "root", quantity: maxSafeInteger},
		{action: "consume", sourceID: "root", quantity: maxSafeInteger},
		{action: "adjust", targetID: "root", quantity: maxSafeInteger, reason: "Exact maximum"},
		{action: "adjust", targetID: "root", quantity: 0, reason: "Exact zero"},
	} {
		command.itemID = fixture.quantity.ID
		key := fmt.Sprintf("safe-extreme-%d", index)
		reply, err := callStockTest(t.Context(), inventory, fixture.token, key, fixture.quantity.Revision+int64(index), command)
		if err != nil || (index%2 == 0 && reply.QuantityBase != maxSafeInteger) || (index%2 == 1 && reply.QuantityBase != 0) {
			t.Fatal("safe endpoint was converted or rounded", err)
		}
		event := stockEventTest(t, fixture, key)
		want := maxSafeInteger
		if index%2 == 1 {
			want = -want
		}
		if event.QuantityBase != want {
			t.Fatal("derived extreme movement overflowed", event.QuantityBase)
		}
	}
	execSQL(t, s.db, "UPDATE stock_balances SET quantity_base=? WHERE item_id=? AND location_id='target'", maxSafeInteger, fixture.quantity.ID)
	execSQL(t, s.db, "CREATE TRIGGER stock_arithmetic_precedes_write BEFORE UPDATE ON stock_balances BEGIN SELECT RAISE(ABORT,'arithmetic must be checked first'); END")
	for _, action := range []string{"receive", "transfer"} {
		before := domainState(t, s)
		command := stockCommand{action: action, itemID: fixture.quantity.ID, targetID: "target", quantity: 1}
		if action == "transfer" {
			command.sourceID = "source"
		}
		if reply, err := callStockTest(t.Context(), inventory, fixture.token, "overflow-"+action, fixture.quantity.Revision+4, command); !errors.Is(err, ErrStockOverflow) || reply != (Stock{}) {
			t.Fatal("target overflow was not checked before either write", err)
		}
		requireDomainState(t, s, before)
	}
}

func TestInventoryTypedBoundsRejectUnsafeIDsQuantitiesNotesReasonsAndMissingOriginalProof(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for _, original := range stockCommandsTest(fixture.quantity.ID) {
		for _, invalid := range []string{"item empty", "item wide", "item UTF", "source empty", "source wide", "source UTF", "target empty", "target wide", "target UTF", "same locations", "negative", "unsafe", "zero", "note wide", "note UTF", "reason empty", "reason wide", "reason UTF", "proof absent", "proof negative", "proof unsafe", "key short", "key wide", "key UTF"} {
			if strings.HasPrefix(invalid, "source") && original.sourceID == "" || strings.HasPrefix(invalid, "target") && original.targetID == "" || invalid == "same locations" && original.action != "transfer" || invalid == "zero" && original.action == "adjust" || strings.HasPrefix(invalid, "reason") && original.action != "adjust" {
				continue
			}
			t.Run(original.action+"/"+invalid, func(t *testing.T) {
				command, revision, key, want := original, fixture.quantity.Revision, "invalid-stock-input", ErrDomainInput
				switch invalid {
				case "item empty":
					command.itemID = ""
				case "item wide":
					command.itemID = strings.Repeat("界", 129)
				case "item UTF":
					command.itemID = string([]byte{0xff})
				case "source empty":
					command.sourceID = ""
				case "source wide":
					command.sourceID = strings.Repeat("界", 129)
				case "source UTF":
					command.sourceID = string([]byte{0xff})
				case "target empty":
					command.targetID = ""
				case "target wide":
					command.targetID = strings.Repeat("界", 129)
				case "target UTF":
					command.targetID = string([]byte{0xff})
				case "same locations":
					command.targetID = command.sourceID
				case "negative":
					command.quantity = -1
				case "unsafe":
					command.quantity = maxSafeInteger + 1
				case "zero":
					command.quantity = 0
				case "note wide":
					command.note = "\x00" + strings.Repeat("界", 10000)
				case "note UTF":
					command.note = string([]byte{0xff})
				case "reason empty":
					command.reason = ""
				case "reason wide":
					command.reason = "\x00" + strings.Repeat("界", 10000)
				case "reason UTF":
					command.reason = string([]byte{0xff})
				case "proof absent":
					revision, want = 0, ErrPreconditionRequired
				case "proof negative":
					revision = -1
				case "proof unsafe":
					revision = maxSafeInteger + 1
				case "key short":
					key = "short"
				case "key wide":
					key = strings.Repeat("界", 129)
				case "key UTF":
					key = "invalid-" + string([]byte{0xff})
				}
				before := domainState(t, fixture.store)
				if reply, err := callStockTest(t.Context(), inventory, fixture.token, key, revision, command); !errors.Is(err, want) || reply != (Stock{}) {
					t.Fatal("invalid typed command released a Stock DTO", err)
				}
				requireDomainState(t, fixture.store, before)
			})
		}
	}
}

func TestInventoryRuntimeGuardsRejectTrackingStaleScopeMissingLocationsAndInsufficientSource(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for _, original := range stockCommandsTest(fixture.quantity.ID) {
		for _, guard := range []string{"mode", "missing item", "stale proof", "missing leaf", "insufficient", "missing balance"} {
			if (guard == "insufficient" || guard == "missing balance") && original.sourceID == "" {
				continue
			}
			t.Run(original.action+"/"+guard, func(t *testing.T) {
				command, revision, want := original, fixture.quantity.Revision, ErrNotFound
				switch guard {
				case "mode":
					command.itemID, want = fixture.individual.ID, ErrStockTracking
				case "missing item":
					command.itemID = "absent"
				case "stale proof":
					revision, want = revision+1, ErrRevisionConflict
				case "missing leaf":
					if command.targetID != "" {
						command.targetID = "absent"
					} else {
						command.sourceID = "absent"
					}
				case "insufficient":
					command.quantity, want = 1501, ErrStockInsufficient
				case "missing balance":
					command.sourceID, want = "root", ErrStockInsufficient
				}
				before := domainState(t, fixture.store)
				if reply, err := callStockTest(t.Context(), inventory, fixture.token, "stock-guard-"+guard, revision, command); !errors.Is(err, want) || reply != (Stock{}) {
					t.Fatal("runtime physical guard was skipped", err)
				}
				requireDomainState(t, fixture.store, before)
			})
		}
	}
}

func TestInventoryArchivedCatalogMetadataDoesNotPretendToDisposeItsPhysicalStock(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	archived, err := fixture.catalog.Archive(t.Context(), fixture.token, "archive-quantity-metadata", fixture.quantity.ID, fixture.quantity.Revision, "Catalog metadata only")
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := inventory.Receive(t.Context(), fixture.token, "receive-archived-catalog", archived.Revision, StockReceive{ItemID: archived.ID, TargetLocationID: "target", QuantityBase: 10}); err != nil || reply.QuantityBase != 10 {
		t.Fatal("catalog archival unexpectedly disposed or froze physical history", err)
	}
	if _, err := fixture.catalog.Delete(t.Context(), fixture.token, "delete-quantity-history", archived.ID, archived.Revision+1); !errors.Is(err, ErrItemHistory) {
		t.Fatal("real movement/balance history did not guard catalog deletion", err)
	}
}

func TestInventoryCanonicalUnicodePlacementKeepsOriginalMeasurementAndExactMaximumNotes(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	s := fixture.store
	itemID := strings.Repeat("界", 123) + "<&\"\n "
	locationID := strings.Repeat("😀", 123) + "<&\"\t "
	execSQL(t, s.db, "INSERT INTO items(id,name,tracking_mode,unit_code,base_unit,scale_factor,created_at,updated_at) VALUES(?,'Imported quantity','quantity','公升','ml',1000,'2026','2026')", itemID)
	seedCatalogProjection(t, s, "item", itemID, ItemData{ID: itemID, Name: "Imported quantity", TrackingMode: "quantity", UnitCode: "公升", BaseUnit: "ml", ScaleFactor: 1000, Status: "active"})
	execSQL(t, s.db, "INSERT INTO locations(id,code,name,created_at,updated_at) VALUES(?,'UNICODE','Imported','2026','2026')", locationID)
	seedCatalogProjection(t, s, "location", locationID, LocationData{ID: locationID, Code: "UNICODE", Name: "Imported", Kind: "other", Status: "active"})
	note := strings.Repeat("😀", 10000)
	reply, err := inventory.Adjust(t.Context(), fixture.token, strings.Repeat("界", 128), 1, StockAdjust{ItemID: itemID, TargetLocationID: locationID, QuantityBase: 1500, Reason: note, Note: note})
	if err != nil || reply.QuantityBase != 1500 || reply.ScaleFactor != 1000 || reply.UnitCode != "公升" || reply.BaseUnit != "ml" || reply.Revision != 1 {
		t.Fatal("Unicode pair placement guessed conversion or rejected exact bounds", err)
	}
	if scalar[string](t, s.db, "SELECT entity_id FROM projection_revisions WHERE entity_type='stock' AND entity_id=json_array(?,?)", itemID, locationID) != projectionPair(itemID, locationID) {
		t.Fatal("Go/SQLite composite spelling split one stock identity")
	}
}

func TestInventoryUsesOneUTCInstantForSourceHistoryAndFrozenPublication(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	now := fixture.auth.now().Add(time.Minute + 123456789*time.Nanosecond).In(time.FixedZone("offset", 8*3600))
	fixture.auth.now = func() time.Time { return now }
	command := StockReceive{ItemID: fixture.quantity.ID, TargetLocationID: "root", QuantityBase: 1}
	reply, err := inventory.Receive(t.Context(), fixture.token, "stock-utc-timestamp", fixture.quantity.Revision, command)
	if err != nil {
		t.Fatal(err)
	}
	event := stockEventTest(t, fixture, "stock-utc-timestamp")
	if event.OccurredAt != stamp(now.UTC()) || !strings.HasSuffix(event.OccurredAt, "Z") || scalar[string](t, fixture.store.db, "SELECT updated_at FROM stock_balances WHERE item_id=? AND location_id=?", reply.ItemID, reply.LocationID) != event.OccurredAt || scalar[string](t, fixture.store.db, "SELECT created_at FROM sync_changes WHERE entity_type='inventory_event' AND entity_id=?", event.ID) != event.OccurredAt {
		t.Fatal("physical state/history publication used different or local instants")
	}
}
