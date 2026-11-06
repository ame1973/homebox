package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestUnitMissingAndStaleSubjectsWithholdEveryTypedResult(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	for _, original := range unitCallsTest(fixture) {
		for _, failure := range []string{"missing", "stale"} {
			command := original
			want := ErrNotFound
			if failure == "missing" {
				command.subject = "missing-unit-or-item"
			} else {
				command.proof++
				want = ErrRevisionConflict
			}
			before := domainState(t, fixture.store)
			if value, err := callUnitTest(t.Context(), inventory, fixture.token, "missing-stale-"+command.kind+failure, command); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing/stale Unit subject released a typed result", command.kind, err)
			}
			requireDomainState(t, fixture.store, before)
		}
	}
}

func TestUnitGuardReadFailuresAndMalformedOwnedMediaWithholdAllEffects(t *testing.T) {
	for _, failure := range []struct{ kind, table string }{{"move", "asset_disposals"}, {"status", "asset_profiles"}, {"delete", "asset_cost_events"}, {"delete", "media_links"}} {
		t.Run(failure.kind+failure.table, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			var command unitCallTest
			for _, candidate := range unitCallsTest(fixture) {
				if candidate.kind == failure.kind {
					command = candidate
				}
			}
			before := domainState(t, fixture.store)
			execSQL(t, fixture.store.db, "ALTER TABLE "+failure.table+" RENAME TO unavailable_unit_guard")
			if value, err := callUnitTest(t.Context(), inventory, fixture.token, "failed-unit-guard-read", command); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("failed reference/asset query authorized a Unit mutation", err)
			}
			execSQL(t, fixture.store.db, "ALTER TABLE unavailable_unit_guard RENAME TO "+failure.table)
			requireDomainState(t, fixture.store, before)
		})
	}
	fixture, inventory := inventoryFixture(t)
	execSQL(t, fixture.store.db, "INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at) VALUES('bad-photo','private',?,'image/png',1,1,1,'2026')", strings.Repeat("a", 64))
	execSQL(t, fixture.store.db, "INSERT INTO media_links(id,media_id,item_unit_id) VALUES(CAST(x'ff' AS TEXT),'bad-photo',?)", fixture.unit.ID)
	before := domainState(t, fixture.store)
	if ack, err := inventory.DeleteUnit(t.Context(), fixture.token, "invalid-owned-unit-link", fixture.unit.ID, fixture.unit.Revision); !errors.Is(err, ErrDomainState) || ack.OK {
		t.Fatal("malformed retained link identity was repaired/seeded during Unit deletion", err)
	}
	requireDomainState(t, fixture.store, before)
}

func TestUnitSearchRetainedBoundsApplyToAllCatalogInvalidatorsWithoutPublishingCorruptText(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	long := strings.Repeat("😀", 10000)
	execSQL(t, fixture.store.db, "UPDATE item_units SET asset_code=?,serial_number=?,notes=? WHERE id=?", long, long, long, fixture.unit.ID)
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	item, err := fixture.catalog.Patch(t.Context(), fixture.token, "catalog-wide-retained-unit-code", item.ID, item.Revision, ItemPatch{Name: testPointer("Rebuilt owner")})
	if err != nil || !strings.Contains(projectedSearchText(t, fixture.store, "unit", fixture.unit.ID), long) {
		t.Fatal("shared catalog invalidation imposed an obsolete short Unit-code policy", err)
	}
	if _, err := inventory.PatchUnit(t.Context(), fixture.token, "wide-unit-notes-followup", fixture.unit.ID, fixture.unit.Revision, UnitPatch{Notes: testPointer("UnitOnlyText")}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"asset_code", "serial_number", "notes"} {
		for _, text := range []string{string([]byte{0xff}), "\x00" + strings.Repeat("界", 10000)} {
			// Each corrupt source remains untouched; a final-state materializer must
			// never repair it or publish a valid Item prefix before failing the Unit.
			execSQL(t, fixture.store.db, "UPDATE item_units SET "+field+"=? WHERE id=?", text, fixture.unit.ID)
			before := domainState(t, fixture.store)
			item = getCatalogItem(t, fixture.catalog, fixture.token, item.ID)
			if value, err := fixture.catalog.Patch(t.Context(), fixture.token, "bad-shared-unit-search", item.ID, item.Revision, ItemPatch{Name: testPointer("MustRollback")}); !errors.Is(err, ErrDomainState) || value != (Item{}) {
				t.Fatal("corrupt bounded Unit source entered shared approved search", field, err)
			}
			requireDomainState(t, fixture.store, before)
			if scalar[string](t, fixture.store.db, "SELECT CAST("+field+" AS BLOB) FROM item_units WHERE id=?", fixture.unit.ID) != text {
				t.Fatal("failed publication repaired retained source bytes", field)
			}
			execSQL(t, fixture.store.db, "UPDATE item_units SET "+field+"=? WHERE id=?", long, fixture.unit.ID)
		}
	}
}
