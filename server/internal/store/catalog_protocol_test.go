package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCatalogConstructionAndInputBoundsNeverClaimMutationKeys(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	for _, check := range []func() error{
		func() error { _, err := NewCatalog(nil); return err },
		func() error { _, err := NewCatalog(&Auth{}); return err },
		func() error { _, err := (*Store)(nil).NewCatalog(auth); return err },
		func() error { _, err := s.NewCatalog(nil); return err },
		func() error { _, err := openTestStore(t).NewCatalog(auth); return err },
	} {
		if err := check(); !errors.Is(err, ErrDomainInput) {
			t.Fatal("catalog bound outside its private authenticated store", err)
		}
	}
	maximum := createCatalogItem(t, catalog, token, ItemCreate{Name: strings.Repeat("😀", 300), Brand: testPointer(strings.Repeat("😀", 300)),
		Model: testPointer(strings.Repeat("😀", 300)), Description: strings.Repeat("😀", 10000), Notes: strings.Repeat("😀", 10000),
		UnitCode: testPointer(strings.Repeat("😀", 32)), BaseUnit: testPointer("g" + strings.Repeat("_", 31)), ScaleFactor: testPointer(maxSafeInteger)})
	if len([]rune(maximum.Name)) != 300 || maximum.ScaleFactor != maxSafeInteger {
		t.Fatal("valid code-point or integer maxima were narrowed")
	}
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Boundary"})
	before := domainState(t, s)
	for index, input := range []ItemCreate{
		{}, {Name: strings.Repeat("x", 301)}, {Name: string([]byte{0xff})}, {Name: "Item", Brand: testPointer(strings.Repeat("x", 301))},
		{Name: "Item", Model: testPointer(strings.Repeat("x", 301))}, {Name: "Item", CategoryID: testPointer("")}, {Name: "Item", CategoryID: testPointer(strings.Repeat("x", 129))},
		{Name: "Item", Description: strings.Repeat("x", 10001)}, {Name: "Item", Notes: string([]byte{0xff})}, {Name: "Item", TrackingMode: testPointer("mixed")},
		{Name: "Item", UnitCode: testPointer("")}, {Name: "Item", UnitCode: testPointer(strings.Repeat("x", 33))}, {Name: "Item", BaseUnit: testPointer("g\n")},
		{Name: "Item", ScaleFactor: testPointer(int64(0))}, {Name: "Item", ScaleFactor: testPointer(int64(-1))}, {Name: "Item", ScaleFactor: testPointer(maxSafeInteger + 1)},
		{Name: "Item", Status: testPointer("deleted")},
	} {
		if value, err := catalog.Create(t.Context(), token, fmt.Sprintf("invalid-item-create-%d", index), input); !errors.Is(err, ErrDomainInput) || value.ID != "" {
			t.Fatal("invalid create crossed the domain boundary", err)
		}
	}
	for index, patch := range []ItemPatch{
		{}, {Name: testPointer("")}, {Name: testPointer(strings.Repeat("x", 301))}, {Name: testPointer(string([]byte{0xff}))},
		{Brand: NullableString{Set: true, Value: testPointer(strings.Repeat("x", 301))}}, {Model: NullableString{Set: true, Value: testPointer(string([]byte{0xff}))}},
		{CategoryID: NullableID{Set: true, Value: testPointer("")}}, {CategoryID: NullableID{Set: true, Value: testPointer(strings.Repeat("x", 129))}},
		{Brand: NullableString{Value: testPointer("not supplied")}, Name: testPointer("Item")},
		{Model: NullableString{Value: testPointer("not supplied")}, Name: testPointer("Item")},
		{CategoryID: NullableID{Value: testPointer("not supplied")}, Name: testPointer("Item")},
		{Description: testPointer(strings.Repeat("x", 10001))}, {Notes: testPointer(strings.Repeat("x", 10001))},
		{TrackingMode: testPointer("mixed")}, {UnitCode: testPointer("")}, {UnitCode: testPointer(strings.Repeat("x", 33))},
		{BaseUnit: testPointer("1g")}, {BaseUnit: testPointer("g\n")}, {ScaleFactor: testPointer(int64(0))},
		{ScaleFactor: testPointer(maxSafeInteger + 1)}, {Status: testPointer("deleted")},
	} {
		if value, err := catalog.Patch(t.Context(), token, fmt.Sprintf("invalid-item-patch-%d", index), item.ID, 1, patch); !errors.Is(err, ErrDomainInput) || value.ID != "" {
			t.Fatal("invalid patch crossed the domain boundary", err)
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 129), string([]byte{0xff})} {
		for _, check := range []func() error{
			func() error { _, err := catalog.Get(t.Context(), token, id); return err },
			func() error {
				_, err := catalog.Patch(t.Context(), token, "invalid-item-identity", id, 1, ItemPatch{Name: testPointer("Name")})
				return err
			},
			func() error {
				_, err := catalog.Archive(t.Context(), token, "invalid-archive-identity", id, 1, "")
				return err
			},
			func() error {
				_, err := catalog.Delete(t.Context(), token, "invalid-delete-identity", id, 1)
				return err
			},
		} {
			if err := check(); !errors.Is(err, ErrDomainInput) {
				t.Fatal("invalid identity was repaired or admitted", err)
			}
		}
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		want := ErrDomainInput
		if revision == 0 {
			want = ErrPreconditionRequired
		}
		for _, check := range []func() error{
			func() error {
				_, err := catalog.Patch(t.Context(), token, "invalid-item-revision", item.ID, revision, ItemPatch{Name: testPointer("Name")})
				return err
			},
			func() error {
				_, err := catalog.Archive(t.Context(), token, "invalid-archive-revision", item.ID, revision, "")
				return err
			},
			func() error {
				_, err := catalog.Delete(t.Context(), token, "invalid-delete-revision", item.ID, revision)
				return err
			},
		} {
			if err := check(); !errors.Is(err, want) {
				t.Fatal("missing/unsafe revision escaped its precondition", err)
			}
		}
	}
	for _, reason := range []string{strings.Repeat("x", 10001), string([]byte{0xff})} {
		if _, err := catalog.Archive(t.Context(), token, "invalid-archive-reason", item.ID, 1, reason); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid audit reason was admitted", err)
		}
	}
	for _, page := range []struct {
		after string
		limit int
	}{{"", 0}, {"", 101}, {strings.Repeat("x", 129), 1}, {string([]byte{0xff}), 1}} {
		if value, err := catalog.List(t.Context(), token, page.after, page.limit); !errors.Is(err, ErrDomainInput) || value.Data != nil {
			t.Fatal("invalid internal continuation exposed a page", err)
		}
	}
	if _, err := catalog.Create(t.Context(), token, "short", ItemCreate{Name: "Name"}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("invalid mutation key was claimed", err)
	}
	requireDomainState(t, s, before)
}

func TestCatalogFrozenRepliesBindEveryOriginalOperationBodyTargetAndProof(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	input := ItemCreate{Name: "Created"}
	item, err := catalog.Create(t.Context(), token, "original-create-item", input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := catalog.Patch(t.Context(), token, "original-patch-item", item.ID, 1, ItemPatch{Name: testPointer("First")})
	if err != nil {
		t.Fatal(err)
	}
	current, err := catalog.Patch(t.Context(), token, "newer-patch-item", item.ID, 2, ItemPatch{Name: testPointer("Current")})
	if err != nil {
		t.Fatal(err)
	}
	_, fresh := login(t, auth)
	replayed, err := catalog.Patch(t.Context(), fresh, "original-patch-item", item.ID, 1, ItemPatch{Name: testPointer("First")})
	if err != nil || !reflect.DeepEqual(first, replayed) || !reflect.DeepEqual(current, getCatalogItem(t, catalog, fresh, item.ID)) {
		t.Fatal("fresh login could not retrieve a historical original reply without rebasing", err)
	}
	before := domainState(t, s)
	for _, check := range []func() error{
		func() error {
			_, err := catalog.Create(t.Context(), fresh, "original-create-item", ItemCreate{Name: "Changed"})
			return err
		},
		func() error {
			_, err := catalog.Patch(t.Context(), fresh, "original-patch-item", item.ID, 1, ItemPatch{Name: testPointer("Changed")})
			return err
		},
		func() error {
			_, err := catalog.Patch(t.Context(), fresh, "original-patch-item", item.ID, 2, ItemPatch{Name: testPointer("First")})
			return err
		},
		func() error {
			_, err := catalog.Patch(t.Context(), fresh, "original-patch-item", "another-target", 1, ItemPatch{Name: testPointer("First")})
			return err
		},
		func() error {
			_, err := catalog.Archive(t.Context(), fresh, "original-patch-item", item.ID, 1, "")
			return err
		},
		func() error {
			_, err := catalog.Delete(t.Context(), fresh, "original-patch-item", item.ID, 1)
			return err
		},
	} {
		if err := check(); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatal("a changed request reused a catalog key", err)
		}
	}
	requireDomainState(t, s, before)
	archived, err := catalog.Archive(t.Context(), fresh, "original-archive-item", item.ID, 3, "Exact reason")
	if err != nil || archived.Revision != 4 {
		t.Fatal(err)
	}
	noop, err := catalog.Archive(t.Context(), fresh, "already-archived-item", item.ID, 4, "Another exact reason")
	if err != nil || !reflect.DeepEqual(noop, archived) {
		t.Fatal("repeated archival invented another resource revision", err)
	}
	active, err := catalog.Patch(t.Context(), fresh, "original-restore-item", item.ID, 4, ItemPatch{Status: testPointer("active")})
	if err != nil || active.Revision != 5 {
		t.Fatal(err)
	}
	if historical, err := catalog.Archive(t.Context(), fresh, "original-archive-item", item.ID, 3, "Exact reason"); err != nil || !reflect.DeepEqual(historical, archived) || getCatalogItem(t, catalog, fresh, item.ID).Status != "active" {
		t.Fatal("archive replay changed the restored source", err)
	}
	if _, err := catalog.Archive(t.Context(), fresh, "original-archive-item", item.ID, 3, "Different reason"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("audit reason was not part of the original fingerprint", err)
	}
	ack, err := catalog.Delete(t.Context(), fresh, "original-delete-item", item.ID, 5)
	if err != nil || ack.Revision != 6 {
		t.Fatal(err)
	}
	before = domainState(t, s)
	if replay, err := catalog.Delete(t.Context(), fresh, "original-delete-item", item.ID, 5); err != nil || !reflect.DeepEqual(ack, replay) {
		t.Fatal("deleted source was required for its original acknowledgement", err)
	}
	if replay, err := catalog.Create(t.Context(), fresh, "original-create-item", input); err != nil || !reflect.DeepEqual(item, replay) || scalar[int](t, s.db, "SELECT COUNT(*) FROM items") != 0 {
		t.Fatal("creation replay resurrected a deleted source", err)
	}
	if _, err := catalog.Delete(t.Context(), fresh, "another-delete-item", item.ID, 5); !errors.Is(err, ErrNotFound) {
		t.Fatal("a novel deletion key fabricated success for a missing source", err)
	}
	requireDomainState(t, s, before)
}

func TestCatalogConcurrentOriginalRequestsHaveOneIdentityAndConflictingEditsHaveOneWinner(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	start := make(chan struct{})
	values, failures := make(chan Item, 12), make(chan error, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			<-start
			value, err := catalog.Create(t.Context(), token, "concurrent-original-item", ItemCreate{Name: "Concurrent"})
			values <- value
			failures <- err
		})
	}
	close(start)
	group.Wait()
	close(values)
	close(failures)
	var item Item
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	for value := range values {
		if item.ID != "" && !reflect.DeepEqual(value, item) {
			t.Fatal("concurrent identical creates received different frozen responses")
		}
		item = value
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM items") != 1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 1 {
		t.Fatal("concurrent identical keys created more than one item/event")
	}
	start = make(chan struct{})
	failures = make(chan error, 2)
	for index := range 2 {
		group.Go(func() {
			<-start
			_, err := catalog.Patch(t.Context(), token, fmt.Sprintf("conflicting-item-%d", index), item.ID, 1, ItemPatch{Name: testPointer(fmt.Sprintf("Winner%d", index))})
			failures <- err
		})
	}
	close(start)
	group.Wait()
	close(failures)
	var successes int
	for err := range failures {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrRevisionConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 || getCatalogItem(t, catalog, token, item.ID).Revision != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events") != 2 {
		t.Fatal("two original stale reviews were silently serialized into overwrites")
	}
}

func TestCatalogAllReadsWritesAndFrozenRepliesRequireActiveCompletedEpochSession(t *testing.T) {
	for _, state := range []string{"mandatory", "revoked", "expired", "epoch", "invalid"} {
		t.Run(state, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item, err := catalog.Create(t.Context(), token, "catalog-replay-denial", ItemCreate{Name: "Original"})
			if err != nil {
				t.Fatal(err)
			}
			want := ErrAuthentication
			switch state {
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(auth.now()))
			case "expired":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(auth.now()))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-epoch'")
			case "invalid":
				token = "invalid"
			}
			before := domainState(t, s)
			for _, check := range []func() error{
				func() error { _, err := catalog.Get(t.Context(), token, item.ID); return err },
				func() error { _, err := catalog.List(t.Context(), token, "", 100); return err },
				func() error {
					_, err := catalog.Create(t.Context(), token, "denied-new-item", ItemCreate{Name: "New"})
					return err
				},
				func() error {
					_, err := catalog.Patch(t.Context(), token, "denied-item-patch", item.ID, 1, ItemPatch{Name: testPointer("New")})
					return err
				},
				func() error {
					_, err := catalog.Archive(t.Context(), token, "denied-item-archive", item.ID, 1, "")
					return err
				},
				func() error {
					_, err := catalog.Delete(t.Context(), token, "denied-item-delete", item.ID, 1)
					return err
				},
				func() error {
					_, err := catalog.Create(t.Context(), token, "catalog-replay-denial", ItemCreate{Name: "Original"})
					return err
				},
			} {
				if err := check(); !errors.Is(err, want) {
					t.Fatal("catalog operation bypassed transactional authorization", err)
				}
				requireDomainState(t, s, before)
			}
		})
	}
}

func TestCatalogExpiredClaimsAndCancelledRequestsCannotBecomeNewWrites(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	item, err := catalog.Create(t.Context(), token, "expired-original-item", ItemCreate{Name: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	later := auth.now().Add(31 * 24 * time.Hour)
	auth.now = func() time.Time { return later }
	_, fresh := login(t, auth)
	before := domainState(t, s)
	if value, err := catalog.Create(t.Context(), fresh, "expired-original-item", ItemCreate{Name: "Original"}); !errors.Is(err, ErrIdempotencyExpired) || value.ID != "" {
		t.Fatal("an expired claim was deleted/reused as a fresh create", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := catalog.Create(ctx, fresh, "cancelled-item-create", ItemCreate{Name: "New"}); !errors.Is(err, context.Canceled) || value.ID != "" {
		t.Fatal("a cancelled request acquired a new mutation", err)
	}
	if value, err := catalog.Get(ctx, fresh, item.ID); !errors.Is(err, context.Canceled) || value.ID != "" {
		t.Fatal("a cancelled read exposed a resource", err)
	}
	requireDomainState(t, s, before)
}
