package store

import (
	"reflect"
	"testing"
)

func TestMediaLinkUnlinkPublishesIndependentTombstoneAndActualOwnerScopes(t *testing.T) {
	for _, ownerType := range []string{"item", "unit", "location"} {
		t.Run(ownerType, func(t *testing.T) {
			fixture, library, _ := mediaLibraryFixture(t, ownerType)
			s := fixture.store
			before := domainState(t, s)
			var unit Unit
			if ownerType == "unit" {
				var err error
				unit, err = loadUnit(t.Context(), s.db, fixture.ownerID)
				if err != nil {
					t.Fatal(err)
				}
			}
			var ack ActionAck
			changed := requireProjectionReplay(t, s, func() {
				var err error
				ack, err = library.Unlink(t.Context(), fixture.token, "media-unlink-original", "owned-a", 1)
				if err != nil {
					t.Fatal(err)
				}
			})
			if !ack.OK || ack.OperationID == "" || ack.Revision != 2 {
				t.Fatal("unlink did not acknowledge the independent link tombstone", ack)
			}
			for _, key := range []projectionKey{{"media_link", "owned-a"}, {"media", "shared-a"}, {ownerType, fixture.ownerID}} {
				if !changed[key] {
					t.Fatal("unlink omitted a real dependent aggregate", key)
				}
			}
			if ownerType == "unit" {
				if !changed[projectionKey{"item", unit.ItemID}] || !changed[projectionKey{"location", "source"}] || !changed[projectionKey{"location", "root"}] || changed[projectionKey{"location", "target"}] {
					t.Fatal("Unit-owned unlink did not advance the actual Item and ancestor path")
				}
				afterUnit, err := loadUnit(t.Context(), s.db, fixture.ownerID)
				if err != nil || afterUnit.Revision != unit.Revision+1 || !reflect.DeepEqual(afterUnit.UnitData, unit.UnitData) {
					t.Fatal("unlink changed a Unit's physical state or metadata", err)
				}
			}
			for key := range changed {
				if key.kind == "search_doc" || key.kind == "inventory_event" || key.kind == "media" && key.id == "shared-b" || key.kind == "media_link" && key.id != "owned-a" {
					t.Fatal("unlink republished an unrelated scope", key)
				}
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE id='owned-a'") != 0 ||
				scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media_link' AND entity_id='owned-a'") != 2 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='media_link' AND entity_id='owned-a'") != 0 ||
				scalar[int](t, s.db, "SELECT COUNT(*) FROM sync_changes WHERE entity_type='media_link' AND entity_id='owned-a' AND entity_revision=2 AND operation='delete' AND projection_json IS NULL") != 1 {
				t.Fatal("unlink omitted the real source deletion or independent tombstone")
			}
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links WHERE id IN ('owned-b','owned-c','sibling')") != 3 ||
				scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-a'") != 8 ||
				scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='media' AND entity_id='shared-b'") != 13 {
				t.Fatal("unlink damaged a shared link or advanced the wrong Media")
			}
			after := domainState(t, s)
			for _, table := range []string{"media", "inventory_events", "stock_balances", "ocr_results", "search_documents", "unit_search_documents"} {
				if before[table] != after[table] {
					t.Fatal("unlink changed unrelated source", table)
				}
			}
		})
	}
}
