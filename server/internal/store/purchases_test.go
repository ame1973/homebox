package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type purchaseFixture struct {
	holdingFixture
	purchase  Purchase
	unitLine  PurchaseLine
	stockLine PurchaseLine
}

// Retained source fixture, including historical named-Unit bulk allocation.
// Real catalog/auth establish authority; command tests create new history separately.
func purchasesFixture(t *testing.T) purchaseFixture {
	t.Helper()
	fixture := holdingsFixture(t)
	purchase := Purchase{PurchaseData{ID: "purchase", MerchantName: "OriginalMerchant", PurchasedAt: "2026-11-06T12:00:00.123456789+08:00", Currency: "JPY", ReceiptNote: "Original receipt é\n"}, 7}
	unit := PurchaseLine{PurchaseLineData{ID: "unit-line", PurchaseID: purchase.ID, ItemID: fixture.individual.ID, ItemUnitID: testPointer(fixture.unit.ID), QuantityBase: 2, PaidMinor: 120000, Note: "OriginalUnitLine"}, 3}
	stock := PurchaseLine{PurchaseLineData{ID: "stock-line", PurchaseID: purchase.ID, ItemID: fixture.quantity.ID, QuantityBase: 1500, PaidMinor: 0, Note: "OriginalStockLine"}, 5}
	execSQL(t, fixture.store.db, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES(?,?,?,?,?,'legacy-created')", purchase.ID, purchase.MerchantName, purchase.PurchasedAt, purchase.Currency, purchase.ReceiptNote)
	seedCatalogProjection(t, fixture.store, "purchase", purchase.ID, purchase.PurchaseData)
	setCatalogProjectionRevision(t, fixture.store, "purchase", purchase.ID, purchase.Revision)
	for _, line := range []PurchaseLine{unit, stock} {
		execSQL(t, fixture.store.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note) VALUES(?,?,?,?,?,?,?)", line.ID, line.PurchaseID, line.ItemID, line.ItemUnitID, line.QuantityBase, line.PaidMinor, line.Note)
		seedCatalogProjection(t, fixture.store, "purchase_line", line.ID, line.PurchaseLineData)
		setCatalogProjectionRevision(t, fixture.store, "purchase_line", line.ID, line.Revision)
	}
	return purchaseFixture{fixture, purchase, unit, stock}
}

func TestPurchaseRepresentationPreservesSafeExactAmountsIndependentRevisionsAndBoundedText(t *testing.T) {
	id := strings.Repeat("😀", 128)
	purchase := Purchase{PurchaseData{ID: id, MerchantName: strings.Repeat("😀", 300), PurchasedAt: "2026-11-06T12:00:00.123456789+23:59", Currency: "JPY", ReceiptNote: strings.Repeat("😀", 10000)}, maxSafeInteger}
	line := PurchaseLine{PurchaseLineData{ID: id, PurchaseID: id, ItemID: id, ItemUnitID: &id, QuantityBase: maxSafeInteger, PaidMinor: maxSafeInteger, Note: strings.Repeat("😀", 10000)}, maxSafeInteger}
	for _, value := range []domainResult{purchase, line} {
		if err := value.validateDomainResult(); err != nil {
			t.Fatal("valid retained bounds rejected", err)
		}
	}
	purchase.MerchantName, purchase.ReceiptNote = "\x00Exact é\n", ""
	line.ItemUnitID, line.PaidMinor, line.Note = nil, 0, "\x00Exact é\n"
	if purchase.validateDomainResult() != nil || line.validateDomainResult() != nil {
		t.Fatal("zero amount/null allocation/exact text was repaired")
	}
	for _, field := range []string{"id", "merchant", "receipt", "date", "currency"} {
		for _, text := range []string{"", string([]byte{0xff}), strings.Repeat("界", 10001)} {
			if text == "" && field == "receipt" {
				continue
			}
			value := purchase
			switch field {
			case "id":
				value.ID = text
			case "merchant":
				value.MerchantName = text
			case "receipt":
				value.ReceiptNote = text
			case "date":
				value.PurchasedAt = text
			case "currency":
				value.Currency = text
			}
			if !errors.Is(value.validateDomainResult(), ErrDomainState) {
				t.Fatal("invalid header representation accepted", field)
			}
		}
	}
	for _, field := range []string{"id", "purchase", "item", "unit", "note"} {
		for _, text := range []string{"", string([]byte{0xff}), strings.Repeat("界", 10001)} {
			if text == "" && field == "note" {
				continue
			}
			value := line
			switch field {
			case "id":
				value.ID = text
			case "purchase":
				value.PurchaseID = text
			case "item":
				value.ItemID = text
			case "unit":
				value.ItemUnitID = &text
			case "note":
				value.Note = text
			}
			if !errors.Is(value.validateDomainResult(), ErrDomainState) {
				t.Fatal("invalid line representation accepted", field)
			}
		}
	}
	for _, revision := range []int64{-1, 0, maxSafeInteger + 1} {
		p, l := purchase, line
		p.Revision, l.Revision = revision, revision
		if p.validateDomainResult() == nil || l.validateDomainResult() == nil {
			t.Fatal("unsafe independent revision accepted")
		}
	}
	for _, count := range []int64{-1, 0, maxSafeInteger + 1} {
		value := line
		value.QuantityBase = count
		if value.validateDomainResult() == nil {
			t.Fatal("invalid base count accepted")
		}
	}
	for _, amount := range []int64{-1, maxSafeInteger + 1} {
		value := line
		value.PaidMinor = amount
		if value.validateDomainResult() == nil {
			t.Fatal("invalid minor-unit allocation accepted")
		}
	}
}

func TestPurchaseInstantValidationIsLosslessStrictAndUTCRepresentableWithoutChangingOtherDomains(t *testing.T) {
	for _, value := range []string{"0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999999Z", "2026-11-06T12:00:00.1+23:59", "2026-11-06T12:00:00.123456789-12:00"} {
		if !validPurchaseInstant(value) {
			t.Fatal("valid exact instant rejected", value)
		}
	}
	for _, value := range []string{"", "2026", "2026-11-06", "2026-11-06t00:00:00z", "2026-11-06 00:00:00Z", "2026-11-06T0:00:00Z", "2026-11-06T00:00:00,1Z", "2026-11-06T00:00:60Z", "2026-02-30T00:00:00Z", "2026-11-06T25:00:00Z", "2026-11-06T00:00:00+24:00", "2026-11-06T00:00:00+00:60", "2026-11-06T00:00:00.1234567890Z", "2026-11-06T00:00:00.1234567890000000Z", "0000-01-01T00:00:00Z", "10000-01-01T00:00:00Z", "0001-01-01T00:00:00+00:01", "9999-12-31T23:59:59-00:01", "2026-11-06T00:00:00Z\n", "2026-11-06T00:00:00Z\x00"} {
		if validPurchaseInstant(value) {
			t.Fatal("unsupported/truncated financial instant accepted", value)
		}
	}
	for _, value := range []string{"JPY", "TWD", "USD", "ZZZ"} {
		if !validCurrencyCode(value) {
			t.Fatal("bounded retained code spelling rejected", value)
		}
	}
	for _, value := range []string{"", "JP", "JPY\n", "jpy", "JP0", "JP\x00", "日本円"} {
		if validCurrencyCode(value) {
			t.Fatal("invalid currency spelling accepted", value)
		}
	}
}

func TestPurchaseLoadersUseExactSourceAndActualAuthorityWithoutReadSideEffects(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	checks := []struct {
		kind, id string
		want     domainResult
		load     func(context.Context, queryer, string) (domainResult, error)
		scan     func(rowScanner) (domainResult, error)
	}{
		{"purchase", fixture.purchase.ID, fixture.purchase, func(ctx context.Context, q queryer, id string) (domainResult, error) { return loadPurchase(ctx, q, id) }, func(row rowScanner) (domainResult, error) { return scanPurchase(row) }},
		{"purchase_line", fixture.unitLine.ID, fixture.unitLine, func(ctx context.Context, q queryer, id string) (domainResult, error) {
			return loadPurchaseLine(ctx, q, id)
		}, func(row rowScanner) (domainResult, error) { return scanPurchaseLine(row) }},
	}
	for _, check := range checks {
		t.Run(check.kind, func(t *testing.T) {
			execSQL(t, s.db, "UPDATE current_projections SET payload_json='{\"NeverCopyPurchaseCache\":true}' WHERE entity_type=? AND entity_id=?", check.kind, check.id)
			before := domainState(t, s)
			if value, err := check.load(t.Context(), s.db, check.id); err != nil || !reflect.DeepEqual(value, check.want) {
				t.Fatal("source/revision replaced by cache or new-write policy", err)
			}
			requireDomainState(t, s, before)
			if value, err := check.load(t.Context(), s.db, "missing"); !errors.Is(err, ErrNotFound) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("missing source exposed a DTO", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if value, err := check.load(ctx, s.db, check.id); !errors.Is(err, context.Canceled) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("canceled source escaped", err)
			}
			if value, err := check.scan(s.db.QueryRowContext(t.Context(), "SELECT 1")); err == nil || !reflect.ValueOf(value).IsZero() {
				t.Fatal("bad scan exposed partial resource", err)
			}
			for _, revision := range []int64{0, maxSafeInteger + 1} {
				execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", check.kind, check.id)
				execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", check.kind, check.id)
				if revision != 0 {
					execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,?,?)", check.kind, check.id, revision)
				}
				before = domainState(t, s)
				if value, err := check.load(t.Context(), s.db, check.id); !errors.Is(err, ErrDomainState) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("missing/unsafe authority was reseeded", err)
				}
				requireDomainState(t, s, before)
				if revision == 0 {
					execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,?,?)", check.kind, check.id, 1)
				}
			}
			execSQL(t, s.db, "UPDATE projection_revisions SET revision=? WHERE entity_type=? AND entity_id=?", reflect.ValueOf(check.want).FieldByName("Revision").Int(), check.kind, check.id)
		})
	}
	if value, err := loadPurchaseLine(t.Context(), s.db, fixture.stockLine.ID); err != nil || value.PaidMinor != 0 || value.QuantityBase != 1500 || value.ItemUnitID != nil {
		t.Fatal("zero-paid unallocated history was hidden or converted", err)
	}
	for _, value := range []any{fixture.purchase.PurchaseData, fixture.unitLine.PurchaseLineData} {
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), "revision") || strings.Contains(string(encoded), "created_at") || strings.Contains(string(encoded), "mutation_event_id") {
			t.Fatal("private metadata/revision leaked into offline payload", err)
		}
	}
}

func TestPurchaseLineAuthorityRejectsMissingCountersWrongOwnershipAndInvalidRelatedSource(t *testing.T) {
	fixture := purchasesFixture(t)
	s := fixture.store
	for _, key := range []projectionKey{{"purchase", fixture.purchase.ID}, {"item", fixture.individual.ID}, {"unit", fixture.unit.ID}} {
		execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", key.kind, key.id)
		execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", key.kind, key.id)
		if value, err := loadPurchaseLine(t.Context(), s.db, fixture.unitLine.ID); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
			t.Fatal("missing related counter was invented", key, err)
		}
		execSQL(t, s.db, "INSERT INTO projection_revisions VALUES(?,?,?)", key.kind, key.id, maxSafeInteger+1)
		if value, err := loadPurchaseLine(t.Context(), s.db, fixture.unitLine.ID); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
			t.Fatal("unsafe related counter accepted", key, err)
		}
		execSQL(t, s.db, "UPDATE projection_revisions SET revision=1 WHERE entity_type=? AND entity_id=?", key.kind, key.id)
	}
	execSQL(t, s.db, "INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor) VALUES('wrong-owner',?,?,?,1,0)", fixture.purchase.ID, fixture.quantity.ID, fixture.unit.ID)
	execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('purchase_line','wrong-owner',1)")
	if value, err := loadPurchaseLine(t.Context(), s.db, "wrong-owner"); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
		t.Fatal("foreign Unit was allocated to a quantity Item", err)
	}
	execSQL(t, s.db, "UPDATE purchases SET purchased_at='2026' WHERE id=?", fixture.purchase.ID)
	if value, err := loadPurchaseLine(t.Context(), s.db, fixture.unitLine.ID); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
		t.Fatal("invalid actual Purchase was hidden by line cache", err)
	}
}

func TestPurchaseScannersRejectPrivateMetadataGapsAndUnrepresentableProducerInstants(t *testing.T) {
	s := openTestStore(t)
	for _, fields := range []string{"NULL,'event'", "'2026-11-06T00:00:00Z',NULL", "'2026','event'", "'2026-11-06T00:00:00Z',''"} {
		if value, err := scanPurchaseLine(s.db.QueryRowContext(t.Context(), "SELECT 'line','purchase','item',NULL,1,0,'',1,"+fields)); !errors.Is(err, ErrDomainState) || value != (PurchaseLine{}) {
			t.Fatal("invalid private producer context accepted", fields, err)
		}
	}
	if value, err := scanPurchaseLine(s.db.QueryRowContext(t.Context(), "SELECT 'line','purchase','item',NULL,1,0,'',1,'2026-11-06T00:00:00.123456789Z','event'")); err != nil || value.PaidMinor != 0 {
		t.Fatal("valid private metadata rejected", err)
	}
}
