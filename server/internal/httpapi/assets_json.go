package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"

	"homebox/internal/store"
)

const assetBodyLimit = 128 << 10

func assetValidation(w http.ResponseWriter, requestID string) bool {
	writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Use complete documented asset decisions, explicit nullable intent, original proofs and exact safe money/usage integers.", requestID)
	return false
}

type assetTextField struct {
	name        string
	min, max    int
	optional    bool
	destination *string
}

func assetTextFields(w http.ResponseWriter, requestID string, values map[string]json.RawMessage, fields ...assetTextField) bool {
	for _, field := range fields {
		raw, exists := values[field.name]
		if !exists && field.optional {
			continue
		}
		value, ok := jsonString(raw, field.min, field.max)
		if !ok {
			return assetValidation(w, requestID)
		}
		*field.destination = *value
	}
	return true
}

func assetNullableInteger(raw json.RawMessage, minimum int64) (*int64, bool) {
	if bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	value, ok := exactSafeInteger(raw)
	return &value, ok && value >= minimum
}

func readAssetProfile(w http.ResponseWriter, r *http.Request, requestID string) (store.AssetProfileWrite, bool) {
	values, ok := readJSONObject(w, r, requestID, assetBodyLimit, "item_revision", "asset_revision", "cost_basis", "currency", "manual_acquisition_minor", "in_service_at", "retired_at", "estimated_residual_minor", "usage_unit")
	if !ok {
		return store.AssetProfileWrite{}, false
	}
	if len(values) != 9 {
		return store.AssetProfileWrite{}, assetValidation(w, requestID)
	}
	var input store.AssetProfileWrite
	input.ItemRevision, ok = exactSafeInteger(values["item_revision"])
	if !ok || input.ItemRevision < 1 {
		return store.AssetProfileWrite{}, assetValidation(w, requestID)
	}
	for _, field := range []struct {
		name        string
		minimum     int64
		destination **int64
	}{{"asset_revision", 1, &input.AssetRevision}, {"manual_acquisition_minor", 0, &input.ManualAcquisitionMinor}, {"estimated_residual_minor", 0, &input.EstimatedResidualMinor}} {
		value, ok := assetNullableInteger(values[field.name], field.minimum)
		if !ok {
			return store.AssetProfileWrite{}, assetValidation(w, requestID)
		}
		*field.destination = value
	}
	for _, field := range []struct {
		name        string
		destination **string
	}{{"in_service_at", &input.InServiceAt}, {"retired_at", &input.RetiredAt}} {
		value, ok := jsonNullableString(values[field.name], 20, 35)
		if !ok {
			return store.AssetProfileWrite{}, assetValidation(w, requestID)
		}
		*field.destination = value.Value
	}
	if !assetTextFields(w, requestID, values, assetTextField{"cost_basis", 6, 8, false, &input.CostBasis}, assetTextField{"currency", 3, 3, false, &input.Currency}, assetTextField{"usage_unit", 5, 7, false, &input.UsageUnit}) {
		return store.AssetProfileWrite{}, false
	}
	if !slices.Contains([]string{"manual", "purchase"}, input.CostBasis) || (input.CostBasis == "manual") != (input.ManualAcquisitionMinor != nil) {
		return store.AssetProfileWrite{}, assetValidation(w, requestID)
	}
	return input, true
}

func assetEventReference(values map[string]json.RawMessage) (*string, bool) {
	raw, exists := values["correction_of"]
	if !exists {
		return nil, true
	}
	value, ok := jsonParent(raw)
	return value.Value, ok
}

func readAssetUsage(w http.ResponseWriter, r *http.Request, requestID string) (store.UsageWrite, bool) {
	values, ok := readJSONObject(w, r, requestID, assetBodyLimit, "delta", "usage_unit", "event_type", "occurred_at", "note", "correction_of")
	if !ok {
		return store.UsageWrite{}, false
	}
	var input store.UsageWrite
	input.Delta, ok = exactSafeInteger(values["delta"])
	if !ok || input.Delta == 0 {
		return store.UsageWrite{}, assetValidation(w, requestID)
	}
	if !assetTextFields(w, requestID, values, assetTextField{"usage_unit", 5, 7, false, &input.UsageUnit}, assetTextField{"event_type", 3, 10, false, &input.EventType},
		assetTextField{"occurred_at", 20, 35, false, &input.OccurredAt}, assetTextField{"note", 0, 10000, true, &input.Note}) {
		return store.UsageWrite{}, false
	}
	input.CorrectionOf, ok = assetEventReference(values)
	if !ok || !slices.Contains([]string{"use", "correction"}, input.EventType) || (input.EventType == "correction") != (input.CorrectionOf != nil) || input.EventType == "use" && input.Delta < 1 {
		return store.UsageWrite{}, assetValidation(w, requestID)
	}
	return input, true
}

func readAssetCost(w http.ResponseWriter, r *http.Request, requestID string) (store.CostWrite, bool) {
	values, ok := readJSONObject(w, r, requestID, assetBodyLimit, "amount_minor", "currency", "kind", "direction", "occurred_at", "note", "correction_of")
	if !ok {
		return store.CostWrite{}, false
	}
	var input store.CostWrite
	input.AmountMinor, ok = exactSafeInteger(values["amount_minor"])
	if !ok || input.AmountMinor < 1 {
		return store.CostWrite{}, assetValidation(w, requestID)
	}
	if !assetTextFields(w, requestID, values, assetTextField{"currency", 3, 3, false, &input.Currency}, assetTextField{"kind", 3, 11, false, &input.Kind},
		assetTextField{"direction", 6, 7, false, &input.Direction}, assetTextField{"occurred_at", 20, 35, false, &input.OccurredAt}, assetTextField{"note", 0, 10000, true, &input.Note}) {
		return store.CostWrite{}, false
	}
	input.CorrectionOf, ok = assetEventReference(values)
	if !ok || !slices.Contains([]string{"maintenance", "repair", "accessory", "fee", "refund", "adjustment"}, input.Kind) ||
		!slices.Contains([]string{"expense", "credit"}, input.Direction) || (input.Kind == "adjustment") != (input.CorrectionOf != nil) {
		return store.CostWrite{}, assetValidation(w, requestID)
	}
	return input, true
}

func readAssetRetirement(w http.ResponseWriter, r *http.Request, requestID string) (store.AssetRetireWrite, bool) {
	values, ok := readJSONObject(w, r, requestID, assetBodyLimit, "unit_revision", "retired_at", "note")
	if !ok {
		return store.AssetRetireWrite{}, false
	}
	var input store.AssetRetireWrite
	input.UnitRevision, ok = exactSafeInteger(values["unit_revision"])
	if !ok || input.UnitRevision < 1 {
		return store.AssetRetireWrite{}, assetValidation(w, requestID)
	}
	if !assetTextFields(w, requestID, values, assetTextField{"retired_at", 20, 35, false, &input.RetiredAt}, assetTextField{"note", 0, 10000, true, &input.Note}) {
		return store.AssetRetireWrite{}, false
	}
	return input, true
}

func readAssetDisposal(w http.ResponseWriter, r *http.Request, requestID string) (store.AssetDisposalWrite, bool) {
	values, ok := readJSONObject(w, r, requestID, assetBodyLimit, "unit_revision", "kind", "disposed_at", "realized_proceeds_minor", "currency", "note")
	if !ok {
		return store.AssetDisposalWrite{}, false
	}
	var input store.AssetDisposalWrite
	input.UnitRevision, ok = exactSafeInteger(values["unit_revision"])
	if !ok || input.UnitRevision < 1 {
		return store.AssetDisposalWrite{}, assetValidation(w, requestID)
	}
	input.RealizedProceedsMinor, ok = exactSafeInteger(values["realized_proceeds_minor"])
	if !ok || input.RealizedProceedsMinor < 0 {
		return store.AssetDisposalWrite{}, assetValidation(w, requestID)
	}
	if !assetTextFields(w, requestID, values, assetTextField{"kind", 4, 9, false, &input.Kind}, assetTextField{"disposed_at", 20, 35, false, &input.DisposedAt},
		assetTextField{"currency", 3, 3, false, &input.Currency}, assetTextField{"note", 0, 10000, true, &input.Note}) {
		return store.AssetDisposalWrite{}, false
	}
	if !slices.Contains([]string{"sold", "donated", "recycled", "discarded", "other"}, input.Kind) || input.Kind != "sold" && input.RealizedProceedsMinor != 0 {
		return store.AssetDisposalWrite{}, assetValidation(w, requestID)
	}
	return input, true
}
