package httpapi

import (
	"bytes"
	"net/http"

	"homebox/internal/store"
)

// The outer object enforces required original proofs and an explicit nullable
// replacement. Nested objects are parsed with the same exact-name/duplicate rules.
func readPurchaseLineCorrection(w http.ResponseWriter, r *http.Request, requestID string) (store.PurchaseLineCorrectionCreate, bool) {
	values, ok := readJSONObject(w, r, requestID, purchaseBodyLimit, "line_id", "line_revision", "item_revision", "action", "replacement", "reason")
	if !ok {
		return store.PurchaseLineCorrectionCreate{}, false
	}
	lineID, ok := jsonString(values["line_id"], 1, 128)
	if !ok {
		return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
	}
	lineRevision, ok := exactSafeInteger(values["line_revision"])
	if !ok || lineRevision < 1 {
		return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
	}
	itemRevision, ok := exactSafeInteger(values["item_revision"])
	if !ok || itemRevision < 1 {
		return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
	}
	action, ok := jsonString(values["action"], 4, 7)
	if !ok {
		return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
	}
	reason, ok := jsonString(values["reason"], 1, 10000)
	if !ok {
		return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
	}
	input := store.PurchaseLineCorrectionCreate{LineID: *lineID, LineRevision: lineRevision, ItemRevision: itemRevision, Action: *action, Reason: *reason}
	replacement, supplied := values["replacement"]
	if !supplied {
		return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
	}
	switch input.Action {
	case "void":
		if !bytes.Equal(replacement, []byte("null")) {
			return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
		}
	case "replace":
		fields, ok := parseJSONObject(w, requestID, replacement, "item_id", "item_revision", "item_unit_id", "quantity_base", "paid_minor", "note")
		if !ok {
			return store.PurchaseLineCorrectionCreate{}, false
		}
		value, ok := purchaseLineFields(w, requestID, fields)
		if !ok {
			return store.PurchaseLineCorrectionCreate{}, false
		}
		input.Replacement = &value
	default:
		return store.PurchaseLineCorrectionCreate{}, purchaseValidation(w, requestID)
	}
	return input, true
}

func readDisposalCorrection(w http.ResponseWriter, r *http.Request, requestID string) (store.DisposalCorrectionWrite, bool) {
	values, ok := readJSONObject(w, r, requestID, assetBodyLimit, "asset_revision", "action", "replacement", "reason")
	if !ok {
		return store.DisposalCorrectionWrite{}, false
	}
	revision, ok := exactSafeInteger(values["asset_revision"])
	if !ok || revision < 1 {
		return store.DisposalCorrectionWrite{}, assetValidation(w, requestID)
	}
	action, ok := jsonString(values["action"], 4, 7)
	if !ok {
		return store.DisposalCorrectionWrite{}, assetValidation(w, requestID)
	}
	reason, ok := jsonString(values["reason"], 1, 10000)
	if !ok {
		return store.DisposalCorrectionWrite{}, assetValidation(w, requestID)
	}
	input := store.DisposalCorrectionWrite{AssetRevision: revision, Action: *action, Reason: *reason}
	replacement, supplied := values["replacement"]
	if !supplied {
		return store.DisposalCorrectionWrite{}, assetValidation(w, requestID)
	}
	switch input.Action {
	case "void":
		if !bytes.Equal(replacement, []byte("null")) {
			return store.DisposalCorrectionWrite{}, assetValidation(w, requestID)
		}
	case "replace":
		fields, ok := parseJSONObject(w, requestID, replacement, "kind", "disposed_at", "realized_proceeds_minor", "currency", "note")
		if !ok {
			return store.DisposalCorrectionWrite{}, false
		}
		value, ok := disposalFinancialFields(w, requestID, fields)
		if !ok {
			return store.DisposalCorrectionWrite{}, false
		}
		input.Replacement = &value
	default:
		return store.DisposalCorrectionWrite{}, assetValidation(w, requestID)
	}
	return input, true
}
