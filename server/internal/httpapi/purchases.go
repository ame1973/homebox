package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"homebox/internal/store"
)

func purchaseRoute(path string) (route, id, allow string) {
	if path == "/api/v1/purchases" {
		return "collection", "", "GET, POST"
	}
	if !strings.HasPrefix(path, "/api/v1/") {
		return "", "", ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 2 && parts[0] == "purchases" && parts[1] != "" {
		return "resource", parts[1], "GET, PATCH"
	}
	if len(parts) == 3 && parts[1] != "" {
		if parts[0] == "purchases" && parts[2] == "lines" {
			return "lines", parts[1], "GET, POST"
		}
		if parts[0] == "items" && parts[2] == "purchases" {
			return "item", parts[1], http.MethodGet
		}
	}
	return "", "", ""
}

func (a *authAPI) servePurchases(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, id, allow := purchaseRoute(r.URL.Path)
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
	isList := r.Method == http.MethodGet && route != "resource"
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) || !isList && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_PURCHASE_URL", "Use an exact purchase path and only documented collection parameters.", requestID)
		return true
	}
	token, _ := sessionToken(r)
	if r.Method == http.MethodGet {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		switch route {
		case "resource":
			value, err := a.purchases.Get(r.Context(), token, id)
			writePurchaseResult(w, value, value.Revision, err, requestID)
		case "collection":
			cursor, size, filters, ok := purchaseListQuery(w, r, requestID)
			if !ok {
				return true
			}
			value, err := a.purchases.Page(r.Context(), token, cursor, size, filters)
			if errors.Is(err, store.ErrDomainInput) {
				purchaseQueryError(w, requestID)
			} else if err != nil {
				writePurchaseError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
		case "item":
			cursor, size, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
			if !ok {
				return true
			}
			value, err := a.purchases.PageForItem(r.Context(), token, id, cursor, size)
			if err != nil {
				writePurchaseError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
		case "lines":
			cursor, size, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
			if !ok {
				return true
			}
			value, err := a.purchases.PageLines(r.Context(), token, id, cursor, size)
			if err != nil {
				writePurchaseError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
		}
		return true
	}
	revision, supplied, ok := resourceRevision(w, r, requestID, route != "collection")
	if !ok {
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	switch route {
	case "collection":
		if supplied {
			writeError(w, http.StatusBadRequest, "INVALID_REVISION", "Purchase header creation does not accept an unrelated If-Match.", requestID)
			return true
		}
		input, ok := readPurchaseCreate(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.purchases.Create(r.Context(), token, key, input)
		writePurchaseResult(w, value, value.Revision, err, requestID)
	case "resource":
		input, ok := readPurchasePatch(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.purchases.Patch(r.Context(), token, key, id, revision, input)
		writePurchaseResult(w, value, value.Revision, err, requestID)
	case "lines":
		input, ok := readPurchaseLine(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.purchases.CreateLine(r.Context(), token, key, id, revision, input)
		writePurchaseResult(w, value, value.Revision, err, requestID)
	}
	return true
}

func writePurchaseResult(w http.ResponseWriter, value any, revision int64, err error, requestID string) {
	if err != nil {
		writePurchaseError(w, err, requestID)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
	writeJSON(w, value)
}

func writePurchaseError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Purchase data is temporarily unavailable. Retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired), errors.Is(err, store.ErrIdempotencyConflict):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		purchaseValidation(w, requestID)
		return
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The purchase or referenced resource does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the list; its continuation is invalid, expired, or differently scoped."
	case errors.Is(err, store.ErrPreconditionRequired):
		status, code, message = http.StatusConflict, "PRECONDITION_REQUIRED", "Send the original reviewed Purchase revision."
	case errors.Is(err, store.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "REVISION_CONFLICT", "The reviewed Purchase or Item interpretation changed. Refresh and review before submitting again."
	case errors.Is(err, store.ErrIdempotencyExpired):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_EXPIRED", "The original acknowledgement expired. Review current state before using a new key."
	case errors.Is(err, store.ErrPurchaseCurrencyLocked):
		status, code, message = http.StatusConflict, "PURCHASE_CURRENCY_LOCKED", "An allocated purchase cannot change currency, including zero-paid history."
	case errors.Is(err, store.ErrPurchaseCurrency):
		status, code, message = http.StatusConflict, "PURCHASE_CURRENCY_CONFLICT", "Use a supported finite-minor-unit currency compatible with the reviewed financial source."
	case errors.Is(err, store.ErrPurchaseAssetBasis):
		status, code, message = http.StatusConflict, "ASSET_COST_BASIS_CONFLICT", "A manual asset acquisition basis cannot also receive a purchase allocation."
	case errors.Is(err, store.ErrAssetOverflow):
		status, code, message = http.StatusConflict, "ASSET_TOTAL_OVERFLOW", "The proposed asset totals exceed the exact safe financial range. Review the financial source; nothing was allocated."
	case errors.Is(err, store.ErrUnitTracking):
		status, code, message = http.StatusConflict, "TRACKING_MODE_CONFLICT", "A named Unit allocation requires an individually tracked Item."
	}
	writeError(w, status, code, message, requestID)
}
