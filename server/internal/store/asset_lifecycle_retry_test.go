package store

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestAssetLifecycleConcurrentOriginalProofsCommitOnlyOnePhysicalFinancialAction(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		for _, sameKey := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{false: "different keys", true: "same original key"}[sameKey], func(t *testing.T) {
				fixture, asset := manualAssetFixture(t, 0)
				service := assetReader(t, fixture)
				var wait sync.WaitGroup
				values := make([]domainResult, 2)
				failures := make([]error, 2)
				for i := range 2 {
					wait.Add(1)
					go func() {
						defer wait.Done()
						key := "lifecycle-concurrent-one"
						if i == 1 && !sameKey {
							key = "lifecycle-concurrent-two"
						}
						values[i], failures[i] = recordAssetLifecycle(t.Context(), service, fixture.token, key, fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind)
					}()
				}
				wait.Wait()
				succeeded, conflicted := 0, 0
				for i, err := range failures {
					if err == nil && !zeroAssetEvent(values[i]) {
						succeeded++
					} else if errors.Is(err, ErrRevisionConflict) && zeroAssetEvent(values[i]) {
						conflicted++
					} else {
						t.Fatal("unexpected concurrent lifecycle result", values[i], err)
					}
				}
				if sameKey && (succeeded != 2 || !reflect.DeepEqual(values[0], values[1])) || !sameKey && (succeeded != 1 || conflicted != 1) || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM asset_profile_changes") != 1 {
					t.Fatal("concurrent original proofs duplicated physical removal, finance or audit context")
				}
			})
		}
	}
}

func TestAssetLifecycleCompetingRetirementAndDisposalDoNotShareOrRebaseProofs(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "different keys", true: "same key different operations"}[sameKey], func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			service := assetReader(t, fixture)
			var wait sync.WaitGroup
			values := make([]domainResult, 2)
			failures := make([]error, 2)
			for i, kind := range []string{"retire", "dispose"} {
				wait.Add(1)
				go func() {
					defer wait.Done()
					key := "lifecycle-racing-one"
					if i == 1 && !sameKey {
						key = "lifecycle-racing-two"
					}
					values[i], failures[i] = recordAssetLifecycle(t.Context(), service, fixture.token, key, fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind)
				}()
			}
			wait.Wait()
			want, succeeded, rejected := ErrRevisionConflict, 0, 0
			if sameKey {
				want = ErrIdempotencyConflict
			}
			for i, err := range failures {
				if err == nil && !zeroAssetEvent(values[i]) {
					succeeded++
				} else if errors.Is(err, want) && zeroAssetEvent(values[i]) {
					rejected++
				} else {
					t.Fatal("racing lifecycle silently switched operation or refreshed either proof", values[i], err)
				}
			}
			if succeeded != 1 || rejected != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM inventory_events") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM asset_profile_changes") != 1 {
				t.Fatal("racing retire/dispose committed a mixed or doubled lifecycle")
			}
		})
	}
}

func TestAssetLifecycleChangedOriginalProofBodyOwnerOrOperationCannotReuseAClaim(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			if _, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-bound-original", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err != nil {
				t.Fatal(err)
			}
			before := assetProfileState(t, s)
			for _, change := range []string{"Asset proof", "Unit proof", "owner", "operation", "body", "equivalent date"} {
				owner, assetProof, unitProof, operation := fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind
				if change == "Asset proof" {
					assetProof++
				} else if change == "Unit proof" {
					unitProof++
				} else if change == "owner" {
					owner = "unqueried-other-unit"
				} else if change == "operation" {
					operation = map[string]string{"retire": "dispose", "dispose": "retire"}[kind]
				}
				var value domainResult
				var err error
				if change == "body" || change == "equivalent date" {
					if kind == "retire" {
						input := retirementInput(unitProof)
						if change == "body" {
							input.Note = "Changed original intent"
						} else {
							input.RetiredAt = "2026-11-07T04:00:00.123456789Z"
						}
						value, err = service.Retire(t.Context(), fixture.token, "lifecycle-bound-original", owner, assetProof, input)
					} else {
						input := disposalInput(unitProof, 0)
						if change == "body" {
							input.RealizedProceedsMinor = 1
						} else {
							input.DisposedAt = "2026-11-07T04:00:00.123456789Z"
						}
						value, err = service.Dispose(t.Context(), fixture.token, "lifecycle-bound-original", owner, assetProof, input)
					}
				} else {
					value, err = recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-bound-original", owner, assetProof, unitProof, operation)
				}
				if !errors.Is(err, ErrIdempotencyConflict) || !zeroAssetEvent(value) {
					t.Fatal("original lifecycle claim was changed or silently rebased", change, value, err)
				}
				requireAssetEventState(t, s, before)
			}
		})
	}
}

func TestAssetLifecycleOriginalReplySurvivesLaterMetadataMissingAuthorityAndFreshLogin(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			original, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-original-before-change", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind)
			if err != nil {
				t.Fatal(err)
			}
			unit, err := loadUnit(t.Context(), s.db, fixture.unit.ID)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := s.NewInventory(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = inventory.PatchUnit(t.Context(), fixture.token, "lifecycle-later-metadata", unit.ID, unit.Revision, UnitPatch{Notes: testPointer("Later real metadata")}); err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID)
			execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID)
			_, fresh := login(t, fixture.auth)
			before := assetProfileState(t, s)
			value, err := recordAssetLifecycle(t.Context(), service, fresh, "lifecycle-original-before-change", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind)
			if err != nil || !reflect.DeepEqual(value, original) {
				t.Fatal("explicit reconciliation read current proofs/currency or repeated a physical action", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetLifecycleExpiredOriginalClaimsCannotBecomeNewTerminalActions(t *testing.T) {
	for _, kind := range []string{"retire", "dispose"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			if _, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-expired-original", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); err != nil {
				t.Fatal(err)
			}
			later := fixture.auth.now().Add(31 * 24 * time.Hour)
			fixture.auth.now = func() time.Time { return later }
			_, fresh := login(t, fixture.auth)
			before := assetProfileState(t, s)
			if value, err := recordAssetLifecycle(t.Context(), service, fresh, "lifecycle-expired-original", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); !errors.Is(err, ErrIdempotencyExpired) || !zeroAssetEvent(value) {
				t.Fatal("expired original lifecycle key was deleted, reused or replayed as another action", value, err)
			}
			requireAssetEventState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM inventory_events") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_profile_changes") != 1 {
				t.Fatal("expired claim duplicated physical history or immutable context")
			}
		})
	}
}
