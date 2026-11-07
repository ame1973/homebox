package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func purchaseCommands(t *testing.T) (purchaseFixture, *Purchases) {
	t.Helper()
	fixture := purchasesFixture(t)
	service, err := NewPurchases(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, service
}
func purchaseCommandState(t *testing.T, s *Store) map[string]string {
	t.Helper()
	state := domainState(t, s)
	state["purchase_header_corrections"] = scalar[string](t, s.db, `SELECT json_group_array(json_array(mutation_event_id,purchase_id,context_json)) FROM (SELECT * FROM purchase_header_corrections ORDER BY mutation_event_id)`)
	state["purchase_line_corrections"] = scalar[string](t, s.db, `SELECT json_group_array(json_array(mutation_event_id,revision,purchase_id,line_id,replacement_line_id,action,reason,created_at,context_json)) FROM (SELECT * FROM purchase_line_corrections ORDER BY mutation_event_id)`)
	return state
}
func requirePurchaseCommandState(t *testing.T, s *Store, before map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, purchaseCommandState(t, s)) {
		t.Fatal("failed purchase command changed authoritative source/audit/projection/session state")
	}
}
func createPurchaseCommand(t *testing.T, p *Purchases, token, key, code string) Purchase {
	t.Helper()
	value, err := p.Create(t.Context(), token, key, PurchaseCreate{MerchantName: "  Exact merchant é  ", PurchasedAt: "2026-11-06T12:00:00.123456789+08:00", Currency: code})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPurchaseServiceConstructorsAndActualHeaderCreationDoNotInventPhysicalOrFinancialTotals(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	for _, a := range []*Auth{nil, {}} {
		if value, err := NewPurchases(a); !errors.Is(err, ErrDomainInput) || value != nil {
			t.Fatal("unbound purchase service accepted", err)
		}
	}
	if value, err := (*Store)(nil).NewPurchases(fixture.auth); !errors.Is(err, ErrDomainInput) || value != nil {
		t.Fatal("nil store accepted", err)
	}
	if value, err := s.NewPurchases(nil); !errors.Is(err, ErrDomainInput) || value != nil {
		t.Fatal("nil authorization accepted", err)
	}
	if value, err := s.NewPurchases(&Auth{store: &Store{}}); !errors.Is(err, ErrDomainInput) || value != nil {
		t.Fatal("foreign store authorization accepted", err)
	}
	before := purchaseCommandState(t, s)
	var created Purchase
	changed := requireProjectionReplay(t, s, func() { created = createPurchaseCommand(t, p, fixture.token, "create-real-purchase", "JPY") })
	if len(changed) != 1 || !changed[projectionKey{"purchase", created.ID}] || created.Revision != 1 || created.MerchantName != "  Exact merchant é  " || created.PurchasedAt != "2026-11-06T04:00:00.123456789Z" || created.ReceiptNote != "" {
		t.Fatal("creation guessed defaults/total, lost precision or reused identity")
	}
	after := purchaseCommandState(t, s)
	for _, table := range []string{"items", "item_units", "stock_balances", "inventory_events", "purchase_lines", "media", "media_links", "ai_jobs", "ai_review_entries", "search_documents", "unit_search_documents"} {
		if before[table] != after[table] {
			t.Fatal("header creation implicitly changed another domain", table)
		}
	}
	if value, err := p.Get(t.Context(), fixture.token, created.ID); err != nil || value != created {
		t.Fatal("actual source GET did not retain independent header", err)
	}
	execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"UntrustedPurchaseCache\":true}' WHERE entity_type='purchase' AND entity_id=?", created.ID)
	if value, err := p.Get(t.Context(), fixture.token, created.ID); err != nil || value != created {
		t.Fatal("GET copied projection cache", err)
	}
	if value, err := p.Get(t.Context(), fixture.token, "missing"); !errors.Is(err, ErrNotFound) || value != (Purchase{}) {
		t.Fatal("missing header was fabricated", err)
	}
	for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
		if value, err := p.Get(t.Context(), fixture.token, id); !errors.Is(err, ErrDomainInput) || value != (Purchase{}) {
			t.Fatal("invalid Get identity accepted", err)
		}
	}
}

func TestPurchaseServicePartialCorrectionPreservesImmutableBeforeAfterAndExactNoopContext(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	original := fixture.purchase
	merchant, note := "\x00  Reviewed merchant é\n  ", ""
	var corrected Purchase
	changed := requireProjectionReplay(t, s, func() {
		var err error
		corrected, err = p.Patch(t.Context(), fixture.token, "correct-header", original.ID, original.Revision, PurchasePatch{MerchantName: &merchant, ReceiptNote: &note})
		if err != nil {
			t.Fatal(err)
		}
	})
	if corrected.Revision != 8 || corrected.MerchantName != merchant || corrected.ReceiptNote != "" || corrected.PurchasedAt != original.PurchasedAt || corrected.Currency != original.Currency || len(changed) != 6 {
		t.Fatal("partial correction changed omitted source or missed dependent publication", len(changed))
	}
	for _, key := range []projectionKey{{"purchase", original.ID}, {"item", fixture.individual.ID}, {"item", fixture.quantity.ID}, {"search_doc", projectionPair("item", fixture.individual.ID)}, {"search_doc", projectionPair("item", fixture.quantity.ID)}, {"search_doc", projectionPair("unit", fixture.unit.ID)}} {
		if !changed[key] {
			t.Fatal("correction omitted a real dependency", key)
		}
	}
	var context struct {
		Before Purchase      `json:"before"`
		After  Purchase      `json:"after"`
		Input  PurchasePatch `json:"input"`
	}
	if err := json.Unmarshal([]byte(scalar[string](t, s.db, "SELECT context_json FROM purchase_header_corrections WHERE purchase_id=?", original.ID)), &context); err != nil || context.Before != original || context.After != corrected || context.Input.MerchantName == nil || *context.Input.MerchantName != merchant || context.Input.Currency != nil {
		t.Fatal("original financial context was lost or copied from cache", err)
	}
	for _, query := range []string{"UPDATE purchase_header_corrections SET context_json='{}'", "DELETE FROM purchase_header_corrections"} {
		if _, err := s.db.ExecContext(t.Context(), query); err == nil {
			t.Fatal("financial correction audit remained mutable")
		}
	}
	counters := scalar[string](t, s.db, "SELECT json_group_array(json_array(entity_type,entity_id,revision)) FROM (SELECT * FROM projection_revisions ORDER BY entity_type,entity_id)")
	equivalent := "2026-11-06T04:00:00.123456789Z"
	if value, err := p.Patch(t.Context(), fixture.token, "equal-header-instant", corrected.ID, corrected.Revision, PurchasePatch{PurchasedAt: &equivalent}); err != nil || value != corrected {
		t.Fatal("equal instant normalized retained text or advanced source", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_header_corrections") != 2 || scalar[string](t, s.db, "SELECT json_group_array(json_array(entity_type,entity_id,revision)) FROM (SELECT * FROM projection_revisions ORDER BY entity_type,entity_id)") != counters {
		t.Fatal("no-op lost immutable intent or invented projection revisions")
	}
}

func TestPurchaseServiceAllocationsBindBothOriginalProofsAndRemainIndependentExactHistory(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	parent := createPurchaseCommand(t, p, fixture.token, "new-allocation-parent", "TWD")
	before := purchaseCommandState(t, s)
	var line PurchaseLine
	changed := requireProjectionReplay(t, s, func() {
		var err error
		line, err = p.CreateLine(t.Context(), fixture.token, "allocate-stock", parent.ID, parent.Revision, PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1500, PaidMinor: 0, Note: "\x00  Exact zero allocation é\n"})
		if err != nil {
			t.Fatal(err)
		}
	})
	if line.Revision != 1 || line.PurchaseID != parent.ID || line.ItemID != fixture.quantity.ID || line.ItemUnitID != nil || line.QuantityBase != 1500 || line.PaidMinor != 0 || len(changed) != 4 {
		t.Fatal("allocation invented an owner/price/quantity/independent counter", len(changed))
	}
	current, err := p.Get(t.Context(), fixture.token, parent.ID)
	if err != nil || current.Revision != 2 || getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID).Revision != fixture.quantity.Revision+1 {
		t.Fatal("real owner/interpretation aggregates were not advanced", err)
	}
	after := purchaseCommandState(t, s)
	for _, table := range []string{"locations", "item_units", "stock_balances", "inventory_events", "media", "media_links", "ai_jobs", "ai_review_entries"} {
		if after[table] != before[table] {
			t.Fatal("financial allocation silently received/converted physical state", table)
		}
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_lines l JOIN mutation_events e ON e.id=l.mutation_event_id WHERE l.id=? AND l.created_at=e.occurred_at", line.ID) != 1 {
		t.Fatal("new immutable allocation lost exact producer audit association")
	}
	unit, err := p.CreateLine(t.Context(), fixture.token, "allocate-individual-unit", parent.ID, current.Revision, PurchaseLineCreate{ItemID: fixture.individual.ID, ItemRevision: fixture.individual.Revision, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 1, PaidMinor: 123456})
	if err != nil || unit.Revision != 1 || unit.ItemUnitID == nil || *unit.ItemUnitID != fixture.unit.ID || scalar[int64](t, s.db, "SELECT revision FROM projection_revisions WHERE entity_type='unit' AND entity_id=?", fixture.unit.ID) != fixture.unit.Revision {
		t.Fatal("named allocation altered physical Unit proof or inferred price multiplication", err)
	}
	for _, statement := range []string{"UPDATE purchase_lines SET paid_minor=0 WHERE id=?", "DELETE FROM purchase_lines WHERE id=?"} {
		if _, err = s.db.ExecContext(t.Context(), statement, unit.ID); err == nil {
			t.Fatal("new allocation was destructively editable")
		}
	}
}

func TestPurchaseServiceFirstAllocationCannotReinterpretConcurrentItemMeasurementOrHeaderCurrency(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	parent := createPurchaseCommand(t, p, fixture.token, "interpretation-parent", "JPY")
	item := createCatalogItem(t, fixture.catalog, fixture.token, ItemCreate{Name: "Unplaced measured item", TrackingMode: testPointer("quantity"), UnitCode: testPointer("kg"), BaseUnit: testPointer("g"), ScaleFactor: testPointer(int64(1000))})
	input := PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1500, PaidMinor: 123}
	changed, err := fixture.catalog.Patch(t.Context(), fixture.token, "concurrent-measurement", item.ID, item.Revision, ItemPatch{UnitCode: testPointer("L"), BaseUnit: testPointer("ml"), ScaleFactor: testPointer(int64(1000))})
	if err != nil {
		t.Fatal(err)
	}
	before := purchaseCommandState(t, s)
	if value, err := p.CreateLine(t.Context(), fixture.token, "stale-first-allocation", parent.ID, parent.Revision, input); !errors.Is(err, ErrRevisionConflict) || value != (PurchaseLine{}) {
		t.Fatal("first allocation silently used a new measurement", err)
	}
	requirePurchaseCommandState(t, s, before)
	code := "USD"
	updated, err := p.Patch(t.Context(), fixture.token, "concurrent-parent-currency", parent.ID, parent.Revision, PurchasePatch{Currency: &code})
	if err != nil {
		t.Fatal(err)
	}
	input.ItemRevision = changed.Revision
	before = purchaseCommandState(t, s)
	if value, err := p.CreateLine(t.Context(), fixture.token, "stale-parent-allocation", parent.ID, parent.Revision, input); !errors.Is(err, ErrRevisionConflict) || value != (PurchaseLine{}) {
		t.Fatal("original amount silently acquired a different currency", err)
	}
	requirePurchaseCommandState(t, s, before)
	line, err := p.CreateLine(t.Context(), fixture.token, "explicit-new-interpretation", parent.ID, updated.Revision, input)
	if err != nil || line.QuantityBase != 1500 || line.PaidMinor != 123 {
		t.Fatal("explicitly reviewed proofs converted raw values", err)
	}
	item = getCatalogItem(t, fixture.catalog, fixture.token, item.ID)
	if _, err = fixture.catalog.Patch(t.Context(), fixture.token, "referenced-measurement", item.ID, item.Revision, ItemPatch{UnitCode: testPointer("kg"), BaseUnit: testPointer("g")}); !errors.Is(err, ErrItemHistory) {
		t.Fatal("even zero/current placement-independent financial history must lock interpretation", err)
	}
}

func TestPurchaseServiceCurrencyPolicyDoesNotRepriceRetainedOrZeroPaidHistory(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	for _, code := range []string{"JPY", "TWD", "KWD", "CLF", "XAD"} {
		createPurchaseCommand(t, p, fixture.token, "currency-"+code, code)
	}
	for _, code := range []string{"ZZZ", "XAU", "XTS", "XXX", "BGN"} {
		before := purchaseCommandState(t, s)
		if value, err := p.Create(t.Context(), fixture.token, "unsupported-"+code, PurchaseCreate{MerchantName: "Merchant", PurchasedAt: "2026-11-06T00:00:00Z", Currency: code}); !errors.Is(err, ErrPurchaseCurrency) || value != (Purchase{}) {
			t.Fatal("undefined/retired currency exponent was guessed for new money", code, err)
		}
		requirePurchaseCommandState(t, s, before)
	}
	parent := createPurchaseCommand(t, p, fixture.token, "zero-paid-locked-parent", "JPY")
	_, err := p.CreateLine(t.Context(), fixture.token, "zero-paid-lock", parent.ID, parent.Revision, PurchaseLineCreate{ItemID: fixture.quantity.ID, ItemRevision: fixture.quantity.Revision, QuantityBase: 1, PaidMinor: 0})
	if err != nil {
		t.Fatal(err)
	}
	parent, err = p.Get(t.Context(), fixture.token, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := "USD"
	before := purchaseCommandState(t, s)
	if value, err := p.Patch(t.Context(), fixture.token, "reprice-zero", parent.ID, parent.Revision, PurchasePatch{Currency: &code}); !errors.Is(err, ErrPurchaseCurrencyLocked) || value != (Purchase{}) {
		t.Fatal("zero paid history was treated as no allocation", err)
	}
	requirePurchaseCommandState(t, s, before)
	execSQL(t, s.db, "UPDATE purchases SET currency='ZZZ' WHERE id=?", fixture.purchase.ID)
	merchant := "Explicit retained correction"
	if value, err := p.Patch(t.Context(), fixture.token, "retained-code-correction", fixture.purchase.ID, fixture.purchase.Revision, PurchasePatch{MerchantName: &merchant}); err != nil || value.Currency != "ZZZ" {
		t.Fatal("descriptive correction silently repaired retained currency", err)
	}
	parent, err = p.Get(t.Context(), fixture.token, fixture.purchase.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.quantity.ID)
	before = purchaseCommandState(t, s)
	if value, err := p.CreateLine(t.Context(), fixture.token, "new-undefined-money", parent.ID, parent.Revision, PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, QuantityBase: 1, PaidMinor: 0}); !errors.Is(err, ErrPurchaseCurrency) || value != (PurchaseLine{}) {
		t.Fatal("retained code spelling authorized new money", err)
	}
	requirePurchaseCommandState(t, s, before)
}

func TestPurchaseServiceSnapshotsCallerPointersBeforeFingerprintAndTransactionalClock(t *testing.T) {
	fixture, p := purchaseCommands(t)
	s := fixture.store
	text := "Original requested merchant"
	clock := fixture.auth.now
	fixture.auth.now = func() time.Time { text = "Changed after fingerprint"; return clock() }
	value, err := p.Patch(t.Context(), fixture.token, "snapshot-purchase-patch", fixture.purchase.ID, fixture.purchase.Revision, PurchasePatch{MerchantName: &text})
	if err != nil || value.MerchantName != "Original requested merchant" {
		t.Fatal("caller pointer changed audited/fingerprinted command", err)
	}
	fixture.auth.now = clock
	parent := createPurchaseCommand(t, p, fixture.token, "snapshot-allocation-parent", "JPY")
	unitID := fixture.unit.ID
	item := getCatalogItem(t, fixture.catalog, fixture.token, fixture.individual.ID)
	fixture.auth.now = func() time.Time { unitID = "missing"; return clock() }
	line, err := p.CreateLine(t.Context(), fixture.token, "snapshot-allocation-unit", parent.ID, parent.Revision, PurchaseLineCreate{ItemID: item.ID, ItemRevision: item.Revision, ItemUnitID: &unitID, QuantityBase: 1, PaidMinor: 0})
	if err != nil || line.ItemUnitID == nil || *line.ItemUnitID != fixture.unit.ID {
		t.Fatal("caller pointer changed a reviewed Unit target", err)
	}
	if strings.Contains(scalar[string](t, s.db, "SELECT context_json FROM purchase_header_corrections"), "Changed after fingerprint") {
		t.Fatal("immutable audit retained a mutated caller value")
	}
}
