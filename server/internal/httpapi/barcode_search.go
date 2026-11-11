package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"homebox/internal/barcode"
	"homebox/internal/store"
)

// Barcode discovery is a read of product metadata, never a physical action.
func (a *authAPI) serveBarcodeSearch(w http.ResponseWriter, r *http.Request, requestID string) bool {
	if r.URL.Path != "/api/v1/search/barcodes" {
		return false
	}
	if !a.requireAdmin(w, r, requestID) {
		return true
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	if r.URL.RawPath != "" {
		barcodeSearchQueryError(w, requestID)
		return true
	}
	if !requireNoBody(w, r, requestID) {
		return true
	}
	value, symbology, cursor, limit, ok := barcodeSearchQuery(w, r, requestID)
	if !ok {
		return true
	}
	token, _ := sessionToken(r)
	matches, err := a.catalog.SearchBarcodes(r.Context(), token, value, symbology, cursor, limit)
	if errors.Is(err, store.ErrDomainInput) {
		barcodeSearchQueryError(w, requestID)
	} else if err != nil {
		writeCatalogError(w, err, requestID)
	} else {
		writeJSON(w, matches)
	}
	return true
}

func barcodeSearchQueryError(w http.ResponseWriter, requestID string) {
	writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use one exact barcode value, optional symbology, and a bounded limit and cursor.", requestID)
}

func barcodeSearchQuery(w http.ResponseWriter, r *http.Request, requestID string) (string, string, string, int, bool) {
	invalid := func() (string, string, string, int, bool) {
		barcodeSearchQueryError(w, requestID)
		return "", "", "", 0, false
	}
	if len(r.URL.RawQuery) > 16<<10 {
		return invalid()
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values["value"]) != 1 || !boundedString(values.Get("value"), 1, 512) {
		return invalid()
	}
	value := values.Get("value")
	delete(values, "value")
	symbology := ""
	if entries, present := values["symbology"]; present {
		if len(entries) != 1 || !barcode.ValidSymbology(entries[0]) {
			return invalid()
		}
		symbology = entries[0]
		delete(values, "symbology")
	}
	cursor, limit, ok := metadataQueryValues(values, 50)
	if !ok || cursor != "" && !boundedString(cursor, 1, 2048) {
		return invalid()
	}
	return value, symbology, cursor, limit, true
}
