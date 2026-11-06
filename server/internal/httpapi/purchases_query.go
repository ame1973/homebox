package httpapi

import (
	"net/http"
	"net/url"
	"strconv"

	"homebox/internal/store"
)

func purchaseQueryError(w http.ResponseWriter, requestID string) {
	writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one value per documented purchase filter, valid civil bounds, and a bounded page size/cursor.", requestID)
}

func purchaseListQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, int, store.PurchaseFilters, bool) {
	var filters store.PurchaseFilters
	invalid := func() (string, int, store.PurchaseFilters, bool) {
		purchaseQueryError(w, requestID)
		return "", 0, store.PurchaseFilters{}, false
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
			if !boundedString(value, 1, 2048) {
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
