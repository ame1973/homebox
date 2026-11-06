package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"homebox/internal/store"
)

func holdingRoute(path string) (route, id, allow string) {
	if path == "/api/v1/inventory" {
		return "inventory", "", http.MethodGet
	}
	if !strings.HasPrefix(path, "/api/v1/") {
		return "", "", ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 3 && parts[0] == "items" && parts[1] != "" && parts[2] == "units" {
		return "units", parts[1], "GET, POST"
	}
	if len(parts) == 2 && parts[0] == "units" && parts[1] != "" {
		return "unit", parts[1], "GET, PATCH, DELETE"
	}
	if len(parts) == 3 && parts[0] == "units" && parts[1] != "" && (parts[2] == "move" || parts[2] == "status") {
		return parts[2], parts[1], http.MethodPost
	}
	if len(parts) == 2 && parts[0] == "stock" {
		switch parts[1] {
		case "receive", "consume", "transfer", "adjust":
			return parts[1], "", http.MethodPost
		}
	}
	return "", "", ""
}

func (a *authAPI) serveHoldings(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, id, allow := holdingRoute(r.URL.Path)
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
	isList := route == "inventory" || route == "units" && r.Method == http.MethodGet
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) || !isList && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_HOLDING_URL", "Use an exact holding path and only documented collection parameters.", requestID)
		return true
	}
	token, _ := sessionToken(r)
	if r.Method == http.MethodGet {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		switch route {
		case "unit":
			value, err := a.inventory.GetUnit(r.Context(), token, id)
			writeHoldingResult(w, value, value.Revision, err, requestID)
		case "units":
			cursor, limit, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
			if !ok {
				return true
			}
			value, err := a.inventory.PageUnits(r.Context(), token, id, cursor, limit)
			if err != nil {
				writeHoldingError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
		case "inventory":
			cursor, limit, filters, ok := stockListQuery(w, r, requestID)
			if !ok {
				return true
			}
			value, err := a.inventory.PageStock(r.Context(), token, cursor, limit, filters)
			if errors.Is(err, store.ErrDomainInput) {
				holdingQueryError(w, requestID)
			} else if err != nil {
				writeHoldingError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
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
		ack, err := a.inventory.DeleteUnit(r.Context(), token, key, id, revision)
		if err != nil {
			writeHoldingError(w, err, requestID)
		} else {
			writeJSON(w, ack)
		}
		return true
	}
	switch route {
	case "units":
		input, ok := readUnitCreate(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.inventory.CreateUnit(r.Context(), token, key, id, revision, input)
		writeHoldingResult(w, value, value.Revision, err, requestID)
	case "unit":
		input, ok := readUnitPatch(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.inventory.PatchUnit(r.Context(), token, key, id, revision, input)
		writeHoldingResult(w, value, value.Revision, err, requestID)
	case "move":
		target, ok := readUnitMove(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.inventory.MoveUnit(r.Context(), token, key, id, revision, target)
		writeHoldingResult(w, value, value.Revision, err, requestID)
	case "status":
		input, ok := readUnitStatus(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.inventory.SetUnitStatus(r.Context(), token, key, id, revision, input)
		writeHoldingResult(w, value, value.Revision, err, requestID)
	default:
		input, ok := readStockInput(w, r, requestID, route)
		if !ok {
			return true
		}
		var value store.Stock
		var err error
		switch route {
		case "receive":
			value, err = a.inventory.Receive(r.Context(), token, key, revision, store.StockReceive{ItemID: input.itemID, TargetLocationID: input.targetID, QuantityBase: input.quantity, Note: input.note})
		case "consume":
			value, err = a.inventory.Consume(r.Context(), token, key, revision, store.StockConsume{ItemID: input.itemID, SourceLocationID: input.sourceID, QuantityBase: input.quantity, Note: input.note})
		case "transfer":
			value, err = a.inventory.Transfer(r.Context(), token, key, revision, store.StockTransfer{ItemID: input.itemID, SourceLocationID: input.sourceID, TargetLocationID: input.targetID, QuantityBase: input.quantity, Note: input.note})
		case "adjust":
			value, err = a.inventory.Adjust(r.Context(), token, key, revision, store.StockAdjust{ItemID: input.itemID, TargetLocationID: input.targetID, QuantityBase: input.quantity, Reason: input.reason, Note: input.note})
		}
		writeHoldingResult(w, value, value.Revision, err, requestID)
	}
	return true
}

func writeHoldingResult(w http.ResponseWriter, value any, revision int64, err error, requestID string) {
	if err != nil {
		writeHoldingError(w, err, requestID)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
	writeJSON(w, value)
}

func writeHoldingError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Holding data is temporarily unavailable. Retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired), errors.Is(err, store.ErrIdempotencyConflict):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		holdingValidation(w, requestID)
		return
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The holding or referenced resource does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the list; its continuation is invalid, expired, or differently scoped."
	case errors.Is(err, store.ErrPreconditionRequired):
		status, code, message = http.StatusConflict, "PRECONDITION_REQUIRED", "Send the original reviewed Item or Unit revision."
	case errors.Is(err, store.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "REVISION_CONFLICT", "The reviewed resource changed. Refresh and review before submitting again."
	case errors.Is(err, store.ErrIdempotencyExpired):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_EXPIRED", "The original acknowledgement expired. Review current state before using a new key."
	case errors.Is(err, store.ErrLocationArchived):
		status, code, message = http.StatusConflict, "LOCATION_ARCHIVED", "New placement requires a complete active destination path."
	case errors.Is(err, store.ErrStockTracking), errors.Is(err, store.ErrUnitTracking):
		status, code, message = http.StatusConflict, "TRACKING_MODE_CONFLICT", "Use the Item's actual individual or quantity tracking workflow."
	case errors.Is(err, store.ErrStockInsufficient):
		status, code, message = http.StatusConflict, "INSUFFICIENT_STOCK", "The reviewed source balance cannot supply this quantity."
	case errors.Is(err, store.ErrStockOverflow):
		status, code, message = http.StatusConflict, "QUANTITY_OVERFLOW", "The target balance would exceed the exact safe base quantity."
	case errors.Is(err, store.ErrUnitHistory):
		status, code, message = http.StatusConflict, "UNIT_HAS_HISTORY", "Retained purchase or asset records prevent Unit deletion; preserve their history."
	case errors.Is(err, store.ErrUnitDisposed):
		status, code, message = http.StatusConflict, "UNIT_DISPOSED", "Disposed Units cannot move or reactivate through ordinary status changes."
	case errors.Is(err, store.ErrUnitAssetLifecycle):
		status, code, message = http.StatusConflict, "ASSET_LIFECYCLE_REQUIRED", "Use the separate asset lifecycle workflow for this change."
	}
	writeError(w, status, code, message, requestID)
}
