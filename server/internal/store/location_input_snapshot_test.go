package store

import (
	"reflect"
	"testing"
	"time"
)

func TestLocationCommandsSnapshotCallerInputsBeforeFingerprintAndClock(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		s, locations, auth, token := locationFixture(t)
		originalParent := createLocation(t, locations, token, "CREATE-PARENT", nil)
		otherParent := createLocation(t, locations, token, "CREATE-OTHER", nil)
		parent, kind, status := originalParent.ID, "box", "active"
		input := LocationCreate{ParentID: &parent, Code: "CREATE-CHILD", Name: "Reviewed child", Kind: &kind, Status: &status}
		clock := auth.now
		auth.now = func() time.Time {
			parent, kind, status = otherParent.ID, "shelf", "archived"
			return clock()
		}
		created, err := locations.Create(t.Context(), token, "location-create-input-snapshot", originalParent.Revision, input)
		auth.now = clock
		if err != nil || created.ParentID == nil || *created.ParentID != originalParent.ID || created.Kind != "box" || created.Status != "active" {
			t.Fatal("caller pointers changed the fingerprinted location creation", created, err)
		}
		if scalar[string](t, s.db, "SELECT parent_id FROM locations WHERE id=?", created.ID) != originalParent.ID ||
			getLocation(t, locations, token, originalParent.ID).Revision != originalParent.Revision+1 || getLocation(t, locations, token, otherParent.ID).Revision != otherParent.Revision {
			t.Fatal("creation changed the wrong actual parent or aggregate")
		}
		parent, kind, status = originalParent.ID, "box", "active"
		replay, err := locations.Create(t.Context(), token, "location-create-input-snapshot", originalParent.Revision, input)
		if err != nil || !reflect.DeepEqual(created, replay) {
			t.Fatal("original creation did not reconcile its frozen response", err)
		}
	})
	t.Run("patch", func(t *testing.T) {
		s, locations, auth, token := locationFixture(t)
		location := createLocation(t, locations, token, "PATCH-SOURCE", nil)
		originalParent := createLocation(t, locations, token, "PATCH-PARENT", nil)
		otherParent := createLocation(t, locations, token, "PATCH-OTHER", nil)
		parent, code, name, kind, status, description := originalParent.ID, "PATCH-REVIEWED", "Reviewed name", "box", "active", "Reviewed description"
		position := int64(7)
		input := LocationPatch{ParentID: NullableID{Set: true, Value: &parent}, Code: &code, Name: &name, Kind: &kind, Status: &status, SortOrder: &position, Description: &description}
		clock := auth.now
		auth.now = func() time.Time {
			parent, code, name, kind, status, description = otherParent.ID, "PATCH-CHANGED", "Changed name", "shelf", "archived", "Changed description"
			position = 9
			return clock()
		}
		updated, err := locations.Patch(t.Context(), token, "location-patch-input-snapshot", location.ID, location.Revision, input)
		auth.now = clock
		if err != nil || updated.ParentID == nil || *updated.ParentID != originalParent.ID || updated.Code != "PATCH-REVIEWED" ||
			updated.Name != "Reviewed name" || updated.Kind != "box" || updated.Status != "active" || updated.SortOrder != 7 || updated.Description != "Reviewed description" {
			t.Fatal("caller pointers changed the fingerprinted location patch", updated, err)
		}
		if scalar[string](t, s.db, "SELECT to_parent_id FROM location_events WHERE location_id=?", location.ID) != originalParent.ID ||
			getLocation(t, locations, token, originalParent.ID).Revision != originalParent.Revision+1 || getLocation(t, locations, token, otherParent.ID).Revision != otherParent.Revision {
			t.Fatal("patch recorded the wrong move or parent aggregate")
		}
		parent, code, name, kind, status, description = originalParent.ID, "PATCH-REVIEWED", "Reviewed name", "box", "active", "Reviewed description"
		position = 7
		replay, err := locations.Patch(t.Context(), token, "location-patch-input-snapshot", location.ID, location.Revision, input)
		if err != nil || !reflect.DeepEqual(updated, replay) {
			t.Fatal("original patch did not reconcile its frozen response", err)
		}
	})
	t.Run("equal patch", func(t *testing.T) {
		s, locations, auth, token := locationFixture(t)
		location := createLocation(t, locations, token, "PATCH-EQUAL", nil)
		name := location.Name
		input := LocationPatch{Name: &name}
		changes := scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes")
		clock := auth.now
		auth.now = func() time.Time {
			name = "Changed after fingerprint"
			return clock()
		}
		result, err := locations.Patch(t.Context(), token, "location-equal-input-snapshot", location.ID, location.Revision, input)
		auth.now = clock
		if err != nil || !reflect.DeepEqual(location, result) || scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes") != changes {
			t.Fatal("fingerprinted equal intent changed the location or published a revision", result, err)
		}
		name = location.Name
		replay, err := locations.Patch(t.Context(), token, "location-equal-input-snapshot", location.ID, location.Revision, input)
		if err != nil || !reflect.DeepEqual(location, replay) {
			t.Fatal("equal original intent did not reconcile", err)
		}
	})
	t.Run("move", func(t *testing.T) {
		s, locations, auth, token := locationFixture(t)
		location := createLocation(t, locations, token, "MOVE-SOURCE", nil)
		originalParent := createLocation(t, locations, token, "MOVE-PARENT", nil)
		otherParent := createLocation(t, locations, token, "MOVE-OTHER", nil)
		parent := originalParent.ID
		clock := auth.now
		auth.now = func() time.Time {
			parent = otherParent.ID
			return clock()
		}
		moved, err := locations.Move(t.Context(), token, "location-move-input-snapshot", location.ID, location.Revision, &parent)
		auth.now = clock
		if err != nil || moved.ParentID == nil || *moved.ParentID != originalParent.ID {
			t.Fatal("caller pointer changed the fingerprinted location move", moved, err)
		}
		if scalar[string](t, s.db, "SELECT to_parent_id FROM location_events WHERE location_id=?", location.ID) != originalParent.ID ||
			getLocation(t, locations, token, originalParent.ID).Revision != originalParent.Revision+1 || getLocation(t, locations, token, otherParent.ID).Revision != otherParent.Revision {
			t.Fatal("move recorded the wrong destination or parent aggregate")
		}
		parent = originalParent.ID
		replay, err := locations.Move(t.Context(), token, "location-move-input-snapshot", location.ID, location.Revision, &parent)
		if err != nil || !reflect.DeepEqual(moved, replay) {
			t.Fatal("original move did not reconcile its frozen response", err)
		}
	})
}
