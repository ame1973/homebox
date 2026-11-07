package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func recordAssetLifecycle(ctx context.Context, service *Assets, token, key, id string, revision, unitRevision int64, kind string) (domainResult, error) {
	if kind == "retire" {
		return service.Retire(ctx, token, key, id, revision, retirementInput(unitRevision))
	}
	return service.Dispose(ctx, token, key, id, revision, disposalInput(unitRevision, 0))
}

func TestAssetLifecycleRejectsInvalidRetirementInputsWithoutAClaim(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	for _, edit := range []struct {
		name string
		set  func(*AssetRetireWrite)
	}{
		{"missing Unit proof", func(v *AssetRetireWrite) { v.UnitRevision = 0 }},
		{"negative Unit proof", func(v *AssetRetireWrite) { v.UnitRevision = -1 }},
		{"unsafe Unit proof", func(v *AssetRetireWrite) { v.UnitRevision = maxSafeInteger + 1 }},
		{"missing date", func(v *AssetRetireWrite) { v.RetiredAt = "" }},
		{"invalid calendar", func(v *AssetRetireWrite) { v.RetiredAt = "2025-02-29T00:00:00Z" }},
		{"nonlexical date", func(v *AssetRetireWrite) { v.RetiredAt = "2026-11-07t00:00:00z" }},
		{"overprecision", func(v *AssetRetireWrite) { v.RetiredAt = "2026-11-07T00:00:00.1234567891Z" }},
		{"UTC underflow", func(v *AssetRetireWrite) { v.RetiredAt = "0001-01-01T00:00:00+01:00" }},
		{"UTC overflow", func(v *AssetRetireWrite) { v.RetiredAt = "9999-12-31T23:59:59-01:00" }},
		{"native-NUL note bounds", func(v *AssetRetireWrite) { v.Note = "\x00" + strings.Repeat("界", 10000) }},
		{"invalid note Unicode", func(v *AssetRetireWrite) { v.Note = string([]byte{0xff}) }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			input := retirementInput(fixture.unit.Revision)
			edit.set(&input)
			before := assetProfileState(t, s)
			if value, err := service.Retire(t.Context(), fixture.token, "lifecycle-invalid-retirement", fixture.unit.ID, asset.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Unit{}) {
				t.Fatal("invalid financial/physical retirement input became source", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetLifecycleRejectsInvalidDisposalInputsIncludingExplicitNonSaleZero(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	for _, edit := range []struct {
		name string
		set  func(*AssetDisposalWrite)
	}{
		{"missing Unit proof", func(v *AssetDisposalWrite) { v.UnitRevision = 0 }},
		{"negative Unit proof", func(v *AssetDisposalWrite) { v.UnitRevision = -1 }},
		{"unsafe Unit proof", func(v *AssetDisposalWrite) { v.UnitRevision = maxSafeInteger + 1 }},
		{"unknown kind", func(v *AssetDisposalWrite) { v.Kind = "delete" }},
		{"negative proceeds", func(v *AssetDisposalWrite) { v.RealizedProceedsMinor = -1 }},
		{"unsafe proceeds", func(v *AssetDisposalWrite) { v.RealizedProceedsMinor = maxSafeInteger + 1 }},
		{"donated proceeds", func(v *AssetDisposalWrite) { v.Kind, v.RealizedProceedsMinor = "donated", 1 }},
		{"recycled proceeds", func(v *AssetDisposalWrite) { v.Kind, v.RealizedProceedsMinor = "recycled", 1 }},
		{"discarded proceeds", func(v *AssetDisposalWrite) { v.Kind, v.RealizedProceedsMinor = "discarded", 1 }},
		{"other proceeds", func(v *AssetDisposalWrite) { v.Kind, v.RealizedProceedsMinor = "other", 1 }},
		{"lowercase currency", func(v *AssetDisposalWrite) { v.Currency = "jpy" }},
		{"noncurrency", func(v *AssetDisposalWrite) { v.Currency = "123" }},
		{"missing date", func(v *AssetDisposalWrite) { v.DisposedAt = "" }},
		{"invalid calendar", func(v *AssetDisposalWrite) { v.DisposedAt = "2025-02-29T00:00:00Z" }},
		{"overprecision", func(v *AssetDisposalWrite) { v.DisposedAt = "2026-11-07T00:00:00.1234567891Z" }},
		{"UTC overflow", func(v *AssetDisposalWrite) { v.DisposedAt = "9999-12-31T23:59:59-01:00" }},
		{"native-NUL note bounds", func(v *AssetDisposalWrite) { v.Note = "\x00" + strings.Repeat("界", 10000) }},
		{"invalid note Unicode", func(v *AssetDisposalWrite) { v.Note = string([]byte{0xff}) }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			input := disposalInput(fixture.unit.Revision, 0)
			edit.set(&input)
			before := assetProfileState(t, s)
			if value, err := service.Dispose(t.Context(), fixture.token, "lifecycle-invalid-disposal", fixture.unit.ID, asset.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Disposal{}) {
				t.Fatal("invalid terminal/money decision became source", value, err)
			}
			requireAssetEventState(t, s, before)
		})
	}
}

func TestAssetLifecycleOriginalProofsKeysAndIdentitiesAreIndependentAndSafe(t *testing.T) {
	fixture, _ := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 7)
	setCatalogProjectionRevision(t, s, "unit", fixture.unit.ID, 3)
	setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, 5)
	for _, kind := range []string{"retire", "dispose"} {
		for _, input := range []struct {
			id, key     string
			asset, unit int64
			want        error
		}{
			{"", "lifecycle-valid-key", 7, 3, ErrDomainInput},
			{strings.Repeat("界", 129), "lifecycle-valid-key", 7, 3, ErrDomainInput},
			{string([]byte{0xff}), "lifecycle-valid-key", 7, 3, ErrDomainInput},
			{fixture.unit.ID, "short", 7, 3, ErrDomainInput},
			{fixture.unit.ID, strings.Repeat("x", 129), 7, 3, ErrDomainInput},
			{fixture.unit.ID, "lifecycle-valid-key", 0, 3, ErrPreconditionRequired},
			{fixture.unit.ID, "lifecycle-valid-key", -1, 3, ErrDomainInput},
			{fixture.unit.ID, "lifecycle-valid-key", maxSafeInteger + 1, 3, ErrDomainInput},
			{"new", "lifecycle-valid-key", 7, 3, ErrNotFound},
			{fixture.unit.ID, "lifecycle-valid-key", 6, 3, ErrRevisionConflict},
			{fixture.unit.ID, "lifecycle-valid-key", 3, 7, ErrRevisionConflict},
			{fixture.unit.ID, "lifecycle-valid-key", 5, 3, ErrRevisionConflict},
			{fixture.unit.ID, "lifecycle-valid-key", 7, 2, ErrRevisionConflict},
			{fixture.unit.ID, "lifecycle-valid-key", 7, 5, ErrRevisionConflict},
		} {
			before := assetProfileState(t, s)
			if value, err := recordAssetLifecycle(t.Context(), service, fixture.token, input.key, input.id, input.asset, input.unit, kind); !errors.Is(err, input.want) || !zeroAssetEvent(value) {
				t.Fatal("lifecycle accepted a missing, unsafe or unrelated original proof", kind, input.asset, input.unit, value, err)
			}
			requireAssetEventState(t, s, before)
		}
	}
}

func TestAssetLifecycleARealMoveInvalidatesOnlyTheSeparateOriginalUnitProof(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	inventory, err := s.NewInventory(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inventory.MoveUnit(t.Context(), fixture.token, "lifecycle-before-review-move", fixture.unit.ID, fixture.unit.Revision, "target"); err != nil {
		t.Fatal(err)
	}
	service := assetReader(t, fixture)
	current, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || current.Revision != asset.Revision {
		t.Fatal("physical move invented a financial revision", current, err)
	}
	for _, kind := range []string{"retire", "dispose"} {
		before := assetProfileState(t, s)
		if value, err := recordAssetLifecycle(t.Context(), service, fixture.token, "lifecycle-stale-placement", fixture.unit.ID, asset.Revision, fixture.unit.Revision, kind); !errors.Is(err, ErrRevisionConflict) || !zeroAssetEvent(value) {
			t.Fatal("financial proof alone silently covered a moved Unit", kind, value, err)
		}
		requireAssetEventState(t, s, before)
	}
}

func TestAssetLifecycleRetirementUsesActualInstantOrderWithoutGuessingIndependentDisposalDates(t *testing.T) {
	fixture, asset := manualAssetFixture(t, 0)
	s := fixture.store
	service := assetReader(t, fixture)
	execSQL(t, s.db, "UPDATE asset_profiles SET in_service_at='2026-11-07T12:00:00+08:00' WHERE item_unit_id=?", fixture.unit.ID)
	input := retirementInput(fixture.unit.Revision)
	input.RetiredAt = "2026-11-07T12:00:00+09:00"
	before := assetProfileState(t, s)
	if value, err := service.Retire(t.Context(), fixture.token, "lifecycle-reversed-instants", fixture.unit.ID, asset.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Unit{}) {
		t.Fatal("lexically ordered but reversed actual service interval was accepted", value, err)
	}
	requireAssetEventState(t, s, before)
	input.RetiredAt = "2026-11-07T05:00:00Z"
	retired, err := service.Retire(t.Context(), fixture.token, "lifecycle-valid-offset-order", fixture.unit.ID, asset.Revision, input)
	if err != nil || retired.Status != "retired" {
		t.Fatal("valid actual offset order was rejected as lexical order", retired, err)
	}
	disposal := disposalInput(retired.Revision, 0)
	disposal.DisposedAt = "2026-11-06T00:00:00Z"
	if _, err = service.Dispose(t.Context(), fixture.token, "lifecycle-independent-dates", fixture.unit.ID, asset.Revision+1, disposal); err != nil {
		t.Fatal("disposal invented a new cross-service-date constraint", err)
	}
	current, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || current.InServiceAt == nil || *current.InServiceAt != "2026-11-07T12:00:00+08:00" || current.RetiredAt == nil || *current.RetiredAt != "2026-11-07T05:00:00.000000000Z" {
		t.Fatal("disposal inferred, cleared or repriced independent actual service dates", current, err)
	}
}

func TestAssetLifecycleAllExplicitDisposalKindsAndFiniteMatchingNewMoney(t *testing.T) {
	for _, kind := range []string{"sold", "donated", "recycled", "discarded", "other"} {
		t.Run(kind, func(t *testing.T) {
			fixture, asset := manualAssetFixture(t, 0)
			service := assetReader(t, fixture)
			input := disposalInput(fixture.unit.Revision, 0)
			input.Kind = kind
			for _, code := range []string{"ZZZ", "USD"} {
				input.Currency = code
				before := assetProfileState(t, fixture.store)
				want := ErrDomainInput
				if code == "ZZZ" {
					execSQL(t, fixture.store.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
					before = assetProfileState(t, fixture.store)
					want = ErrAssetCurrency
				}
				if value, err := service.Dispose(t.Context(), fixture.token, "lifecycle-new-money-currency", fixture.unit.ID, asset.Revision, input); !errors.Is(err, want) || value != (Disposal{}) {
					t.Fatal("new disposal repriced FX or accepted unsupported zero money", value, err)
				}
				requireAssetEventState(t, fixture.store, before)
				execSQL(t, fixture.store.db, "UPDATE asset_profiles SET currency='JPY' WHERE item_unit_id=?", fixture.unit.ID)
			}
			input.Currency = "JPY"
			value, err := service.Dispose(t.Context(), fixture.token, "lifecycle-explicit-kind", fixture.unit.ID, asset.Revision, input)
			if err != nil || value.Kind != kind || value.RealizedProceedsMinor != 0 || value.Revision != 1 {
				t.Fatal("explicit zero disposal kind was inferred, converted or rejected", value, err)
			}
		})
	}
}
