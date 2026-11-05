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

func TestCatalogTagsAdmissionRequiresExplicitBoundedCompleteOriginalSet(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	_, tags := catalogTags(t, auth, token, 2)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	before := domainState(t, s)
	for _, input := range [][]string{nil, make([]string, 101), {""}, {strings.Repeat("界", 129)}, {string([]byte{0xff})}, {tags[0].ID, tags[0].ID}} {
		if value, err := catalog.ReplaceTags(t.Context(), token, "invalid-complete-tag-set", item.ID, 1, input); !errors.Is(err, ErrDomainInput) || value.ID != "" {
			t.Fatal("missing/null/duplicate/oversized/invalid membership was accepted", err)
		}
	}
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if _, err := catalog.ReplaceTags(t.Context(), token, "invalid-membership-owner", id, 1, []string{}); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid owner bypassed the original-subject gate", err)
		}
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		want := ErrDomainInput
		if revision == 0 {
			want = ErrPreconditionRequired
		}
		if value, err := catalog.ReplaceTags(t.Context(), token, "invalid-membership-proof", item.ID, revision, []string{}); !errors.Is(err, want) || value.ID != "" {
			t.Fatal("missing/unsafe Item proof was repaired or ignored", err)
		}
	}
	if _, err := catalog.ReplaceTags(t.Context(), token, "short", item.ID, 1, []string{}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("invalid membership key was claimed", err)
	}
	for _, input := range [][]string{{"missing-tag"}, {tags[0].ID, "missing-tag"}} {
		if value, err := catalog.ReplaceTags(t.Context(), token, "missing-membership-tag", item.ID, 1, input); !errors.Is(err, ErrNotFound) || value.ID != "" {
			t.Fatal("partial/implicit tag creation replaced a rejected complete set", err)
		}
	}
	if value, err := catalog.ReplaceTags(t.Context(), token, "missing-membership-owner", "missing-owner", 1, []string{tags[0].ID}); !errors.Is(err, ErrNotFound) || value.ID != "" {
		t.Fatal("missing owner acquired a fabricated membership acknowledgement", err)
	}
	if _, err := catalog.ReplaceTags(t.Context(), token, "stale-membership-proof", item.ID, 2, []string{}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale equal-empty set was treated as an authorized no-op", err)
	}
	requireDomainState(t, s, before)
}

func TestCatalogTagsAuthorizationExpiredClaimsAndCancellationNeverReplay(t *testing.T) {
	for _, state := range []string{"mandatory", "revoked", "expired", "epoch", "invalid", "expired claim", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			_, tags := catalogTags(t, auth, token, 1)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			if _, err := catalog.ReplaceTags(t.Context(), token, "membership-auth-original", item.ID, 1, []string{tags[0].ID}); err != nil {
				t.Fatal(err)
			}
			ctx, want := t.Context(), ErrAuthentication
			switch state {
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(auth.now()))
			case "expired":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(auth.now()))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-tag-set'")
			case "invalid":
				token = "invalid"
			case "expired claim":
				now := auth.now().Add(31 * 24 * time.Hour)
				auth.now = func() time.Time { return now }
				_, token = login(t, auth)
				want = ErrIdempotencyExpired
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			before := domainState(t, s)
			if value, err := catalog.ReplaceTags(ctx, token, "membership-auth-original", item.ID, 1, []string{tags[0].ID}); !errors.Is(err, want) || value.ID != "" {
				t.Fatal("original membership key bypassed active-session/expiry/cancellation rules", err)
			}
			if state != "expired claim" {
				if value, err := catalog.ReplaceTags(ctx, token, "membership-auth-new", item.ID, 2, []string{}); !errors.Is(err, want) || value.ID != "" {
					t.Fatal("new membership write bypassed transactional authorization", err)
				}
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogTagsConcurrentOriginalsAndConflictingSetsHaveOneCommit(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	_, tags := catalogTags(t, auth, token, 2)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	type reply struct {
		item Item
		err  error
	}
	start, results := make(chan struct{}), make(chan reply, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			<-start
			value, err := catalog.ReplaceTags(t.Context(), token, "concurrent-original-membership", item.ID, 1, []string{tags[0].ID})
			results <- reply{value, err}
		})
	}
	close(start)
	group.Wait()
	close(results)
	var original Item
	for result := range results {
		if result.err != nil || original.ID != "" && !reflect.DeepEqual(original, result.item) {
			t.Fatal("concurrent identical sets produced different frozen owning Items", result.err)
		}
		original = result.item
	}
	if original.Revision != 2 || scalar[int](t, s.db, "SELECT COUNT(*) FROM item_tags") != 1 ||
		scalar[int](t, s.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='put_items_id_tags'") != 1 {
		t.Fatal("concurrent originals applied membership more than once")
	}
	start, results = make(chan struct{}), make(chan reply, 2)
	for index, ids := range [][]string{{tags[1].ID}, {}} {
		group.Go(func() {
			<-start
			value, err := catalog.ReplaceTags(t.Context(), token, fmt.Sprintf("concurrent-conflicting-membership-%d", index), item.ID, 2, ids)
			results <- reply{value, err}
		})
	}
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
		} else if !errors.Is(result.err, ErrRevisionConflict) {
			t.Fatal(result.err)
		}
	}
	if successes != 1 || getCatalogItem(t, catalog, token, item.ID).Revision != 3 {
		t.Fatal("two complete-set reviews were silently rebased into overwrites")
	}
}

func TestCatalogTagsSafeLimitNoopAndAuthorityQueryFailuresRemainReadOnly(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	_, tags := catalogTags(t, auth, token, 1)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	if _, err := catalog.ReplaceTags(t.Context(), token, "maximum-tag-set-baseline", item.ID, 1, []string{tags[0].ID}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []projectionKey{{"item", item.ID}, {"tag", tags[0].ID}, {"item_tag", projectionPair(item.ID, tags[0].ID)}} {
		setCatalogProjectionRevision(t, s, key.kind, key.id, maxSafeInteger)
	}
	execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
	execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
	before := domainState(t, s)
	value, err := catalog.ReplaceTags(t.Context(), token, "maximum-tag-set-noop", item.ID, maxSafeInteger, []string{tags[0].ID})
	if err != nil || value.Revision != maxSafeInteger {
		t.Fatal("equal complete set invented a revision at a safe limit", err)
	}
	after := domainState(t, s)
	for table, contents := range before {
		if table != "api_idempotency" && table != "mutation_events" && after[table] != contents {
			t.Fatal("safe-limit no-op changed source/publication", table)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateItemTagAuthority(ctx, s.db, item.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("failed authority query was treated as a valid membership state", err)
	}
}
