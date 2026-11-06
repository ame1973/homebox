package httpapi

import (
	"encoding/json"
	"net/http"

	"homebox/internal/store"
)

const holdingBodyLimit = 128 << 10
const unitMoveBodyLimit = 16 << 10

func holdingValidation(w http.ResponseWriter, requestID string) bool {
	writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Use exact documented holding fields, nullable metadata, explicit lifecycle choices and safe base quantities.", requestID)
	return false
}

func validUnitInputStatus(status string, terminal bool) bool {
	switch status {
	case "stored", "in_use", "lent", "lost":
		return true
	case "retired", "disposed":
		return terminal
	default:
		return false
	}
}

func unitMetadataFields(w http.ResponseWriter, values map[string]json.RawMessage, requestID string) (store.UnitPatch, bool) {
	var input store.UnitPatch
	if raw, exists := values["serial_number"]; exists {
		value, ok := jsonNullableString(raw, 0, 10000)
		if !ok {
			return input, holdingValidation(w, requestID)
		}
		input.SerialNumber = value
	}
	if raw, exists := values["notes"]; exists {
		value, ok := jsonString(raw, 0, 10000)
		if !ok {
			return input, holdingValidation(w, requestID)
		}
		input.Notes = value
	}
	return input, true
}

func readUnitCreate(w http.ResponseWriter, r *http.Request, requestID string) (store.UnitCreate, bool) {
	values, ok := readJSONObject(w, r, requestID, holdingBodyLimit, "location_id", "serial_number", "notes", "status")
	if !ok {
		return store.UnitCreate{}, false
	}
	location, ok := jsonParent(values["location_id"])
	if !ok {
		return store.UnitCreate{}, holdingValidation(w, requestID)
	}
	metadata, ok := unitMetadataFields(w, values, requestID)
	if !ok {
		return store.UnitCreate{}, false
	}
	input := store.UnitCreate{LocationID: location.Value, SerialNumber: metadata.SerialNumber.Value}
	if metadata.Notes != nil {
		input.Notes = *metadata.Notes
	}
	if raw, exists := values["status"]; exists {
		value, ok := jsonString(raw, 1, 8)
		if !ok || !validUnitInputStatus(*value, false) {
			return store.UnitCreate{}, holdingValidation(w, requestID)
		}
		input.Status = value
	}
	return input, true
}

func readUnitPatch(w http.ResponseWriter, r *http.Request, requestID string) (store.UnitPatch, bool) {
	values, ok := readJSONObject(w, r, requestID, holdingBodyLimit, "serial_number", "notes")
	if !ok {
		return store.UnitPatch{}, false
	}
	if len(values) == 0 {
		return store.UnitPatch{}, holdingValidation(w, requestID)
	}
	return unitMetadataFields(w, values, requestID)
}

func readUnitMove(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	values, ok := readJSONObject(w, r, requestID, unitMoveBodyLimit, "target_location_id")
	if !ok {
		return "", false
	}
	value, ok := jsonString(values["target_location_id"], 1, 128)
	if !ok {
		return "", holdingValidation(w, requestID)
	}
	return *value, true
}

func readUnitStatus(w http.ResponseWriter, r *http.Request, requestID string) (store.UnitStatus, bool) {
	values, ok := readJSONObject(w, r, requestID, holdingBodyLimit, "status", "note", "disposal_kind")
	if !ok {
		return store.UnitStatus{}, false
	}
	status, ok := jsonString(values["status"], 1, 8)
	if !ok || !validUnitInputStatus(*status, true) {
		return store.UnitStatus{}, holdingValidation(w, requestID)
	}
	input := store.UnitStatus{Status: *status}
	if raw, exists := values["note"]; exists {
		value, ok := jsonString(raw, 0, 10000)
		if !ok {
			return store.UnitStatus{}, holdingValidation(w, requestID)
		}
		input.Note = *value
	}
	if raw, exists := values["disposal_kind"]; exists {
		value, ok := jsonString(raw, 1, 9)
		if !ok {
			return store.UnitStatus{}, holdingValidation(w, requestID)
		}
		switch *value {
		case "sold", "donated", "recycled", "discarded":
		default:
			return store.UnitStatus{}, holdingValidation(w, requestID)
		}
		input.DisposalKind = value
	}
	if (input.Status == "disposed") != (input.DisposalKind != nil) {
		return store.UnitStatus{}, holdingValidation(w, requestID)
	}
	return input, true
}

type stockHTTPInput struct {
	itemID, sourceID, targetID string
	quantity                   int64
	note, reason               string
}

func readStockInput(w http.ResponseWriter, r *http.Request, requestID, kind string) (stockHTTPInput, bool) {
	fields := []string{"item_id", "quantity_base", "note"}
	switch kind {
	case "receive":
		fields = append(fields, "target_location_id")
	case "consume":
		fields = append(fields, "source_location_id")
	case "transfer":
		fields = append(fields, "source_location_id", "target_location_id")
	case "adjust":
		fields = append(fields, "target_location_id", "reason")
	default:
		return stockHTTPInput{}, holdingValidation(w, requestID)
	}
	values, ok := readJSONObject(w, r, requestID, holdingBodyLimit, fields...)
	if !ok {
		return stockHTTPInput{}, false
	}
	item, ok := jsonString(values["item_id"], 1, 128)
	if !ok {
		return stockHTTPInput{}, holdingValidation(w, requestID)
	}
	quantity, ok := exactSafeInteger(values["quantity_base"])
	if !ok || quantity < 0 || kind != "adjust" && quantity == 0 {
		return stockHTTPInput{}, holdingValidation(w, requestID)
	}
	input := stockHTTPInput{itemID: *item, quantity: quantity}
	for _, field := range []struct {
		name        string
		required    bool
		min, max    int
		destination *string
	}{
		{"source_location_id", kind == "consume" || kind == "transfer", 1, 128, &input.sourceID},
		{"target_location_id", kind != "consume", 1, 128, &input.targetID},
		{"reason", kind == "adjust", 1, 10000, &input.reason}, {"note", false, 0, 10000, &input.note},
	} {
		raw, exists := values[field.name]
		if !exists && !field.required {
			continue
		}
		value, ok := jsonString(raw, field.min, field.max)
		if !ok {
			return stockHTTPInput{}, holdingValidation(w, requestID)
		}
		*field.destination = *value
	}
	return input, true
}
