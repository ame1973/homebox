package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPurchaseLineCorrectionOriginalReplaySurvivesNewInterpretationFreshLoginAndMissingCurrentSource(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1500, PaidMinor: 0}
	original, err := purchases.CorrectLine(t.Context(), fixture.token, "original-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	assets := assetReader(t, fixture.holdingFixture)
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	if _, err = assets.PutProfile(t.Context(), fixture.token, "later-manual-interpretation", fixture.unit.ID, fixture.unit.Revision, manualProfileInput(item.Revision, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = purchases.Patch(t.Context(), fixture.token, "later-source-description", fixture.purchase.ID, fixture.purchase.Revision+1, PurchasePatch{MerchantName: testPointer("Later merchant")}); err != nil {
		t.Fatal(err)
	}
	// Reconciliation is of the original committed response, not a new read or a
	// rebased proposal. Even unavailable current authority must not duplicate it.
	execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type='purchase_line' AND entity_id=?", fixture.unitLine.ID)
	_, fresh := login(t, fixture.auth)
	before := assetProfileState(t, fixture.store)
	if frozen, err := purchases.CorrectLine(t.Context(), fresh, "original-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("original reconciliation depended on later/missing source or current eligibility", err)
	}
	requireAssetProfileState(t, fixture.store, before)
	for _, change := range []func(*PurchaseLineCorrectionCreate){
		func(v *PurchaseLineCorrectionCreate) { v.LineRevision++ },
		func(v *PurchaseLineCorrectionCreate) { v.ItemRevision++ },
		func(v *PurchaseLineCorrectionCreate) { v.Reason += " " },
		func(v *PurchaseLineCorrectionCreate) { v.Replacement.ItemRevision++ },
		func(v *PurchaseLineCorrectionCreate) { v.Replacement.PaidMinor++ },
		func(v *PurchaseLineCorrectionCreate) { v.Replacement.Note = "Changed exact intent" },
		func(v *PurchaseLineCorrectionCreate) {
			v.Replacement.ItemUnitID = testPointer("missing")
			v.Replacement.QuantityBase = 1
		},
		func(v *PurchaseLineCorrectionCreate) { v.Action = "void"; v.Replacement = nil },
	} {
		altered, replacement := input, *input.Replacement
		altered.Replacement = &replacement
		change(&altered)
		if value, err := purchases.CorrectLine(t.Context(), fresh, "original-line-correction", fixture.purchase.ID, fixture.purchase.Revision, altered); !errors.Is(err, ErrIdempotencyConflict) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("changed original proof/body reused a committed correction key", err)
		}
		requireAssetProfileState(t, fixture.store, before)
	}
	if value, err := purchases.CorrectLine(t.Context(), fresh, "original-line-correction", fixture.purchase.ID, fixture.purchase.Revision+1, input); !errors.Is(err, ErrIdempotencyConflict) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("changed Purchase proof reused a committed correction key", err)
	}
	if value, err := purchases.CreateLine(t.Context(), fresh, "original-line-correction", fixture.purchase.ID, fixture.purchase.Revision, *input.Replacement); !errors.Is(err, ErrIdempotencyConflict) || value != (PurchaseLine{}) {
		t.Fatal("another financial operation reused a correction key", err)
	}
	requireAssetProfileState(t, fixture.store, before)
}

func TestPurchaseLineCorrectionExpiryRetainsClaimAfterFreshAuthentication(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.stockLine)
	if _, err := purchases.CorrectLine(t.Context(), fixture.token, "expiring-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal(err)
	}
	now := fixture.auth.now().Add(domainReplyLifetime + time.Second)
	fixture.auth.now = func() time.Time { return now }
	_, fresh := login(t, fixture.auth)
	before := purchaseCommandState(t, fixture.store)
	if value, err := purchases.CorrectLine(t.Context(), fresh, "expiring-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrIdempotencyExpired) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("expired correction claim was discarded/reapplied", err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
}

func TestPurchaseLineCorrectionRequiresCompletedAuthorizationBeforeSourceHistoryAndReplay(t *testing.T) {
	for _, failure := range []string{"revoked", "mandatory", "epoch", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases := purchaseCommands(t)
			input := reviewedLineVoid(t, fixture, fixture.stockLine)
			if _, err := purchases.CorrectLine(t.Context(), fixture.token, "protected-line-correction", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
				t.Fatal(err)
			}
			want, ctx := ErrAuthentication, t.Context()
			switch failure {
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='new-correction-epoch'")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			before := purchaseCommandState(t, fixture.store)
			for _, key := range []string{"protected-line-correction", "fresh-protected-correction"} {
				if value, err := purchases.CorrectLine(ctx, fixture.token, key, fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("uncompleted session exposed source/frozen correction", err)
				}
			}
			if page, err := purchases.PageLineCorrections(ctx, fixture.token, fixture.purchase.ID, "", 50); !errors.Is(err, want) || page.Data != nil || page.NextCursor != nil {
				t.Fatal("uncompleted session exposed private correction history", err)
			}
			requirePurchaseCommandState(t, fixture.store, before)
		})
	}
}

func TestPurchaseLineCorrectionChecksOriginalIndependentOwnerLineAndItemProofs(t *testing.T) {
	for _, failure := range []string{"missing Purchase", "foreign Purchase", "missing Line", "stale Purchase", "stale Line", "stale Item", "missing Purchase proof", "unsafe Purchase proof", "Purchase counter", "Line counter", "Item counter", "Unit counter", "short key", "long key"} {
		t.Run(failure, func(t *testing.T) {
			fixture, purchases := purchaseCommands(t)
			input := reviewedLineVoid(t, fixture, fixture.unitLine)
			owner, revision, key, want := fixture.purchase.ID, fixture.purchase.Revision, "independent-correction-proofs", ErrDomainState
			switch failure {
			case "missing Purchase":
				owner, want = "missing", ErrNotFound
			case "foreign Purchase":
				parent := createPurchaseCommand(t, purchases, fixture.token, "another-real-correction-parent", "JPY")
				owner, revision, want = parent.ID, parent.Revision, ErrNotFound
			case "missing Line":
				input.LineID, want = "missing", ErrNotFound
			case "stale Purchase":
				revision, want = revision+1, ErrRevisionConflict
			case "stale Line":
				input.LineRevision, want = input.LineRevision+1, ErrRevisionConflict
			case "stale Item":
				input.ItemRevision, want = input.ItemRevision+1, ErrRevisionConflict
			case "missing Purchase proof":
				revision, want = 0, ErrPreconditionRequired
			case "unsafe Purchase proof":
				revision, want = maxSafeInteger+1, ErrDomainInput
			case "short key":
				key, want = "short", ErrDomainInput
			case "long key":
				key, want = strings.Repeat("x", 129), ErrDomainInput
			default:
				proof := projectionKey{"purchase", fixture.purchase.ID}
				if failure == "Line counter" {
					proof = projectionKey{"purchase_line", fixture.unitLine.ID}
				} else if failure == "Item counter" {
					proof = projectionKey{"item", fixture.individual.ID}
				} else if failure == "Unit counter" {
					proof = projectionKey{"unit", fixture.unit.ID}
				}
				execSQL(t, fixture.store.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", proof.kind, proof.id)
				execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", proof.kind, proof.id)
			}
			before := purchaseCommandState(t, fixture.store)
			if value, err := purchases.CorrectLine(t.Context(), fixture.token, key, owner, revision, input); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing/foreign/stale original authority was invented or rebased", failure, err)
			}
			requirePurchaseCommandState(t, fixture.store, before)
		})
	}
}

func TestPurchaseLineCorrectionCounterExhaustionNeverReseedsAuthorityOrPartiallyPublishes(t *testing.T) {
	for _, scope := range []string{"Purchase", "original Line", "original Item", "replacement Item", "Asset", "item search", "Unit search", "catalog", "sequence"} {
		t.Run(scope, func(t *testing.T) {
			fixture, purchases, _, _ := purchaseAssetFixture(t)
			s := fixture.store
			input := reviewedLineVoid(t, fixture, fixture.unitLine)
			input.Action = "replace"
			input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1500, PaidMinor: 0}
			switch scope {
			case "Purchase":
				setCatalogProjectionRevision(t, s, "purchase", fixture.purchase.ID, maxSafeInteger)
				fixture.purchase.Revision = maxSafeInteger
			case "original Line":
				setCatalogProjectionRevision(t, s, "purchase_line", fixture.unitLine.ID, maxSafeInteger)
				input.LineRevision = maxSafeInteger
			case "original Item":
				setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, maxSafeInteger)
				input.ItemRevision = maxSafeInteger
			case "replacement Item":
				setCatalogProjectionRevision(t, s, "item", fixture.quantity.ID, maxSafeInteger)
				input.Replacement.ItemRevision = maxSafeInteger
			case "Asset":
				setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, maxSafeInteger)
			case "item search":
				setCatalogProjectionRevision(t, s, "search_doc", projectionPair("item", fixture.individual.ID), maxSafeInteger)
			case "Unit search":
				key := projectionPair("unit", fixture.unit.ID)
				execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('search_doc',?,?)", key, maxSafeInteger)
			case "catalog":
				execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			case "sequence":
				execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			}
			before := assetProfileState(t, s)
			if value, err := purchases.CorrectLine(t.Context(), fixture.token, "exhausted-correction-authority", fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrRevisionOverflow) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("exhausted original/dependent authority exposed partial correction", scope, err)
			}
			requireAssetProfileState(t, s, before)
		})
	}
	fixture, purchases, _, _ := purchaseAssetFixture(t)
	setCatalogProjectionRevision(t, fixture.store, "unit", fixture.unit.ID, maxSafeInteger)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: input.ItemRevision, ItemUnitID: &fixture.unit.ID, QuantityBase: 1, PaidMinor: 0}
	if _, err := purchases.CorrectLine(t.Context(), fixture.token, "unchanged-physical-proof-not-required", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal("unchanged Unit counter became another financial write precondition", err)
	}
	if scalar[int64](t, fixture.store.db, "SELECT revision FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID) != maxSafeInteger {
		t.Fatal("financial correction advanced unchanged physical authority")
	}
}

func TestPurchaseLineCorrectionSnapshotsWholeReplacementAndNullableUnitBeforeClockAndFingerprint(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	input.Action = "replace"
	unitID := fixture.unit.ID
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: input.ItemRevision, ItemUnitID: &unitID, QuantityBase: 1, PaidMinor: 13, Note: "Original exact replacement"}
	clock := fixture.auth.now
	fixture.auth.now = func() time.Time {
		unitID = "missing"
		input.Replacement.PaidMinor = 999
		input.Replacement.Note = "Changed after fingerprint"
		return clock()
	}
	value, err := purchases.CorrectLine(t.Context(), fixture.token, "snapshot-whole-line-replacement", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil || value.After == nil || value.After.ItemUnitID == nil || *value.After.ItemUnitID != fixture.unit.ID || value.After.PaidMinor != 13 || value.After.Note != "Original exact replacement" {
		t.Fatal("caller mutation changed the reviewed replacement/nullable Unit", err)
	}
	if strings.Contains(scalar[string](t, fixture.store.db, "SELECT context_json FROM purchase_line_corrections"), "Changed after fingerprint") {
		t.Fatal("private exact context retained mutated caller memory")
	}
}

func TestPurchaseLineCorrectionConcurrencyUsesOneOriginalResultOrConflictsWithoutDuplicatingHistory(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "competing reviewed keys", true: "identical original key"}[sameKey], func(t *testing.T) {
			fixture, purchases := purchaseCommands(t)
			input := reviewedLineVoid(t, fixture, fixture.stockLine)
			type result struct {
				value PurchaseLineCorrection
				err   error
			}
			results := make(chan result, 2)
			var group sync.WaitGroup
			for index := range 2 {
				group.Add(1)
				go func(index int) {
					defer group.Done()
					key := "concurrent-line-correction-a"
					if index == 1 && !sameKey {
						key = "concurrent-line-correction-b"
					}
					value, err := purchases.CorrectLine(t.Context(), fixture.token, key, fixture.purchase.ID, fixture.purchase.Revision, input)
					results <- result{value, err}
				}(index)
			}
			group.Wait()
			close(results)
			var successful []PurchaseLineCorrection
			conflicts := 0
			for reply := range results {
				if reply.err == nil {
					successful = append(successful, reply.value)
				} else if errors.Is(reply.err, ErrRevisionConflict) && reflect.ValueOf(reply.value).IsZero() {
					conflicts++
				} else {
					t.Fatal("unexpected competing correction result", reply.err)
				}
			}
			if sameKey {
				if len(successful) != 2 || conflicts != 0 || !reflect.DeepEqual(successful[0], successful[1]) {
					t.Fatal("same key did not reconcile one exact original decision")
				}
			} else if len(successful) != 1 || conflicts != 1 {
				t.Fatal("competing original proofs both committed")
			}
			if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM purchase_line_corrections") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_purchases_id_line_corrections'") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM purchase_lines") != 2 {
				t.Fatal("concurrent void invented a replacement or duplicated history")
			}
		})
	}
}
