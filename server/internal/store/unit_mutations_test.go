package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func createUnitTest(t *testing.T, fixture holdingFixture, inventory *Inventory, key string, location *string) Unit {
	t.Helper()
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	value, err := inventory.CreateUnit(t.Context(), fixture.token, key, item.ID, item.Revision, UnitCreate{LocationID: location})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func requireUnitEventTest(t *testing.T, fixture holdingFixture, key, unitID, action string, quantity int64, from, to *string) {
	t.Helper()
	event := stockEventTest(t, fixture, key)
	if event.ItemUnitID == nil || *event.ItemUnitID != unitID || event.Action != action || event.QuantityBase != quantity || !sameParent(event.FromLocationID, from) || !sameParent(event.ToLocationID, to) {
		t.Fatal("Unit event lost exact subject/action/direction", event)
	}
}

func TestUnitCommandsPublishIndependentFinalStateApprovedUnitSearchAndImmutableHistory(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	before := domainState(t, fixture.store)
	input := UnitCreate{LocationID: testPointer("source"), SerialNumber: testPointer(""), Notes: "\x00Original unit é\n"}
	var unit Unit
	changed := requireProjectionReplay(t, fixture.store, func() {
		var err error
		unit, err = inventory.CreateUnit(t.Context(), fixture.token, "unit-flow-create", fixture.individual.ID, fixture.individual.Revision, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if unit.Revision != 1 || unit.AssetCode != "AST-"+unit.ID || unit.SerialNumber == nil || *unit.SerialNumber != "" || unit.Status != "stored" || unit.Notes != input.Notes || len(changed) != 6 {
		t.Fatal("creation guessed defaults/code, changed null text or omitted dependencies", unit, len(changed))
	}
	requireUnitEventTest(t, fixture, "unit-flow-create", unit.ID, "unit_create", 1, nil, testPointer("source"))
	if !changed[projectionKey{"unit", unit.ID}] || !changed[projectionKey{"search_doc", projectionPair("unit", unit.ID)}] || changed[projectionKey{"search_doc", projectionPair("item", unit.ItemID)}] || changed[projectionKey{"unit", fixture.unit.ID}] {
		t.Fatal("Unit creation published unrelated item/sibling text or omitted its own search")
	}
	original := unit
	changed = requireProjectionReplay(t, fixture.store, func() {
		var err error
		unit, err = inventory.PatchUnit(t.Context(), fixture.token, "unit-flow-edit", unit.ID, unit.Revision, UnitPatch{SerialNumber: NullableString{Set: true, Value: testPointer("UnitScopedSerial")}, Notes: testPointer("UnitScopedNotes")})
		if err != nil {
			t.Fatal(err)
		}
	})
	if unit.Revision != 2 || unit.AssetCode != original.AssetCode || len(changed) != 6 || !strings.Contains(projectedSearchText(t, fixture.store, "unit", unit.ID), "UnitScopedSerial") || strings.Contains(projectedSearchText(t, fixture.store, "item", unit.ItemID), "UnitScopedSerial") {
		t.Fatal("metadata edit changed identity or leaked Unit fields into Item text")
	}
	requireUnitEventTest(t, fixture, "unit-flow-edit", unit.ID, "unit_edit", 0, nil, nil)
	changed = requireProjectionReplay(t, fixture.store, func() {
		var err error
		unit, err = inventory.MoveUnit(t.Context(), fixture.token, "unit-flow-move", unit.ID, unit.Revision, "target")
		if err != nil {
			t.Fatal(err)
		}
	})
	if unit.Revision != 3 || unit.LocationID == nil || *unit.LocationID != "target" || len(changed) != 7 {
		t.Fatal("move rewrote identity/status or repeated a shared ancestor")
	}
	requireUnitEventTest(t, fixture, "unit-flow-move", unit.ID, "unit_move", 1, testPointer("source"), testPointer("target"))
	unit, err := inventory.SetUnitStatus(t.Context(), fixture.token, "unit-flow-retire", unit.ID, unit.Revision, UnitStatus{Status: "retired", Note: "Physical retirement only"})
	if err != nil || unit.Revision != 4 || unit.LocationID == nil || *unit.LocationID != "target" {
		t.Fatal("retirement removed physical placement or inferred asset accounting", err)
	}
	requireUnitEventTest(t, fixture, "unit-flow-retire", unit.ID, "unit_status_retired", 0, nil, nil)
	unit, err = inventory.SetUnitStatus(t.Context(), fixture.token, "unit-flow-dispose", unit.ID, unit.Revision, UnitStatus{Status: "disposed", Note: "\x00Explicit discard é\n", DisposalKind: testPointer("discarded")})
	if err != nil || unit.Revision != 5 || unit.LocationID == nil || *unit.LocationID != "target" {
		t.Fatal("explicit non-asset disposal failed or erased retained last placement", err)
	}
	requireUnitEventTest(t, fixture, "unit-flow-dispose", unit.ID, "unit_dispose_discarded", -1, testPointer("target"), nil)
	var ack ActionAck
	changed = requireProjectionReplay(t, fixture.store, func() {
		var err error
		ack, err = inventory.DeleteUnit(t.Context(), fixture.token, "unit-flow-delete", unit.ID, unit.Revision)
		if err != nil {
			t.Fatal("creation/movement history made an unbacked record undeletable", err)
		}
	})
	if !ack.OK || ack.Revision != 6 || len(changed) != 6 || fixture.unit.ID == unit.ID {
		t.Fatal("deletion lost its independent revision or affected another Unit")
	}
	requireUnitEventTest(t, fixture, "unit-flow-delete", unit.ID, "unit_delete", 0, testPointer("target"), nil)
	for _, key := range []projectionKey{{"unit", unit.ID}, {"search_doc", projectionPair("unit", unit.ID)}} {
		if !changed[key] || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id) != 0 || scalar[int](t, fixture.store.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type=? AND entity_id=? ORDER BY seq DESC LIMIT 1", key.kind, key.id) != 1 {
			t.Fatal("deleted Unit/search omitted an independent null tombstone", key)
		}
	}
	if value, err := inventory.GetUnit(t.Context(), fixture.token, unit.ID); !errors.Is(err, ErrNotFound) || value != (Unit{}) {
		t.Fatal("deleted Unit remained current", err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events WHERE item_unit_id=?", unit.ID) != 6 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM unit_search_fts WHERE unit_search_fts MATCH 'UnitScopedSerial'") != 0 {
		t.Fatal("delete erased immutable history or retained stale FTS")
	}
	after := domainState(t, fixture.store)
	for _, table := range []string{"stock_balances", "purchases", "purchase_lines", "asset_profiles", "asset_usage_events", "asset_cost_events", "asset_disposals", "ai_jobs", "ai_review_entries", "ocr_results", "search_documents"} {
		if before[table] != after[table] {
			t.Fatal("Unit operation fabricated another domain's data", table)
		}
	}
	_, fresh := login(t, fixture.auth)
	frozen, err := inventory.CreateUnit(t.Context(), fresh, "unit-flow-create", fixture.individual.ID, fixture.individual.Revision, input)
	if err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("frozen original creation was rebased after source deletion/fresh login", err)
	}
}

func TestUnitCreateDefaultsAllInitialStatesExplicitUnknownLocationAndArchivedItemMetadata(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for index, status := range []string{"stored", "in_use", "lent", "lost"} {
		item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
		input := UnitCreate{LocationID: nil, Status: &status}
		unit, err := inventory.CreateUnit(t.Context(), fixture.token, fmt.Sprintf("unit-initial-state-%d", index), item.ID, item.Revision, input)
		if err != nil || unit.LocationID != nil || unit.SerialNumber != nil || unit.Notes != "" || unit.Status != status || unit.Revision != 1 {
			t.Fatal("initial state/null/default was guessed or rejected", err)
		}
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	archived, err := fixture.catalog.Archive(t.Context(), fixture.token, "archive-unit-master", item.ID, item.Revision, "")
	if err != nil {
		t.Fatal(err)
	}
	unit, err := inventory.CreateUnit(t.Context(), fixture.token, "unit-on-archived-master", item.ID, archived.Revision, UnitCreate{})
	if err != nil || unit.Status != "stored" {
		t.Fatal("catalog archival silently disposed/froze an individual physical record", err)
	}
}

func TestUnitPatchPreservesOmittedNullEmptyAndLongRetainedSearchRepresentation(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	long := strings.Repeat("😀", 10000)
	execSQL(t, fixture.store.db, "UPDATE item_units SET asset_code=?,serial_number=?,notes=? WHERE id=?", long, long, long, fixture.unit.ID)
	value, err := inventory.PatchUnit(t.Context(), fixture.token, "wide-retained-unit-patch", fixture.unit.ID, fixture.unit.Revision, UnitPatch{Notes: testPointer("ChangedNotes")})
	if err != nil || value.AssetCode != long || value.SerialNumber == nil || *value.SerialNumber != long || !strings.Contains(projectedSearchText(t, fixture.store, "unit", value.ID), long) {
		t.Fatal("search imposed a new 128-character asset-code policy on valid retained Unit source", err)
	}
	value, err = inventory.PatchUnit(t.Context(), fixture.token, "clear-retained-unit-serial", value.ID, value.Revision, UnitPatch{SerialNumber: NullableString{Set: true}})
	if err != nil || value.SerialNumber != nil || value.Notes != "ChangedNotes" {
		t.Fatal("null serial clear overwrote omitted notes", err)
	}
	value, err = inventory.PatchUnit(t.Context(), fixture.token, "empty-retained-unit-serial", value.ID, value.Revision, UnitPatch{SerialNumber: NullableString{Set: true, Value: testPointer("")}, Notes: testPointer("")})
	if err != nil || value.SerialNumber == nil || *value.SerialNumber != "" || value.Notes != "" {
		t.Fatal("supplied-empty and null serial were conflated", err)
	}
}

func TestUnitEqualMetadataMoveAndStatusOnlyRetainOriginalReceiptsAndContext(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	setCatalogProjectionRevision(t, fixture.store, "unit", fixture.unit.ID, maxSafeInteger)
	setCatalogProjectionRevision(t, fixture.store, "item", fixture.individual.ID, maxSafeInteger)
	setCatalogProjectionRevision(t, fixture.store, "location", "source", maxSafeInteger)
	execSQL(t, fixture.store.db, "UPDATE locations SET status='archived' WHERE id='source'")
	before := domainState(t, fixture.store)
	for _, call := range []func() (Unit, error){
		func() (Unit, error) {
			return inventory.PatchUnit(t.Context(), fixture.token, "equal-unit-metadata", fixture.unit.ID, maxSafeInteger, UnitPatch{Notes: testPointer(fixture.unit.Notes)})
		},
		func() (Unit, error) {
			return inventory.MoveUnit(t.Context(), fixture.token, "equal-unit-location", fixture.unit.ID, maxSafeInteger, "source")
		},
		func() (Unit, error) {
			return inventory.SetUnitStatus(t.Context(), fixture.token, "equal-unit-status", fixture.unit.ID, maxSafeInteger, UnitStatus{Status: "stored", Note: "\x00Exact no-op memo é\n"})
		},
	} {
		value, err := call()
		if err != nil || value.Revision != maxSafeInteger {
			t.Fatal("no-op incremented/exhausted physical authority", err)
		}
	}
	after := domainState(t, fixture.store)
	for _, table := range []string{"items", "item_units", "locations", "inventory_events", "projection_revisions", "current_projections", "sync_changes", "data_revision", "unit_search_documents", "sqlite_sequence"} {
		if before[table] != after[table] {
			t.Fatal("no-op invented movement/source/search/counter changes", table)
		}
	}
	if scalar[string](t, fixture.store.db, "SELECT note FROM mutation_events WHERE operation='post_units_id_status'") != "\x00Exact no-op memo é\n" {
		t.Fatal("equal-status receipt lost exact memo")
	}
}

func TestUnitPhysicalPolicyActiveDestinationsArchivedOutflowRetirementAndTerminalDisposal(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	execSQL(t, fixture.store.db, "UPDATE locations SET status='archived' WHERE id='source'")
	unit, err := inventory.MoveUnit(t.Context(), fixture.token, "leave-archived-unit", fixture.unit.ID, fixture.unit.Revision, "target")
	if err != nil {
		t.Fatal("outflow could not leave a valid archived source", err)
	}
	before := domainState(t, fixture.store)
	if _, err := inventory.MoveUnit(t.Context(), fixture.token, "cannot-reenter-archived-unit", unit.ID, unit.Revision, "source"); !errors.Is(err, ErrLocationArchived) {
		t.Fatal("Unit entered an inactive destination", err)
	}
	requireDomainState(t, fixture.store, before)
	unit, err = inventory.SetUnitStatus(t.Context(), fixture.token, "retire-keeps-unit-placement", unit.ID, unit.Revision, UnitStatus{Status: "retired"})
	if err != nil || unit.LocationID == nil || *unit.LocationID != "target" {
		t.Fatal("retirement became implicit physical removal", err)
	}
	unit, err = inventory.MoveUnit(t.Context(), fixture.token, "move-retired-unit", unit.ID, unit.Revision, "root")
	if err != nil || unit.Status != "retired" {
		t.Fatal("retired storage could not be moved independently", err)
	}
	unit, err = inventory.SetUnitStatus(t.Context(), fixture.token, "dispose-nonasset-unit", unit.ID, unit.Revision, UnitStatus{Status: "disposed", DisposalKind: testPointer("recycled")})
	if err != nil {
		t.Fatal(err)
	}
	before = domainState(t, fixture.store)
	if value, err := inventory.MoveUnit(t.Context(), fixture.token, "cannot-move-disposed-unit", unit.ID, unit.Revision, "target"); !errors.Is(err, ErrUnitDisposed) || value != (Unit{}) {
		t.Fatal("disposed Unit moved", err)
	}
	if value, err := inventory.SetUnitStatus(t.Context(), fixture.token, "cannot-revive-disposed-unit", unit.ID, unit.Revision, UnitStatus{Status: "stored"}); !errors.Is(err, ErrUnitDisposed) || value != (Unit{}) {
		t.Fatal("ordinary status restored a disposed Unit", err)
	}
	requireDomainState(t, fixture.store, before)
}

func seedUnitAssetProfileTest(t *testing.T, fixture holdingFixture, retired *string) {
	t.Helper()
	execSQL(t, fixture.store.db, "INSERT INTO asset_profiles(item_unit_id,retired_at,cost_basis,manual_acquisition_minor,currency,created_at,updated_at) VALUES(?,?,'manual',0,'TWD','2026','2026')", fixture.unit.ID, retired)
}

func TestUnitAssetLifecycleAndRetainedFinancialReferencesCannotBeBypassed(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	seedUnitAssetProfileTest(t, fixture, nil)
	before := domainState(t, fixture.store)
	for _, input := range []UnitStatus{{Status: "retired"}, {Status: "disposed", DisposalKind: testPointer("sold")}} {
		if value, err := inventory.SetUnitStatus(t.Context(), fixture.token, "asset-status-bypass-"+input.Status, fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrUnitAssetLifecycle) || value != (Unit{}) {
			t.Fatal("ordinary Unit status bypassed asset dates/disposal/proceeds", err)
		}
		requireDomainState(t, fixture.store, before)
	}
	if ack, err := inventory.DeleteUnit(t.Context(), fixture.token, "delete-financial-unit", fixture.unit.ID, fixture.unit.Revision); !errors.Is(err, ErrUnitHistory) || ack.OK {
		t.Fatal("Unit deletion erased an asset reference", err)
	}
	requireDomainState(t, fixture.store, before)
	unit, err := inventory.SetUnitStatus(t.Context(), fixture.token, "asset-operational-unit-status", fixture.unit.ID, fixture.unit.Revision, UnitStatus{Status: "in_use"})
	if err != nil || unit.Status != "in_use" || scalar[int](t, fixture.store.db, "SELECT in_service_at IS NULL FROM asset_profiles") != 1 {
		t.Fatal("operational status inferred a financial service date", err)
	}
	execSQL(t, fixture.store.db, "UPDATE asset_profiles SET retired_at='2026-11-04T12:00:00Z' WHERE item_unit_id=?", unit.ID)
	before = domainState(t, fixture.store)
	if _, err := inventory.SetUnitStatus(t.Context(), fixture.token, "cannot-reactivate-retired-profile", unit.ID, unit.Revision, UnitStatus{Status: "stored"}); !errors.Is(err, ErrUnitAssetLifecycle) {
		t.Fatal("operational status reactivated a retired asset profile", err)
	}
	requireDomainState(t, fixture.store, before)
	execSQL(t, fixture.store.db, "INSERT INTO asset_disposals(item_unit_id,disposed_at,kind,currency,created_at) VALUES(?,'2026-11-04T12:00:00Z','donated','TWD','2026')", unit.ID)
	before = domainState(t, fixture.store)
	if _, err := inventory.MoveUnit(t.Context(), fixture.token, "cannot-move-disposed-profile", unit.ID, unit.Revision, "target"); !errors.Is(err, ErrUnitDisposed) {
		t.Fatal("a stale Unit status moved a disposed asset", err)
	}
	if _, err := inventory.SetUnitStatus(t.Context(), fixture.token, "cannot-change-disposed-profile", unit.ID, unit.Revision, UnitStatus{Status: "lost"}); !errors.Is(err, ErrUnitDisposed) {
		t.Fatal("a stale Unit status bypassed immutable asset disposal", err)
	}
	requireDomainState(t, fixture.store, before)
}

func TestUnitDeletionPreservesOwnImmutableHistorySharedMediaAndOtherOwnerLinks(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	unit := createUnitTest(t, fixture, inventory, "created-deletable-unit", testPointer("source"))
	execSQL(t, fixture.store.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('shared-image','private-key',?,'image/png',1,1,1,'2026')", strings.Repeat("a", 64))
	execSQL(t, fixture.store.db, "INSERT INTO media_links(id,media_id,item_unit_id) VALUES('unit-photo','shared-image',?)", unit.ID)
	execSQL(t, fixture.store.db, "INSERT INTO media_links(id,media_id,item_id) VALUES('other-photo','shared-image',?)", fixture.quantity.ID)
	for _, id := range []string{"unit-photo", "other-photo"} {
		ownerType, ownerID := "unit", unit.ID
		if id == "other-photo" {
			ownerType, ownerID = "item", fixture.quantity.ID
		}
		seedCatalogProjection(t, fixture.store, "media_link", id, map[string]any{"id": id, "media_id": "shared-image", "owner_type": ownerType, "owner_id": ownerID, "is_cover": false, "sort_order": 0, "description": ""})
	}
	beforeOther := scalar[string](t, fixture.store.db, "SELECT payload_json FROM current_projections WHERE entity_type='media_link' AND entity_id='other-photo'")
	ack, err := inventory.DeleteUnit(t.Context(), fixture.token, "delete-created-unit-with-photo", unit.ID, unit.Revision)
	if err != nil || !ack.OK || ack.Revision != unit.Revision+1 {
		t.Fatal("own creation history blocked an explicitly unbacked deletion", err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events WHERE item_unit_id=?", unit.ID) != 2 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_links WHERE id='other-photo'") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM media_links WHERE id='unit-photo'") != 0 {
		t.Fatal("Unit deletion erased history/shared media or retained an owned source link")
	}
	if scalar[string](t, fixture.store.db, "SELECT payload_json FROM current_projections WHERE entity_type='media_link' AND entity_id='other-photo'") != beforeOther || scalar[int](t, fixture.store.db, "SELECT projection_json IS NULL FROM sync_changes WHERE entity_type='media_link' AND entity_id='unit-photo' ORDER BY seq DESC LIMIT 1") != 1 {
		t.Fatal("Unit deletion omitted its stable link tombstone or changed another owner")
	}
	requireUnitEventTest(t, fixture, "delete-created-unit-with-photo", unit.ID, "unit_delete", -1, testPointer("source"), nil)
	if _, err := fixture.catalog.Delete(t.Context(), fixture.token, "cannot-delete-unit-master-history", unit.ItemID, getCatalogItem(t, fixture.catalog, fixture.token, unit.ItemID).Revision); !errors.Is(err, ErrItemHistory) {
		t.Fatal("retained Unit event identity lost its Item history guard", err)
	}
}
