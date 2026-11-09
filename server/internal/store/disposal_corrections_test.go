package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func disposalCommands(t *testing.T) (holdingFixture, *Assets, Asset, DisposalRecord) {
	t.Helper()
	fixture, asset := manualAssetFixture(t, 100)
	service := assetReader(t, fixture)
	if _, err := service.Dispose(t.Context(), fixture.token, "actual-original-disposal", fixture.unit.ID, asset.Revision, disposalInput(fixture.unit.Revision, 200)); err != nil {
		t.Fatal(err)
	}
	var err error
	fixture.unit, err = loadUnit(t.Context(), fixture.store.db, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.individual = getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	asset, err = service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, service, asset, record
}

func disposalCorrectionInput(assetRevision, amount int64) DisposalCorrectionWrite {
	return DisposalCorrectionWrite{AssetRevision: assetRevision, Action: "replace", Reason: "Exact reviewed financial reason\n\x00😀",
		Replacement: &DisposalFinancialWrite{Kind: "sold", DisposedAt: "2026-11-07T12:00:00.123456789+08:00", RealizedProceedsMinor: amount, Currency: "JPY", Note: "Exact financial note\n\x00😀"}}
}

func disposalCorrectionState(t *testing.T, s *Store) map[string]string {
	t.Helper()
	state := assetProfileState(t, s)
	state["asset_disposal_corrections"] = scalar[string](t, s.db, `SELECT json_group_array(json_array(mutation_event_id,revision,item_unit_id,previous_correction_id,subject_revision,action,reason,created_at,context_json)) FROM (SELECT * FROM asset_disposal_corrections ORDER BY mutation_event_id)`)
	return state
}

func requireDisposalCorrectionState(t *testing.T, s *Store, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, disposalCorrectionState(t, s)) {
		t.Fatal("failed disposal correction changed source/context/counters/current/outbox/audit/reply/session authority")
	}
}

func TestDisposalCorrectionRetainsExactOriginalAndOnlyPublishesFinancialDecisionsThroughVoidAndReinstatement(t *testing.T) {
	fixture, service, asset, original := disposalCommands(t)
	s := fixture.store
	if original.Current == nil || *original.Current != original.Original || original.Revision != 1 {
		t.Fatal("original record guessed a void or reconstructed different source", original)
	}
	setCatalogProjectionRevision(t, s, "disposal", fixture.unit.ID, 7)
	setCatalogProjectionRevision(t, s, "asset", fixture.unit.ID, 13)
	setCatalogProjectionRevision(t, s, "item", fixture.individual.ID, 19)
	execSQL(t, s.db, "UPDATE asset_profiles SET estimated_residual_minor=50 WHERE item_unit_id=?", fixture.unit.ID)
	for index, decision := range []string{"replace", "equal replacement", "void", "reinstate zero", "zero is active", "reinstate original"} {
		before := disposalCorrectionState(t, s)
		record, err := service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID)
		if err != nil {
			t.Fatal(err)
		}
		input := disposalCorrectionInput(13+int64(index), 300)
		switch decision {
		case "void", "zero is active":
			input.Action, input.Replacement = "void", nil
		case "reinstate zero":
			input.Replacement.Kind, input.Replacement.RealizedProceedsMinor = "donated", 0
		case "reinstate original":
			input.Replacement.RealizedProceedsMinor = original.Original.RealizedProceedsMinor
			input.Replacement.Note = original.Original.Note
		}
		var entry DisposalCorrection
		changed := requireProjectionReplay(t, s, func() {
			entry, err = service.CorrectDisposal(t.Context(), fixture.token, "financial-decision-"+decision, fixture.unit.ID, record.Revision, input)
			if err != nil {
				t.Fatal(decision, err)
			}
		})
		if entry.Revision != 1 || entry.ID == fixture.unit.ID || entry.ItemUnitID != fixture.unit.ID || entry.CreatedAt != stamp(fixture.auth.now()) || entry.Action != input.Action || entry.Reason != input.Reason || !reflect.DeepEqual(entry.Before, record) || entry.After.Original != original.Original || entry.After.Revision != record.Revision+1 {
			t.Fatal("correction lost exact original/whole records/proofs/reason/actual time or independent entry revision", entry)
		}
		if len(changed) != 3 || !changed[projectionKey{"disposal", fixture.unit.ID}] || !changed[projectionKey{"asset", fixture.unit.ID}] || !changed[projectionKey{"item", fixture.individual.ID}] {
			t.Fatal("financial correction omitted an aggregate or changed physical/search/history scopes", changed)
		}
		current, err := service.GetDisposal(t.Context(), fixture.token, fixture.unit.ID)
		if input.Action == "void" {
			if !errors.Is(err, ErrNotFound) || current != (Disposal{}) || entry.After.Current != nil || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='disposal' AND entity_id=?", fixture.unit.ID) != 0 {
				t.Fatal("financial void became zero proceeds or resurrected original current source", err)
			}
		} else if err != nil || current.Revision != record.Revision+1 || entry.After.Current == nil || current.DisposalData != *entry.After.Current || current.Note != input.Replacement.Note || current.RealizedProceedsMinor != input.Replacement.RealizedProceedsMinor || current.DisposedAt != original.Original.DisposedAt {
			t.Fatal("replacement/reinstatement rounded dates, lost explicit zero or used the raw original as current", current, err)
		}
		retained, err := service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID)
		if err != nil || !reflect.DeepEqual(retained, entry.After) {
			t.Fatal("retained actual subject proof was lost after its decision", err)
		}
		summary, err := service.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2020-01-01T00:00:00Z")
		proceeds := int64(0)
		if entry.After.Current != nil {
			proceeds = entry.After.Current.RealizedProceedsMinor
		}
		if err != nil || summary.AcquisitionMinor != 100 || summary.RealizedProceedsMinor != proceeds || summary.NetCostMinor != 100-proceeds || summary.ProjectedNetCostMinor == nil || *summary.ProjectedNetCostMinor != 50 || summary.ElapsedDays != nil || summary.PerUseMinor != nil {
			t.Fatal("current posted finance was clamped, reconstructed as_of or mixed with residual/service/usage", summary, err)
		}
		after := disposalCorrectionState(t, s)
		for _, table := range []string{"asset_disposals", "item_units", "stock_balances", "inventory_events", "locations", "search_documents", "unit_search_documents", "purchases", "purchase_lines", "purchase_header_corrections", "purchase_line_corrections", "asset_usage_events", "asset_cost_events", "asset_profile_changes", "media", "media_links", "ai_jobs"} {
			if before[table] != after[table] {
				t.Fatal("financial correction rewrote original/physical/unchanged source", table)
			}
		}
		if scalar[int](t, s.db, "SELECT COUNT(*) FROM asset_disposal_corrections c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE c.mutation_event_id=? AND e.operation='post_units_id_disposal_corrections' AND e.reason=?", entry.ID, input.Reason) != 1 {
			t.Fatal("correction did not retain the actual immutable audit association")
		}
	}
	asset, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || asset.Revision != 19 || asset.RetiredAt != nil || asset.InServiceAt != nil || fixture.unit.Status != "disposed" {
		t.Fatal("financial decisions guessed service/retirement/physical changes", err)
	}
	before := disposalCorrectionState(t, s)
	for _, statement := range []string{"UPDATE asset_disposal_corrections SET reason=reason", "DELETE FROM asset_disposal_corrections", "UPDATE asset_disposals SET note=note", "DELETE FROM asset_disposals"} {
		if _, err := s.db.ExecContext(t.Context(), statement); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatal("retained original or financial history became mutable", err)
		}
		requireDisposalCorrectionState(t, s, before)
	}
}

func TestDisposalCorrectionFinancialVoidNeverBypassesOriginalPhysicalOrLifecycleReferences(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 0)
	input.Action, input.Replacement = "void", nil
	entry, err := service.CorrectDisposal(t.Context(), fixture.token, "explicit-financial-void", fixture.unit.ID, record.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	asset, err = service.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := disposalCorrectionState(t, fixture.store)
	if value, err := service.CorrectDisposal(t.Context(), fixture.token, "fresh-double-void", fixture.unit.ID, entry.After.Revision, DisposalCorrectionWrite{AssetRevision: asset.Revision, Action: "void", Reason: "Not another zero"}); !errors.Is(err, ErrAssetHistory) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("fresh void of absent current finance invented another decision", err)
	}
	if value, err := service.Dispose(t.Context(), fixture.token, "ordinary-creation-after-void", fixture.unit.ID, asset.Revision, disposalInput(fixture.unit.Revision, 0)); !errors.Is(err, ErrAssetHistory) || value != (Disposal{}) {
		t.Fatal("ordinary disposal bypassed retained history or reseeded a revision-one identity", err)
	}
	if value, err := service.Retire(t.Context(), fixture.token, "retirement-after-financial-void", fixture.unit.ID, asset.Revision, retirementInput(fixture.unit.Revision)); !errors.Is(err, ErrUnitDisposed) || value != (Unit{}) {
		t.Fatal("financial void permitted physical retirement/revival", err)
	}
	requireDisposalCorrectionState(t, fixture.store, before)
	// Even a corrupt retained physical status is not permission to remove again.
	execSQL(t, fixture.store.db, "UPDATE item_units SET status='stored' WHERE id=?", fixture.unit.ID)
	before = disposalCorrectionState(t, fixture.store)
	if _, err := service.Retire(t.Context(), fixture.token, "retained-history-still-terminal", fixture.unit.ID, asset.Revision, retirementInput(fixture.unit.Revision)); !errors.Is(err, ErrUnitDisposed) {
		t.Fatal("raw historical disposal references vanished with current accounting", err)
	}
	requireDisposalCorrectionState(t, fixture.store, before)
}

func TestDisposalCorrectionReadsAndReinstatementKeepBroadOriginalDateSpelling(t *testing.T) {
	for _, instant := range [][2]string{
		{"0001-01-01T00:00:00+00:00", "0001-01-01T00:00:00Z"},
		{"9999-12-31T23:59:59.999999999Z", "9999-12-31T23:59:59.999999999Z"},
		{"2026-11-07T12:00:00.123456789+08:00", "2026-11-07T04:00:00.123456789Z"},
		{"2026-11-07T04:00:00.123456789-00:00", "2026-11-07T04:00:00.123456789+00:00"},
	} {
		t.Run(instant[0], func(t *testing.T) {
			fixture := assetsFixture(t, true)
			service := assetReader(t, fixture.holdingFixture)
			execSQL(t, fixture.store.db, "DROP TRIGGER asset_disposals_no_update")
			execSQL(t, fixture.store.db, "UPDATE asset_disposals SET disposed_at=? WHERE item_unit_id=?", instant[0], fixture.unit.ID)
			record, err := service.GetDisposalRecord(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil || record.Current == nil || record.Original.DisposedAt != instant[0] || *record.Current != record.Original {
				t.Fatal("retained source normalized or guessed original date/revision", err)
			}
			asset, err := service.Get(t.Context(), fixture.token, fixture.unit.ID)
			if err != nil {
				t.Fatal(err)
			}
			void, err := service.CorrectDisposal(t.Context(), fixture.token, "legacy-date-financial-void", fixture.unit.ID, record.Revision, DisposalCorrectionWrite{AssetRevision: asset.Revision, Action: "void", Reason: "Explicit historical void"})
			if err != nil {
				t.Fatal(err)
			}
			input := disposalCorrectionInput(asset.Revision+1, 0)
			input.Replacement.DisposedAt = instant[1]
			entry, err := service.CorrectDisposal(t.Context(), fixture.token, "legacy-date-explicit-reinstatement", fixture.unit.ID, void.After.Revision, input)
			if err != nil || entry.After.Current == nil || entry.After.Current.DisposedAt != instant[0] || entry.After.Original != record.Original {
				t.Fatal("reinstatement discarded equivalent retained original spelling or nanoseconds", err)
			}
		})
	}
}

func TestDisposalCorrectionDomainResultsRejectInvalidOwnershipProofProgressionAndDecisions(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	entry, err := service.CorrectDisposal(t.Context(), fixture.token, "valid-domain-result-context", fixture.unit.ID, record.Revision, disposalCorrectionInput(asset.Revision, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*DisposalCorrection){
		func(v *DisposalCorrection) { v.ID = "" }, func(v *DisposalCorrection) { v.Revision = 2 },
		func(v *DisposalCorrection) { v.ItemUnitID = "another" }, func(v *DisposalCorrection) { v.Reason = "" },
		func(v *DisposalCorrection) { v.CreatedAt = "2026" }, func(v *DisposalCorrection) { v.Before.ItemUnitID = "another" },
		func(v *DisposalCorrection) { v.After.ItemUnitID = "another" }, func(v *DisposalCorrection) { v.After.Original.Note += " " },
		func(v *DisposalCorrection) { v.After.Revision = v.Before.Revision }, func(v *DisposalCorrection) { v.Before.Revision = maxSafeInteger },
		func(v *DisposalCorrection) { v.After.Current = nil }, func(v *DisposalCorrection) { v.Action = "delete" },
		func(v *DisposalCorrection) { v.Action = "void" },
		func(v *DisposalCorrection) { v.Action, v.Before.Current, v.After.Current = "void", nil, nil },
	} {
		value := entry
		change(&value)
		if !errors.Is(value.validateDomainResult(), ErrDomainState) {
			t.Fatal("invalid frozen correction ownership/revision/decision passed resource validation")
		}
	}
	for _, encoded := range []string{"", "null", "[]", "{}", "{}{}", "{", string([]byte{0xff})} {
		if _, err := decodeDisposalCorrectionContext(encoded); !errors.Is(err, ErrDomainState) {
			t.Fatal("invalid/incomplete private context became a retained financial decision")
		}
	}
}

func TestDisposalCorrectionPreservesEquivalentRetainedDatesAndCanonicalizesChangedNanoseconds(t *testing.T) {
	fixture, service, asset, record := disposalCommands(t)
	input := disposalCorrectionInput(asset.Revision, 0)
	input.Replacement.DisposedAt = "2026-11-07T04:00:00.123456788Z"
	entry, err := service.CorrectDisposal(t.Context(), fixture.token, "changed-submillisecond-date", fixture.unit.ID, record.Revision, input)
	if err != nil || entry.After.Current == nil || entry.After.Current.DisposedAt != "2026-11-07T04:00:00.123456788Z" {
		t.Fatal("changed date was rounded to its prior millisecond", err)
	}
	input.AssetRevision++
	input.Replacement.DisposedAt = "2026-11-07T12:00:00.123456788+08:00"
	second, err := service.CorrectDisposal(t.Context(), fixture.token, "equivalent-offset-date", fixture.unit.ID, entry.After.Revision, input)
	if err != nil || second.After.Current == nil || second.After.Current.DisposedAt != entry.After.Current.DisposedAt {
		t.Fatal("equivalent retained date spelling changed", err)
	}
	var context disposalCorrectionContext
	if encoded := scalar[string](t, fixture.store.db, "SELECT context_json FROM asset_disposal_corrections WHERE mutation_event_id=?", second.ID); json.Unmarshal([]byte(encoded), &context) != nil || context.Input.Replacement.DisposedAt != input.Replacement.DisposedAt || context.Asset.Revision != input.AssetRevision || context.Before.Revision != entry.After.Revision {
		t.Fatal("exact supplied instant/proofs were replaced by canonical reply data")
	}
}
