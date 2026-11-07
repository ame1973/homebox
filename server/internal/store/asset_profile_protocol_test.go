package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAssetProfileRejectsInvalidFullDecisionsAndOriginalProofsWithoutWrites(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	service := assetReader(t, fixture)
	base := manualProfileInput(fixture.individual.Revision, 0)
	before := assetProfileState(t, s)
	for _, field := range []string{"item proof", "asset proof", "basis", "currency", "manual", "residual", "usage", "service", "retired", "chronology"} {
		input := base
		switch field {
		case "item proof":
			input.ItemRevision = 0
		case "asset proof":
			input.AssetRevision = testPointer(int64(0))
		case "basis":
			input.CostBasis = "both"
		case "currency":
			input.Currency = "jpy"
		case "manual":
			input.ManualAcquisitionMinor = nil
		case "residual":
			input.EstimatedResidualMinor = testPointer(maxSafeInteger + 1)
		case "usage":
			input.UsageUnit = "hours"
		case "service":
			input.InServiceAt = testPointer("2026-11-07T00:00:00.1234567890Z")
		case "retired":
			input.RetiredAt = testPointer("0001-01-01T00:00:00+00:01")
		case "chronology":
			input.InServiceAt = testPointer("2026-11-07T04:00:00Z")
			input.RetiredAt = testPointer("2026-11-07T12:00:00+09:00")
		}
		if value, err := service.PutProfile(t.Context(), fixture.token, "profile-invalid-input", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Asset{}) {
			t.Fatal("invalid complete profile was repaired or accepted", field, value, err)
		}
		requireAssetProfileState(t, s, before)
	}
	for _, number := range []int64{-1, maxSafeInteger + 1} {
		for _, field := range []string{"item proof", "asset proof", "manual", "residual"} {
			input := base
			switch field {
			case "item proof":
				input.ItemRevision = number
			case "asset proof":
				input.AssetRevision = &number
			case "manual":
				input.ManualAcquisitionMinor = &number
			case "residual":
				input.EstimatedResidualMinor = &number
			}
			if value, err := service.PutProfile(t.Context(), fixture.token, "profile-unsafe-input", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrDomainInput) || value != (Asset{}) {
				t.Fatal("unsafe body integer was accepted", field, value, err)
			}
			requireAssetProfileState(t, s, before)
		}
	}
	for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
		want := ErrDomainInput
		if revision == 0 {
			want = ErrPreconditionRequired
		}
		if value, err := service.PutProfile(t.Context(), fixture.token, "profile-invalid-unit-proof", fixture.unit.ID, revision, base); !errors.Is(err, want) || value != (Asset{}) {
			t.Fatal("missing/unsafe original Unit proof was accepted", value, err)
		}
		requireAssetProfileState(t, s, before)
	}
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := service.PutProfile(t.Context(), fixture.token, "profile-invalid-unit-id", id, fixture.unit.Revision, base); !errors.Is(err, ErrDomainInput) || value != (Asset{}) {
			t.Fatal("invalid owner identity was accepted", value, err)
		}
	}
	for _, key := range []string{"short", strings.Repeat("a", 129)} {
		if value, err := service.PutProfile(t.Context(), fixture.token, key, fixture.unit.ID, fixture.unit.Revision, base); !errors.Is(err, ErrDomainInput) || value != (Asset{}) {
			t.Fatal("invalid original key was accepted", value, err)
		}
	}
	requireAssetProfileState(t, s, before)
}

func TestAssetProfileBindsActualUnitItemAndNullableAssetPresenceIndependently(t *testing.T) {
	fixture := holdingsFixture(t)
	s := fixture.store
	service := assetReader(t, fixture)
	base := manualProfileInput(fixture.individual.Revision, 0)
	for _, proof := range []string{"absent expected Asset", "stale Unit", "stale Item", "missing Unit"} {
		input, id, revision, want := base, fixture.unit.ID, fixture.unit.Revision, ErrRevisionConflict
		switch proof {
		case "absent expected Asset":
			input.AssetRevision = testPointer(int64(1))
		case "stale Unit":
			revision++
		case "stale Item":
			input.ItemRevision++
		case "missing Unit":
			id, want = "missing", ErrNotFound
		}
		before := assetProfileState(t, s)
		if value, err := service.PutProfile(t.Context(), fixture.token, "profile-original-scopes", id, revision, input); !errors.Is(err, want) || value != (Asset{}) {
			t.Fatal("profile guessed presence or rebased an original proof", proof, value, err)
		}
		requireAssetProfileState(t, s, before)
	}
	created, err := service.PutProfile(t.Context(), fixture.token, "profile-create-scope", fixture.unit.ID, fixture.unit.Revision, base)
	if err != nil {
		t.Fatal(err)
	}
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range []string{"reviewed absence", "stale Asset", "stale Item"} {
		input := retainedProfileInput(item.Revision, created)
		switch proof {
		case "reviewed absence":
			input.AssetRevision = nil
		case "stale Asset":
			input.AssetRevision = testPointer(created.Revision + 1)
		case "stale Item":
			input.ItemRevision = base.ItemRevision
		}
		before := assetProfileState(t, s)
		if value, err := service.PutProfile(t.Context(), fixture.token, "profile-present-scopes", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrRevisionConflict) || value != (Asset{}) {
			t.Fatal("coincident Unit/Asset counters bypassed explicit presence or original proofs", proof, value, err)
		}
		requireAssetProfileState(t, s, before)
	}
}

func TestAssetProfileNewMoneyUsesFinitePolicyButRetainedNoopAndDatesDoNotReprice(t *testing.T) {
	for _, code := range []string{"ZZZ", "XXX", "XAU"} {
		fixture := holdingsFixture(t)
		service := assetReader(t, fixture)
		input := manualProfileInput(fixture.individual.Revision, 0)
		input.Currency = code
		before := assetProfileState(t, fixture.store)
		if value, err := service.PutProfile(t.Context(), fixture.token, "profile-unsupported-money", fixture.unit.ID, fixture.unit.Revision, input); !errors.Is(err, ErrAssetCurrency) || value != (Asset{}) {
			t.Fatal("new zero-money profile accepted unknown/nonfinite currency", code, value, err)
		}
		requireAssetProfileState(t, fixture.store, before)
	}
	fixture, asset := manualAssetFixture(t, 100)
	s := fixture.store
	service := assetReader(t, fixture)
	execSQL(t, s.db, "UPDATE asset_profiles SET currency='ZZZ' WHERE item_unit_id=?", fixture.unit.ID)
	asset.Currency = "ZZZ"
	input := retainedProfileInput(fixture.individual.Revision, asset)
	if value, err := service.PutProfile(t.Context(), fixture.token, "profile-retained-noop", fixture.unit.ID, fixture.unit.Revision, input); err != nil || !reflect.DeepEqual(value, asset) {
		t.Fatal("no-op applied new-money policy to unchanged retained source", value, err)
	}
	input.InServiceAt = testPointer("2024-11-07T12:00:00.123456789+08:00")
	updated, err := service.PutProfile(t.Context(), fixture.token, "profile-retained-date", fixture.unit.ID, fixture.unit.Revision, input)
	if err != nil || updated.Currency != "ZZZ" || updated.ManualAcquisitionMinor == nil || *updated.ManualAcquisitionMinor != 100 {
		t.Fatal("date correction repriced/converted retained money or guessed its registry exponent", updated, err)
	}
	item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"manual amount", "residual", "basis"} {
		proposal := retainedProfileInput(item.Revision, updated)
		switch field {
		case "manual amount":
			proposal.ManualAcquisitionMinor = testPointer(int64(101))
		case "residual":
			proposal.EstimatedResidualMinor = testPointer(int64(0))
		case "basis":
			proposal.CostBasis, proposal.ManualAcquisitionMinor = "purchase", nil
		}
		before := assetProfileState(t, s)
		if value, err := service.PutProfile(t.Context(), fixture.token, "profile-new-retained-money", fixture.unit.ID, fixture.unit.Revision, proposal); !errors.Is(err, ErrAssetCurrency) || value != (Asset{}) {
			t.Fatal("changed financial interpretation bypassed current currency policy", field, value, err)
		}
		requireAssetProfileState(t, s, before)
	}
}

func TestAssetProfileCannotRetireOrRevivePhysicalStateByReplacement(t *testing.T) {
	for _, status := range []string{"stored", "retired", "disposed"} {
		t.Run(status, func(t *testing.T) {
			fixture := holdingsFixture(t)
			s := fixture.store
			execSQL(t, s.db, "UPDATE item_units SET status=? WHERE id=?", status, fixture.unit.ID)
			input := manualProfileInput(fixture.individual.Revision, 0)
			input.InServiceAt = testPointer("2024-11-07T00:00:00Z")
			input.RetiredAt = testPointer("2026-11-07T12:00:00+08:00")
			before := assetProfileState(t, s)
			service := assetReader(t, fixture)
			value, err := service.PutProfile(t.Context(), fixture.token, "profile-historical-retirement", fixture.unit.ID, fixture.unit.Revision, input)
			if status == "stored" {
				if !errors.Is(err, ErrUnitAssetLifecycle) || value != (Asset{}) {
					t.Fatal("profile opt-in silently retired an ordinary active Unit", value, err)
				}
				requireAssetProfileState(t, s, before)
				return
			}
			if err != nil || value.RetiredAt == nil || *value.RetiredAt != "2026-11-07T04:00:00.000000000Z" || scalar[string](t, s.db, "SELECT status FROM item_units WHERE id=?", fixture.unit.ID) != status || scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_disposals") != 0 {
				t.Fatal("explicit historical opt-in changed terminal status or invented disposal/proceeds", value, err)
			}
			item, err := fixture.catalog.Get(t.Context(), fixture.token, fixture.individual.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, retired := range []*string{nil, testPointer("2026-11-08T00:00:00Z")} {
				proposal := retainedProfileInput(item.Revision, value)
				proposal.RetiredAt = retired
				before := assetProfileState(t, s)
				if reply, err := service.PutProfile(t.Context(), fixture.token, "profile-forbidden-retirement-change", fixture.unit.ID, fixture.unit.Revision, proposal); !errors.Is(err, ErrUnitAssetLifecycle) || reply != (Asset{}) {
					t.Fatal("replacement changed/cleared retirement instead of explicit lifecycle review", reply, err)
				}
				requireAssetProfileState(t, s, before)
			}
		})
	}
}
