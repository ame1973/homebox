package httpapi

import (
	"net/http"
	"net/url"
	"strconv"

	"homebox/internal/store"
)

func holdingQueryError(w http.ResponseWriter, requestID string) {
	writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one value per documented inventory filter, valid civil bounds, and a bounded page size/cursor.", requestID)
}

func unitLookupQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, bool) {
	invalid := func() (string, bool) {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one exact Unit identity in the id query parameter.", requestID)
		return "", false
	}
	id, ok := exactIdentityQuery(r)
	if !ok {
		return invalid()
	}
	return id, true
}

func stockListQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, int, store.StockFilters, bool) {
	var filters store.StockFilters
	invalid := func() (string, int, store.StockFilters, bool) {
		holdingQueryError(w, requestID)
		return "", 0, store.StockFilters{}, false
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
		"item_id": {&filters.ItemID, 1, 128},
		"from":    {&filters.From, 10, 10}, "to": {&filters.To, 10, 10}, "currency": {&filters.Currency, 3, 3},
		"category_id": {&filters.CategoryID, 1, 128}, "location_id": {&filters.LocationID, 1, 128}, "scope": {&filters.Scope, 1, 7},
		"status": {&filters.Status, 1, 8}, "merchant": {&filters.Merchant, 1, 300},
	}
	cursor, limit := "", 50
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
			limit = parsed
		case "cursor":
			if !boundedString(value, 1, 4096) {
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
	return cursor, limit, filters, true
}
