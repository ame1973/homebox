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

func TestDisposalCorrectionRejectsInvalidDecisionsAndIndependentOriginalProofsWithoutWrites(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	before := disposalCorrectionState(t, fixture.store)
	for _, field := range []string{"action", "replace null", "void nonnull", "reason empty", "reason large", "reason UTF8", "note large", "note UTF8", "kind", "non-sale proceeds", "currency", "instant", "overflow year", "fraction width", "negative proceeds", "unsafe proceeds", "missing Asset", "unsafe Asset"} {
		input := disposalCorrectionInput(asset.Revision, 0)
		switch field {
		case "action":
			input.Action = "delete"
		case "replace null":
			input.Replacement = nil
		case "void nonnull":
			input.Action = "void"
		case "reason empty":
			input.Reason = ""
		case "reason large":
			input.Reason = strings.Repeat("😀", 10001)
		case "reason UTF8":
			input.Reason = string([]byte{0xff})
		case "note large":
			input.Replacement.Note = strings.Repeat("界", 10001)
		case "note UTF8":
			input.Replacement.Note = string([]byte{0xff})
		case "kind":
			input.Replacement.Kind = "returned"
		case "non-sale proceeds":
			input.Replacement.Kind, input.Replacement.RealizedProceedsMinor = "donated", 1
		case "currency":
			input.Replacement.Currency = "jpy"
		case "instant":
			input.Replacement.DisposedAt = "2026-02-30T00:00:00Z"
		case "overflow year":
			input.Replacement.DisposedAt = "9999-12-31T23:59:59-00:01"
		case "fraction width":
			input.Replacement.DisposedAt = "2026-11-07T00:00:00.1234567890Z"
		case "negative proceeds":
			input.Replacement.RealizedProceedsMinor = -1
		case "unsafe proceeds":
			input.Replacement.RealizedProceedsMinor = maxSafeInteger + 1
		case "missing Asset":
			input.AssetRevision = 0
		case "unsafe Asset":
			input.AssetRevision = maxSafeInteger + 1
		}
		if value, err := service.CorrectDisposal(t.Context(), fixture.token, "invalid-financial-decision", fixture.unit.ID, record.Revision, input); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid full financial decision was repaired or returned partial success", field, err)
		}
		requireDisposalCorrectionState(t, fixture.store, before)
	}
	input := disposalCorrectionInput(asset.Revision, 0)
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := service.CorrectDisposal(t.Context(), fixture.token, "invalid-disposal-owner", id, record.Revision, input); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid subject identity entered the transaction", err)
		}
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		want := ErrDomainInput
		if revision == 0 {
			want = ErrPreconditionRequired
		}
		if value, err := service.CorrectDisposal(t.Context(), fixture.token, "invalid-disposal-proof", fixture.unit.ID, revision, input); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("missing/unsafe Disposal proof was accepted", err)
		}
	}
	for _, key := range []string{"short", strings.Repeat("x", 129)} {
		if value, err := service.CorrectDisposal(t.Context(), fixture.token, key, fixture.unit.ID, record.Revision, input); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid original key was accepted", err)
		}
	}
	for _, scope := range []string{"missing subject", "stale subject", "stale Asset"} {
		id, revision, proposal, want := fixture.unit.ID, record.Revision, input, ErrRevisionConflict
		switch scope {
		case "missing subject":
			id, want = "missing", ErrNotFound
		case "stale subject":
			revision++
		case "stale Asset":
			proposal.AssetRevision++
		}
		if value, err := service.CorrectDisposal(t.Context(), fixture.token, "independent-original-proofs", id, revision, proposal); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("original independent proof was guessed or rebased", scope, err)
		}
	}
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestDisposalCorrectionReplayRetainsWholeOriginalDecisionAfterLaterInterpretationMissingSourceAndFreshLogin(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 0)
	original, err := service.CorrectDisposal(t.Context(), fixture.token, "original-financial-correction", fixture.unit.ID, record.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	void := DisposalCorrectionWrite{AssetRevision: asset.Revision + 1, Action: "void", Reason: "Later explicit void"}
	if _, err := service.CorrectDisposal(t.Context(), fixture.token, "later-financial-void", fixture.unit.ID, original.After.Revision, void); err != nil {
		t.Fatal(err)
	}
	current, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	profile := retainedProfileInput(item.Revision, current)
	profile.Currency = "USD"
	if _, err := service.PutProfile(t.Context(), fixture.token, "later-reviewed-financial-interpretation", fixture.unit.ID, fixture.unit.Revision, profile); err != nil {
		t.Fatal(err)
	}
	execSQL(t, fixture.store.db, "DELETE FROM projection_revisions WHERE entity_type='disposal' AND entity_id=?", fixture.unit.ID)
	_, fresh := login(t, fixture.auth)
	before := disposalCorrectionState(t, fixture.store)
	if frozen, err := service.CorrectDisposal(t.Context(), fresh, "original-financial-correction", fixture.unit.ID, record.Revision, input); err != nil || !reflect.DeepEqual(frozen, original) {
		t.Fatal("original reconciliation required current source/proofs/eligibility or installed a newer result", err)
	}
	for _, change := range []func(*DisposalCorrectionWrite){
		func(v *DisposalCorrectionWrite) { v.AssetRevision++ },
		func(v *DisposalCorrectionWrite) { v.Reason += " " },
		func(v *DisposalCorrectionWrite) { v.Replacement.Note += " " },
		func(v *DisposalCorrectionWrite) { v.Replacement.RealizedProceedsMinor++ },
		func(v *DisposalCorrectionWrite) { v.Replacement.DisposedAt = "2026-11-07T04:00:00.123456788Z" },
		func(v *DisposalCorrectionWrite) { v.Replacement.Currency = "USD" },
		func(v *DisposalCorrectionWrite) { v.Action, v.Replacement = "void", nil },
	} {
		proposal, replacement := input, *input.Replacement
		proposal.Replacement = &replacement
		change(&proposal)
		if value, err := service.CorrectDisposal(t.Context(), fresh, "original-financial-correction", fixture.unit.ID, record.Revision, proposal); !errors.Is(err, ErrIdempotencyConflict) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("changed exact body/proof reused an original key", err)
		}
	}
	if value, err := service.CorrectDisposal(t.Context(), fresh, "original-financial-correction", fixture.unit.ID, record.Revision+1, input); !errors.Is(err, ErrIdempotencyConflict) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("changed subject proof reused an original key", err)
	}
	if value, err := service.RecordCost(t.Context(), fresh, "original-financial-correction", fixture.unit.ID, asset.Revision, CostWrite{AmountMinor: 1, Currency: "JPY", Kind: "repair", Direction: "expense", OccurredAt: "2026-11-07T00:00:00Z"}); !errors.Is(err, ErrIdempotencyConflict) || value != (Cost{}) {
		t.Fatal("different financial operation reused a committed key", err)
	}
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestDisposalCorrectionExpiredClaimRemainsReservedAfterFreshLogin(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 0)
	if _, err := service.CorrectDisposal(t.Context(), fixture.token, "expiring-disposal-correction", fixture.unit.ID, record.Revision, input); err != nil {
		t.Fatal(err)
	}
	now := fixture.auth.now().Add(domainReplyLifetime + time.Second)
	fixture.auth.now = func() time.Time { return now }
	_, fresh := login(t, fixture.auth)
	before := disposalCorrectionState(t, fixture.store)
	if value, err := service.CorrectDisposal(t.Context(), fresh, "expiring-disposal-correction", fixture.unit.ID, record.Revision, input); !errors.Is(err, ErrIdempotencyExpired) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("expired claim was deleted/reapplied", err)
	}
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestDisposalCorrectionCompletedAuthorizationPrecedesSourceHistoryAndFrozenReply(t *testing.T) {
	for _, failure := range []string{"revoked", "mandatory", "epoch", "invalid", "expired", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			input := disposalCorrectionInput(asset.Revision, 0)
			if _, err := service.CorrectDisposal(t.Context(), fixture.token, "protected-disposal-correction", fixture.unit.ID, record.Revision, input); err != nil {
				t.Fatal(err)
			}
			ctx, token, want := t.Context(), fixture.token, ErrAuthentication
			switch failure {
			case "revoked":
				execSQL(t, fixture.store.db, "UPDATE sessions SET revoked_at=?", stamp(fixture.auth.now()))
			case "mandatory":
				execSQL(t, fixture.store.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "epoch":
				execSQL(t, fixture.store.db, "UPDATE sync_state SET dataset_epoch='different-financial-epoch'")
			case "invalid":
				token = "invalid"
			case "expired":
				execSQL(t, fixture.store.db, "UPDATE sessions SET expires_at=?", stamp(fixture.auth.now()))
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			before := disposalCorrectionState(t, fixture.store)
			for _, key := range []string{"protected-disposal-correction", "fresh-protected-disposal-correction"} {
				if value, err := service.CorrectDisposal(ctx, token, key, fixture.unit.ID, record.Revision, input); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("uncompleted session exposed original financial response/source", err)
				}
			}
			for _, read := range []func() (any, error){
				func() (any, error) { return service.GetDisposalRecord(ctx, token, fixture.unit.ID) },
				func() (any, error) { return service.PageDisposalCorrections(ctx, token, fixture.unit.ID, "", 50) },
			} {
				if value, err := read(); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("uncompleted session exposed retained subject/history", err)
				}
			}
			requireDisposalCorrectionState(t, fixture.store, before)
		})
	}
}

func TestDisposalCorrectionSnapshotsWholeReplacementBeforeClockAndFingerprint(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 13)
	original := *input.Replacement
	clock := fixture.auth.now
	fixture.auth.now = func() time.Time {
		input.Replacement.Note = "Mutated caller memory"
		input.Replacement.RealizedProceedsMinor = 999
		input.Replacement.DisposedAt = "2000-01-01T00:00:00Z"
		return clock()
	}
	entry, err := service.CorrectDisposal(t.Context(), fixture.token, "snapshot-disposal-correction", fixture.unit.ID, record.Revision, input)
	if err != nil || entry.After.Current == nil || entry.After.Current.RealizedProceedsMinor != original.RealizedProceedsMinor || entry.After.Current.Note != original.Note || entry.After.Current.DisposedAt != record.Original.DisposedAt {
		t.Fatal("caller mutation changed the exact reviewed proposal after fingerprint", err)
	}
	if strings.Contains(scalar[string](t, fixture.store.db, "SELECT context_json FROM asset_disposal_corrections"), "Mutated caller memory") {
		t.Fatal("private exact context retained mutated caller input")
	}
}

func TestDisposalCorrectionConcurrencyCommitsOneDecisionOrReconcilesOneOriginalKey(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "competing original keys", true: "same original key"}[sameKey], func(t *testing.T) {
			fixture, service, asset, record := disposalCommands(t)
			input := disposalCorrectionInput(asset.Revision, 0)
			type result struct {
				value DisposalCorrection
				err   error
			}
			results := make(chan result, 2)
			var group sync.WaitGroup
			for index := range 2 {
				group.Add(1)
				go func(index int) {
					defer group.Done()
					key := "concurrent-disposal-a"
					if index == 1 && !sameKey {
						key = "concurrent-disposal-b"
					}
					value, err := service.CorrectDisposal(t.Context(), fixture.token, key, fixture.unit.ID, record.Revision, input)
					results <- result{value, err}
				}(index)
			}
			group.Wait()
			close(results)
			var successful []DisposalCorrection
			conflicts := 0
			for reply := range results {
				if reply.err == nil {
					successful = append(successful, reply.value)
				} else if errors.Is(reply.err, ErrRevisionConflict) && reflect.ValueOf(reply.value).IsZero() {
					conflicts++
				} else {
					t.Fatal("unexpected concurrent financial correction result", reply.err)
				}
			}
			if sameKey && (len(successful) != 2 || conflicts != 0 || !reflect.DeepEqual(successful[0], successful[1])) || !sameKey && (len(successful) != 1 || conflicts != 1) {
				t.Fatal("concurrent original proofs duplicated or failed to reconcile one decision")
			}
			if scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM asset_disposal_corrections") != 1 || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_units_id_disposal_corrections'") != 1 {
				t.Fatal("concurrent delivery duplicated immutable financial history")
			}
		})
	}
}
