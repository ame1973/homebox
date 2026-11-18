package store

import (
	"database/sql"
	"reflect"
	"testing"

	"homebox/internal/offlineprojection"
)

// This exercises only the producer kinds created below; it does not establish
// complete backfill, source approval, authorization, or synchronization.
func TestActualDomainProjectionPayloadsMatchOfflineContract(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	locations, err := NewLocations(auth)
	if err != nil {
		t.Fatal(err)
	}
	taxonomy, err := NewTaxonomy(auth)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := NewInventory(auth)
	if err != nil {
		t.Fatal(err)
	}

	root := createLocation(t, locations, token, "ROOT", nil)
	child := createLocation(t, locations, token, "CHILD", nil)
	if _, err := locations.Move(t.Context(), token, "schema-move-location", child.ID, child.Revision, &root.ID); err != nil {
		t.Fatal(err)
	}
	category := createCategory(t, taxonomy, token, "分類 😀", nil)
	tag := createTag(t, taxonomy, token, "標籤")
	individual := createCatalogItem(t, catalog, token, ItemCreate{Name: "單件 😀", CategoryID: &category.ID})
	barcode, err := catalog.CreateBarcode(t.Context(), token, "schema-create-barcode", individual.ID, individual.Revision, BarcodeWrite{"EAN_13", "4006381333931"})
	if err != nil {
		t.Fatal(err)
	}
	owner := getCatalogItem(t, catalog, token, individual.ID)
	if _, err := catalog.CreateAlias(t.Context(), token, "schema-create-alias", individual.ID, owner.Revision, "別名"); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	if _, err := catalog.CreateAttribute(t.Context(), token, "schema-create-attribute", individual.ID, owner.Revision, AttributeWrite{AttributeKey: "顏色", AttributeValue: "藍色"}); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	if _, err := catalog.ReplaceTags(t.Context(), token, "schema-add-tag", individual.ID, owner.Revision, []string{tag.ID}); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	unit, err := inventory.CreateUnit(t.Context(), token, "schema-create-unit", individual.ID, owner.Revision, UnitCreate{LocationID: &child.ID, SerialNumber: testPointer("序號"), Notes: "說明"})
	if err != nil {
		t.Fatal(err)
	}
	quantity := createCatalogItem(t, catalog, token, ItemCreate{Name: "數量", TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000))})
	if _, err := inventory.Receive(t.Context(), token, "schema-receive-stock", quantity.Revision, StockReceive{ItemID: quantity.ID, TargetLocationID: child.ID, QuantityBase: 1234}); err != nil {
		t.Fatal(err)
	}
	purchases, err := NewPurchases(auth)
	if err != nil {
		t.Fatal(err)
	}
	purchase := createPurchaseCommand(t, purchases, token, "schema-create-purchase", "JPY")
	quantity = getCatalogItem(t, catalog, token, quantity.ID)
	if _, err := purchases.CreateLine(t.Context(), token, "schema-create-line", purchase.ID, purchase.Revision, PurchaseLineCreate{ItemID: quantity.ID, ItemRevision: quantity.Revision, QuantityBase: 1234, PaidMinor: 100}); err != nil {
		t.Fatal(err)
	}
	assets, err := NewAssets(auth)
	if err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	profile, err := assets.PutProfile(t.Context(), token, "schema-create-profile", unit.ID, unit.Revision, manualProfileInput(owner.Revision, 100))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.RecordUsage(t.Context(), token, "schema-create-usage", unit.ID, profile.Revision, usageInput(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.RecordCost(t.Context(), token, "schema-create-cost", unit.ID, profile.Revision+1, costInput(30)); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.Dispose(t.Context(), token, "schema-create-disposal", unit.ID, profile.Revision+2, disposalInput(unit.Revision, 25)); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DeleteBarcode(t.Context(), token, "schema-delete-barcode", barcode.ID, barcode.Revision); err != nil {
		t.Fatal(err)
	}
	owner = getCatalogItem(t, catalog, token, individual.ID)
	if _, err := catalog.ReplaceTags(t.Context(), token, "schema-remove-tag", individual.ID, owner.Revision, []string{}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.QueryContext(t.Context(), "SELECT entity_type,entity_id,operation,entity_revision,projection_json FROM sync_changes ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	replayed := make(map[projectionKey]projectionValue)
	lastRevision := make(map[projectionKey]int64)
	upsertKinds := make(map[string]bool)
	deleteKinds := make(map[string]bool)
	for rows.Next() {
		var key projectionKey
		var operation string
		var revision int64
		var payload sql.NullString
		if err := rows.Scan(&key.kind, &key.id, &operation, &revision, &payload); err != nil {
			t.Fatal(err)
		}
		if revision <= lastRevision[key] {
			t.Fatal("projection revision did not advance", key)
		}
		lastRevision[key] = revision
		switch operation {
		case "upsert":
			if !payload.Valid {
				t.Fatal("upsert lacks a frozen payload", key)
			}
			if err := offlineprojection.ValidatePayload(key.kind, []byte(payload.String)); err != nil {
				t.Fatal("actual frozen payload failed the offline contract", key, err)
			}
			if err := validateSyncRecord(key.kind, key.id, operation, revision, 1, []byte(payload.String)); err != nil {
				t.Fatal("actual frozen record failed identity validation", key, err)
			}
			upsertKinds[key.kind] = true
			replayed[key] = projectionValue{Revision: revision, Payload: payload.String}
		case "delete":
			if payload.Valid {
				t.Fatal("tombstone retained a payload", key)
			}
			if err := validateSyncRecord(key.kind, key.id, operation, revision, 1, nil); err != nil {
				t.Fatal("actual tombstone failed identity validation", key, err)
			}
			deleteKinds[key.kind] = true
			delete(replayed, key)
		default:
			t.Fatal("unexpected frozen operation", operation)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"location", "location_event", "category", "tag", "item", "barcode", "alias", "attribute", "item_tag", "unit", "stock", "inventory_event", "search_doc", "purchase", "purchase_line", "asset", "usage", "cost", "disposal"} {
		if !upsertKinds[kind] {
			t.Fatal("fixture did not exercise the expected producer", kind)
		}
	}
	for _, kind := range []string{"barcode", "item_tag"} {
		if !deleteKinds[kind] {
			t.Fatal("fixture did not exercise the expected tombstone", kind)
		}
	}
	current := currentProjectionState(t, s)
	if !reflect.DeepEqual(replayed, current) {
		t.Fatal("frozen changes did not reproduce the exact current payloads")
	}
	for key, projection := range current {
		if err := offlineprojection.ValidatePayload(key.kind, []byte(projection.Payload)); err != nil {
			t.Fatal("actual current payload failed the offline contract", key, err)
		}
		if err := validateSyncRecord(key.kind, key.id, "upsert", projection.Revision, 1, []byte(projection.Payload)); err != nil {
			t.Fatal("actual current record failed identity validation", key, err)
		}
	}
}

// Legacy fixture metadata is not evidence of verified image bytes. The real
// attachment operation publishes both independent Media and link projections.
func TestActualMediaAttachmentPayloadsMatchOfflineContract(t *testing.T) {
	fixture, library, input := mediaLibraryFixture(t, "item")
	link, err := library.Attach(t.Context(), fixture.token, "schema-attach-media", "shared-a", 7, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []projectionKey{{"media", "shared-a"}, {"media_link", link.ID}} {
		current := scalar[string](t, fixture.store.db, "SELECT payload_json FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
		frozen := scalar[string](t, fixture.store.db, "SELECT projection_json FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", key.kind, key.id)
		if current != frozen {
			t.Fatal("attachment did not freeze the current Media projection", key)
		}
		if err := offlineprojection.ValidatePayload(key.kind, []byte(current)); err != nil {
			t.Fatal("actual Media payload failed the offline contract", key, err)
		}
		revision := scalar[int64](t, fixture.store.db, "SELECT entity_revision FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
		if err := validateSyncRecord(key.kind, key.id, "upsert", revision, 1, []byte(current)); err != nil {
			t.Fatal("actual Media record failed identity validation", key, err)
		}
	}
	if _, err := library.Unlink(t.Context(), fixture.token, "schema-unlink-media", link.ID, link.Revision); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media_link' AND entity_id=? AND operation='delete' AND projection_json IS NULL", link.ID) != 1 {
		t.Fatal("unlink did not publish a null independent link tombstone")
	}
}
