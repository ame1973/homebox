package store

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func recordAssetEvent(ctx context.Context, service *Assets, token, key, id string, revision int64, kind string) (domainResult, error) {
	if kind == "usage" {
		return service.RecordUsage(ctx, token, key, id, revision, usageInput(1))
	}
	return service.RecordCost(ctx, token, key, id, revision, costInput(1))
}

func zeroAssetEvent(value domainResult) bool {
	return value == nil || reflect.ValueOf(value).IsZero()
}

func TestAssetEventsConcurrentOriginalProofsNeverDoubleAppend(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
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
						key := "event-concurrent-one"
						if i == 1 && !sameKey {
							key = "event-concurrent-two"
						}
						values[i], failures[i] = recordAssetEvent(t.Context(), service, fixture.token, key, fixture.unit.ID, asset.Revision, kind)
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
						t.Fatal("unexpected concurrent event result", values[i], err)
					}
				}
				if sameKey && (succeeded != 2 || !reflect.DeepEqual(values[0], values[1])) || !sameKey && (succeeded != 1 || conflicted != 1) || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM asset_"+kind+"_events") != 1 {
					t.Fatal("competing Asset proofs double-appended or original-key replay wrote another event")
				}
			})
		}
	}
}

func TestAssetEventsChangedOriginalBodyProofOwnerOrOperationCannotReuseAClaim(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			if _, err := recordAssetEvent(t.Context(), service, fixture.token, "event-bound-original", fixture.unit.ID, asset.Revision, kind); err != nil {
				t.Fatal(err)
			}
			before := assetProfileState(t, s)
			for _, change := range []string{"proof", "owner", "operation", "body", "equivalent date spelling"} {
				key, owner, proof, operation := "event-bound-original", fixture.unit.ID, asset.Revision, kind
				if change == "proof" {
					proof++
				} else if change == "owner" {
					owner = "other-unqueried-owner"
				} else if change == "operation" {
					operation = map[string]string{"usage": "cost", "cost": "usage"}[kind]
				}
				var value domainResult
				var err error
				if change == "body" || change == "equivalent date spelling" {
					if kind == "usage" {
						input := usageInput(1)
						if change == "body" {
							input.Delta = 2
						} else {
							input.OccurredAt = "2026-11-07T04:00:00.123456789Z"
						}
						value, err = service.RecordUsage(t.Context(), fixture.token, key, owner, proof, input)
					} else {
						input := costInput(1)
						if change == "body" {
							input.AmountMinor = 2
						} else {
							input.OccurredAt = "2026-11-07T04:00:00.123456789Z"
						}
						value, err = service.RecordCost(t.Context(), fixture.token, key, owner, proof, input)
					}
				} else {
					value, err = recordAssetEvent(t.Context(), service, fixture.token, key, owner, proof, operation)
				}
				if !errors.Is(err, ErrIdempotencyConflict) || !zeroAssetEvent(value) {
					t.Fatal("original event claim accepted a changed command or semantic date rebase", change, value, err)
				}
				requireAssetEventState(t, s, before)
			}
			if value, err := recordAssetEvent(t.Context(), service, fixture.token, "event-new-key-stale-proof", fixture.unit.ID, asset.Revision, kind); !errors.Is(err, ErrRevisionConflict) || !zeroAssetEvent(value) {
				t.Fatal("fresh key silently refreshed a stale Asset proof", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetEventsOriginalReconciliationPrecedesCurrentMoveCurrencyAndMissingCounters(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			original, err := recordAssetEvent(t.Context(), service, fixture.token, "event-original-before-changes", fixture.unit.ID, asset.Revision, kind)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := s.NewInventory(fixture.auth)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = inventory.MoveUnit(t.Context(), fixture.token, "event-later-real-move", fixture.unit.ID, fixture.unit.Revision, "target"); err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
			execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID)
			execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='asset' AND entity_id=?", fixture.unit.ID)
			_, fresh := login(t, fixture.auth)
			before := assetProfileState(t, s)
			value, err := recordAssetEvent(t.Context(), service, fresh, "event-original-before-changes", fixture.unit.ID, asset.Revision, kind)
			if err != nil || !reflect.DeepEqual(value, original) {
				t.Fatal("explicit original reconciliation fetched current proofs or revalidated current finance", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetEventsExpiredOriginalClaimsStayRetainedAfterFreshLogin(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			if _, err := recordAssetEvent(t.Context(), service, fixture.token, "event-expired-original", fixture.unit.ID, asset.Revision, kind); err != nil {
				t.Fatal(err)
			}
			later := fixture.auth.now().Add(31 * 24 * time.Hour)
			fixture.auth.now = func() time.Time { return later }
			_, fresh := login(t, fixture.auth)
			before := assetProfileState(t, s)
			if value, err := recordAssetEvent(t.Context(), service, fresh, "event-expired-original", fixture.unit.ID, asset.Revision, kind); !errors.Is(err, ErrIdempotencyExpired) || !zeroAssetEvent(value) {
				t.Fatal("expired claim was deleted/reused or replaced with another financial append", value, err)
			}
			requireAssetEventState(t, s, before)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_"+kind+"_events") != 1 {
				t.Fatal("expired original event was rewritten or appended again")
			}
		})
	}
}
