package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAssetPurchaseConcurrentOriginalProofsPostOneFinancialAttribution(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "different keys", true: "same key"}[sameKey], func(t *testing.T) {
			fixture, purchases, assets, profile := purchaseAssetFixture(t)
			input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 123}
			type result struct {
				value PurchaseLine
				err   error
			}
			start, results := make(chan struct{}), make(chan result, 2)
			for index := range 2 {
				key := "purchase-asset-race-first"
				if index == 1 && !sameKey {
					key = "purchase-asset-race-second"
				}
				go func() {
					<-start
					value, err := purchases.CreateLine(t.Context(), fixture.token, key, fixture.purchase.ID, fixture.purchase.Revision, input)
					results <- result{value, err}
				}()
			}
			close(start)
			first, second := <-results, <-results
			if sameKey {
				if first.err != nil || second.err != nil || !reflect.DeepEqual(first.value, second.value) {
					t.Fatal("identical original commands did not return one frozen financial attribution", first.err, second.err)
				}
			} else {
				if first.err != nil {
					first, second = second, first
				}
				if first.err != nil || !errors.Is(second.err, ErrRevisionConflict) || second.value != (PurchaseLine{}) {
					t.Fatal("competing reviewed commands both posted or silently rebased", first.err, second.err)
				}
			}
			current, err := assets.Get(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil || current.Revision != profile.Revision+1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM purchase_lines WHERE item_unit_id=?", fixture.unit.ID) != 2 {
				t.Fatal("concurrent allocation changed financial source/authority more than once", current, err)
			}
		})
	}
}

func TestAssetPurchaseOriginalRepliesSurviveFreshLoginAndInvalidLaterSourceWithoutRebaseOrExpiredReuse(t *testing.T) {
	fixture, purchases, _, _ := purchaseAssetFixture(t)
	s := fixture.store
	input := PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 123}
	original, err := purchases.CreateLine(t.Context(), fixture.token, "purchase-asset-original-reply", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
	execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type='asset'")
	execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type='asset'")
	_, fresh := login(t, fixture.auth)
	before := assetProfileState(t, s)
	if frozen, err := purchases.CreateLine(t.Context(), fresh, "purchase-asset-original-reply", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("current policy/authority rebased an original financial response", frozen, err)
	}
	requireAssetProfileState(t, s, before)
	changed := input
	changed.PaidMinor++
	for _, test := range []struct {
		proof int64
		input PurchaseLineCreate
	}{{fixture.purchase.Revision, changed}, {fixture.purchase.Revision + 1, input}} {
		if value, err := purchases.CreateLine(t.Context(), fresh, "purchase-asset-original-reply", fixture.purchase.ID, test.proof, test.input); !errors.Is(err, ErrIdempotencyConflict) || value != (PurchaseLine{}) {
			t.Fatal("changed original financial command reused its committed claim", value, err)
		}
		requireAssetProfileState(t, s, before)
	}
	parent, err := purchases.Get(t.Context(), fresh, fixture.purchase.ID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := fixture.catalog.Get(t.Context(), fresh, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed = input
	changed.ItemRevision = item.Revision
	before = assetProfileState(t, s)
	if value, err := purchases.CreateLine(t.Context(), fresh, "purchase-asset-new-review", parent.ID, parent.Revision, changed); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
		t.Fatal("fresh key skipped complete current Asset authority", value, err)
	}
	requireAssetProfileState(t, s, before)
	now := fixture.auth.now().Add(domainReplyLifetime + time.Second)
	fixture.auth.now = func() time.Time { return now }
	_, fresh = login(t, fixture.auth)
	before = assetProfileState(t, s)
	if value, err := purchases.CreateLine(t.Context(), fresh, "purchase-asset-original-reply", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrIdempotencyExpired) || value != (PurchaseLine{}) {
		t.Fatal("expired financial claim became another allocation or lost its original identity", value, err)
	}
	requireAssetProfileState(t, s, before)
}
