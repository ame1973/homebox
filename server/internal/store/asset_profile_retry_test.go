package store

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestAssetProfileConcurrentReviewedAbsenceNeverOverwritesOrDoubleCreates(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "different keys", true: "same original key"}[sameKey], func(t *testing.T) {
			fixture := holdingsFixture(t)
			service := assetReader(t, fixture)
			input := manualProfileInput(fixture.individual.Revision, 0)
			var wait sync.WaitGroup
			values := make([]Asset, 2)
			errorsFound := make([]error, 2)
			for i := range 2 {
				wait.Add(1)
				go func() {
					defer wait.Done()
					key := "profile-concurrent-one"
					if i == 1 && !sameKey {
						key = "profile-concurrent-two"
					}
					values[i], errorsFound[i] = service.PutProfile(t.Context(), fixture.token, key, fixture.unit.ID, fixture.unit.Revision, input)
				}()
			}
			wait.Wait()
			succeeded, conflicted := 0, 0
			for i, err := range errorsFound {
				if err == nil {
					succeeded++
					if values[i].Revision != 1 {
						t.Fatal("concurrent opt-in did not preserve independent revision one")
					}
				} else if errors.Is(err, ErrRevisionConflict) && values[i] == (Asset{}) {
					conflicted++
				} else {
					t.Fatal("unexpected concurrent result", values[i], err)
				}
			}
			if sameKey && (succeeded != 2 || !reflect.DeepEqual(values[0], values[1])) || !sameKey && (succeeded != 1 || conflicted != 1) || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM asset_profiles") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM asset_profile_changes") != 1 {
				t.Fatal("reviewed absence was inferred from coincident counters or original-key replay wrote again")
			}
		})
	}
}

func TestAssetProfileExpiredOriginalClaimRequiresReviewWithoutDeletingOrReusingIt(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	input := manualProfileInput(fixture.individual.Revision, 0)
	if _, err := service.PutProfile(t.Context(), fixture.token, "profile-expired-original", fixture.unit.ID, fixture.unit.Revision, input); err != nil {
		t.Fatal(err)
	}
	later := fixture.auth.now().Add(31 * 24 * time.Hour)
	fixture.auth.now = func() time.Time { return later }
	_, fresh := login(t, fixture.auth)
	before := assetProfileState(t, fixture.store)
	if value, err := service.PutProfile(t.Context(), fresh, "profile-expired-original", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrIdempotencyExpired) || value != (Asset{}) {
		t.Fatal("expired financial claim was reused as new money or rebased", value, err)
	}
	requireAssetProfileState(t, fixture.store, before)
	if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM api_idempotency WHERE operation='put_units_id_asset'") != 1 {
		t.Fatal("expired original financial claim was deleted")
	}
}

func TestAssetProfileReconciliationPrecedesCurrentMoveAndCurrencyEligibility(t *testing.T) {
	fixture := holdingsFixture(t)
	service := assetReader(t, fixture)
	input := manualProfileInput(fixture.individual.Revision, 100)
	original, err := service.PutProfile(t.Context(), fixture.token, "profile-before-current-eligibility", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := fixture.store.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.MoveUnit(t.Context(), fixture.token, "profile-later-real-move", fixture.unit.ID, fixture.unit.Revision, "target"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, fixture.store.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
	_, fresh := login(t, fixture.auth)
	before := assetProfileState(t, fixture.store)
	if value, err := service.PutProfile(t.Context(), fresh, "profile-before-current-eligibility", fixture.unit.ID, fixture.unit.Revision, input); err != nil || !reflect.DeepEqual(value, original) {
		t.Fatal("original reconciliation fetched/rebased current proofs or reapplied current currency policy", value, err)
	}
	requireAssetProfileState(t, fixture.store, before)
}
