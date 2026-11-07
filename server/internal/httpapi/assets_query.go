package httpapi

import (
	"net/http"
	"net/url"
	"strconv"

	"homebox/internal/store"
)

func assetQueryError(w http.ResponseWriter, requestID string) {
	writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one value per documented asset filter, valid civil/as_of bounds and bounded live paging.", requestID)
}

func assetListQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, int, store.AssetFilters, bool) {
	var filters store.AssetFilters
	invalid := func() (string, int, store.AssetFilters, bool) {
		assetQueryError(w, requestID)
		return "", 0, store.AssetFilters{}, false
	}
	if len(r.URL.RawQuery) > 16<<10 {
		return invalid()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	fields := map[string]struct {
		destination *string
		min, max    int
	}{
		"from": {&filters.From, 10, 10}, "to": {&filters.To, 10, 10}, "currency": {&filters.Currency, 3, 3},
		"category_id": {&filters.CategoryID, 1, 128}, "location_id": {&filters.LocationID, 1, 128}, "scope": {&filters.Scope, 1, 7},
		"status": {&filters.Status, 1, 8}, "merchant": {&filters.Merchant, 1, 300},
	}
	cursor, size := "", 50
	for name, entries := range values {
		if len(entries) != 1 {
			return invalid()
		}
		value := entries[0]
		switch name {
		case "limit":
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				return invalid()
			}
			size = parsed
		case "cursor":
			if len(value) > 2048 || !boundedString(value, 1, 2048) {
				return invalid()
			}
			cursor = value
		default:
			field, known := fields[name]
			if !known || !boundedString(value, field.min, field.max) {
				return invalid()
			}
			*field.destination = value
		}
	}
	return cursor, size, filters, true
}

func assetSummaryQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	invalid := func() (string, bool) {
		assetQueryError(w, requestID)
		return "", false
	}
	if len(r.URL.RawQuery) > 16<<10 {
		return invalid()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values) > 1 {
		return invalid()
	}
	if len(values) == 0 {
		return "", true
	}
	entries, exists := values["as_of"]
	if !exists || len(entries) != 1 || !boundedString(entries[0], 20, 35) {
		return invalid()
	}
	return entries[0], true
}
