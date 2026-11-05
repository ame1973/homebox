package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"homebox/internal/store"
)

func catalogLinkRoute(path string) (route, kind, id, allow string) {
	const prefix = "/api/v1/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", "", ""
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 3 && parts[0] == "items" && parts[1] != "" {
		switch parts[2] {
		case "barcodes", "aliases", "attributes":
			return "collection", parts[2], parts[1], "GET, POST"
		case "tags":
			return "collection", "tags", parts[1], "GET, PUT"
		}
	}
	if len(parts) == 2 && parts[1] != "" {
		switch parts[0] {
		case "barcodes", "aliases", "attributes":
			return "resource", parts[0], parts[1], "PATCH, DELETE"
		}
	}
	return "", "", "", ""
}

func (a *authAPI) serveCatalogLinks(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, kind, id, allow := catalogLinkRoute(r.URL.Path)
	if route == "" {
		return false
	}
	if !a.requireAdmin(w, r, requestID) {
		return true
	}
	if !slices.Contains(strings.Split(allow, ", "), r.Method) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	isList := route == "collection" && r.Method == http.MethodGet
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || !boundedString(id, 1, 128) ||
		!isList && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_CATALOG_URL", "Use an exact catalog path and only documented list parameters.", requestID)
		return true
	}
	token, _ := sessionToken(r)
	if isList {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		cursor, limit, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
		if !ok {
			return true
		}
		var page any
		var err error
		switch kind {
		case "barcodes":
			page, err = a.catalog.PageBarcodes(r.Context(), token, id, cursor, limit)
		case "aliases":
			page, err = a.catalog.PageAliases(r.Context(), token, id, cursor, limit)
		case "attributes":
			page, err = a.catalog.PageAttributes(r.Context(), token, id, cursor, limit)
		case "tags":
			page, err = a.catalog.PageTags(r.Context(), token, id, cursor, limit)
		}
		if err != nil {
			writeCatalogError(w, err, requestID)
		} else {
			writeJSON(w, page)
		}
		return true
	}
	revision, _, ok := resourceRevision(w, r, requestID, true)
	if !ok {
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	if r.Method == http.MethodDelete {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		var ack store.ActionAck
		var err error
		switch kind {
		case "barcodes":
			ack, err = a.catalog.DeleteBarcode(r.Context(), token, key, id, revision)
		case "aliases":
			ack, err = a.catalog.DeleteAlias(r.Context(), token, key, id, revision)
		case "attributes":
			ack, err = a.catalog.DeleteAttribute(r.Context(), token, key, id, revision)
		}
		if err != nil {
			writeCatalogError(w, err, requestID)
		} else {
			writeJSON(w, ack)
		}
		return true
	}
	switch kind {
	case "barcodes":
		input, ok := readBarcodeWrite(w, r, requestID)
		if !ok {
			return true
		}
		var value store.Barcode
		var err error
		if route == "collection" {
			value, err = a.catalog.CreateBarcode(r.Context(), token, key, id, revision, input)
		} else {
			value, err = a.catalog.PatchBarcode(r.Context(), token, key, id, revision, input)
		}
		writeCatalogLinkResult(w, value, value.Revision, err, requestID)
	case "aliases":
		alias, ok := readAliasWrite(w, r, requestID)
		if !ok {
			return true
		}
		var value store.Alias
		var err error
		if route == "collection" {
			value, err = a.catalog.CreateAlias(r.Context(), token, key, id, revision, alias)
		} else {
			value, err = a.catalog.PatchAlias(r.Context(), token, key, id, revision, alias)
		}
		writeCatalogLinkResult(w, value, value.Revision, err, requestID)
	case "attributes":
		input, ok := readAttributeFields(w, r, requestID, route == "collection")
		if !ok {
			return true
		}
		var value store.Attribute
		var err error
		if route == "collection" {
			value, err = a.catalog.CreateAttribute(r.Context(), token, key, id, revision, store.AttributeWrite{AttributeKey: *input.AttributeKey, AttributeValue: *input.AttributeValue})
		} else {
			value, err = a.catalog.PatchAttribute(r.Context(), token, key, id, revision, input)
		}
		writeCatalogLinkResult(w, value, value.Revision, err, requestID)
	case "tags":
		ids, ok := readItemTags(w, r, requestID)
		if !ok {
			return true
		}
		item, err := a.catalog.ReplaceTags(r.Context(), token, key, id, revision, ids)
		writeCatalogResult(w, item, err, requestID)
	}
	return true
}

func writeCatalogLinkResult(w http.ResponseWriter, value any, revision int64, err error, requestID string) {
	if err != nil {
		writeCatalogError(w, err, requestID)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
	writeJSON(w, value)
}
