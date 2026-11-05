package httpapi

import (
	"net/http"

	"homebox/internal/store"
)

const catalogBodyLimit = 128 << 10

func catalogValidation(w http.ResponseWriter, requestID string) bool {
	writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Catalog fields must use their documented names, nullable types, Unicode and integer bounds.", requestID)
	return false
}

func readItemFields(w http.ResponseWriter, r *http.Request, requestID string) (store.ItemPatch, bool) {
	var input store.ItemPatch
	values, ok := readJSONObject(w, r, requestID, catalogBodyLimit,
		"name", "brand", "model", "category_id", "description", "notes", "tracking_mode", "unit_code", "base_unit", "scale_factor", "status")
	if !ok {
		return input, false
	}
	if len(values) == 0 {
		return input, catalogValidation(w, requestID)
	}
	for _, field := range []struct {
		name        string
		min, max    int
		destination **string
	}{
		{"name", 1, 300, &input.Name}, {"description", 0, 10000, &input.Description}, {"notes", 0, 10000, &input.Notes},
		{"tracking_mode", 1, 10, &input.TrackingMode}, {"unit_code", 1, 32, &input.UnitCode}, {"base_unit", 1, 32, &input.BaseUnit}, {"status", 1, 8, &input.Status},
	} {
		if raw, exists := values[field.name]; exists {
			value, ok := jsonString(raw, field.min, field.max)
			if !ok {
				return input, catalogValidation(w, requestID)
			}
			*field.destination = value
		}
	}
	if input.TrackingMode != nil && *input.TrackingMode != "individual" && *input.TrackingMode != "quantity" ||
		input.Status != nil && *input.Status != "active" && *input.Status != "archived" {
		return input, catalogValidation(w, requestID)
	}
	for _, field := range []struct {
		name        string
		min, max    int
		destination *store.NullableString
	}{
		{"brand", 0, 300, &input.Brand}, {"model", 0, 300, &input.Model}, {"category_id", 1, 128, &input.CategoryID},
	} {
		if raw, exists := values[field.name]; exists {
			value, ok := jsonNullableString(raw, field.min, field.max)
			if !ok {
				return input, catalogValidation(w, requestID)
			}
			*field.destination = value
		}
	}
	if raw, exists := values["scale_factor"]; exists {
		value, ok := exactSafeInteger(raw)
		if !ok || value < 1 {
			return input, catalogValidation(w, requestID)
		}
		input.ScaleFactor = &value
	}
	return input, true
}

func readItemCreate(w http.ResponseWriter, r *http.Request, requestID string) (store.ItemCreate, bool) {
	fields, ok := readItemFields(w, r, requestID)
	if !ok {
		return store.ItemCreate{}, false
	}
	if fields.Name == nil {
		return store.ItemCreate{}, catalogValidation(w, requestID)
	}
	input := store.ItemCreate{Name: *fields.Name, Brand: fields.Brand.Value, Model: fields.Model.Value, CategoryID: fields.CategoryID.Value,
		TrackingMode: fields.TrackingMode, UnitCode: fields.UnitCode, BaseUnit: fields.BaseUnit, ScaleFactor: fields.ScaleFactor, Status: fields.Status}
	if fields.Description != nil {
		input.Description = *fields.Description
	}
	if fields.Notes != nil {
		input.Notes = *fields.Notes
	}
	return input, true
}

func readItemArchive(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	values, ok := readJSONObject(w, r, requestID, catalogBodyLimit, "reason")
	if !ok {
		return "", false
	}
	if raw, exists := values["reason"]; exists {
		value, ok := jsonString(raw, 0, 10000)
		if !ok {
			return "", catalogValidation(w, requestID)
		}
		return *value, true
	}
	return "", true
}
