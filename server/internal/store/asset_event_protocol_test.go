package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAssetEventsRejectInvalidUsageDecisionsWithoutClaimingAKey(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	for _, change := range []struct {
		name string
		edit func(*UsageWrite)
	}{
		{"zero", func(v *UsageWrite) { v.Delta = 0 }},
		{"negative use", func(v *UsageWrite) { v.Delta = -1 }},
		{"unsafe positive", func(v *UsageWrite) { v.Delta = maxSafeInteger + 1 }},
		{"unsafe negative", func(v *UsageWrite) { v.Delta = -maxSafeInteger - 1 }},
		{"unknown unit", func(v *UsageWrite) { v.UsageUnit = "hours" }},
		{"unknown event type", func(v *UsageWrite) { v.EventType = "reset" }},
		{"missing correction", func(v *UsageWrite) { v.EventType = "correction" }},
		{"unexpected reference", func(v *UsageWrite) { v.CorrectionOf = testPointer("existing") }},
		{"empty reference", func(v *UsageWrite) { v.EventType, v.CorrectionOf = "correction", testPointer("") }},
		{"long reference", func(v *UsageWrite) {
			v.EventType, v.CorrectionOf = "correction", testPointer(strings.Repeat("界", 129))
		}},
		{"invalid reference Unicode", func(v *UsageWrite) { v.EventType, v.CorrectionOf = "correction", testPointer(string([]byte{0xff})) }},
		{"missing instant", func(v *UsageWrite) { v.OccurredAt = "" }},
		{"nonlexical instant", func(v *UsageWrite) { v.OccurredAt = "2026-11-07t04:00:00z" }},
		{"invalid calendar", func(v *UsageWrite) { v.OccurredAt = "2025-02-29T04:00:00Z" }},
		{"overprecision", func(v *UsageWrite) { v.OccurredAt = "2026-11-07T04:00:00.1234567891Z" }},
		{"UTC underflow", func(v *UsageWrite) { v.OccurredAt = "0001-01-01T00:00:00+01:00" }},
		{"UTC overflow", func(v *UsageWrite) { v.OccurredAt = "9999-12-31T23:59:59-01:00" }},
		{"long native-NUL note", func(v *UsageWrite) { v.Note = "\x00" + strings.Repeat("界", 10000) }},
		{"invalid note Unicode", func(v *UsageWrite) { v.Note = string([]byte{0xff}) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			input := usageInput(1)
			change.edit(&input)
			before := assetProfileState(t, s)
			if value, err := service.RecordUsage(t.Context(), fixture.token, "event-invalid-usage", fixture.unit.ID, asset.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Usage{}) {
				t.Fatal("invalid usage became a financial event", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetEventsRejectInvalidCostDecisionsWithoutClaimingAKey(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	for _, change := range []struct {
		name string
		edit func(*CostWrite)
	}{
		{"zero", func(v *CostWrite) { v.AmountMinor = 0 }},
		{"negative magnitude", func(v *CostWrite) { v.AmountMinor = -1 }},
		{"unsafe magnitude", func(v *CostWrite) { v.AmountMinor = maxSafeInteger + 1 }},
		{"lowercase currency", func(v *CostWrite) { v.Currency = "jpy" }},
		{"noncurrency", func(v *CostWrite) { v.Currency = "123" }},
		{"empty currency", func(v *CostWrite) { v.Currency = "" }},
		{"unknown kind", func(v *CostWrite) { v.Kind = "depreciation" }},
		{"unknown direction", func(v *CostWrite) { v.Direction = "negative" }},
		{"missing adjustment reference", func(v *CostWrite) { v.Kind = "adjustment" }},
		{"unexpected reference", func(v *CostWrite) { v.CorrectionOf = testPointer("existing") }},
		{"empty reference", func(v *CostWrite) { v.Kind, v.CorrectionOf = "adjustment", testPointer("") }},
		{"long reference", func(v *CostWrite) { v.Kind, v.CorrectionOf = "adjustment", testPointer(strings.Repeat("界", 129)) }},
		{"invalid reference Unicode", func(v *CostWrite) { v.Kind, v.CorrectionOf = "adjustment", testPointer(string([]byte{0xff})) }},
		{"missing instant", func(v *CostWrite) { v.OccurredAt = "" }},
		{"invalid calendar", func(v *CostWrite) { v.OccurredAt = "2025-02-29T04:00:00Z" }},
		{"overprecision", func(v *CostWrite) { v.OccurredAt = "2026-11-07T04:00:00.1234567891Z" }},
		{"UTC overflow", func(v *CostWrite) { v.OccurredAt = "9999-12-31T23:59:59-01:00" }},
		{"long native-NUL note", func(v *CostWrite) { v.Note = "\x00" + strings.Repeat("界", 10000) }},
		{"invalid note Unicode", func(v *CostWrite) { v.Note = string([]byte{0xff}) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			input := costInput(1)
			change.edit(&input)
			before := assetProfileState(t, s)
			if value, err := service.RecordCost(t.Context(), fixture.token, "event-invalid-cost", fixture.unit.ID, asset.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Cost{}) {
				t.Fatal("invalid cost became new money", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetEventsEnforceOriginalSafeProofKeyAndIdentityBounds(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	for _, value := range []struct {
		id, key string
		proof   int64
		want    error
	}{
		{"", "event-proof-valid", asset.Revision, ErrDomainInput},
		{strings.Repeat("界", 129), "event-proof-valid", asset.Revision, ErrDomainInput},
		{string([]byte{0xff}), "event-proof-valid", asset.Revision, ErrDomainInput},
		{fixture.unit.ID, "short", asset.Revision, ErrDomainInput},
		{fixture.unit.ID, strings.Repeat("x", 129), asset.Revision, ErrDomainInput},
		{fixture.unit.ID, "event-proof-valid", 0, ErrPreconditionRequired},
		{fixture.unit.ID, "event-proof-valid", -1, ErrDomainInput},
		{fixture.unit.ID, "event-proof-valid", maxSafeInteger + 1, ErrDomainInput},
		{"new", "event-proof-valid", asset.Revision, ErrNotFound},
		{fixture.unit.ID, "event-proof-valid", asset.Revision + 1, ErrRevisionConflict},
	} {
		before := assetProfileState(t, s)
		if reply, err := service.RecordUsage(t.Context(), fixture.token, value.key, value.id, value.proof, usageInput(1)); !errors.Is(err, value.want) || reply != (Usage{}) {
			t.Fatal("usage accepted an invalid/wrong scope", value.proof, reply, err)
		}
		requireAssetEventState(t, s, before)
		if reply, err := service.RecordCost(t.Context(), fixture.token, value.key, value.id, value.proof, costInput(1)); !errors.Is(err, value.want) || reply != (Cost{}) {
			t.Fatal("cost accepted an invalid/wrong scope", value.proof, reply, err)
		}
		requireAssetEventState(t, s, before)
	}
}

func TestAssetEventsFiniteNewCostPolicyDoesNotRepriceRetainedUsage(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	for _, code := range []string{"ZZZ", "USD"} {
		input := costInput(1)
		input.Currency = code
		before := assetProfileState(t, s)
		want := ErrDomainInput
		if code == "ZZZ" {
			want = ErrAssetCurrency
		}
		if value, err := service.RecordCost(t.Context(), fixture.token, "event-new-money-policy", fixture.unit.ID, asset.Revision, input); !errors.Is(err, want) || value != (Cost{}) {
			t.Fatal("cost guessed an exponent or converted a different currency", value, err)
		}
		requireAssetEventState(t, s, before)
	}
	execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
	if value, err := service.RecordUsage(t.Context(), fixture.token, "event-retained-money-usage", fixture.unit.ID, asset.Revision, usageInput(1)); err != nil || value.Delta != 1 {
		t.Fatal("nonmonetary usage applied new-money policy to retained currency", value, err)
	}
	if scalar[string](t, s.db, "SELECT currency FROM asset_profiles WHERE item_unit_id=?", fixture.unit.ID) != "ZZZ" {
		t.Fatal("usage repaired/repriced a retained financial interpretation")
	}
}

func TestAssetEventsSnapshotCorrectionPointersBeforeFingerprintAndSourceValidation(t *testing.T) {
	for _, kind := range []string{"usage", "cost"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			s := fixture.store
			service := assetReader(t, fixture)
			target := "validation"
			if kind == "usage" {
				insertAssetUsage(t, s, target, fixture.unit.ID, usageInput(1).OccurredAt, 2, nil)
			} else {
				insertAssetCost(t, s, target, fixture.unit.ID, 2, "expense")
			}
			base := fixture.auth.now()
			fixture.auth.now = func() time.Time { target = "changed-after-fingerprint"; return base }
			if kind == "usage" {
				input := usageInput(-1)
				input.EventType, input.CorrectionOf = "correction", &target
				value, err := service.RecordUsage(t.Context(), fixture.token, "event-owned-snapshot", fixture.unit.ID, asset.Revision, input)
				if err != nil || value.CorrectionOf == nil || *value.CorrectionOf != "validation" || target != "changed-after-fingerprint" {
					t.Fatal("usage borrowed a caller pointer or rejected a valid imported validation ID", value, err)
				}
				input.CorrectionOf = testPointer("validation")
				if replay, err := service.RecordUsage(t.Context(), fixture.token, "event-owned-snapshot", fixture.unit.ID, asset.Revision, input); err != nil || !reflect.DeepEqual(value, replay) {
					t.Fatal("usage source and frozen fingerprint used different reference snapshots", err)
				}
			} else {
				input := costInput(1)
				input.Kind, input.Direction, input.CorrectionOf = "adjustment", "credit", &target
				value, err := service.RecordCost(t.Context(), fixture.token, "event-owned-snapshot", fixture.unit.ID, asset.Revision, input)
				if err != nil || value.CorrectionOf == nil || *value.CorrectionOf != "validation" || target != "changed-after-fingerprint" {
					t.Fatal("cost borrowed a caller pointer or rejected a valid imported validation ID", value, err)
				}
				input.CorrectionOf = testPointer("validation")
				if replay, err := service.RecordCost(t.Context(), fixture.token, "event-owned-snapshot", fixture.unit.ID, asset.Revision, input); err != nil || !reflect.DeepEqual(value, replay) {
					t.Fatal("cost source and frozen fingerprint used different reference snapshots", err)
				}
			}
		})
	}
}
