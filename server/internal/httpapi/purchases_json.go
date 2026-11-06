package httpapi

import (
	"net/http"

	"homebox/internal/store"
)

const purchaseBodyLimit = 128 << 10

func purchaseValidation(w http.ResponseWriter, requestID string) bool {
	writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Use exact documented purchase fields, explicit allocation intent and safe independent money/base integers.", requestID)
	return false
}

func readPurchaseFields(w http.ResponseWriter, r *http.Request, requestID string, create bool) (store.PurchasePatch, bool) {
	values, ok := readJSONObject(w, r, requestID, purchaseBodyLimit, "merchant_name", "purchased_at", "currency", "receipt_note")
	if !ok {
		return store.PurchasePatch{}, false
	}
	if len(values) == 0 {
		return store.PurchasePatch{}, purchaseValidation(w, requestID)
	}
	var input store.PurchasePatch
	for _, field := range []struct {
		name        string
		min, max    int
		destination **string
	}{
		{"merchant_name", 1, 300, &input.MerchantName}, {"purchased_at", 20, 35, &input.PurchasedAt},
		{"currency", 3, 3, &input.Currency}, {"receipt_note", 0, 10000, &input.ReceiptNote},
	} {
		if raw, exists := values[field.name]; exists {
			value, ok := jsonString(raw, field.min, field.max)
			if !ok {
				return store.PurchasePatch{}, purchaseValidation(w, requestID)
			}
			*field.destination = value
		}
	}
	if create && (input.MerchantName == nil || input.PurchasedAt == nil || input.Currency == nil) {
		return store.PurchasePatch{}, purchaseValidation(w, requestID)
	}
	// Actual instant/UTC-year and new-money registry rules remain authoritative in
	// the store. Syntax/type/width acceptance is not financial eligibility.
	return input, true
}
func readPurchaseCreate(w http.ResponseWriter, r *http.Request, requestID string) (store.PurchaseCreate, bool) {
	fields, ok := readPurchaseFields(w, r, requestID, true)
	if !ok {
		return store.PurchaseCreate{}, false
	}
	input := store.PurchaseCreate{MerchantName: *fields.MerchantName, PurchasedAt: *fields.PurchasedAt, Currency: *fields.Currency}
	if fields.ReceiptNote != nil {
		input.ReceiptNote = *fields.ReceiptNote
	}
	return input, true
}
func readPurchasePatch(w http.ResponseWriter, r *http.Request, requestID string) (store.PurchasePatch, bool) {
	return readPurchaseFields(w, r, requestID, false)
}

func readPurchaseLine(w http.ResponseWriter, r *http.Request, requestID string) (store.PurchaseLineCreate, bool) {
	values, ok := readJSONObject(w, r, requestID, purchaseBodyLimit, "item_id", "item_revision", "item_unit_id", "quantity_base", "paid_minor", "note")
	if !ok {
		return store.PurchaseLineCreate{}, false
	}
	item, ok := jsonString(values["item_id"], 1, 128)
	if !ok {
		return store.PurchaseLineCreate{}, purchaseValidation(w, requestID)
	}
	itemRevision, ok := exactSafeInteger(values["item_revision"])
	if !ok || itemRevision < 1 {
		return store.PurchaseLineCreate{}, purchaseValidation(w, requestID)
	}
	unit, ok := jsonParent(values["item_unit_id"])
	if !ok {
		return store.PurchaseLineCreate{}, purchaseValidation(w, requestID)
	}
	quantity, ok := exactSafeInteger(values["quantity_base"])
	if !ok || quantity < 1 || unit.Value != nil && quantity != 1 {
		return store.PurchaseLineCreate{}, purchaseValidation(w, requestID)
	}
	paid, ok := exactSafeInteger(values["paid_minor"])
	if !ok || paid < 0 {
		return store.PurchaseLineCreate{}, purchaseValidation(w, requestID)
	}
	input := store.PurchaseLineCreate{ItemID: *item, ItemRevision: itemRevision, ItemUnitID: unit.Value, QuantityBase: quantity, PaidMinor: paid}
	if raw, exists := values["note"]; exists {
		value, ok := jsonString(raw, 0, 10000)
		if !ok {
			return store.PurchaseLineCreate{}, purchaseValidation(w, requestID)
		}
		input.Note = *value
	}
	return input, true
}
