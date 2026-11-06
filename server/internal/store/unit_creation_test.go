package store

import (
	"errors"
	"testing"
)

func TestUnitCreationRejectsMissingInactiveAndUnprovenDestinationsBeforeSourcePublication(t *testing.T) {
	for _, failure := range []string{"missing", "archived leaf", "archived ancestor", "missing ancestor counter"} {
		t.Run(failure, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			destination, want := "target", ErrLocationArchived
			switch failure {
			case "missing":
				destination, want = "absent", ErrNotFound
			case "archived leaf":
				execSQL(t, fixture.store.db, "UPDATE locations SET status='archived' WHERE id='target'")
			case "archived ancestor":
				execSQL(t, fixture.store.db, "UPDATE locations SET status='archived' WHERE id='root'")
			case "missing ancestor counter":
				execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type='location' AND entity_id='root'")
				execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type='location' AND entity_id='root'")
				want = ErrDomainState
			}
			before := domainState(t, fixture.store)
			if unit, err := inventory.CreateUnit(t.Context(), fixture.token, "bad-unit-creation-placement", fixture.individual.ID, fixture.individual.Revision, UnitCreate{LocationID: &destination}); !errors.Is(err, want) || unit != (Unit{}) {
				t.Fatal("invalid destination/path created a Unit/code/event", err)
			}
			requireDomainState(t, fixture.store, before)
		})
	}
}

func TestUnitNewEpochOriginalKeyCreatesOnlyANewExplicitlyReviewedIdentity(t *testing.T) {
	fixture, inventory := inventoryFixture(t)
	first := createUnitTest(t, fixture, inventory, "same-unit-key-two-epochs", nil)
	execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='new-unit-dataset-epoch'")
	before := domainState(t, fixture.store)
	if unit, err := inventory.CreateUnit(t.Context(), fixture.token, "same-unit-key-two-epochs", fixture.individual.ID, fixture.individual.Revision, UnitCreate{}); !errors.Is(err, ErrAuthentication) || unit != (Unit{}) {
		t.Fatal("restore revived an old Unit credential/receipt", err)
	}
	requireDomainState(t, fixture.store, before)
	_, fixture.token = login(t, fixture.auth)
	before = domainState(t, fixture.store)
	if _, err := inventory.CreateUnit(t.Context(), fixture.token, "same-unit-key-two-epochs", fixture.individual.ID, fixture.individual.Revision, UnitCreate{}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("a new epoch silently adopted an old Item proof", err)
	}
	requireDomainState(t, fixture.store, before)
	second, err := inventory.CreateUnit(t.Context(), fixture.token, "same-unit-key-two-epochs", fixture.individual.ID, fixture.individual.Revision+1, UnitCreate{})
	if err != nil || second.ID == first.ID || second.AssetCode == first.AssetCode || second.Revision != 1 {
		t.Fatal("new scoped receipt reused/rebased an earlier Unit identity", err)
	}
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events WHERE idempotency_key IS NULL") != 2 {
		t.Fatal("Unit producer persisted an unscoped raw movement key")
	}
}
