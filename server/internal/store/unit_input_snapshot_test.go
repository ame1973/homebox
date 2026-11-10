package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnitCommandsSnapshotCallerInputsBeforeFingerprintAndClock(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		fixture, inventory := inventoryFixture(t)
		location, serial, status := "source", "Reviewed serial", "in_use"
		input := UnitCreate{LocationID: &location, SerialNumber: &serial, Status: &status, Notes: "Reviewed note"}
		clock := fixture.auth.now
		fixture.auth.now = func() time.Time {
			location, serial, status = "target", "Changed serial", "lent"
			return clock()
		}
		created, err := inventory.CreateUnit(t.Context(), fixture.token, "unit-create-input-snapshot", fixture.individual.ID, fixture.individual.Revision, input)
		fixture.auth.now = clock
		if err != nil || created.LocationID == nil || *created.LocationID != "source" || created.SerialNumber == nil || *created.SerialNumber != "Reviewed serial" || created.Status != "in_use" {
			t.Fatal("caller pointers changed the fingerprinted Unit creation", created, err)
		}
		requireUnitEventTest(t, fixture, "unit-create-input-snapshot", created.ID, "unit_create", 1, nil, testPointer("source"))
		location, serial, status = "source", "Reviewed serial", "in_use"
		replay, err := inventory.CreateUnit(t.Context(), fixture.token, "unit-create-input-snapshot", fixture.individual.ID, fixture.individual.Revision, input)
		if err != nil || !reflect.DeepEqual(created, replay) {
			t.Fatal("original Unit creation did not reconcile", err)
		}
	})
	t.Run("patch", func(t *testing.T) {
		fixture, inventory := inventoryFixture(t)
		serial, notes := "Reviewed serial", "Reviewed notes"
		input := UnitPatch{SerialNumber: NullableString{Set: true, Value: &serial}, Notes: &notes}
		clock := fixture.auth.now
		fixture.auth.now = func() time.Time {
			serial, notes = "Changed serial", "Changed notes"
			return clock()
		}
		updated, err := inventory.PatchUnit(t.Context(), fixture.token, "unit-patch-input-snapshot", fixture.unit.ID, fixture.unit.Revision, input)
		fixture.auth.now = clock
		if err != nil || updated.SerialNumber == nil || *updated.SerialNumber != "Reviewed serial" || updated.Notes != "Reviewed notes" || updated.Revision != 2 {
			t.Fatal("caller pointers changed the fingerprinted Unit patch", updated, err)
		}
		search := projectedSearchText(t, fixture.store, "unit", fixture.unit.ID)
		if !strings.Contains(search, "Reviewed serial") || strings.Contains(search, "Changed serial") {
			t.Fatal("Unit search did not publish the reviewed serial")
		}
		requireUnitEventTest(t, fixture, "unit-patch-input-snapshot", updated.ID, "unit_edit", 0, nil, nil)
		serial, notes = "Reviewed serial", "Reviewed notes"
		replay, err := inventory.PatchUnit(t.Context(), fixture.token, "unit-patch-input-snapshot", fixture.unit.ID, fixture.unit.Revision, input)
		if err != nil || !reflect.DeepEqual(updated, replay) {
			t.Fatal("original Unit patch did not reconcile", err)
		}
	})
	t.Run("equal patch", func(t *testing.T) {
		fixture, inventory := inventoryFixture(t)
		notes := fixture.unit.Notes
		input := UnitPatch{Notes: &notes}
		changes := scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes")
		movements := scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events")
		clock := fixture.auth.now
		fixture.auth.now = func() time.Time {
			notes = "Changed after fingerprint"
			return clock()
		}
		result, err := inventory.PatchUnit(t.Context(), fixture.token, "unit-equal-input-snapshot", fixture.unit.ID, fixture.unit.Revision, input)
		fixture.auth.now = clock
		if err != nil || !reflect.DeepEqual(fixture.unit, result) || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM sync_changes") != changes ||
			scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events") != movements {
			t.Fatal("fingerprinted equal intent changed the Unit or published movement", result, err)
		}
		notes = fixture.unit.Notes
		replay, err := inventory.PatchUnit(t.Context(), fixture.token, "unit-equal-input-snapshot", fixture.unit.ID, fixture.unit.Revision, input)
		if err != nil || !reflect.DeepEqual(fixture.unit, replay) {
			t.Fatal("equal original Unit intent did not reconcile", err)
		}
	})
	t.Run("disposal kind", func(t *testing.T) {
		fixture, inventory := inventoryFixture(t)
		kind := "recycled"
		input := UnitStatus{Status: "disposed", DisposalKind: &kind, Note: "Reviewed disposal"}
		clock := fixture.auth.now
		fixture.auth.now = func() time.Time {
			kind = "sold"
			return clock()
		}
		disposed, err := inventory.SetUnitStatus(t.Context(), fixture.token, "unit-dispose-input-snapshot", fixture.unit.ID, fixture.unit.Revision, input)
		fixture.auth.now = clock
		if err != nil || disposed.Status != "disposed" || disposed.Revision != 2 {
			t.Fatal("reviewed physical disposal failed", disposed, err)
		}
		requireUnitEventTest(t, fixture, "unit-dispose-input-snapshot", disposed.ID, "unit_dispose_recycled", -1, testPointer("source"), nil)
		scoped := stockScopedKeyTest(t, fixture, "unit-dispose-input-snapshot")
		if scalar[string](t, fixture.store.db, `SELECT e.reason FROM inventory_events e JOIN mutation_events m ON m.id=e.mutation_event_id WHERE m.idempotency_key=?`, scoped) != "recycled" ||
			scalar[string](t, fixture.store.db, `SELECT e.note FROM inventory_events e JOIN mutation_events m ON m.id=e.mutation_event_id WHERE m.idempotency_key=?`, scoped) != "Reviewed disposal" ||
			scalar[string](t, fixture.store.db, "SELECT reason FROM mutation_events WHERE idempotency_key=?", scoped) != "recycled" {
			t.Fatal("disposal history did not retain the fingerprinted kind and note")
		}
		kind = "recycled"
		replay, err := inventory.SetUnitStatus(t.Context(), fixture.token, "unit-dispose-input-snapshot", fixture.unit.ID, fixture.unit.Revision, input)
		if err != nil || !reflect.DeepEqual(disposed, replay) {
			t.Fatal("original Unit disposal did not reconcile", err)
		}
	})
}
