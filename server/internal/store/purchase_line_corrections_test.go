package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func reviewedLineVoid(t *testing.T, fixture purchaseFixture, line PurchaseLine) PurchaseLineCorrectionCreate {
	t.Helper()
	item := getCatalogItem(t, fixture.catalog, fixture.token, line.ItemID)
	return PurchaseLineCorrectionCreate{LineID: line.ID, LineRevision: line.Revision, ItemRevision: item.Revision,
		Action: "void", Reason: "Exact reviewed correction é\n\x00"}
}

func TestPurchaseLineCorrectionVoidRetainsOriginalSourceAndPublishesOnlyActualCurrentEffects(t *testing.T) {
	for _, kind := range []string{"Unit", "quantity"} {
		t.Run(kind, func(t *testing.T) {
			fixture, purchases := purchaseCommands(t)
			s, original := fixture.store, fixture.unitLine
			wantChanges := 5
			if kind == "quantity" {
				original, wantChanges = fixture.stockLine, 4
			}
			input := reviewedLineVoid(t, fixture, original)
			before := purchaseCommandState(t, s)
			now := fixture.auth.now().Add(time.Minute)
			fixture.auth.now = func() time.Time { return now }
			var reply PurchaseLineCorrection
			changed := requireProjectionReplay(t, s, func() {
				var err error
				reply, err = purchases.CorrectLine(t.Context(), fixture.token, "void-real-original", fixture.purchase.ID, fixture.purchase.Revision, input)
				if err != nil {
					t.Fatal(err)
				}
			})
			if reply.Revision != 1 || reply.Action != "void" || reply.After != nil || !reflect.DeepEqual(reply.Before, original) || reply.CreatedAt != stamp(now) || reply.Reason != input.Reason || len(changed) != wantChanges {
				t.Fatal("void changed its original context, date, independent entry proof or publication scopes", reply, len(changed))
			}
			for _, key := range []projectionKey{{"purchase_line", original.ID}, {"purchase", fixture.purchase.ID}, {"item", original.ItemID}, {"search_doc", projectionPair("item", original.ItemID)}} {
				if !changed[key] {
					t.Fatal("void omitted an actual current dependency", key)
				}
			}
			stored, err := loadPurchaseLine(t.Context(), s.db, original.ID)
			if err != nil || !reflect.DeepEqual(stored.PurchaseLineData, original.PurchaseLineData) || stored.Revision != original.Revision+1 || scalar[int](t, s.db, "SELECT COUNT(*) FROM current_projections WHERE entity_type='purchase_line' AND entity_id=?", original.ID) != 0 {
				t.Fatal("void edited/deleted posted source or retained its current projection", err)
			}
			if scalar[int](t, s.db, `SELECT COUNT(*) FROM sync_changes WHERE entity_type='purchase_line' AND entity_id=? AND operation='delete' AND entity_revision=? AND projection_json IS NULL`, original.ID, original.Revision+1) != 1 {
				t.Fatal("void did not publish its exact independent tombstone")
			}
			encoded, _ := json.Marshal(reply)
			if scalar[int](t, s.db, `SELECT COUNT(*) FROM purchase_line_corrections c JOIN mutation_events e ON e.id=c.mutation_event_id
 WHERE c.mutation_event_id=? AND c.revision=1 AND c.created_at=e.occurred_at AND e.operation='post_purchases_id_line_corrections' AND e.reason=? AND e.result_json=?`, reply.ID, input.Reason, string(encoded)) != 1 {
				t.Fatal("correction lacks its actual immutable audit/original response")
			}
			after := purchaseCommandState(t, s)
			for _, table := range []string{"purchases", "purchase_lines", "locations", "item_units", "stock_balances", "inventory_events", "asset_profiles", "asset_usage_events", "asset_cost_events", "asset_disposals", "media", "media_links", "ai_jobs", "purchase_header_corrections"} {
				if after[table] != before[table] {
					t.Fatal("financial void changed original/physical/unrelated source", table)
				}
			}
			page, err := purchases.PageLines(t.Context(), fixture.token, fixture.purchase.ID, "", 100)
			if err != nil || len(page.Data) != 1 || page.Data[0].ID == original.ID || page.NextCursor != nil {
				t.Fatal("current collection resurrected a voided raw attribution", page, err)
			}
			history, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 50)
			if err != nil || len(history.Data) != 1 || !reflect.DeepEqual(history.Data[0], reply) || history.NextCursor != nil {
				t.Fatal("actual immutable correction history was lost", err)
			}
			for _, statement := range []string{"UPDATE purchase_line_corrections SET reason=reason", "DELETE FROM purchase_line_corrections", "UPDATE purchase_lines SET paid_minor=0 WHERE id='" + original.ID + "'", "DELETE FROM purchase_lines WHERE id='" + original.ID + "'"} {
				if _, err = s.db.ExecContext(t.Context(), statement); err == nil {
					t.Fatal("posted source or correction history remained destructively mutable")
				}
			}
		})
	}
}

func TestPurchaseLineCorrectionReplacesWrongAttributionWithNewIndependentLineAndWholeAmount(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	s := fixture.store
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 2500, PaidMinor: 13, Note: "Exact new evidence é\n\x00"}
	before := purchaseCommandState(t, s)
	var reply PurchaseLineCorrection
	changed := requireProjectionReplay(t, s, func() {
		var err error
		reply, err = purchases.CorrectLine(t.Context(), fixture.token, "replace-wrong-attribution", fixture.purchase.ID, fixture.purchase.Revision, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	if reply.After == nil || reply.After.ID == fixture.unitLine.ID || reply.After.Revision != 1 || reply.After.ItemID != fixture.quantity.ID || reply.After.ItemUnitID != nil || reply.After.QuantityBase != 2500 || reply.After.PaidMinor != 13 || reply.After.Note != input.Replacement.Note || len(changed) != 8 {
		t.Fatal("replacement reowned the old Line, multiplied money or omitted source scopes", reply, len(changed))
	}
	for _, key := range []projectionKey{{"purchase_line", fixture.unitLine.ID}, {"purchase_line", reply.After.ID}, {"purchase", fixture.purchase.ID},
		{"item", fixture.individual.ID}, {"item", fixture.quantity.ID}, {"search_doc", projectionPair("item", fixture.individual.ID)},
		{"search_doc", projectionPair("item", fixture.quantity.ID)}, {"search_doc", projectionPair("unit", fixture.unit.ID)}} {
		if !changed[key] {
			t.Fatal("replacement missed an actual old/new source dependency", key)
		}
	}
	var context purchaseLineCorrectionContext
	if err := json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT context_json FROM purchase_line_corrections WHERE mutation_event_id=?", reply.ID)), &context); err != nil ||
		!reflect.DeepEqual(context.Input, input) || !reflect.DeepEqual(context.Before, fixture.unitLine) || !reflect.DeepEqual(context.After, reply.After) ||
		context.Item.ID != fixture.individual.ID || context.Item.Revision != input.ItemRevision || context.ReplacementItem == nil || context.ReplacementItem.Revision != input.Replacement.ItemRevision {
		t.Fatal("complete exact original/new financial interpretation or intent was lost", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_lines WHERE id=? AND created_at=? AND mutation_event_id=?", reply.After.ID, reply.CreatedAt, reply.ID) != 1 {
		t.Fatal("replacement is not a real independently posted allocation")
	}
	after := purchaseCommandState(t, s)
	for _, table := range []string{"purchases", "locations", "item_units", "stock_balances", "inventory_events", "asset_profiles", "asset_usage_events", "asset_cost_events", "asset_disposals", "media", "media_links", "ai_jobs"} {
		if after[table] != before[table] {
			t.Fatal("replacement performed physical receipt or changed unrelated source", table)
		}
	}
	old, err := purchases.PageForItem(t.Context(), fixture.token, fixture.individual.ID, "", 100)
	if err != nil || len(old.Data) != 0 {
		t.Fatal("voided attribution still qualified the original Item association", old, err)
	}
	current, err := purchases.PageForItem(t.Context(), fixture.token, fixture.quantity.ID, "", 100)
	if err != nil || len(current.Data) != 1 || current.Data[0].Revision != fixture.purchase.Revision+1 {
		t.Fatal("new active attribution did not qualify its actual Item", current, err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_documents WHERE unit_id=? AND details LIKE '%OriginalUnitLine%'", fixture.unit.ID) != 0 {
		t.Fatal("approved Unit text resurrected voided purchase evidence")
	}
}

func TestPurchaseLineCorrectionSameItemCoalescesCountersAndSupportsLaterIndependentVoid(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.stockLine)
	input.Action = "replace"
	input.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: input.ItemRevision, QuantityBase: 1500, PaidMinor: 0}
	first, err := purchases.CorrectLine(t.Context(), fixture.token, "same-item-financial-replacement", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil || first.After == nil {
		t.Fatal(err)
	}
	if current := getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID); current.Revision != input.ItemRevision+1 {
		t.Fatal("same old/new Item advanced more than once")
	}
	secondInput := reviewedLineVoid(t, fixture, *first.After)
	second, err := purchases.CorrectLine(t.Context(), fixture.token, "void-new-replacement", fixture.purchase.ID, fixture.purchase.Revision+1, secondInput)
	if err != nil || second.After != nil || second.Before.ID != first.After.ID || second.Before.Revision != 1 {
		t.Fatal("later void reused the predecessor's identity/proof", second, err)
	}
	before := purchaseCommandState(t, fixture.store)
	frozen, err := purchases.CorrectLine(t.Context(), fixture.token, "same-item-financial-replacement", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil || !reflect.DeepEqual(frozen, first) {
		t.Fatal("original-key reconciliation was rebased after replacement itself was voided", err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
	history, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 100)
	if err != nil || len(history.Data) != 2 {
		t.Fatal("later correction hid an original immutable decision", err)
	}
	if _, err = purchases.CorrectLine(t.Context(), fixture.token, "fresh-key-already-voided", fixture.purchase.ID, fixture.purchase.Revision+2, input); !errors.Is(err, ErrPurchaseLineVoided) {
		t.Fatal("fresh correction silently voided a historical attribution again", err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
}

func TestPurchaseLineCorrectionRejectsInvalidOrAmbiguousBodyBeforeAnyMutation(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	original := reviewedLineVoid(t, fixture, fixture.unitLine)
	before := purchaseCommandState(t, fixture.store)
	for index, change := range []func(*PurchaseLineCorrectionCreate){
		func(v *PurchaseLineCorrectionCreate) { v.LineID = "" },
		func(v *PurchaseLineCorrectionCreate) { v.LineID = string([]byte{0xff}) },
		func(v *PurchaseLineCorrectionCreate) { v.LineID = strings.Repeat("界", 129) },
		func(v *PurchaseLineCorrectionCreate) { v.LineRevision = 0 },
		func(v *PurchaseLineCorrectionCreate) { v.LineRevision = maxSafeInteger + 1 },
		func(v *PurchaseLineCorrectionCreate) { v.ItemRevision = 0 },
		func(v *PurchaseLineCorrectionCreate) { v.ItemRevision = maxSafeInteger + 1 },
		func(v *PurchaseLineCorrectionCreate) { v.Action = "delete" },
		func(v *PurchaseLineCorrectionCreate) { v.Action = "replace" },
		func(v *PurchaseLineCorrectionCreate) { v.Replacement = &PurchaseLineCreate{} },
		func(v *PurchaseLineCorrectionCreate) { v.Reason = "" },
		func(v *PurchaseLineCorrectionCreate) { v.Reason = string([]byte{0xff}) },
		func(v *PurchaseLineCorrectionCreate) { v.Reason = strings.Repeat("😀", 10001) },
		func(v *PurchaseLineCorrectionCreate) {
			v.Action = "replace"
			v.Replacement = &PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: &fixture.unit.ID, QuantityBase: 2}
		},
	} {
		input := original
		change(&input)
		if value, err := purchases.CorrectLine(t.Context(), fixture.token, fmt.Sprintf("invalid-correction-%d", index), fixture.purchase.ID, fixture.purchase.Revision, input); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("invalid correction body reached authority or exposed a result", index, err)
		}
		requirePurchaseCommandState(t, fixture.store, before)
	}
}

func TestPurchaseLineCorrectionVoidRemovesActualAcquisitionWithoutRepricingLegacyCurrency(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	execSQL(t, fixture.store.db, "UPDATE purchases SET currency='ZZZ' WHERE id=?", fixture.purchase.ID)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	replacement := input
	replacement.Action = "replace"
	replacement.Replacement = &PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1, PaidMinor: 0}
	before := purchaseCommandState(t, fixture.store)
	if value, err := purchases.CorrectLine(t.Context(), fixture.token, "legacy-correction-new-money", fixture.purchase.ID, fixture.purchase.Revision, replacement); !errors.Is(err, ErrPurchaseCurrency) || !reflect.ValueOf(value).IsZero() {
		t.Fatal("replacement repriced an unsupported retained currency", err)
	}
	requirePurchaseCommandState(t, fixture.store, before)
	if _, err := purchases.CorrectLine(t.Context(), fixture.token, "legacy-correction-void-only", fixture.purchase.ID, fixture.purchase.Revision, input); err != nil {
		t.Fatal("nonmonetary void imposed today's registry policy", err)
	}
	assets := assetReader(t, fixture.holdingFixture)
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	if _, err := assets.PutProfile(t.Context(), fixture.token, "manual-after-explicit-void", fixture.unit.ID, fixture.unit.Revision,
		AssetProfileWrite{ItemRevision: item.Revision, CostBasis: "manual", ManualAcquisitionMinor: testPointer(int64(0)), Currency: "JPY", UsageUnit: "times"}); err != nil {
		t.Fatal("voided old attribution was incorrectly treated as current acquisition", err)
	}
	summary, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z")
	if err != nil || summary.AcquisitionMinor != 0 || summary.NetCostMinor != 0 {
		t.Fatal("current financial totals counted voided raw source", summary, err)
	}
	if _, err = purchases.Patch(t.Context(), fixture.token, "no-currency-relabel-after-void", fixture.purchase.ID, fixture.purchase.Revision+1, PurchasePatch{Currency: testPointer("USD")}); !errors.Is(err, ErrPurchaseCurrencyLocked) {
		t.Fatal("void erased historical currency/reference guards", err)
	}
}

func TestPurchaseLineCorrectionResultValidationRejectsMalformedFrozenIdentityProofAndDecision(t *testing.T) {
	fixture, purchases := purchaseCommands(t)
	input := reviewedLineVoid(t, fixture, fixture.stockLine)
	original, err := purchases.CorrectLine(t.Context(), fixture.token, "validate-real-correction-result", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*PurchaseLineCorrection){
		func(v *PurchaseLineCorrection) { v.ID = "" },
		func(v *PurchaseLineCorrection) { v.Revision = 2 },
		func(v *PurchaseLineCorrection) { v.PurchaseID = "" },
		func(v *PurchaseLineCorrection) { v.Reason = "" },
		func(v *PurchaseLineCorrection) { v.CreatedAt = "2026-11-07" },
		func(v *PurchaseLineCorrection) { v.Before.QuantityBase = 0 },
		func(v *PurchaseLineCorrection) { v.Before.PurchaseID = "another-owner" },
		func(v *PurchaseLineCorrection) { v.Before.Revision = maxSafeInteger },
		func(v *PurchaseLineCorrection) { v.Action = "delete" },
		func(v *PurchaseLineCorrection) { v.Action = "replace" },
		func(v *PurchaseLineCorrection) {
			after := v.Before
			after.ID = "new-source"
			after.Revision = 1
			v.After = &after
		},
		func(v *PurchaseLineCorrection) { after := v.Before; v.Action, v.After = "replace", &after },
		func(v *PurchaseLineCorrection) {
			after := v.Before
			after.ID = "new-source"
			after.Revision = 2
			v.Action, v.After = "replace", &after
		},
		func(v *PurchaseLineCorrection) {
			after := v.Before
			after.ID = "new-source"
			after.Revision = 1
			after.PurchaseID = "another-owner"
			v.Action, v.After = "replace", &after
		},
	} {
		value := original
		change(&value)
		if err := value.validateDomainResult(); !errors.Is(err, ErrDomainState) {
			t.Fatal("malformed original response was accepted as a successful immutable correction", err)
		}
	}
}

func TestPurchaseLineCorrectionPreservesCompleteUnicodeReasonIncludingLeadingNativeNUL(t *testing.T) {
	for _, reason := range []string{"\x00", "\x00Exact é\n", "\x00" + strings.Repeat("😀", 9999)} {
		t.Run(fmt.Sprint(len(reason)), func(t *testing.T) {
			fixture, purchases := purchaseCommands(t)
			input := reviewedLineVoid(t, fixture, fixture.stockLine)
			input.Reason = reason
			value, err := purchases.CorrectLine(t.Context(), fixture.token, "exact-native-unicode-reason", fixture.purchase.ID, fixture.purchase.Revision, input)
			if err != nil || value.Reason != reason {
				t.Fatal("a valid exact reason was rejected, truncated or repaired", err)
			}
			page, err := purchases.PageLineCorrections(t.Context(), fixture.token, fixture.purchase.ID, "", 50)
			if err != nil || len(page.Data) != 1 || page.Data[0].Reason != reason {
				t.Fatal("retained reason lost code points or embedded NUL", err)
			}
		})
	}
}

func TestPurchaseLineCorrectionValidatesCompleteProposedAssetHistoryAfterVoid(t *testing.T) {
	fixture, purchases, assets, profile := purchaseAssetFixture(t)
	input := reviewedLineVoid(t, fixture, fixture.unitLine)
	originalUnit := fixture.unit
	reply, err := purchases.CorrectLine(t.Context(), fixture.token, "void-backed-acquisition", fixture.purchase.ID, fixture.purchase.Revision, input)
	if err != nil || reply.After != nil {
		t.Fatal(err)
	}
	current, err := assets.Get(t.Context(), fixture.token, fixture.unit.ID)
	if err != nil || current.Revision != profile.Revision+1 || !reflect.DeepEqual(current.AssetData, profile.AssetData) {
		t.Fatal("void changed Asset interpretation instead of financial authority", current, err)
	}
	summary, err := assets.CostSummary(t.Context(), fixture.token, fixture.unit.ID, "2026-11-07T04:00:00Z")
	if err != nil || summary.AcquisitionMinor != 0 || summary.NetCostMinor != 0 {
		t.Fatal("backed current totals retained the voided allocation", summary, err)
	}
	stored, err := loadUnit(t.Context(), fixture.store.db, fixture.unit.ID)
	if err != nil || !reflect.DeepEqual(stored, originalUnit) || scalar[int](t, fixture.store.db, "SELECT COUNT(*) FROM purchase_lines WHERE id=?", fixture.unitLine.ID) != 1 {
		t.Fatal("financial void removed physical inventory or original history", err)
	}
}
